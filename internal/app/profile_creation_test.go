package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/authschema"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

func creationDocument(t *testing.T, account, workspace string) authschema.Document {
	t.Helper()
	document := lockingDocument(t, account)
	var value map[string]any
	if err := json.Unmarshal(document.Raw, &value); err != nil {
		t.Fatal("invalid synthetic authentication fixture")
	}
	claims, _ := json.Marshal(map[string]string{"workspace_id": workspace, "email": account + "@example.invalid"})
	tokens := value["tokens"].(map[string]any)
	tokens["id_token"] = "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	tokens["access_token"], tokens["refresh_token"] = "synthetic-access-"+account, "synthetic-refresh-"+account
	raw, _ := json.Marshal(value)
	document, err := authschema.Parse(raw)
	if err != nil {
		t.Fatal("invalid synthetic credentials")
	}
	return document
}

func TestCreatedProfileAliasDoesNotResolveExistingID(t *testing.T) {
	for _, action := range []string{"addDocument", "import-current"} {
		for _, casing := range []string{"exact", "case-folded"} {
			t.Run(action+"/"+casing, func(t *testing.T) {
				root := t.TempDir()
				runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "codex"))
				data, err := runtime.manager.Init()
				if err != nil {
					t.Fatal(err)
				}
				documentA := creationDocument(t, "a", "workspace-a")
				documentB := creationDocument(t, "b", "workspace-b")
				profileA := vault.NewProfile("existing-a", "test", documentA.Raw, documentA.Tokens.AccountID, documentA.WorkspaceID, documentA.Email, data.UpdatedAt)
				profileA.ID = "000000000000000000000000000000aa"
				data.Profiles = []vault.Profile{profileA}
				if err := runtime.manager.Save(data); err != nil {
					t.Fatal(err)
				}
				beforeA, err := json.Marshal(profileA)
				if err != nil {
					t.Fatal("could not snapshot the existing synthetic profile")
				}
				alias := profileA.ID
				if casing == "case-folded" {
					alias = strings.ToUpper(alias)
				}
				if err := runtime.home.WriteAuth(documentB.Raw); err != nil {
					t.Fatal(err)
				}
				liveHash, err := runtime.home.AuthHash()
				if err != nil {
					t.Fatal(err)
				}
				var returned vault.Profile
				if action == "addDocument" {
					lock, err := runtime.acquireVaultLock()
					if err != nil {
						t.Fatal(err)
					}
					returned, err = addDocument(runtime.manager, alias, "test-new", documentB)
					_ = lock.Close()
					if err != nil {
						t.Fatal(err)
					}
				} else {
					options := lockingOptions(runtime)
					var output bytes.Buffer
					options.JSON, options.Output = true, &output
					command := newAccountImportCommand(options)
					command.SetArgs([]string{alias})
					command.SilenceErrors, command.SilenceUsage = true, true
					if err := command.Execute(); err != nil {
						t.Fatal(err)
					}
					var view accountView
					if err := json.Unmarshal(output.Bytes(), &view); err != nil || !view.Active || view.Alias != alias || view.AccountID != documentB.Tokens.AccountID || view.WorkspaceID != documentB.WorkspaceID || view.Email != documentB.Email {
						t.Fatal("import-current returned another account's metadata")
					}
					returned.ID = view.ID
					if bytes.Contains(output.Bytes(), []byte("synthetic-refresh-")) || bytes.Contains(output.Bytes(), []byte("synthetic-access-")) {
						t.Fatal("import-current exposed credentials")
					}
				}
				loaded, err := runtime.manager.Load()
				if err != nil || len(loaded.Profiles) != 2 {
					t.Fatal("the new profile was not persisted")
				}
				savedA, err := loaded.FindByID(profileA.ID)
				if err != nil {
					t.Fatal(err)
				}
				afterA, err := json.Marshal(savedA)
				if err != nil || !bytes.Equal(beforeA, afterA) {
					t.Fatal("creation changed the existing profile")
				}
				if returned.ID == "" || returned.ID == profileA.ID {
					t.Fatal("post-creation resolution returned the existing profile by ID")
				}
				savedB, err := loaded.FindByID(returned.ID)
				if err != nil || savedB.Alias != alias || !bytes.Equal(savedB.Auth, documentB.Raw) || savedB.AccountID != documentB.Tokens.AccountID || savedB.WorkspaceID != documentB.WorkspaceID || savedB.Email != documentB.Email {
					t.Fatal("the created profile did not retain its own credentials and metadata")
				}
				if action == "addDocument" {
					encoded, err := json.Marshal(returned)
					persisted, savedErr := json.Marshal(savedB)
					if err != nil || savedErr != nil || !bytes.Equal(encoded, persisted) || returned.Source != "test-new" {
						t.Fatal("addDocument did not return the newly persisted profile")
					}
				} else {
					state, err := appstate.Load(runtime.paths.State)
					if err != nil || state.ActiveProfileID != savedB.ID || state.AuthHash != liveHash || savedB.Source != "import-current" {
						t.Fatal("import-current recorded another profile as active")
					}
					observation, err := runtime.switcher().Observe()
					if err != nil || observation.ProfileID != savedB.ID || observation.RecordedAlias != alias {
						t.Fatal("import-current did not identify the newly imported account")
					}
				}
				currentHash, err := runtime.home.AuthHash()
				if err != nil || currentHash != liveHash {
					t.Fatal("profile creation changed the live credentials")
				}
			})
		}
	}
}
