package accountusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexhome"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
	appconfig "github.com/SilkageNet/codex-switch/internal/config"
	"github.com/SilkageNet/codex-switch/internal/secretstore"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

type fakeRunner struct {
	snapshot codexusage.Snapshot
	auth     json.RawMessage
	err      error
}

type retryRunner struct {
	calls    int
	failures int
	snapshot codexusage.Snapshot
	auth     json.RawMessage
	inputs   []json.RawMessage
}

func (runner *retryRunner) Query(_ context.Context, auth json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
	runner.calls++
	runner.inputs = append(runner.inputs, append(json.RawMessage(nil), auth...))
	if runner.calls <= runner.failures {
		return codexusage.Snapshot{}, runner.auth, errors.New("temporary connection failure")
	}
	return runner.snapshot, runner.auth, nil
}

func (runner fakeRunner) Query(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
	return runner.snapshot, runner.auth, runner.err
}

func TestRefreshCachesUsageAndAdoptsRotatedCredentials(t *testing.T) {
	service, manager, profile := testService(t, false)
	updated := authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z")
	lifetime := int64(1234)
	service.Runner = fakeRunner{
		snapshot: codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro", TokenUsage: &codexusage.TokenUsage{Summary: codexusage.TokenUsageSummary{LifetimeTokens: &lifetime}}},
		auth:     updated,
	}
	results, err := service.Refresh(context.Background(), []string{profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	if results[profile.ID].Error != "" {
		t.Fatalf("unexpected refresh warning: %s", results[profile.ID].Error)
	}
	loaded, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := loaded.Find(profile.ID)
	document, _ := authschema.Parse(saved.Auth)
	if document.Tokens.RefreshToken != "refresh-new" {
		t.Fatal("rotated credential was not saved")
	}
	cache, err := service.Cached()
	if err != nil || cache.Profiles[profile.ID].PlanType != "pro" {
		t.Fatalf("usage was not cached: %#v, %v", cache, err)
	}
}

func TestRefreshCollectsAllQueriesBeforePersistingMixedResults(t *testing.T) {
	service, manager, first := testService(t, false)
	data, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	second := vault.NewProfile("b", "test", authBytes("account-b", "refresh-old", "2026-08-20T00:00:00Z"), "account-b", "", "", time.Time{})
	second.ID = "synthetic-second-profile"
	if err := data.Add(second, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(data); err != nil {
		t.Fatal(err)
	}
	var queried []string
	service.queryAttempts = 1
	service.Runner = queryFunc(func(_ context.Context, raw json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
		loaded, err := manager.Load()
		if err != nil {
			t.Fatal(err)
		}
		for _, original := range []vault.Profile{first, second} {
			saved, err := loaded.FindByID(original.ID)
			if err != nil || !bytes.Equal(saved.Auth, original.Auth) {
				t.Fatal("credentials were persisted before all queries completed")
			}
		}
		document, err := authschema.Parse(raw)
		if err != nil {
			t.Fatal("query received invalid synthetic credentials")
		}
		account := document.Tokens.AccountID
		queried = append(queried, account)
		candidate := authBytes(account, "refresh-new", "2026-08-20T01:00:00Z")
		if account == second.AccountID {
			return codexusage.Snapshot{}, candidate, errors.New("temporary connection failure")
		}
		return codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro"}, candidate, nil
	})
	results, err := service.Refresh(context.Background(), []string{first.ID, second.ID, first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(queried, ",") != "account-a,account-b" || len(results) != 2 || results[first.ID].Error != "" || results[second.ID].Error == "" {
		t.Fatal("query selection or mixed result attribution changed")
	}
	loaded, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range []vault.Profile{first, second} {
		saved, err := loaded.FindByID(original.ID)
		if err != nil {
			t.Fatal(err)
		}
		document, err := authschema.Parse(saved.Auth)
		if err != nil || document.Tokens.AccountID != original.AccountID || document.Tokens.RefreshToken != "refresh-new" {
			t.Fatal("a query result lost or misattributed its refreshed credentials")
		}
	}
	cache, err := service.Cached()
	if err != nil || len(cache.Profiles) != 1 || cache.Profiles[first.ID].PlanType != "pro" || cache.Failures[second.ID].Message != results[second.ID].Error {
		t.Fatal("mixed success and failure cache state changed")
	}
}

func TestRefreshRetriesAndClearsPersistedFailure(t *testing.T) {
	service, _, profile := testService(t, false)
	runner := &retryRunner{
		failures: 2,
		snapshot: codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro"},
		auth:     authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z"),
	}
	service.Runner = runner
	service.retryBaseDelay = time.Nanosecond
	results, err := service.Refresh(context.Background(), []string{profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 3 || results[profile.ID].Attempts != 3 || results[profile.ID].Error != "" {
		t.Fatalf("unexpected retry result: calls=%d result=%#v", runner.calls, results[profile.ID])
	}
	retriedWith, err := authschema.Parse(runner.inputs[1])
	if err != nil || retriedWith.Tokens.RefreshToken != "refresh-new" {
		t.Fatal("retry did not use rotated credentials")
	}
	cache, err := service.Cached()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Failures[profile.ID]; ok {
		t.Fatalf("successful retry retained failure: %#v", cache.Failures[profile.ID])
	}
}

func TestRefreshPersistsFailureDetails(t *testing.T) {
	service, manager, profile := testService(t, false)
	runner := &retryRunner{failures: 3, auth: authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z")}
	service.Runner = runner
	service.retryBaseDelay = time.Nanosecond
	results, err := service.Refresh(context.Background(), []string{profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := results[profile.ID]
	if result.Attempts != 3 || !strings.Contains(result.Error, "temporary connection failure") {
		t.Fatalf("unexpected failure result: %#v", result)
	}
	cache, err := service.Cached()
	if err != nil {
		t.Fatal(err)
	}
	failure := cache.Failures[profile.ID]
	if failure.Attempts != 3 || failure.AttemptedAt.IsZero() || failure.Message != result.Error {
		t.Fatalf("failure was not persisted: %#v", failure)
	}
	data, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := data.Find(profile.ID)
	document, _ := authschema.Parse(saved.Auth)
	if document.Tokens.RefreshToken != "refresh-new" {
		t.Fatal("rotated credentials from failed queries were not preserved")
	}
}

func TestRefreshProjectsRotatedCredentialsForActiveProfile(t *testing.T) {
	service, manager, profile := testService(t, true)
	service.Runner = fakeRunner{
		snapshot: codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "plus"},
		auth:     authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z"),
	}
	results, err := service.Refresh(context.Background(), []string{profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	if results[profile.ID].Error != "" {
		t.Fatalf("unexpected refresh warning: %s", results[profile.ID].Error)
	}
	live, err := service.Home.ReadAuth()
	if err != nil {
		t.Fatal(err)
	}
	liveDocument, _ := authschema.Parse(live)
	if liveDocument.Tokens.RefreshToken != "refresh-new" {
		t.Fatal("active credential projection was not refreshed")
	}
	loaded, _ := manager.Load()
	saved, _ := loaded.Find(profile.ID)
	savedDocument, _ := authschema.Parse(saved.Auth)
	if savedDocument.Tokens.RefreshToken != "refresh-new" {
		t.Fatal("active vault credential was not refreshed")
	}
}

func TestRefreshRejectsCredentialsForDifferentAccount(t *testing.T) {
	service, manager, profile := testService(t, false)
	service.Runner = fakeRunner{
		snapshot: codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro"},
		auth:     authBytes("account-other", "refresh-new", "2026-08-20T01:00:00Z"),
	}
	results, err := service.Refresh(context.Background(), []string{profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	if results[profile.ID].Error == "" {
		t.Fatal("expected credential identity warning")
	}
	loaded, _ := manager.Load()
	saved, _ := loaded.Find(profile.ID)
	document, _ := authschema.Parse(saved.Auth)
	if document.Tokens.RefreshToken != "refresh-old" {
		t.Fatal("mismatched credential was saved")
	}
	cache, cacheErr := service.Cached()
	if cacheErr != nil {
		t.Fatal(cacheErr)
	}
	if _, ok := cache.Profiles[profile.ID]; ok {
		t.Fatal("usage for mismatched credentials was cached")
	}
}

func TestRefreshUsesLiveAccountWhenRecordedStateIsStale(t *testing.T) {
	service, manager, profile := testService(t, false)
	data, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	otherDocument, err := authschema.Parse(authBytes("account-b", "refresh-b", "2026-08-20T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	updatedAt, _ := otherDocument.GenerationTime()
	other := vault.NewProfile("b", "test", otherDocument.Raw, "account-b", "", "b@example.com", updatedAt)
	if err := data.Add(other, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(data); err != nil {
		t.Fatal(err)
	}
	savedOther, _ := data.Find("b")
	if err := service.Home.WriteAuth(profile.Auth); err != nil {
		t.Fatal(err)
	}
	if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: savedOther.ID, AuthHash: "stale"}); err != nil {
		t.Fatal(err)
	}
	service.Runner = fakeRunner{
		snapshot: codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro"},
		auth:     authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z"),
	}
	results, err := service.Refresh(context.Background(), []string{profile.ID})
	if err != nil {
		t.Fatal(err)
	}
	if results[profile.ID].Error != "" {
		t.Fatalf("unexpected refresh warning: %s", results[profile.ID].Error)
	}
	live, _ := service.Home.ReadAuth()
	liveDocument, _ := authschema.Parse(live)
	if liveDocument.Tokens.RefreshToken != "refresh-new" {
		t.Fatal("actual active profile was not refreshed")
	}
	state, _ := appstate.Load(service.Paths.State)
	if state.ActiveProfileID != profile.ID {
		t.Fatalf("stale active state was not repaired: %#v", state)
	}
}

func testService(t *testing.T, active bool) (Service, *vault.Manager, vault.Profile) {
	t.Helper()
	root := t.TempDir()
	home, err := codexhome.Resolve(filepath.Join(root, "codex"))
	if err != nil || home.Ensure() != nil {
		t.Fatal(err)
	}
	paths, err := appconfig.ResolvePaths(filepath.Join(root, "switch"))
	if err != nil || paths.Ensure() != nil {
		t.Fatal(err)
	}
	manager := vault.New(paths.Vault, secretstore.NewMemoryStore())
	data, err := manager.Init()
	if err != nil {
		t.Fatal(err)
	}
	document, err := authschema.Parse(authBytes("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	updatedAt, _ := document.GenerationTime()
	profile := vault.NewProfile("a", "test", document.Raw, "account-a", "", "a@example.com", updatedAt)
	if err := data.Add(profile, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(data); err != nil {
		t.Fatal(err)
	}
	saved, _ := data.Find("a")
	if active {
		if err := home.WriteAuth(saved.Auth); err != nil {
			t.Fatal(err)
		}
		hash, _ := home.AuthHash()
		if err := appstate.Save(paths.State, appstate.State{ActiveProfileID: saved.ID, AuthHash: hash}); err != nil {
			t.Fatal(err)
		}
	}
	return Service{Home: home, Paths: paths, Vault: manager}, manager, *saved
}

func authBytes(account, refresh, lastRefresh string) []byte {
	return []byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":%q,"account_id":%q},"last_refresh":%q}`, "id-"+refresh, "access-"+refresh, refresh, account, lastRefresh))
}
