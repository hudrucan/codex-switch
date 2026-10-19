package accountusage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
)

const retryCanary = "SYNTHETIC_RETRY_SECRET_CANARY_DO_NOT_OUTPUT"

type queryFunc func(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error)

func (query queryFunc) Query(ctx context.Context, raw json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
	return query(ctx, raw)
}

func retryWorkspaceAuth(account, refresh, stamp, workspace string) json.RawMessage {
	var value map[string]any
	_ = json.Unmarshal(authBytes(account, refresh, stamp), &value)
	if workspace != "" {
		claims, _ := json.Marshal(map[string]string{"workspace_id": workspace})
		value["tokens"].(map[string]any)["id_token"] = "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	}
	raw, _ := json.Marshal(value)
	return raw
}

func TestRetryKeepsNewestValidatedGeneration(t *testing.T) {
	original := retryWorkspaceAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z", "workspace-a")
	fresh := retryWorkspaceAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z", "workspace-a")
	candidates := map[string]json.RawMessage{
		"failed-retry":      nil,
		"older":             original,
		"conflicting":       retryWorkspaceAuth("account-a", "refresh-conflict", "2026-08-20T01:00:00Z", "workspace-a"),
		"cross-account":     retryWorkspaceAuth("account-b", "refresh-other", "2026-08-20T02:00:00Z", "workspace-a"),
		"cross-workspace":   retryWorkspaceAuth("account-a", "refresh-other", "2026-08-20T02:00:00Z", "workspace-b"),
		"missing-workspace": retryWorkspaceAuth("account-a", "refresh-other", "2026-08-20T02:00:00Z", ""),
		"corrupt":           json.RawMessage(`{"auth_mode":"` + retryCanary),
	}
	for name, candidate := range candidates {
		t.Run(name, func(t *testing.T) {
			service := Service{queryAttempts: 3, retryBaseDelay: time.Nanosecond}
			calls := 0
			operationErr := errors.New(retryCanary)
			service.Runner = queryFunc(func(_ context.Context, input json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
				calls++
				document, err := authschema.Parse(input)
				if err != nil {
					t.Fatal("retry received invalid credentials")
				}
				if calls > 1 && document.Tokens.RefreshToken != "refresh-new" {
					t.Fatal("retry regressed to stale or unsafe credentials")
				}
				if calls == 1 {
					return codexusage.Snapshot{}, fresh, operationErr
				}
				if calls == 2 {
					return codexusage.Snapshot{}, candidate, operationErr
				}
				return codexusage.Snapshot{}, nil, operationErr
			})
			_, latest, attempts, err := service.queryWithRetry(context.Background(), original)
			if err == nil || !errors.Is(err, operationErr) || calls != 3 || attempts != 3 {
				t.Fatal("retry lost the original operation failure")
			}
			if strings.Contains(err.Error(), retryCanary) {
				t.Fatal("retry leaked diagnostics")
			}
			document, parseErr := authschema.Parse(latest)
			if parseErr != nil || document.Tokens.RefreshToken != "refresh-new" {
				t.Fatal("failed retry discarded validated credentials")
			}
		})
	}
}

func TestCancellationDuringBackoffPreservesCandidate(t *testing.T) {
	service := Service{queryAttempts: 2, retryBaseDelay: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	service.retryWait = func(ctx context.Context, delay time.Duration) error { close(entered); return waitRetry(ctx, delay) }
	operationErr := errors.New(retryCanary)
	service.Runner = queryFunc(func(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
		return codexusage.Snapshot{}, authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z"), operationErr
	})
	done := make(chan struct{})
	var latest json.RawMessage
	var queryErr error
	go func() {
		_, latest, _, queryErr = service.queryWithRetry(ctx, authBytes("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not enter backoff")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled backoff did not return")
	}
	if !errors.Is(queryErr, context.Canceled) || !errors.Is(queryErr, operationErr) {
		t.Fatal("cancellation lost the original failure")
	}
	document, err := authschema.Parse(latest)
	if err != nil || document.Tokens.RefreshToken != "refresh-new" {
		t.Fatal("backoff cancellation discarded credentials")
	}
}

func TestRefreshPersistsCandidateAfterCancellation(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, position := range []string{"backoff", "before-next-attempt"} {
			name := "inactive/" + position
			if active {
				name = "active/" + position
			}
			t.Run(name, func(t *testing.T) {
				service, manager, profile := testService(t, active)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				service.retryWait = func(context.Context, time.Duration) error {
					cancel()
					if position == "before-next-attempt" {
						return nil
					}
					return context.Canceled
				}
				service.Runner = fakeRunner{auth: authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z"), err: errors.New(retryCanary)}
				results, err := service.Refresh(ctx, []string{profile.ID})
				if err != nil {
					t.Fatal(err)
				}
				if results[profile.ID].Error == "" || !strings.Contains(results[profile.ID].Error, "cancelled") || !results[profile.ID].Snapshot.FetchedAt.IsZero() {
					t.Fatal("cancelled usage was reported as success")
				}
				data, err := manager.Load()
				if err != nil {
					t.Fatal(err)
				}
				saved, err := data.Find(profile.ID)
				if err != nil {
					t.Fatal(err)
				}
				document, err := authschema.Parse(saved.Auth)
				if err != nil || document.Tokens.RefreshToken != "refresh-new" {
					t.Fatal("cancelled query lost the valid refresh")
				}
				if active {
					raw, err := service.Home.ReadAuth()
					if err != nil {
						t.Fatal(err)
					}
					document, err := authschema.Parse(raw)
					if err != nil || document.Tokens.RefreshToken != "refresh-new" {
						t.Fatal("active projection did not retain the refresh")
					}
				}
				encoded, err := json.Marshal(results)
				if err != nil || strings.Contains(string(encoded), retryCanary) {
					t.Fatal("public result exposed diagnostics")
				}
				cached, err := os.ReadFile(service.Paths.UsageCache)
				if err != nil || strings.Contains(string(cached), retryCanary) {
					t.Fatal("persistent cache exposed diagnostics")
				}
				cache, err := service.Cached()
				if err != nil || cache.Failures[profile.ID].Message == "" {
					t.Fatal("usage failure was not persisted")
				}
			})
		}
	}
}

func TestAuxiliaryStateFailureDoesNotDiscardRefresh(t *testing.T) {
	for _, target := range []string{"cache", "state", "live-auth"} {
		t.Run(target, func(t *testing.T) {
			service, manager, profile := testService(t, true)
			service.queryAttempts = 1
			service.Runner = fakeRunner{auth: authBytes("account-a", "refresh-new", "2026-08-20T01:00:00Z"), err: errors.New(retryCanary)}
			path := service.Paths.UsageCache
			if target == "state" {
				path = service.Paths.State
			}
			if target == "live-auth" {
				path = service.Home.AuthPath
			}
			if err := atomicfile.Write(path, []byte("invalid synthetic auxiliary state"), 0o600); err != nil {
				t.Fatal(err)
			}
			results, err := service.Refresh(context.Background(), []string{profile.ID})
			if target == "cache" && err == nil {
				t.Fatal("invalid cache was silently ignored")
			}
			if target != "cache" && (err != nil || results[profile.ID].Error == "") {
				t.Fatal("invalid active state was not reported")
			}
			if err != nil && strings.Contains(err.Error(), retryCanary) {
				t.Fatal("auxiliary failure leaked diagnostics")
			}
			data, loadErr := manager.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			saved, findErr := data.Find(profile.ID)
			if findErr != nil {
				t.Fatal(findErr)
			}
			document, parseErr := authschema.Parse(saved.Auth)
			if parseErr != nil || document.Tokens.RefreshToken != "refresh-new" {
				t.Fatal("auxiliary state failure discarded refreshed credentials")
			}
		})
	}
}
