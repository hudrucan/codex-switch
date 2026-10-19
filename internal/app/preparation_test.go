package app

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/filelock"
	"github.com/SilkageNet/codex-switch/internal/vault"
	"github.com/spf13/cobra"
)

const testBackupPassphrase = "synthetic backup passphrase"

func writeLockingBackup(t *testing.T, runtime runtimeState, alias string) {
	t.Helper()
	document := lockingDocument(t, alias)
	data := vault.Data{Version: 1}
	if err := data.Add(vault.NewProfile(alias, "test", document.Raw, document.Tokens.AccountID, "", "", time.Time{}), false); err != nil {
		t.Fatal(err)
	}
	encoded, err := vault.Export(data, []byte(testBackupPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(filepath.Join(runtime.paths.Root, "test-backup.enc"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

type preparationGate struct {
	started     chan struct{}
	release     chan struct{}
	waitOnce    sync.Once
	releaseOnce sync.Once
}

func newPreparationGate() *preparationGate {
	return &preparationGate{started: make(chan struct{}), release: make(chan struct{})}
}

func (gate *preparationGate) wait() {
	gate.waitOnce.Do(func() { close(gate.started); <-gate.release })
}

func (gate *preparationGate) unblock() {
	gate.releaseOnce.Do(func() { close(gate.release) })
}

type pendingPassphrase struct {
	gate  *preparationGate
	input io.Reader
}

func (input pendingPassphrase) Read(buffer []byte) (int, error) {
	input.gate.wait()
	return input.input.Read(buffer)
}

func startPendingCommand(t *testing.T, command *cobra.Command, args []string, gate *preparationGate) func() error {
	t.Helper()
	command.SetArgs(args)
	command.SilenceErrors = true
	command.SilenceUsage = true
	finished := make(chan struct{})
	var result error
	go func() {
		result = command.Execute()
		close(finished)
	}()
	finish := func() error {
		gate.unblock()
		select {
		case <-finished:
			return result
		case <-time.After(15 * time.Second):
			t.Fatal("interactive command did not finish")
			return nil
		}
	}
	t.Cleanup(func() { _ = finish() })
	select {
	case <-gate.started:
	case <-finished:
		t.Fatalf("command exited before interactive preparation: %v", result)
	case <-time.After(15 * time.Second):
		t.Fatal("command did not reach interactive preparation")
	}
	return finish
}

func prepareInteractiveRuntime(t *testing.T) runtimeState {
	t.Helper()
	root := t.TempDir()
	runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "codex"))
	seedLockingRuntime(t, runtime)
	// An already-live profile lets Use exercise its guarded transaction without
	// bypassing the running-Codex guard or touching any real process/account.
	if err := runtime.home.WriteAuth(lockingDocument(t, "a").Raw); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestPendingLoginDoesNotLockSwitchingAndReloadsVault(t *testing.T) {
	for _, scenario := range []string{"add", "reauth", "add-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := prepareInteractiveRuntime(t)
			before, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			original, err := before.Find("a")
			if err != nil {
				t.Fatal(err)
			}
			document := lockingDocument(t, "login")
			gate := newPreparationGate()
			options := lockingOptions(runtime)
			options.login = func(bool) (authschema.Document, error) {
				gate.wait()
				return document, nil
			}
			alias := "c"
			if scenario == "reauth" {
				alias = "a"
			}
			finish := startPendingCommand(t, newAccountAddCommand(options, scenario == "reauth"), []string{alias}, gate)
			runMutationProcess(t, runtime, "use", false)
			runMutationProcess(t, runtime, "rename-b", false)
			if scenario == "add-conflict" {
				lock, err := runtime.acquireVaultLock()
				if err != nil {
					t.Fatal(err)
				}
				_, addErr := addDocument(runtime.manager, alias, "test", lockingDocument(t, "winner"))
				closeErr := lock.Close()
				if addErr != nil || closeErr != nil {
					t.Fatalf("concurrent add failed: %v, %v", addErr, closeErr)
				}
			}
			err = finish()
			if scenario == "add-conflict" {
				if err == nil || !strings.Contains(err.Error(), "already exists") {
					t.Fatalf("expected current alias conflict, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			loaded, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loaded.Find("concurrent-update"); err != nil {
				t.Fatal("login transaction lost a concurrent rename")
			}
			saved, err := loaded.Find(alias)
			if err != nil {
				t.Fatal(err)
			}
			expectedAccount := "account-login"
			if scenario == "add-conflict" {
				expectedAccount = "account-winner"
			}
			if saved.AccountID != expectedAccount {
				t.Fatal("login transaction saved the wrong account")
			}
			if scenario == "reauth" && saved.ID != original.ID {
				t.Fatal("reauth did not retain the current profile identifier")
			}
		})
	}
}

func TestPendingBackupPassphraseDoesNotLockSwitching(t *testing.T) {
	for _, scenario := range []string{"merge", "replace", "merge-conflict", "replace-invalid-current"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := prepareInteractiveRuntime(t)
			alias := "imported"
			if scenario == "merge-conflict" {
				alias = "a"
			}
			writeLockingBackup(t, runtime, alias)
			gate := newPreparationGate()
			options := lockingOptions(runtime)
			options.Input = pendingPassphrase{gate, strings.NewReader(testBackupPassphrase + "\n")}
			action := "backup-merge"
			if strings.HasPrefix(scenario, "replace") {
				action = "backup-replace"
			}
			command, args := lockingCommand(options, action)
			finish := startPendingCommand(t, command, args, gate)
			runMutationProcess(t, runtime, "use", false)
			runMutationProcess(t, runtime, "rename-b", false)
			if scenario == "replace-invalid-current" {
				lock, err := runtime.acquireVaultLock()
				if err != nil {
					t.Fatal(err)
				}
				writeErr := atomicfile.Write(runtime.paths.Vault, []byte("invalid synthetic vault"), 0o600)
				closeErr := lock.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("fault injection failed: %v, %v", writeErr, closeErr)
				}
			}
			before, err := atomicfile.FileHash(runtime.paths.Vault)
			if err != nil {
				t.Fatal(err)
			}
			err = finish()
			if scenario == "merge-conflict" || scenario == "replace-invalid-current" {
				if err == nil {
					t.Fatal("expected current vault validation to fail")
				}
				after, hashErr := atomicfile.FileHash(runtime.paths.Vault)
				if hashErr != nil || before != after {
					t.Fatal("failed import changed the current vault")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loaded.Find("imported"); err != nil {
				t.Fatal(err)
			}
			if scenario == "merge" {
				if _, err := loaded.Find("concurrent-update"); err != nil {
					t.Fatal("backup merge lost a concurrent rename")
				}
			} else if len(loaded.Profiles) != 1 {
				t.Fatal("explicit replacement did not replace the vault")
			}
		})
	}
}

func TestVaultOnlyCommandsWithMissingCodexHome(t *testing.T) {
	for _, action := range []string{"rename", "rotate-key"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "existing-codex"))
			seedLockingRuntime(t, runtime)
			missing := resolveLockingRuntime(t, runtime.paths.Root, filepath.Join(root, "missing", "codex"))
			runMutationProcess(t, missing, action, false)
			data, err := runtime.manager.Load()
			if err != nil || len(data.Profiles) != 2 {
				t.Fatalf("vault-only command did not preserve a readable vault: %v", err)
			}
			lock, err := runtime.acquireVaultLock()
			if err != nil {
				t.Fatal(err)
			}
			runMutationProcess(t, missing, action, true)
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("vault-only command created or required CODEX_HOME")
			}
		})
	}
}

func TestVaultOnlyCommandsIgnoreUnrelatedHomeLock(t *testing.T) {
	for _, action := range []string{"rename", "rotate-key"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "codex"))
			seedLockingRuntime(t, runtime)
			other := lockingRuntime(t, filepath.Join(root, "other-switch"), runtime.home.Path)
			seedLockingRuntime(t, other)
			lock, err := runtime.acquireLock()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()
			runMutationProcess(t, other, action, false)
			// Sharing the vault still conflicts, even through a different home.
			otherHome := resolveLockingRuntime(t, runtime.paths.Root, filepath.Join(root, "missing-codex"))
			runMutationProcess(t, otherHome, action, true)
		})
	}
}

func TestHomeOperationDoesNotFallBackToVaultOnlyLock(t *testing.T) {
	runtime := prepareInteractiveRuntime(t)
	missing := resolveLockingRuntime(t, runtime.paths.Root, filepath.Join(t.TempDir(), "missing-codex"))
	before, err := atomicfile.FileHash(runtime.paths.Vault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.switcher().Use("a", false); err == nil {
		t.Fatal("home operation silently skipped its missing home lock")
	}
	after, err := atomicfile.FileHash(runtime.paths.Vault)
	if err != nil || before != after {
		t.Fatal("failed home operation changed the vault")
	}
	lock, err := filelock.AcquireVault(runtime.paths.Vault)
	if err != nil {
		t.Fatal("failed home operation leaked its vault lock")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
