package vault

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
)

// diskTestStore models a persistent credential store for synthetic test keys.
// It is confined to temporary homes and is never used by production code.
type diskTestStore struct{ root string }

func (store diskTestStore) Set(key, value string) error {
	return atomicfile.Write(filepath.Join(store.root, key), []byte(value), 0o600)
}

func (store diskTestStore) Get(key string) (string, error) {
	data, err := os.ReadFile(filepath.Join(store.root, key))
	return string(data), err
}

func (store diskTestStore) Delete(key string) error {
	return os.Remove(filepath.Join(store.root, key))
}

func TestVaultCrashHelperProcess(t *testing.T) {
	if os.Getenv("CODEX_SWITCH_CRASH_HELPER") != "1" {
		return
	}
	root := os.Getenv("CODEX_SWITCH_HOME")
	if !filepath.IsAbs(root) {
		t.Fatal("crash helper requires an isolated absolute home")
	}
	manager := New(filepath.Join(root, "vault.enc"), diskTestStore{filepath.Join(root, "test-keys")})
	manager.writeFile = func(path string, encoded []byte, mode os.FileMode) error {
		if os.Getenv("CODEX_SWITCH_CRASH_STAGE") == "after-publication" {
			if err := atomicfile.Write(path, encoded, mode); err != nil {
				return err
			}
		}
		// Exit bypasses all deferred key and file cleanup in this process.
		os.Exit(42)
		return nil
	}
	if os.Getenv("CODEX_SWITCH_CRASH_MODE") == "init" {
		if _, err := manager.Init(); err != nil {
			t.Fatal(err)
		}
	} else if err := withVaultLock(t, manager, manager.RotateKey); err != nil {
		t.Fatal(err)
	}
	t.Fatal("helper did not reach the interruption boundary")
}

func TestKeyRecoveryAfterProcessExit(t *testing.T) {
	for _, mode := range []string{"init", "rotate"} {
		for _, stage := range []string{"before-publication", "after-publication"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				root := t.TempDir()
				store := diskTestStore{filepath.Join(root, "test-keys")}
				manager := New(filepath.Join(root, "vault.enc"), store)
				oldKey := ""
				count := 0
				if mode == "rotate" {
					count = 1
					if err := withVaultLock(t, manager, func() error { return manager.Save(recoveryData()) }); err != nil {
						t.Fatal(err)
					}
					oldKey = publishedKey(t, manager.path)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVaultCrashHelperProcess$")
				command.Env = append(os.Environ(),
					"CODEX_SWITCH_CRASH_HELPER=1", "CODEX_SWITCH_CRASH_MODE="+mode,
					"CODEX_SWITCH_CRASH_STAGE="+stage, "CODEX_SWITCH_HOME="+root,
					"CODEX_HOME="+filepath.Join(root, "codex"), "GORACE=atexit_sleep_ms=0",
				)
				output, err := command.CombinedOutput()
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) || exitError.ExitCode() != 42 {
					t.Fatalf("helper failed outside the crash boundary: %v\n%s", err, output)
				}
				if mode == "rotate" {
					if _, err := store.Get(oldKey); err != nil {
						t.Fatal("interrupted rotation removed the previous decryption key")
					}
				}
				reopened := New(manager.path, diskTestStore{store.root})
				if mode == "init" && stage == "before-publication" {
					if _, err := os.Stat(manager.path); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("interrupted initialization unexpectedly published a vault")
					}
					if _, err := reopened.Init(); err != nil {
						t.Fatal(err)
					}
				}
				assertRecoverableVault(t, reopened, count)
				if err := withVaultLock(t, reopened, reopened.RotateKey); err != nil {
					t.Fatalf("rotation after process exit failed: %v", err)
				}
				assertRecoverableVault(t, New(manager.path, diskTestStore{store.root}), count)
			})
		}
	}
}
