package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexhome"
	appconfig "github.com/SilkageNet/codex-switch/internal/config"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

// Synthetic keys are confined to the test home; no platform store is opened.
type testSecretStore struct{ root string }

func (store testSecretStore) Set(key, value string) error {
	return atomicfile.Write(filepath.Join(store.root, key), []byte(value), 0o600)
}

func (store testSecretStore) Get(key string) (string, error) {
	value, err := os.ReadFile(filepath.Join(store.root, key))
	return string(value), err
}

func (store testSecretStore) Delete(key string) error {
	return os.Remove(filepath.Join(store.root, key))
}

func lockingRuntime(t *testing.T, appHome, codexHome string) runtimeState {
	t.Helper()
	runtime := resolveLockingRuntime(t, appHome, codexHome)
	if err := runtime.paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.home.Ensure(); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func resolveLockingRuntime(t *testing.T, appHome, codexHome string) runtimeState {
	t.Helper()
	paths, err := appconfig.ResolvePaths(appHome)
	if err != nil {
		t.Fatal(err)
	}
	home, err := codexhome.Resolve(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeState{home: home, paths: paths, manager: vault.New(paths.Vault, testSecretStore{filepath.Join(appHome, "test-keys")})}
}

func lockingDocument(t *testing.T, account string) authschema.Document {
	t.Helper()
	raw := json.RawMessage(`{"auth_mode":"chatgpt","tokens":{"id_token":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"account-` + account + `"},"last_refresh":"2026-08-20T00:00:00Z"}`)
	document, err := authschema.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func seedLockingRuntime(t *testing.T, runtime runtimeState) {
	t.Helper()
	if _, err := runtime.manager.Init(); err != nil {
		t.Fatal(err)
	}
	lock, err := runtime.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	for _, alias := range []string{"a", "b"} {
		document := lockingDocument(t, alias)
		if _, err := addDocument(runtime.manager, alias, "test", document); err != nil {
			t.Fatal(err)
		}
	}
}

func lockingOptions(runtime runtimeState) *Options {
	runtime.bin = "synthetic-codex-not-executed"
	return &Options{
		AppHome: runtime.paths.Root,
		Input:   strings.NewReader(testBackupPassphrase + "\n"), Output: io.Discard, Error: io.Discard,
		runtimeLoader: func(bool) (runtimeState, error) { return runtime, nil },
		login: func(bool) (authschema.Document, error) {
			return authschema.Parse([]byte(`{"auth_mode":"chatgpt","tokens":{"id_token":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"account-login"}}`))
		},
	}
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
