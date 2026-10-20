package switcher

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/authschema"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

func TestWorkspaceContinuityPrecedesGenerationPreference(t *testing.T) {
	for _, test := range []struct {
		name, savedWorkspace, profileWorkspace, liveWorkspace string
		wantError                                             bool
	}{
		{"same", "workspace-a", "workspace-a", "workspace-a", false},
		{"missing", "workspace-a", "workspace-a", "", true},
		{"different", "workspace-a", "workspace-a", "workspace-b", true},
		{"persisted-stronger", "", "workspace-a", "", true},
		{"document-stronger", "workspace-a", "", "", true},
		{"newly-known", "", "", "workspace-a", false},
		{"both-unknown", "", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved := collisionDocument(t, "account-a", test.savedWorkspace, "old", "2026-08-20T00:00:00Z")
			live := collisionDocument(t, "account-a", test.liveWorkspace, "new", "2026-08-20T01:00:00Z")
			profile := vault.NewProfile("a", "test", saved.Raw, "account-a", test.profileWorkspace, saved.Email, time.Time{})
			before := append([]byte(nil), profile.Auth...)
			_, _, err := updateProfileFromLive(&profile, live, true)
			if test.wantError {
				if !errors.Is(err, authschema.ErrIdentityMismatch) || !bytes.Equal(before, profile.Auth) || profile.WorkspaceID != test.profileWorkspace {
					t.Fatal("generation preference bypassed workspace continuity")
				}
			} else if err != nil || profile.WorkspaceID != test.liveWorkspace || !bytes.Equal(profile.Auth, live.Raw) {
				t.Fatal("valid workspace continuity was rejected")
			}
		})
	}
}

func TestWorkspaceMismatchLeavesAllStateUnchanged(t *testing.T) {
	for _, action := range []string{"use", "sync-prefer-live", "deactivate"} {
		t.Run(action, func(t *testing.T) {
			service, manager, data := testService(t)
			saved := collisionDocument(t, "account-a", "workspace-a", "old", "2026-08-20T00:00:00Z")
			live := collisionDocument(t, "account-a", "", "new", "2026-08-20T01:00:00Z")
			profile := vault.NewProfile("a", "test", saved.Raw, "account-a", "workspace-a", saved.Email, time.Time{})
			if err := data.Add(profile, false); err != nil {
				t.Fatal(err)
			}
			profile = data.Profiles[0]
			if err := manager.Save(data); err != nil {
				t.Fatal(err)
			}
			if err := service.Home.WriteAuth(live.Raw); err != nil {
				t.Fatal(err)
			}
			if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: profile.ID, AuthHash: "stale"}); err != nil {
				t.Fatal(err)
			}
			paths := []string{service.Paths.Vault, service.Paths.State, service.Home.AuthPath}
			before := make([][]byte, len(paths))
			for i, path := range paths {
				var err error
				before[i], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			observation, err := service.Observe()
			if err != nil || observation.Managed || observation.WorkspaceID != "" || observation.State != AccountStateAmbiguous {
				t.Fatal("observation invented missing workspace evidence")
			}
			switch action {
			case "use":
				_, err = service.Use("a", false)
			case "sync-prefer-live":
				_, err = service.Sync(SyncOptions{PreferLive: true})
			case "deactivate":
				err = service.Deactivate(true)
			}
			if err == nil {
				t.Fatal("identity mismatch allowed a mutation")
			}
			for i, path := range paths {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before[i], after) {
					t.Fatal("failed identity check changed persistent state")
				}
			}
		})
	}
}

func TestExactTokenMaterialCannotHidePersistedWorkspace(t *testing.T) {
	live := collisionDocument(t, "account-a", "", "same", "2026-08-20T00:00:00Z")
	profile := vault.NewProfile("a", "test", live.Raw, "account-a", "workspace-a", "", time.Time{})
	data := vault.Data{Profiles: []vault.Profile{profile}}
	if selected, err := identifyCurrent(&data, live); selected != nil || !errors.Is(err, authschema.ErrIdentityMismatch) {
		t.Fatal("exact material bypassed stronger persisted identity")
	}
	if _, _, err := updateProfileFromLive(&profile, live, true); !errors.Is(err, authschema.ErrIdentityMismatch) {
		t.Fatal("same generation bypassed stronger persisted identity")
	}
}

func TestMissingWorkspaceCannotSelectUnknownFallbackAmongKnownProfiles(t *testing.T) {
	known := collisionDocument(t, "account-a", "workspace-a", "known", "2026-08-20T00:00:00Z")
	unknown := collisionDocument(t, "account-a", "", "unknown", "2026-08-20T00:00:00Z")
	live := collisionDocument(t, "account-a", "", "new", "2026-08-20T01:00:00Z")
	data := vault.Data{Profiles: []vault.Profile{
		vault.NewProfile("known", "test", known.Raw, "account-a", "workspace-a", "", time.Time{}),
		vault.NewProfile("unknown", "test", unknown.Raw, "account-a", "", "", time.Time{}),
	}}
	if selected, err := identifyCurrent(&data, live); selected != nil || !errors.Is(err, authschema.ErrIdentityMismatch) {
		t.Fatal("missing evidence redirected a refresh to an unknown-workspace profile")
	}
	// Exact material may still identify an otherwise valid unknown workspace.
	if selected, err := identifyCurrent(&data, unknown); err != nil || selected == nil || selected.Alias != "unknown" {
		t.Fatal("unambiguous exact-material compatibility was lost")
	}
}
