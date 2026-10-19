package accountusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

func TestRefreshResolvesOnlyExactProfileIDs(t *testing.T) {
	for _, selection := range []string{"target", "alias-owner", "both", "alias-only"} {
		for _, queryFails := range []bool{false, true} {
			name := selection + "/success"
			if queryFails {
				name = selection + "/failed-query"
			}
			t.Run(name, func(t *testing.T) {
				service, manager, target := testService(t, false)
				data, err := manager.Load()
				if err != nil {
					t.Fatal(err)
				}
				target.ID = "00000000000000000000000000000001"
				data.Profiles[0] = target
				other := vault.NewProfile(target.ID, "test", authBytes("account-b", "refresh-b", "2026-08-20T00:00:00Z"), "account-b", "", "", time.Time{})
				other.ID = "other-profile-id"
				// The colliding alias sorts before "a" when the vault is saved,
				// deterministically exercising selection and persistence.
				data.Profiles = append([]vault.Profile{other}, data.Profiles...)
				if err := manager.Save(data); err != nil {
					t.Fatal(err)
				}
				ids := []string{target.ID}
				switch selection {
				case "alias-owner":
					ids = []string{other.ID}
				case "both":
					ids = []string{target.ID, other.ID, target.ID}
				case "alias-only":
					ids = []string{target.Alias}
				}
				queried := map[string]int{}
				service.queryAttempts = 1
				service.Runner = queryFunc(func(_ context.Context, input json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
					document, err := authschema.Parse(input)
					if err != nil {
						t.Fatal("query received invalid synthetic credentials")
					}
					account := document.Tokens.AccountID
					queried[account]++
					fresh := authBytes(account, "refresh-new", "2026-08-20T01:00:00Z")
					if queryFails {
						return codexusage.Snapshot{}, fresh, errors.New(retryCanary)
					}
					return codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro"}, fresh, nil
				})
				results, err := service.Refresh(context.Background(), ids)
				if selection == "alias-only" {
					if err == nil || len(queried) != 0 {
						t.Fatal("internal profile ID was interpreted as a user alias")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				loaded, err := manager.Load()
				if err != nil {
					t.Fatal(err)
				}
				for _, original := range []vault.Profile{target, other} {
					selected := selection == "both" || (selection == "target" && original.ID == target.ID) || (selection == "alias-owner" && original.ID == other.ID)
					saved, err := loaded.FindByID(original.ID)
					if err != nil {
						t.Fatal(err)
					}
					if !selected {
						if queried[original.AccountID] != 0 || !bytes.Equal(saved.Auth, original.Auth) {
							t.Fatal("refresh queried or modified an unintended profile")
						}
						continue
					}
					document, err := authschema.Parse(saved.Auth)
					if err != nil || queried[original.AccountID] != 1 || document.Tokens.RefreshToken != "refresh-new" || document.Tokens.AccountID != original.AccountID {
						t.Fatal("intended profile did not retain its own refreshed credentials")
					}
					result, ok := results[original.ID]
					if !ok || result.ProfileID != original.ID || (result.Error != "") != queryFails {
						t.Fatal("refresh results were attributed to the wrong profile")
					}
				}
			})
		}
	}
}
