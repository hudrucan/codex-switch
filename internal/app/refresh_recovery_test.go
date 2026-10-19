package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/spf13/cobra"
)

const integrationCanary = "SYNTHETIC_INTEGRATION_SECRET_DO_NOT_OUTPUT"

type cliUsageQuery func(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error)

func (query cliUsageQuery) Query(ctx context.Context, raw json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
	return query(ctx, raw)
}

func refreshedCLIAuth(account string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt", "last_refresh": "2026-08-20T01:00:00Z",
		"tokens": map[string]string{"account_id": account, "id_token": "synthetic-new-id", "access_token": integrationCanary, "refresh_token": integrationCanary},
	})
	return raw
}

func corruptUsageAuxiliary(t *testing.T, runtime runtimeState, fault string) {
	t.Helper()
	for kind, path := range map[string]string{"cache": runtime.paths.UsageCache, "state": runtime.paths.State, "live-auth": runtime.home.AuthPath} {
		if fault == kind || fault == "combined" {
			if err := atomicfile.Write(path, []byte(`{"`+integrationCanary+`":`), 0o600); err != nil {
				t.Fatal("could not write synthetic auxiliary corruption")
			}
		}
	}
}

func usageCLICommand(options *Options, name string, cached bool) *cobra.Command {
	command := newAccountListCommand(options)
	args := []string{"--refresh"}
	if name == "usage" || name == "usage-all" {
		command = newAccountUsageCommand(options)
		args = []string{"a"}
		if name == "usage-all" {
			args = []string{"--all"}
		}
	}
	if cached {
		args = []string{"--cached"}
		if name == "usage" || name == "usage-all" {
			selector := "a"
			if name == "usage-all" {
				selector = "--all"
			}
			args = append([]string{selector}, args...)
		}
	}
	command.SetArgs(args)
	command.SilenceErrors, command.SilenceUsage = true, true
	return command
}

func TestExplicitCLIRefreshPersistsCredentialsDespiteAuxiliaryFailures(t *testing.T) {
	// Retry, identity, and auxiliary fault matrices belong to accountusage.
	// These cases own command routing, public output, and selected profiles.
	cases := []struct {
		name, command, fault string
		queryFails           bool
	}{
		{"list-success", "list", "none", false},
		{"list-query-failure", "list", "none", true},
		{"alias-success", "usage", "none", false},
		{"alias-query-failure", "usage", "none", true},
		{"all-success", "usage-all", "none", false},
		{"list-cache-failure", "list", "cache", true},
		{"alias-cache-failure", "usage", "cache", true},
		{"list-state-failure", "list", "state", true},
		{"alias-state-failure", "usage", "state", false},
		{"list-live-auth-failure", "list", "live-auth", false},
		{"alias-live-auth-failure", "usage", "live-auth", true},
		{"all-combined-failure", "usage-all", "combined", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, fault, queryFails := tc.command, tc.fault, tc.queryFails
			runtime := prepareInteractiveRuntime(t)
			original, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			active, err := original.Find("a")
			if err != nil {
				t.Fatal(err)
			}
			liveHash, err := runtime.home.AuthHash()
			if err != nil {
				t.Fatal(err)
			}
			if err := appstate.Save(runtime.paths.State, appstate.State{ActiveProfileID: active.ID, AuthHash: liveHash}); err != nil {
				t.Fatal(err)
			}
			corruptUsageAuxiliary(t, runtime, fault)
			liveBefore, err := runtime.home.AuthHash()
			if err != nil {
				t.Fatal(err)
			}
			stateBefore, err := atomicfile.FileHash(runtime.paths.State)
			if err != nil {
				t.Fatal(err)
			}
			queried := map[string]int{}
			runtime.usageRunner = cliUsageQuery(func(_ context.Context, raw json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
				document, err := authschema.Parse(raw)
				if err != nil {
					t.Fatal("CLI queried invalid credentials instead of the vault profile")
				}
				account := document.Tokens.AccountID
				queried[account]++
				fresh := refreshedCLIAuth(account)
				if queryFails {
					return codexusage.Snapshot{}, fresh, errors.New(integrationCanary)
				}
				return codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro"}, fresh, nil
			})
			options := lockingOptions(runtime)
			var output bytes.Buffer
			options.JSON, options.Output = true, &output
			commandErr := usageCLICommand(options, name, false).Execute()
			if fault != "none" && commandErr == nil {
				t.Fatal("auxiliary corruption was silently treated as success")
			}
			if fault == "none" && !queryFails && (commandErr != nil || !json.Valid(output.Bytes())) {
				t.Fatal("successful CLI refresh changed its JSON behavior")
			}
			if strings.Contains(output.String(), integrationCanary) || (commandErr != nil && strings.Contains(commandErr.Error(), integrationCanary)) {
				t.Fatal("CLI exposed sensitive diagnostics")
			}
			if fault != "none" && output.Len() != 0 {
				t.Fatal("CLI rendered active identity from invalid auxiliary state")
			}
			loaded, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			for _, before := range original.Profiles {
				after, err := loaded.FindByID(before.ID)
				if err != nil {
					t.Fatal(err)
				}
				selected := name != "usage" || before.Alias == "a"
				if !selected {
					if queried[before.AccountID] != 0 || !bytes.Equal(before.Auth, after.Auth) {
						t.Fatal("CLI refreshed an unrelated profile")
					}
					continue
				}
				if queried[before.AccountID] == 0 || !bytes.Equal(after.Auth, refreshedCLIAuth(before.AccountID)) {
					t.Fatal("auxiliary failure prevented persistence of a validated refresh")
				}
			}
			if fault == "state" || fault == "live-auth" || fault == "combined" {
				liveAfter, liveErr := runtime.home.AuthHash()
				stateAfter, stateErr := atomicfile.FileHash(runtime.paths.State)
				if liveErr != nil || stateErr != nil || liveAfter != liveBefore || stateAfter != stateBefore {
					t.Fatal("uncertain active state permitted a live projection or state write")
				}
			} else {
				raw, err := runtime.home.ReadAuth()
				if err != nil || !bytes.Equal(bytes.TrimSpace(raw), refreshedCLIAuth(active.AccountID)) {
					t.Fatal("safe active projection was not preserved")
				}
			}
			if fault != "cache" && fault != "combined" {
				raw, err := os.ReadFile(runtime.paths.UsageCache)
				if err != nil || strings.Contains(string(raw), integrationCanary) {
					t.Fatal("CLI persisted sensitive diagnostics in the usage cache")
				}
			}
		})
	}
}

func TestCachedCLIAndImplicitSelectionNeverRefreshInvalidAuxiliaryState(t *testing.T) {
	cases := []struct{ command, fault string }{
		{"list", "cache"},
		{"usage", "cache"},
		{"usage", "state"},
		{"list", "live-auth"},
		{"usage-all", "combined"},
		{"implicit-usage", "state"},
		{"implicit-usage", "live-auth"},
	}
	for _, tc := range cases {
		t.Run(tc.command+"/"+tc.fault, func(t *testing.T) {
			name, fault := tc.command, tc.fault
			runtime := prepareInteractiveRuntime(t)
			corruptUsageAuxiliary(t, runtime, fault)
			calls := 0
			runtime.usageRunner = cliUsageQuery(func(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
				calls++
				return codexusage.Snapshot{}, nil, errors.New(integrationCanary)
			})
			before := map[string]string{}
			for _, path := range []string{runtime.paths.Vault, runtime.paths.State, runtime.paths.UsageCache, runtime.home.AuthPath} {
				hash, err := atomicfile.FileHash(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = hash
			}
			options := lockingOptions(runtime)
			command := usageCLICommand(options, name, true)
			if name == "implicit-usage" {
				command = newAccountUsageCommand(options)
				command.SetArgs(nil)
				command.SilenceErrors, command.SilenceUsage = true, true
			}
			if err := command.Execute(); err == nil || strings.Contains(err.Error(), integrationCanary) {
				t.Fatal("invalid auxiliary state was accepted or exposed")
			}
			if calls != 0 {
				t.Fatal("cached read or uncertain implicit selection contacted a usage runner")
			}
			for path, hash := range before {
				actual, err := atomicfile.FileHash(path)
				if err != nil || actual != hash {
					t.Fatal("offline or invalid implicit selection mutated persistent data")
				}
			}
		})
	}
}

func TestUsageCLIKeepsAliasSelectionSeparateFromInternalIDs(t *testing.T) {
	for _, selector := range []string{"alias", "colliding-alias", "active"} {
		t.Run(selector, func(t *testing.T) {
			runtime := prepareInteractiveRuntime(t)
			data, err := runtime.manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			active := data.Profiles[0]
			active.ID = "00000000000000000000000000000001"
			other := data.Profiles[1]
			other.Alias = active.ID
			data.Profiles[0], data.Profiles[1] = other, active
			if err := runtime.manager.Save(data); err != nil {
				t.Fatal(err)
			}
			expected := active
			args := []string{active.Alias}
			switch selector {
			case "colliding-alias":
				expected, args = other, []string{other.Alias}
			case "active":
				args = nil
			}
			calls := 0
			runtime.usageRunner = cliUsageQuery(func(_ context.Context, input json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
				calls++
				document, err := authschema.Parse(input)
				if err != nil || document.Tokens.AccountID != expected.AccountID {
					t.Fatal("alias/ID collision redirected the CLI query")
				}
				return codexusage.Snapshot{FetchedAt: time.Now().UTC(), PlanType: "pro"}, refreshedCLIAuth(expected.AccountID), nil
			})
			options := lockingOptions(runtime)
			var output bytes.Buffer
			options.JSON, options.Output = true, &output
			command := newAccountUsageCommand(options)
			command.SetArgs(args)
			command.SilenceErrors, command.SilenceUsage = true, true
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			var view accountView
			if err := json.Unmarshal(output.Bytes(), &view); err != nil || view.ID != expected.ID || view.Alias != expected.Alias || view.Active != (expected.ID == active.ID) || calls != 1 {
				t.Fatal("CLI changed user alias lookup or active profile selection")
			}
		})
	}
}
