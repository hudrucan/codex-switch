package switcher

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/authschema"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

func TestSyncImportKeepsNewProfileWhenAliasMatchesExistingID(t *testing.T) {
	for _, casing := range []string{"exact", "case-folded"} {
		t.Run(casing, func(t *testing.T) {
			service, manager, data := testService(t)
			documentA := collisionDocument(t, "account-a", "workspace-a", "a", "2026-08-20T00:00:00Z")
			documentB := collisionDocument(t, "account-b", "workspace-b", "b", "2026-08-20T01:00:00Z")
			profileA := vault.NewProfile("existing-a", "test", documentA.Raw, documentA.Tokens.AccountID, documentA.WorkspaceID, documentA.Email, data.UpdatedAt)
			profileA.ID = "000000000000000000000000000000aa"
			data.Profiles = []vault.Profile{profileA}
			if err := manager.Save(data); err != nil {
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
			if err := service.Home.WriteAuth(documentB.Raw); err != nil {
				t.Fatal(err)
			}
			liveHash, err := service.Home.AuthHash()
			if err != nil {
				t.Fatal(err)
			}
			if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: profileA.ID, AuthHash: "stale-synthetic-hash"}); err != nil {
				t.Fatal(err)
			}
			result, err := service.Sync(SyncOptions{Alias: alias})
			if err != nil || !result.Imported || !result.CredentialsUpdated || !result.StateRepaired || result.ProfileID == "" || result.ProfileID == profileA.ID || result.Alias != alias {
				t.Fatal("Sync --as returned the existing profile instead of the newly imported profile")
			}
			loaded, err := manager.Load()
			if err != nil || len(loaded.Profiles) != 2 {
				t.Fatal("Sync did not persist the new profile")
			}
			savedA, err := loaded.FindByID(profileA.ID)
			if err != nil {
				t.Fatal(err)
			}
			afterA, err := json.Marshal(savedA)
			if err != nil || !bytes.Equal(beforeA, afterA) {
				t.Fatal("Sync --as changed the existing profile")
			}
			savedB, err := loaded.FindByID(result.ProfileID)
			if err != nil || savedB.Alias != alias || savedB.Source != "sync" || !bytes.Equal(savedB.Auth, documentB.Raw) || savedB.AccountID != documentB.Tokens.AccountID || savedB.WorkspaceID != documentB.WorkspaceID || savedB.Email != documentB.Email {
				t.Fatal("Sync mixed credentials or metadata between profiles")
			}
			state, err := appstate.Load(service.Paths.State)
			if err != nil || state.ActiveProfileID != savedB.ID || state.AuthHash != liveHash {
				t.Fatal("Sync --as recorded the existing profile as active")
			}
			currentHash, err := service.Home.AuthHash()
			if err != nil || currentHash != liveHash {
				t.Fatal("Sync --as modified live credentials")
			}
			observation, err := service.Observe()
			if err != nil || observation.ProfileID != savedB.ID || observation.RecordedAlias != alias || observation.State != AccountStateInSync {
				t.Fatal("Sync --as did not identify the newly imported profile")
			}
		})
	}
}

func collisionDocument(t *testing.T, account, workspace, refresh, stamp string) authschema.Document {
	t.Helper()
	claims, err := json.Marshal(map[string]string{"workspace_id": workspace, "email": workspace + "@example.invalid"})
	if err != nil {
		t.Fatal("could not encode synthetic claims")
	}
	raw, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt", "last_refresh": stamp,
		"tokens": map[string]string{
			"account_id": account, "id_token": "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature",
			"access_token": "synthetic-access-" + refresh, "refresh_token": "synthetic-refresh-" + refresh,
		},
	})
	if err != nil {
		t.Fatal("could not encode synthetic credentials")
	}
	document, err := authschema.Parse(raw)
	if err != nil {
		t.Fatal("invalid synthetic credentials")
	}
	return document
}

func collisionService(t *testing.T, upperAlias, sameAccount bool) (Service, vault.Profile, vault.Profile, authschema.Document) {
	t.Helper()
	service, manager, data := testService(t)
	lock, err := service.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	saved := collisionDocument(t, "account-a", "workspace-a", "a-old", "2026-08-20T00:00:00Z")
	live := collisionDocument(t, "account-a", "workspace-a", "a-new", "2026-08-20T01:00:00Z")
	otherAccount := "account-b"
	if sameAccount {
		otherAccount = "account-a"
	}
	other := collisionDocument(t, otherAccount, "workspace-b", "b-original", "2026-08-20T00:00:00Z")
	profileA := vault.NewProfile("profile-a", "test", saved.Raw, saved.Tokens.AccountID, saved.WorkspaceID, saved.Email, time.Time{})
	profileA.ID = "000000000000000000000000000000aa"
	aliasB := profileA.ID
	if upperAlias {
		aliasB = strings.ToUpper(aliasB)
	}
	profileB := vault.NewProfile(aliasB, "test", other.Raw, other.Tokens.AccountID, other.WorkspaceID, other.Email, time.Time{})
	profileB.ID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	data.Profiles = []vault.Profile{profileB, profileA}
	if err := manager.Save(data); err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.Load()
	if err != nil || len(loaded.Profiles) != 2 || loaded.Profiles[0].ID != profileB.ID {
		t.Fatal("fixture did not preserve the colliding alias before the intended ID")
	}
	if err := service.Home.WriteAuth(live.Raw); err != nil {
		t.Fatal(err)
	}
	if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: profileA.ID, AuthHash: "stale-synthetic-hash"}); err != nil {
		t.Fatal(err)
	}
	return service, profileA, profileB, live
}

func TestObserveRecordedAliasUsesExactProfileID(t *testing.T) {
	for _, upperAlias := range []bool{false, true} {
		for _, hasLive := range []bool{false, true} {
			for _, targetRemoved := range []bool{false, true} {
				t.Run(collisionCase(upperAlias, false)+"/"+map[bool]string{false: "logged-out", true: "live"}[hasLive]+"/"+map[bool]string{false: "present", true: "removed"}[targetRemoved], func(t *testing.T) {
					service, profileA, profileB, _ := collisionService(t, upperAlias, false)
					if !hasLive {
						if err := service.Home.DeleteAuth(); err != nil {
							t.Fatal(err)
						}
					}
					if targetRemoved {
						data, err := service.Vault.Load()
						if err != nil {
							t.Fatal(err)
						}
						data.Profiles = []vault.Profile{profileB}
						if err := service.Vault.Save(data); err != nil {
							t.Fatal(err)
						}
					}
					before, err := service.Vault.Load()
					if err != nil {
						t.Fatal(err)
					}
					stateBefore, err := appstate.Load(service.Paths.State)
					if err != nil {
						t.Fatal(err)
					}
					observation, err := service.Observe()
					if err != nil || observation.RecordedProfileID != profileA.ID {
						t.Fatal("observation did not retain the recorded internal ID")
					}
					alias := profileA.Alias
					if targetRemoved {
						alias = ""
					}
					if observation.RecordedAlias != alias {
						t.Fatal("recorded ID was resolved through another profile's alias")
					}
					if hasLive && !targetRemoved && (!observation.Managed || observation.ProfileID != profileA.ID || observation.Alias != profileA.Alias || observation.WorkspaceID != profileA.WorkspaceID) {
						t.Fatal("live observation was redirected to the alias owner")
					}
					after, err := service.Vault.Load()
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("read-only observation changed vault profiles")
					}
					stateAfter, err := appstate.Load(service.Paths.State)
					if err != nil || stateAfter != stateBefore {
						t.Fatal("read-only observation changed recorded state")
					}
				})
			}
		}
	}
}

func collisionCase(upperAlias, sameAccount bool) string {
	name := "exact-alias"
	if upperAlias {
		name = "case-folded-alias"
	}
	if sameAccount {
		name += "/same-account-different-workspace"
	} else {
		name += "/different-accounts"
	}
	return name
}

func TestUseAndSyncReconcileExactObservedProfileID(t *testing.T) {
	for _, action := range []string{"use", "sync"} {
		for _, upperAlias := range []bool{false, true} {
			for _, sameAccount := range []bool{false, true} {
				t.Run(action+"/"+collisionCase(upperAlias, sameAccount), func(t *testing.T) {
					service, profileA, profileB, live := collisionService(t, upperAlias, sameAccount)
					liveHash, err := service.Home.AuthHash()
					if err != nil {
						t.Fatal(err)
					}
					if action == "use" {
						// Staying on the live account requires no process-guard bypass
						// and must not re-project older saved credentials.
						result, err := service.Use(strings.ToUpper(profileA.Alias), false)
						if err != nil || result.Changed || !result.CredentialsUpdated || !result.StateRepaired || result.ProfileID != profileA.ID || result.Alias != profileA.Alias {
							t.Fatal("Use did not reconcile the observed profile by its exact ID")
						}
					} else {
						result, err := service.Sync(SyncOptions{})
						if err != nil || !result.Changed || !result.CredentialsUpdated || !result.StateRepaired || result.ProfileID != profileA.ID || result.Alias != profileA.Alias {
							t.Fatal("Sync did not reconcile the observed profile by its exact ID")
						}
					}
					loaded, err := service.Vault.Load()
					if err != nil {
						t.Fatal(err)
					}
					updatedA, err := loaded.FindByID(profileA.ID)
					if err != nil || string(updatedA.Auth) != string(live.Raw) || updatedA.ID != profileA.ID || updatedA.Alias != profileA.Alias || updatedA.AccountID != profileA.AccountID || updatedA.WorkspaceID != live.WorkspaceID || updatedA.Email != live.Email {
						t.Fatal("profile A did not retain its credentials and workspace metadata")
					}
					unchangedB, err := loaded.FindByID(profileB.ID)
					if err != nil || !reflect.DeepEqual(*unchangedB, profileB) {
						t.Fatal("profile B received another profile's credentials or metadata")
					}
					state, err := appstate.Load(service.Paths.State)
					if err != nil || state.ActiveProfileID != profileA.ID || state.AuthHash != liveHash {
						t.Fatal("reconciliation attributed active state to the alias owner")
					}
					currentHash, err := service.Home.AuthHash()
					if err != nil || currentHash != liveHash {
						t.Fatal("reconciliation replaced the live credentials")
					}
					observation, err := service.Observe()
					if err != nil || observation.ProfileID != profileA.ID || observation.RecordedAlias != profileA.Alias || observation.State != AccountStateInSync {
						t.Fatal("reconciled observation no longer identifies profile A")
					}
				})
			}
		}
	}
}
