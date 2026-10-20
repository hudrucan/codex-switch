package app

import (
	"github.com/SilkageNet/codex-switch/internal/accountusage"
	"github.com/SilkageNet/codex-switch/internal/codexhome"
	"github.com/SilkageNet/codex-switch/internal/codexlogin"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
	appconfig "github.com/SilkageNet/codex-switch/internal/config"
	"github.com/SilkageNet/codex-switch/internal/filelock"
	"github.com/SilkageNet/codex-switch/internal/secretstore"
	"github.com/SilkageNet/codex-switch/internal/switcher"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

type runtimeState struct {
	home        codexhome.Home
	paths       appconfig.Paths
	manager     *vault.Manager
	bin         string
	binErr      error
	usageRunner accountusage.QueryRunner
}

func (options *Options) loadRuntime(create bool) (runtimeState, error) {
	if options.runtimeLoader != nil {
		return options.runtimeLoader(create)
	}
	home, err := codexhome.Resolve(options.CodexHome)
	if err != nil {
		return runtimeState{}, err
	}
	paths, err := appconfig.ResolvePaths(options.AppHome)
	if err != nil {
		return runtimeState{}, err
	}
	if create {
		if err := paths.Ensure(); err != nil {
			return runtimeState{}, err
		}
		if err := home.Ensure(); err != nil {
			return runtimeState{}, err
		}
	}
	store, err := secretstore.Open()
	if err != nil {
		return runtimeState{}, err
	}
	manager := vault.New(paths.Vault, store)
	if create {
		if _, err := manager.Init(); err != nil {
			return runtimeState{}, err
		}
	}
	bin, binErr := codexlogin.FindBinary(options.CodexBin)
	return runtimeState{home: home, paths: paths, manager: manager, bin: bin, binErr: binErr}, nil
}

func (options *Options) loadRuntimeForDoctor() (runtimeState, error) {
	home, err := codexhome.Resolve(options.CodexHome)
	if err != nil {
		return runtimeState{}, err
	}
	paths, err := appconfig.ResolvePaths(options.AppHome)
	if err != nil {
		return runtimeState{}, err
	}
	store, storeErr := secretstore.Open()
	if storeErr != nil {
		return runtimeState{}, storeErr
	}
	bin, binErr := codexlogin.FindBinary(options.CodexBin)
	return runtimeState{home: home, paths: paths, manager: vault.New(paths.Vault, store), bin: bin, binErr: binErr}, nil
}

func (runtime runtimeState) acquireLock() (*filelock.Operation, error) {
	return filelock.AcquireOperation(runtime.paths.Vault, runtime.home.Path)
}

func (runtime runtimeState) acquireVaultLock() (*filelock.Lock, error) {
	return filelock.AcquireVault(runtime.paths.Vault)
}

func (runtime runtimeState) switcher() switcher.Service {
	return switcher.Service{Home: runtime.home, Paths: runtime.paths, Vault: runtime.manager}
}

func (runtime runtimeState) usageService(version string) accountusage.Service {
	runner := runtime.usageRunner
	if runner == nil {
		runner = codexusage.Runner{
			Binary: runtime.bin, BinaryError: runtime.binErr, ClientVersion: version,
		}
	}
	return accountusage.Service{
		Home:   runtime.home,
		Paths:  runtime.paths,
		Vault:  runtime.manager,
		Runner: runner,
	}
}

func (options *Options) currentView() (accountView, error) {
	runtime, err := options.loadRuntime(false)
	if err != nil {
		return accountView{}, err
	}
	observation, err := runtime.switcher().Observe()
	if err != nil {
		return accountView{}, err
	}
	return viewFromObservation(observation)
}
