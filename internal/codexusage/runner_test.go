package codexusage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerQueriesUsageAndReturnsRefreshedCredentials(t *testing.T) {
	runner := helperRunner(t, false)
	snapshot, updated, err := runner.Query(context.Background(), testAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlanType != "pro" || snapshot.RateLimits == nil || snapshot.TokenUsage == nil {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.MainRateLimit() == nil || snapshot.MainRateLimit().Primary.UsedPercent != 21 {
		t.Fatalf("unexpected main rate limit: %#v", snapshot.MainRateLimit())
	}
	if snapshot.RateLimits.RateLimitResetCredits == nil || snapshot.RateLimits.RateLimitResetCredits.AvailableCount != 2 {
		t.Fatalf("unexpected reset credits: %#v", snapshot.RateLimits.RateLimitResetCredits)
	}
	if credits := snapshot.RateLimits.RateLimitResetCredits.Credits; len(credits) != 1 || credits[0].ExpiresAt == nil || *credits[0].ExpiresAt != 1784246400 {
		t.Fatalf("unexpected reset credit details: %#v", credits)
	}
	if snapshot.TokenUsage.Summary.LifetimeTokens == nil || *snapshot.TokenUsage.Summary.LifetimeTokens != 1234567 {
		t.Fatalf("unexpected token usage: %#v", snapshot.TokenUsage)
	}
	var wire struct {
		Tokens struct {
			Refresh string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(updated, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Tokens.Refresh != "refresh-new" {
		t.Fatal("refreshed credentials were not returned")
	}
}

func TestRunnerAllowsOneUnsupportedUsageMethod(t *testing.T) {
	runner := helperRunner(t, true)
	snapshot, _, err := runner.Query(context.Background(), testAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RateLimits == nil || snapshot.TokenUsage != nil || len(snapshot.Partial) != 1 || snapshot.Partial[0] != "tokenUsage" {
		t.Fatalf("unexpected partial snapshot: %#v", snapshot)
	}
}

func TestRunnerPreservesBothUsageMethodErrors(t *testing.T) {
	runner := helperRunnerMode(t, false, true)
	_, updated, err := runner.Query(context.Background(), testAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
	if err == nil {
		t.Fatal("expected usage query error")
	}
	for _, expected := range []string{"account/rateLimits/read failed (RPC code -32001, diagnostics redacted)", "account/usage/read failed (RPC code -32601, diagnostics redacted)"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error %q does not contain %q", err, expected)
		}
	}
	if !strings.Contains(string(updated), "refresh-new") {
		t.Fatal("rotated credentials were lost on usage failure")
	}
}

func TestProtocolErrorRedactsChildDiagnostics(t *testing.T) {
	err := protocolError("query", errors.Join(context.DeadlineExceeded, errors.New("secret child output")))
	if strings.Contains(err.Error(), "secret child output") || !strings.Contains(err.Error(), "redacted") {
		t.Fatalf("unexpected protocol error: %v", err)
	}
}

func TestRunnerReturnsBinaryDiscoveryError(t *testing.T) {
	discoveryErr := errors.New("standalone Codex CLI required")
	_, _, err := (Runner{BinaryError: discoveryErr}).Query(context.Background(), testAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
	if !errors.Is(err, discoveryErr) {
		t.Fatalf("Query() error = %v, want %v", err, discoveryErr)
	}
}

func helperRunner(t *testing.T, partial bool) Runner {
	return helperRunnerMode(t, partial, false)
}

func helperRunnerMode(t *testing.T, partial, allError bool) Runner {
	t.Helper()
	return Runner{
		Binary:        os.Args[0],
		ClientVersion: "test",
		command: func(ctx context.Context, binary string, args ...string) *exec.Cmd {
			command := exec.CommandContext(ctx, binary, "-test.run=TestCodexUsageHelperProcess", "--")
			command.Env = append(os.Environ(), "CODEX_USAGE_HELPER=1")
			if partial {
				command.Env = append(command.Env, "CODEX_USAGE_PARTIAL=1")
			}
			if allError {
				command.Env = append(command.Env, "CODEX_USAGE_ALL_ERROR=1")
			}
			return command
		},
	}
}

func TestCodexUsageHelperProcess(t *testing.T) {
	if os.Getenv("CODEX_USAGE_HELPER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		id := string(request.ID)
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"id": json.RawMessage(id), "result": map[string]any{}})
		case "account/read":
			_ = encoder.Encode(map[string]any{"id": json.RawMessage(id), "result": map[string]any{
				"account":            map[string]any{"type": "chatgpt", "email": "a@example.com", "planType": "pro"},
				"requiresOpenaiAuth": true,
			}})
		case "account/rateLimits/read":
			if os.Getenv("CODEX_USAGE_ALL_ERROR") == "1" {
				_ = encoder.Encode(map[string]any{"id": json.RawMessage(id), "error": map[string]any{"code": -32001, "message": "overloaded"}})
				continue
			}
			_ = encoder.Encode(map[string]any{"id": json.RawMessage(id), "result": map[string]any{
				"rateLimits":          map[string]any{"planType": "pro", "primary": map[string]any{"usedPercent": 21, "windowDurationMins": 300}},
				"rateLimitsByLimitId": map[string]any{"codex": map[string]any{"planType": "pro", "primary": map[string]any{"usedPercent": 21, "windowDurationMins": 300}}},
				"rateLimitResetCredits": map[string]any{
					"availableCount": 2,
					"credits": []map[string]any{{
						"id":          "RateLimitResetCredit_1",
						"resetType":   "codexRateLimits",
						"status":      "available",
						"grantedAt":   1781654400,
						"expiresAt":   1784246400,
						"title":       "Rate-limit reset",
						"description": "Reset an eligible Codex rate-limit window.",
					}},
				},
			}})
		case "account/usage/read":
			if os.Getenv("CODEX_USAGE_PARTIAL") == "1" || os.Getenv("CODEX_USAGE_ALL_ERROR") == "1" {
				_ = encoder.Encode(map[string]any{"id": json.RawMessage(id), "error": map[string]any{"code": -32601, "message": "method not found"}})
			} else {
				lifetime := int64(1234567)
				_ = encoder.Encode(map[string]any{"id": json.RawMessage(id), "result": map[string]any{
					"summary":           map[string]any{"lifetimeTokens": lifetime},
					"dailyUsageBuckets": []map[string]any{{"startDate": "2026-08-20", "tokens": 99}},
				}})
			}
			home := os.Getenv("CODEX_HOME")
			_ = os.WriteFile(filepath.Join(home, "auth.json"), testAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z"), 0o600)
		}
	}
	os.Exit(0)
}

func testAuth(account, refresh, lastRefresh string) []byte {
	return []byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":%q,"account_id":%q},"last_refresh":%q}`, "id-"+refresh, "access-"+refresh, refresh, account, lastRefresh))
}
