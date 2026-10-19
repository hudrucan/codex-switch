package app

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/codexusage"
	"github.com/SilkageNet/codex-switch/internal/usagecache"
)

func TestCachedCLIJSONRedactsLegacyDiagnostics(t *testing.T) {
	const canary = "SYNTHETIC_CLI_SECRET_CANARY_DO_NOT_OUTPUT"
	for _, commandName := range []string{"list", "usage"} {
		t.Run(commandName, func(t *testing.T) {
			runtime := prepareInteractiveRuntime(t)
			data, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			profile, err := data.Find("a")
			if err != nil {
				t.Fatal(err)
			}
			cache := usagecache.Cache{Version: 1, Profiles: map[string]codexusage.Snapshot{profile.ID: {FetchedAt: time.Now(), PlanType: "pro"}}, Failures: map[string]usagecache.Failure{profile.ID: {Message: canary}}}
			// Bypass Save only to model a cache created by an older binary.
			encoded, err := json.Marshal(cache)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(runtime.paths.UsageCache, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			options := lockingOptions(runtime)
			options.JSON = true
			var output bytes.Buffer
			options.Output = &output
			command := newAccountListCommand(options)
			command.SetArgs([]string{"--cached"})
			if commandName == "usage" {
				command = newAccountUsageCommand(options)
				command.SetArgs([]string{"a", "--cached"})
			}
			command.SilenceErrors, command.SilenceUsage = true, true
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if !json.Valid(output.Bytes()) || strings.Contains(output.String(), canary) || !strings.Contains(output.String(), "diagnostics redacted") {
				t.Fatal("public CLI JSON exposed legacy diagnostics")
			}
			view := usageFromCache(cache, profile.ID, canary, time.Now())
			public, err := json.Marshal(view)
			if err != nil || strings.Contains(string(public), canary) {
				t.Fatal("view boundary exposed untrusted diagnostics")
			}
		})
	}
}
