package filelock

import (
	"path/filepath"
	"testing"
)

func TestOperationReleasesVaultWhenHomeLockFails(t *testing.T) {
	root := t.TempDir()
	vaultPath := filepath.Join(root, "vault.enc")
	homeLock, err := Acquire(filepath.Join(root, ".codex-switch.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = homeLock.Close() }()
	if _, err := AcquireOperation(vaultPath, root); err == nil {
		t.Fatal("expected home lock contention")
	}
	vaultLock, err := Acquire(vaultPath + ".lock")
	if err != nil {
		t.Fatal("failed operation retained the vault lock")
	}
	if err := vaultLock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationReleasesVaultWhenHomeCannotBeOpened(t *testing.T) {
	root := t.TempDir()
	vaultPath := filepath.Join(root, "vault.enc")
	if _, err := AcquireOperation(vaultPath, filepath.Join(root, "missing")); err == nil {
		t.Fatal("expected missing home to fail")
	}
	lock, err := AcquireOperation(vaultPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// Both locks must be reusable after closing a successful operation.
	lock, err = AcquireOperation(vaultPath, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}
