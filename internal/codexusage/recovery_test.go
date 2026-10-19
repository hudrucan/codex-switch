package codexusage

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/authschema"
)

const rpcCanary = "SYNTHETIC_RPC_SECRET_CANARY_DO_NOT_OUTPUT"

func workspaceAuth(account, refresh, stamp, workspace string) []byte {
	var value map[string]any
	_ = json.Unmarshal(testAuth(account, refresh, stamp), &value)
	if workspace != "" {
		claims, _ := json.Marshal(map[string]string{"workspace_id": workspace})
		value["tokens"].(map[string]any)["id_token"] = "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func failureRunner(t *testing.T, mode, method string, candidate []byte) (Runner, **exec.Cmd, string) {
	t.Helper()
	root := t.TempDir()
	candidatePath := filepath.Join(root, "synthetic-auth.json")
	if err := os.WriteFile(candidatePath, candidate, 0o600); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "ready")
	var child *exec.Cmd
	runner := Runner{Binary: os.Args[0], command: func(ctx context.Context, binary string, _ ...string) *exec.Cmd {
		child = exec.CommandContext(ctx, binary, "-test.run=^TestUsageFailureHelperProcess$")
		child.Env = append(os.Environ(), "CODEX_USAGE_FAILURE_HELPER=1", "CODEX_USAGE_FAILURE_MODE="+mode,
			"CODEX_USAGE_FAILURE_METHOD="+method, "CODEX_USAGE_CANDIDATE="+candidatePath, "CODEX_USAGE_READY="+ready, "GORACE=atexit_sleep_ms=0")
		return child
	}}
	return runner, &child, ready
}

func assertChildCleaned(t *testing.T, child *exec.Cmd) {
	t.Helper()
	if child == nil || child.ProcessState == nil {
		t.Fatal("App Server was not reaped")
	}
	for _, value := range child.Env {
		if strings.HasPrefix(value, "CODEX_HOME=") {
			if _, err := os.Stat(strings.TrimPrefix(value, "CODEX_HOME=")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("temporary credential home was not removed")
			}
			return
		}
	}
	t.Fatal("isolated home was not configured")
}

func TestRunnerRecoversRefreshOnEveryProtocolFailure(t *testing.T) {
	original := workspaceAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z", "workspace-a")
	candidate := workspaceAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z", "workspace-a")
	for _, mode := range []string{"crash", "rpc", "partial"} {
		for _, method := range []string{"initialize", "account/read", "account/rateLimits/read", "account/usage/read"} {
			t.Run(mode+"/"+method, func(t *testing.T) {
				runner, child, _ := failureRunner(t, mode, method, candidate)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				snapshot, updated, err := runner.Query(ctx, original)
				if err == nil || !snapshot.FetchedAt.IsZero() {
					t.Fatal("failed query was reported as successful usage")
				}
				if strings.Contains(err.Error(), rpcCanary) || len(err.Error()) > maxPublicError {
					t.Fatal("query exposed untrusted diagnostics")
				}
				if mode == "rpc" && !strings.Contains(err.Error(), "RPC code -32001") {
					t.Fatal("safe RPC error code was lost")
				}
				document, parseErr := authschema.Parse(updated)
				if parseErr != nil || document.Tokens.RefreshToken != "refresh-new" {
					t.Fatal("refresh was lost on a failed query")
				}
				assertChildCleaned(t, *child)
			})
		}
	}
}

func TestRunnerRecoversFinalAuthAfterExitAndDecodeFailures(t *testing.T) {
	for _, mode := range []string{"wait-crash", "bad-result"} {
		for _, method := range []string{"account/read", "account/rateLimits/read", "account/usage/read"} {
			t.Run(mode+"/"+method, func(t *testing.T) {
				runner, child, _ := failureRunner(t, mode, method, testAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z"))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, updated, err := runner.Query(ctx, testAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
				if err == nil || strings.Contains(err.Error(), rpcCanary) {
					t.Fatal("failure was lost or exposed a secret")
				}
				document, parseErr := authschema.Parse(updated)
				if parseErr != nil || document.Tokens.RefreshToken != "refresh-new" {
					t.Fatal("final credentials were not recovered")
				}
				assertChildCleaned(t, *child)
			})
		}
	}
}

func TestRunnerRecoversRefreshOnCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			runner, child, ready := failureRunner(t, "cancel", "account/read", testAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z"))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan struct{})
			var updated json.RawMessage
			var queryErr error
			go func() {
				_, updated, queryErr = runner.Query(ctx, testAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z"))
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("cancelled query did not finish cleanup")
				}
			})
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
		readyLoop:
			for {
				select {
				case <-ticker.C:
					if _, err := os.Stat(ready); err == nil {
						break readyLoop
					}
				case <-done:
					t.Fatal("helper exited before writing refreshed credentials")
				case <-timer.C:
					cancel()
					<-done
					t.Fatal("helper did not reach refresh barrier")
				}
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("query failed to reap cancelled child")
			}
			want := context.Canceled
			if mode == "timeout" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(queryErr, want) {
				t.Fatal("original cancellation classification was lost")
			}
			document, err := authschema.Parse(updated)
			if err != nil || document.Tokens.RefreshToken != "refresh-new" {
				t.Fatal("cancellation lost valid refresh")
			}
			assertChildCleaned(t, *child)
		})
	}
}

func TestRunnerRejectsUnsafeFinalCredentials(t *testing.T) {
	original := workspaceAuth("account-a", "refresh-old", "2026-08-20T00:00:00Z", "workspace-a")
	candidates := map[string][]byte{
		"different-account":   workspaceAuth("account-b", "refresh-new", "2026-08-20T01:00:00Z", "workspace-a"),
		"different-workspace": workspaceAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z", "workspace-b"),
		"missing-workspace":   workspaceAuth("account-a", "refresh-new", "2026-08-20T01:00:00Z", ""),
		"older":               workspaceAuth("account-a", "refresh-stale", "2026-08-19T00:00:00Z", "workspace-a"),
		"ambiguous":           workspaceAuth("account-a", "refresh-conflict", "2026-08-20T00:00:00Z", "workspace-a"),
		"partial":             []byte(`{"tokens":{"refresh_token":"` + rpcCanary),
		"bad-mode":            []byte(`{"auth_mode":"` + rpcCanary + `"}`),
	}
	for name, candidate := range candidates {
		t.Run(name, func(t *testing.T) {
			runner, child, _ := failureRunner(t, "crash", "account/read", candidate)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, updated, err := runner.Query(ctx, original)
			if err == nil || len(updated) != 0 {
				t.Fatal("unsafe credentials were recovered")
			}
			if strings.Contains(err.Error(), rpcCanary) {
				t.Fatal("validation exposed authentication material")
			}
			assertChildCleaned(t, *child)
		})
	}
}

func TestUsageFailureHelperProcess(t *testing.T) {
	if os.Getenv("CODEX_USAGE_FAILURE_HELPER") != "1" {
		return
	}
	mode, target := os.Getenv("CODEX_USAGE_FAILURE_MODE"), os.Getenv("CODEX_USAGE_FAILURE_METHOD")
	home := os.Getenv("CODEX_HOME")
	if !filepath.IsAbs(home) {
		os.Exit(90)
	}
	writeAuth := func() {
		raw, err := os.ReadFile(os.Getenv("CODEX_USAGE_CANDIDATE"))
		if err != nil || os.WriteFile(filepath.Join(home, "auth.json"), raw, 0o600) != nil {
			os.Exit(91)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request rpcRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		method := request.Method
		if method == target && mode != "wait-crash" {
			writeAuth()
			_, _ = fmt.Fprintln(os.Stderr, rpcCanary)
			switch mode {
			case "crash":
				os.Exit(17)
			case "cancel":
				_ = os.WriteFile(os.Getenv("CODEX_USAGE_READY"), []byte("ready"), 0o600)
				time.Sleep(time.Hour)
			case "partial":
				_, _ = fmt.Fprintf(os.Stdout, "{\"id\":%d,\"result\":{\"%s\":\n", *request.ID, rpcCanary)
				continue
			}
		}
		if mode == "rpc" && (method == target || (target == "account/rateLimits/read" && method == "account/usage/read") || (target == "account/usage/read" && method == "account/rateLimits/read")) {
			_ = encoder.Encode(map[string]any{"id": *request.ID, "error": map[string]any{"code": -32001, "message": rpcCanary, "data": map[string]string{"access_token": rpcCanary}}})
			continue
		}
		var result any = map[string]any{}
		switch method {
		case "account/read":
			result = map[string]any{"account": map[string]any{"type": "chatgpt", "planType": "pro"}}
		case "account/rateLimits/read":
			result = map[string]any{"rateLimits": map[string]any{}}
		case "account/usage/read":
			result = map[string]any{"summary": map[string]any{}}
		}
		if mode == "bad-result" && method == target {
			result = rpcCanary
		}
		_ = encoder.Encode(map[string]any{"id": *request.ID, "result": result})
	}
	if mode == "wait-crash" {
		writeAuth()
		os.Exit(17)
	}
	os.Exit(0)
}
