//go:build !windows

package codexusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/authschema"
)

func TestWrapperDescendantsTerminateBeforeCredentialRecovery(t *testing.T) {
	for _, mode := range []string{"cancel", "failure"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			ready, late := filepath.Join(root, "ready"), filepath.Join(root, "late")
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			dead := make(chan struct{})
			var child *exec.Cmd
			runner := Runner{Binary: os.Args[0], confirmExit: func(empty func() (bool, error)) error {
				// Start and Wait have completed on this goroutine before closing
				// the parent descriptor; a filesystem barrier alone is not Go
				// synchronization with exec.Cmd.Start reading ExtraFiles.
				_ = writer.Close()
				if err := waitForServerExit(empty); err != nil {
					return err
				}
				select {
				case <-dead:
					return nil
				case <-time.After(time.Second):
					return errors.New("descendant remained alive before auth inspection")
				}
			}, command: func(ctx context.Context, binary string, _ ...string) *exec.Cmd {
				child = exec.CommandContext(ctx, binary, "-test.run=^TestUsageTreeHelperProcess$")
				child.Env = append(os.Environ(), "CODEX_USAGE_TREE=wrapper", "CODEX_USAGE_TREE_MODE="+mode,
					"CODEX_USAGE_TREE_READY="+ready, "CODEX_USAGE_TREE_LATE="+late, "GORACE=atexit_sleep_ms=0")
				child.ExtraFiles = []*os.File{writer}
				return child
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan struct{})
			var updated json.RawMessage
			var queryErr error
			go func() {
				_, updated, queryErr = runner.Query(ctx, testAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
				close(done)
			}()
			// The descendant retains this descriptor throughout its lifetime. EOF
			// confirms termination independently of the direct wrapper's Wait.
			go func() { _, _ = io.Copy(io.Discard, reader); close(dead) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("tree cleanup did not finish")
				}
			})
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
		readyLoop:
			for {
				select {
				case <-ticker.C:
					if _, err := os.Stat(ready); err == nil {
						break readyLoop
					}
				case <-ctx.Done():
					t.Fatal("descendant did not reach readiness barrier")
				}
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("owned tree was not cleaned")
			}
			select {
			case <-dead:
			case <-time.After(time.Second):
				t.Fatal("descendant outlived credential recovery")
			}
			if queryErr == nil || strings.Contains(queryErr.Error(), rpcCanary) || strings.Contains(queryErr.Error(), "cleanup failed") {
				t.Fatal("query failure or lifecycle cleanup was not safely classified")
			}
			if mode == "cancel" && !errors.Is(queryErr, context.Canceled) {
				t.Fatal("cancellation classification lost")
			}
			document, err := authschema.Parse(updated)
			if err != nil || document.Tokens.RefreshToken != "refresh-new" {
				t.Fatal("terminated tree's valid refresh was lost")
			}
			assertChildCleaned(t, child)
			// A still-running descendant would observe this gate and attempt a
			// late auth write. EOF above proves it can no longer execute the write.
			if err := os.WriteFile(late, []byte("release"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(late + ".written"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("descendant performed a late write")
			}
		})
	}
}

func TestUsageTreeHelperProcess(t *testing.T) {
	role := os.Getenv("CODEX_USAGE_TREE")
	if role == "" {
		return
	}
	if role == "wrapper" {
		child := exec.Command(os.Args[0], "-test.run=^TestUsageTreeHelperProcess$")
		child.Env = withEnvironment(os.Environ(), "CODEX_USAGE_TREE", "descendant")
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		child.ExtraFiles = []*os.File{os.NewFile(3, "liveness")}
		if child.Start() != nil {
			os.Exit(92)
		}
		_ = child.Wait()
		os.Exit(0)
	}
	home := os.Getenv("CODEX_HOME")
	if !filepath.IsAbs(home) {
		os.Exit(93)
	}
	if os.WriteFile(filepath.Join(home, "auth.json"), testAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z"), 0o600) != nil {
		os.Exit(94)
	}
	if os.WriteFile(os.Getenv("CODEX_USAGE_TREE_READY"), []byte("ready"), 0o600) != nil {
		os.Exit(95)
	}
	if os.Getenv("CODEX_USAGE_TREE_MODE") == "failure" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": 0, "error": map[string]any{"code": -32001, "message": rpcCanary}})
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		late := os.Getenv("CODEX_USAGE_TREE_LATE")
		if _, err := os.Stat(late); err == nil {
			_ = os.WriteFile(filepath.Join(home, "auth.json"), []byte(rpcCanary), 0o600)
			_ = os.WriteFile(late+".written", []byte("written"), 0o600)
			os.Exit(0)
		}
	}
}

func TestLifecycleRejectsUnownedProcessGroup(t *testing.T) {
	lifecycle := serverLifecycle{command: &exec.Cmd{}}
	if lifecycle.terminate() == nil {
		t.Fatal("unstarted process group was accepted")
	}
	lifecycle.command.Process = &os.Process{Pid: syscall.Getpgrp()}
	if lifecycle.terminate() == nil {
		t.Fatal("current process group was accepted")
	}
}

func TestLifecycleConfirmationFailsClosed(t *testing.T) {
	if waitForServerExit(func() (bool, error) { return false, errors.New(rpcCanary) }) == nil {
		t.Fatal("unconfirmed lifecycle was accepted")
	}
}
