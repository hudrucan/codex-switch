package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/filelock"
	"github.com/SilkageNet/codex-switch/internal/secretstore"
)

var (
	errStoreDiagnostic = errors.New("synthetic sensitive store diagnostic")
	errWriteFault      = errors.New("injected vault write failure")
	errInterrupted     = errors.New("injected interruption")
)

type faultSecretStore struct {
	memory      *secretstore.MemoryStore
	setFault    string
	deleteFault string
	getFault    string
	getError    error
	lastKey     string
	gets        map[string]int
	deletes     int
	onDelete    func(string)
}

func (store *faultSecretStore) Set(key, value string) error {
	store.lastKey = key
	store.gets[key] = 0
	if store.setFault == "set-before" {
		return errStoreDiagnostic
	}
	if store.setFault == "set-lost" {
		return nil
	}
	if store.setFault == "readback-corrupt" {
		decoded, err := base64.RawStdEncoding.DecodeString(value)
		if err != nil || len(decoded) == 0 {
			return errors.New("invalid synthetic key")
		}
		decoded[0] ^= 1
		value = base64.RawStdEncoding.EncodeToString(decoded)
	}
	if err := store.memory.Set(key, value); err != nil {
		return err
	}
	switch store.setFault {
	case "set-after":
		return errStoreDiagnostic
	case "set-interrupted":
		panic(errInterrupted)
	case "readback-error":
		store.getFault = key
	}
	return nil
}

func (store *faultSecretStore) Get(key string) (string, error) {
	store.gets[key]++
	if key == store.getFault {
		if store.getError != nil {
			return "", store.getError
		}
		return "", errStoreDiagnostic
	}
	return store.memory.Get(key)
}

func TestInitDoesNotReplaceVaultWhenKeyReadFails(t *testing.T) {
	root := t.TempDir()
	store := &faultSecretStore{memory: secretstore.NewMemoryStore(), gets: make(map[string]int)}
	manager := New(filepath.Join(root, "vault.enc"), store)
	if err := withVaultLock(t, manager, func() error { return manager.Save(recoveryData()) }); err != nil {
		t.Fatal(err)
	}
	before, err := atomicfile.FileHash(manager.path)
	if err != nil {
		t.Fatal(err)
	}
	store.getFault = store.lastKey
	store.getError = os.ErrNotExist
	if _, err := manager.Init(); err == nil {
		t.Fatal("initialization must fail when an existing vault's key cannot be read")
	}
	after, err := atomicfile.FileHash(manager.path)
	if err != nil || before != after || store.deletes != 0 {
		t.Fatal("initialization replaced an existing vault after a key-store read failure")
	}
	store.getFault = ""
	if _, err := New(manager.path, store).Init(); err != nil {
		t.Fatal(err)
	}
	assertRecoverableVault(t, manager, 1)
}

func (store *faultSecretStore) Delete(key string) error {
	store.deletes++
	if store.onDelete != nil {
		store.onDelete(key)
	}
	if store.deleteFault == "cleanup-before" {
		return errStoreDiagnostic
	}
	if store.deleteFault == "cleanup-interrupted-before" {
		panic(errInterrupted)
	}
	if err := store.memory.Delete(key); err != nil {
		return err
	}
	if store.deleteFault == "cleanup-after" {
		return errStoreDiagnostic
	}
	if store.deleteFault == "cleanup-interrupted-after" {
		panic(errInterrupted)
	}
	return nil
}

func recoveryData() Data {
	return Data{Version: 1, Profiles: []Profile{{
		ID: "synthetic-profile", Alias: "work", AccountID: "synthetic-account",
		Auth: json.RawMessage(`{"auth_mode":"chatgpt","tokens":{"id_token":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"synthetic-account"}}`),
	}}}
}

func withVaultLock(t *testing.T, manager *Manager, action func() error) error {
	t.Helper()
	lock, err := filelock.AcquireVault(manager.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	return action()
}

func assertRecoverableVault(t *testing.T, manager *Manager, count int) {
	t.Helper()
	data, err := manager.Load()
	if err != nil {
		t.Fatalf("published vault could not be decrypted: %v", err)
	}
	if len(data.Profiles) != count {
		t.Fatal("recovery lost saved profiles")
	}
	if count > 0 && (data.Profiles[0].ID != "synthetic-profile" || !bytes.Equal(data.Profiles[0].Auth, recoveryData().Profiles[0].Auth)) {
		t.Fatal("recovery changed synthetic credentials")
	}
}

func publishedKey(t *testing.T, path string) string {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var wrapper envelope
	if err := json.Unmarshal(encoded, &wrapper); err != nil {
		t.Fatal(err)
	}
	return "master-key/" + wrapper.KeyID
}

func TestKeyWriteFailuresRemainRecoverable(t *testing.T) {
	stages := []string{"before-replace", "replace-uncertain-before", "replace-uncertain-after", "after-replace", "unknown-after-replace", "set-before", "set-after", "set-lost", "readback-error", "readback-corrupt", "verify-publication", "publication-skipped", "interrupt-before", "interrupt-after", "set-interrupted", "cleanup-before", "cleanup-after", "cleanup-interrupted-before", "cleanup-interrupted-after", "success"}
	for _, mode := range []string{"init", "save-create", "rotate"} {
		for _, stage := range stages {
			if mode != "rotate" && strings.HasPrefix(stage, "cleanup-") {
				continue
			}
			t.Run(mode+"/"+stage, func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
				t.Setenv("CODEX_SWITCH_HOME", root)
				store := &faultSecretStore{memory: secretstore.NewMemoryStore(), gets: make(map[string]int)}
				manager := New(filepath.Join(root, "vault.enc"), store)
				var previous []byte
				oldKey := ""
				count := 1
				if mode == "init" {
					count = 0
				}
				if mode == "rotate" {
					if err := withVaultLock(t, manager, func() error { return manager.Save(recoveryData()) }); err != nil {
						t.Fatal(err)
					}
					var err error
					previous, err = os.ReadFile(manager.path)
					if err != nil {
						t.Fatal(err)
					}
					oldKey = publishedKey(t, manager.path)
				}
				store.setFault = stage
				store.deleteFault = stage
				committed := false
				store.onDelete = func(key string) {
					if !committed || key != oldKey || key == store.lastKey || publishedKey(t, manager.path) != store.lastKey || store.gets[store.lastKey] < 2 {
						t.Fatal("key deletion preceded durable publication and read-back verification")
					}
				}
				manager.writeFile = func(path string, encoded []byte, mode os.FileMode) error {
					switch stage {
					case "before-replace":
						return &atomicfile.WriteError{Err: errWriteFault, Replacement: atomicfile.ReplacementNotAttempted}
					case "replace-uncertain-before":
						return &atomicfile.WriteError{Err: errWriteFault, Replacement: atomicfile.ReplacementUncertain}
					case "interrupt-before":
						panic(errInterrupted)
					case "publication-skipped":
						return nil
					}
					if err := atomicfile.Write(path, encoded, mode); err != nil {
						return err
					}
					// The atomicfile tests inject the actual rename/sync failures.
					// Here the writer's result exercises vault key-lifecycle decisions.
					switch stage {
					case "replace-uncertain-after":
						return &atomicfile.WriteError{Err: errWriteFault, Replacement: atomicfile.ReplacementUncertain}
					case "after-replace":
						return &atomicfile.WriteError{Err: errWriteFault, Replacement: atomicfile.ReplacementCompleted}
					case "unknown-after-replace":
						return errWriteFault
					case "interrupt-after":
						panic(errInterrupted)
					case "verify-publication":
						store.getFault = store.lastKey
					}
					committed = true
					return nil
				}
				var operationErr error
				interrupted := false
				func() {
					defer func() {
						if failure := recover(); failure != nil {
							if failure != errInterrupted {
								panic(failure)
							}
							interrupted = true
						}
					}()
					if mode == "init" {
						_, operationErr = manager.Init()
					} else {
						operationErr = withVaultLock(t, manager, func() error {
							if mode == "save-create" {
								return manager.Save(recoveryData())
							}
							return manager.RotateKey()
						})
					}
				}()
				if stage == "success" {
					if operationErr != nil || interrupted {
						t.Fatal("normal key write failed")
					}
				} else if operationErr == nil && !interrupted {
					t.Fatal("fault injection did not interrupt the operation")
				}
				if operationErr != nil && strings.Contains(operationErr.Error(), errStoreDiagnostic.Error()) {
					t.Fatal("key lifecycle error exposed credential-store diagnostics")
				}
				if strings.HasPrefix(stage, "cleanup-") && !interrupted && (operationErr == nil || !strings.Contains(operationErr.Error(), "rotation committed")) {
					t.Fatal("cleanup failure did not report that rotation committed")
				}
				wantDeletes := 0
				if mode == "rotate" && (stage == "success" || strings.HasPrefix(stage, "cleanup-")) {
					wantDeletes = 1
				}
				if store.deletes != wantDeletes {
					t.Fatal("failure deleted a key before commit or attempted rollback deletion")
				}
				store.setFault, store.deleteFault, store.getFault = "", "", ""
				store.onDelete = nil
				reopened := New(manager.path, store)
				current, readErr := os.ReadFile(manager.path)
				if readErr == nil {
					assertRecoverableVault(t, reopened, count)
					if _, err := store.memory.Get(publishedKey(t, manager.path)); err != nil {
						t.Fatal("published vault depends on an unavailable key")
					}
					if mode == "rotate" && wantDeletes == 0 && !bytes.Equal(current, previous) {
						// Model both possible filesystem outcomes after an uncertain
						// replacement: the old envelope or the new envelope survives.
						if err := atomicfile.Write(manager.path, previous, 0o600); err != nil {
							t.Fatal(err)
						}
						assertRecoverableVault(t, reopened, count)
						if err := atomicfile.Write(manager.path, current, 0o600); err != nil {
							t.Fatal(err)
						}
						assertRecoverableVault(t, reopened, count)
					}
				} else if !errors.Is(readErr, os.ErrNotExist) || mode == "rotate" {
					t.Fatal("failure removed an existing vault")
				} else if mode == "init" {
					if _, err := reopened.Init(); err != nil {
						t.Fatal(err)
					}
				} else if err := withVaultLock(t, reopened, func() error { return reopened.Save(recoveryData()) }); err != nil {
					t.Fatal(err)
				}
				// Retry with a fresh manager, without any in-memory recovery state.
				if err := withVaultLock(t, reopened, reopened.RotateKey); err != nil {
					t.Fatalf("rotation after a recoverable failure failed: %v", err)
				}
				assertRecoverableVault(t, New(manager.path, store), count)
			})
		}
	}
}
