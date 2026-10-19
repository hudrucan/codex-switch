package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
	"github.com/SilkageNet/codex-switch/internal/filelock"
	"github.com/spf13/cobra"
)

func lockingCommand(options *Options, action string) (*cobra.Command, []string) {
	switch action {
	case "init":
		return newInitCommand(options), nil
	case "import-current":
		return newAccountImportCommand(options), []string{"current"}
	case "add", "reauth":
		return newAccountAddCommand(options, action == "reauth"), []string{"a"}
	case "rename":
		return newAccountRenameCommand(options), []string{"a", "renamed"}
	case "rename-b":
		return newAccountRenameCommand(options), []string{"b", "concurrent-update"}
	case "rename-b-case":
		return newAccountRenameCommand(options), []string{"b", "B"}
	case "reassign-b":
		return newAccountRenameCommand(options), []string{"c", "b"}
	case "remove":
		return newAccountRemoveCommand(options), []string{"a"}
	case "remove-b":
		return newAccountRemoveCommand(options), []string{"b"}
	case "backup-merge":
		return newVaultCommand(options), []string{"import", "--input", filepath.Join(options.AppHome, "test-backup.enc")}
	case "backup-replace":
		return newVaultCommand(options), []string{"import", "--replace", "--input", filepath.Join(options.AppHome, "test-backup.enc")}
	case "rotate-key":
		return newVaultCommand(options), []string{"rotate-key"}
	case "sync":
		return newSyncCommand(options), nil
	case "use":
		return newUseCommand(options), []string{"a"}
	case "deactivate":
		return newDeactivateCommand(options), nil
	default:
		panic("unknown test command")
	}
}

type unusedUsageRunner struct{}

func (unusedUsageRunner) Query(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
	return codexusage.Snapshot{}, nil, errors.New("unexpected usage query")
}

func runLockingAction(runtime runtimeState, action string) error {
	switch action {
	case "initialize-vault":
		_, err := runtime.manager.Init()
		return err
	case "recover":
		return runtime.switcher().Recover()
	case "usage":
		service := runtime.usageService("test")
		service.Runner = unusedUsageRunner{}
		_, err := service.Refresh(context.Background(), []string{"a"})
		return err
	default:
		command, args := lockingCommand(lockingOptions(runtime), action)
		command.SetArgs(args)
		command.SilenceErrors = true
		command.SilenceUsage = true
		return command.Execute()
	}
}

func TestVaultMutationHelperProcess(t *testing.T) {
	if os.Getenv("CODEX_SWITCH_LOCK_TEST") != "1" {
		return
	}
	runtime := resolveLockingRuntime(t, os.Getenv("CODEX_SWITCH_HOME"), os.Getenv("CODEX_HOME"))
	err := runLockingAction(runtime, os.Getenv("CODEX_SWITCH_LOCK_ACTION"))
	if os.Getenv("CODEX_SWITCH_LOCK_EXPECT_BUSY") == "1" {
		if err == nil || !strings.Contains(err.Error(), "another codex-switch operation is already running") {
			t.Fatalf("expected operation lock contention, got %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}

func runMutationProcess(t *testing.T, runtime runtimeState, action string, busy bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVaultMutationHelperProcess$")
	expectBusy := "0"
	if busy {
		expectBusy = "1"
	}
	command.Env = append(os.Environ(),
		"CODEX_SWITCH_LOCK_TEST=1", "CODEX_SWITCH_LOCK_ACTION="+action,
		"CODEX_SWITCH_LOCK_EXPECT_BUSY="+expectBusy,
		"CODEX_SWITCH_HOME="+runtime.paths.Root, "CODEX_HOME="+runtime.home.Path,
		"GORACE=atexit_sleep_ms=0",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("mutation helper failed: %v\n%s", err, output)
	}
}

func TestVaultMutationsExcludeOtherProcesses(t *testing.T) {
	root := t.TempDir()
	first := lockingRuntime(t, filepath.Join(root, "switch-a"), filepath.Join(root, "codex-a"))
	seedLockingRuntime(t, first)
	otherHome := lockingRuntime(t, first.paths.Root, filepath.Join(root, "codex-b"))
	otherVault := lockingRuntime(t, filepath.Join(root, "switch-b"), first.home.Path)
	seedLockingRuntime(t, otherVault)
	writeLockingBackup(t, first, "imported")
	before := make(map[string]string)
	for _, runtime := range []runtimeState{first, otherHome, otherVault} {
		for _, path := range []string{runtime.paths.Vault, runtime.paths.State, runtime.paths.Journal, runtime.paths.UsageCache, runtime.home.AuthPath, runtime.home.ConfigPath} {
			hash, err := atomicfile.FileHash(path)
			if err != nil {
				t.Fatal(err)
			}
			before[path] = hash
		}
	}
	lock, err := first.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	for _, action := range []string{"init", "initialize-vault", "import-current", "add", "reauth", "rename", "remove", "backup-merge", "backup-replace", "rotate-key", "sync", "use", "deactivate", "recover", "usage"} {
		t.Run(action, func(t *testing.T) {
			runMutationProcess(t, otherHome, action, true)
			// Only operations using live authentication or home-related state
			// must also conflict with a different vault sharing that home.
			switch action {
			case "init", "import-current", "remove", "sync", "use", "deactivate", "recover", "usage":
				runMutationProcess(t, otherVault, action, true)
			}
		})
	}
	for path, hash := range before {
		actual, err := atomicfile.FileHash(path)
		if err != nil || actual != hash {
			t.Fatal("a contended operation changed managed data")
		}
	}
}

func TestMutationRetryPreservesConcurrentUpdate(t *testing.T) {
	root := t.TempDir()
	runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "codex"))
	seedLockingRuntime(t, runtime)
	other := lockingRuntime(t, runtime.paths.Root, filepath.Join(root, "other-codex"))
	lock, err := runtime.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if lock != nil {
			_ = lock.Close()
		}
	}()
	data, err := runtime.manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	runMutationProcess(t, other, "rename", true)
	profile, err := data.Find("b")
	if err != nil {
		t.Fatal(err)
	}
	profile.Alias = "concurrent-update"
	if err := runtime.manager.Save(data); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	lock = nil
	runMutationProcess(t, other, "rename", false)
	loaded, err := runtime.manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"renamed", "concurrent-update"} {
		if _, err := loaded.Find(alias); err != nil {
			t.Fatal("a concurrent update was lost")
		}
	}
}

func TestMutationFailureReleasesOperationLock(t *testing.T) {
	root := t.TempDir()
	runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "codex"))
	seedLockingRuntime(t, runtime)
	command, args := lockingCommand(lockingOptions(runtime), "rename")
	command.SetArgs([]string{args[0], " invalid"})
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err == nil {
		t.Fatal("expected invalid alias to fail")
	}
	runMutationProcess(t, runtime, "rename", false)
}

func TestInitializationCannotOverwriteAnotherProcess(t *testing.T) {
	root := t.TempDir()
	runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "codex"))
	lock, err := filelock.Acquire(runtime.paths.Vault + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	runMutationProcess(t, runtime, "initialize-vault", true)
	if _, err := os.Stat(runtime.paths.Vault); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("contended initialization wrote a vault")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	runMutationProcess(t, runtime, "initialize-vault", false)
	if _, err := runtime.manager.Load(); err != nil {
		t.Fatal(err)
	}
}
