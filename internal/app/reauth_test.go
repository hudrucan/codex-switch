package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
)

func TestPendingReauthValidatesStableTarget(t *testing.T) {
	for _, scenario := range []string{"target-removal", "target-rename", "case-only-rename", "alias-reassignment", "success"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := prepareInteractiveRuntime(t)
			lock, err := runtime.acquireVaultLock()
			if err != nil {
				t.Fatal(err)
			}
			_, addErr := addDocument(runtime.manager, "c", "test", lockingDocument(t, "c"))
			closeErr := lock.Close()
			if addErr != nil || closeErr != nil {
				t.Fatalf("prepare unrelated profile: %v, %v", addErr, closeErr)
			}
			initial, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			target, err := initial.Find("b")
			if err != nil {
				t.Fatal(err)
			}
			document := lockingDocument(t, "reauth-b")
			gate := newPreparationGate()
			options := lockingOptions(runtime)
			options.login = func(bool) (authschema.Document, error) {
				gate.wait()
				return document, nil
			}
			// Alias lookup remains case-insensitive, while the captured alias
			// detects an actual rename during login, including a case-only one.
			finish := startPendingCommand(t, newAccountAddCommand(options, true), []string{"B"}, gate)
			runMutationProcess(t, runtime, "use", false)
			runMutationProcess(t, runtime, "rename", false)
			switch scenario {
			case "target-removal":
				runMutationProcess(t, runtime, "remove-b", false)
			case "target-rename", "alias-reassignment":
				runMutationProcess(t, runtime, "rename-b", false)
				if scenario == "alias-reassignment" {
					runMutationProcess(t, runtime, "reassign-b", false)
				}
			case "case-only-rename":
				runMutationProcess(t, runtime, "rename-b-case", false)
			}
			before, err := atomicfile.FileHash(runtime.paths.Vault)
			if err != nil {
				t.Fatal(err)
			}
			err = finish()
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "reauthentication target changed during login") {
					t.Fatalf("expected target identity conflict, got %v", err)
				}
				after, hashErr := atomicfile.FileHash(runtime.paths.Vault)
				if hashErr != nil || before != after {
					t.Fatal("rejected reauth changed the vault or another profile's credentials")
				}
			}
			loaded, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loaded.Find("renamed"); err != nil {
				t.Fatal("reauth lost an unrelated concurrent update")
			}
			if scenario == "success" {
				saved, err := loaded.Find("b")
				if err != nil {
					t.Fatal(err)
				}
				if saved.ID != target.ID || !saved.CreatedAt.Equal(target.CreatedAt) {
					t.Fatal("successful reauth changed the original profile identity")
				}
				if saved.AccountID != document.Tokens.AccountID || !bytes.Equal(saved.Auth, document.Raw) || saved.Source != "reauth" {
					t.Fatal("successful reauth did not save the validated login")
				}
			}
		})
	}
}

func TestReauthRequiresExistingAliasBeforeLogin(t *testing.T) {
	runtime := prepareInteractiveRuntime(t)
	data, err := runtime.manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	target, err := data.Find("b")
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"missing", " b", target.ID} {
		t.Run(alias, func(t *testing.T) {
			options := lockingOptions(runtime)
			loginCalled := false
			options.login = func(bool) (authschema.Document, error) {
				loginCalled = true
				return lockingDocument(t, "unexpected-login"), nil
			}
			before, err := atomicfile.FileHash(runtime.paths.Vault)
			if err != nil {
				t.Fatal(err)
			}
			command := newAccountAddCommand(options, true)
			command.SetArgs([]string{alias})
			command.SilenceErrors = true
			command.SilenceUsage = true
			if err := command.Execute(); err == nil || loginCalled {
				t.Fatal("reauth must validate an existing alias before starting login")
			}
			after, err := atomicfile.FileHash(runtime.paths.Vault)
			if err != nil || before != after {
				t.Fatal("invalid reauth target changed the vault")
			}
		})
	}
}
