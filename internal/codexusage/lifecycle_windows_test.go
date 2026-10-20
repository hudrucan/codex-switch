package codexusage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsJobContainsWrapperBeforeExecution(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "descendant-pid")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsJobHelperProcess$")
	command.Env = append(os.Environ(), "CODEX_USAGE_JOB=wrapper", "CODEX_USAGE_JOB_READY="+ready, "GORACE=atexit_sleep_ms=0")
	lifecycle, err := newServerLifecycle(command)
	if err != nil {
		t.Fatal("could not create isolated job")
	}
	defer lifecycle.close()
	command.Cancel = lifecycle.terminate
	if err := command.Start(); err != nil {
		t.Fatal("could not start suspended wrapper")
	}
	defer func() { _ = lifecycle.terminate(); _ = command.Wait() }()
	if _, err := os.Stat(ready); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrapper ran before job assignment")
	}
	if err := lifecycle.started(); err != nil {
		t.Fatal("job assignment or resume failed")
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var descendant windows.Handle
	for descendant == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("descendant did not start inside job")
		case <-ticker.C:
			data, err := os.ReadFile(ready)
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				continue
			}
			descendant, err = windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				t.Fatal("could not observe owned descendant")
			}
		}
	}
	defer windows.CloseHandle(descendant)
	cancel()
	if err := lifecycle.terminate(); err != nil {
		t.Fatal("job termination failed")
	}
	_ = command.Wait()
	if command.ProcessState == nil || waitForServerExit(lifecycle.empty) != nil {
		t.Fatal("job lifecycle did not terminate")
	}
	status, err := windows.WaitForSingleObject(descendant, 0)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("owned descendant outlived job cleanup")
	}
}

func TestWindowsJobHelperProcess(t *testing.T) {
	role := os.Getenv("CODEX_USAGE_JOB")
	if role == "" {
		return
	}
	if role == "wrapper" {
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsJobHelperProcess$")
		child.Env = withEnvironment(os.Environ(), "CODEX_USAGE_JOB", "descendant")
		if child.Start() != nil {
			os.Exit(90)
		}
		_ = child.Wait()
		os.Exit(0)
	}
	if os.WriteFile(os.Getenv("CODEX_USAGE_JOB_READY"), []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
		os.Exit(91)
	}
	// Remain runnable until job termination; a timer is not the cleanup proof.
	for {
		time.Sleep(time.Hour)
	}
}
