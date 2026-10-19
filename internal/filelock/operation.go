package filelock

import "path/filepath"

// Operation holds the vault lock before the live-home lock. Writers that use
// live authentication or related state hold both through the read-modify-write.
type Operation struct {
	vault *Lock
	home  *Lock
}

// AcquireVault serializes vault-only transactions without accessing CODEX_HOME.
func AcquireVault(vaultPath string) (*Lock, error) {
	return Acquire(vaultPath + ".lock")
}

func AcquireOperation(vaultPath, codexHome string) (*Operation, error) {
	vault, err := AcquireVault(vaultPath)
	if err != nil {
		return nil, err
	}
	home, err := Acquire(filepath.Join(codexHome, ".codex-switch.lock"))
	if err != nil {
		_ = vault.Close()
		return nil, err
	}
	return &Operation{vault: vault, home: home}, nil
}

func (operation *Operation) Close() error {
	homeErr := operation.home.Close()
	vaultErr := operation.vault.Close()
	if homeErr != nil {
		return homeErr
	}
	return vaultErr
}
