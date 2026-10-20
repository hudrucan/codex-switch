package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SilkageNet/codex-switch/internal/codexusage"
	"github.com/SilkageNet/codex-switch/internal/process"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/switcher"
	"github.com/SilkageNet/codex-switch/internal/usagecache"
	"github.com/spf13/cobra"
)

type forbiddenUIUsageRunner struct{ t *testing.T }

func (runner forbiddenUIUsageRunner) Query(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error) {
	runner.t.Error("UI status invoked the usage runner")
	return codexusage.Snapshot{}, nil, errors.New("usage runner must not be called")
}

func executeUIStatus(options *Options) error {
	root := &cobra.Command{Use: "codex-switch", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().BoolVar(&options.JSON, "json", false, "emit JSON")
	root.AddCommand(newUICommand(options))
	root.SetArgs([]string{"ui", "status", "--json"})
	return root.Execute()
}

func TestUICommandSurface(t *testing.T) {
	root := NewCommand("test")
	command, _, err := root.Find([]string{"ui", "status"})
	if err != nil || command.CommandPath() != "codex-switch ui status" {
		t.Fatal("UI status is not registered at the root")
	}
	if flag := command.InheritedFlags().Lookup("json"); flag == nil {
		t.Fatal("UI status does not inherit the global JSON flag")
	}
	if children := command.Parent().Commands(); len(children) != 1 || children[0] != command {
		t.Fatal("UI group must contain only status")
	}
}

func TestUIStatusCachedReadOnly(t *testing.T) {
	runtime := prepareInteractiveRuntime(t)
	runtime.usageRunner = forbiddenUIUsageRunner{t}
	data, err := runtime.manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, err := data.FindByAlias("a")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := data.FindByAlias("b")
	if err != nil {
		t.Fatal(err)
	}
	// The recorded account intentionally differs from the live account.
	if err := appstate.Save(runtime.paths.State, appstate.State{ActiveProfileID: recorded.ID, AuthHash: "stale"}); err != nil {
		t.Fatal(err)
	}
	fetched := time.Now().UTC().Add(-time.Hour)
	lifetimeTokens := int64(1234)
	cache := usagecache.Cache{Profiles: map[string]codexusage.Snapshot{
		active.ID: {
			FetchedAt: fetched, PlanType: "pro",
			RateLimits: &codexusage.RateLimits{RateLimits: codexusage.RateLimitSnapshot{
				Primary: &codexusage.RateLimitWindow{UsedPercent: 42},
			}},
			TokenUsage: &codexusage.TokenUsage{Summary: codexusage.TokenUsageSummary{LifetimeTokens: &lifetimeTokens}},
		},
	}}
	if err := usagecache.Save(runtime.paths.UsageCache, cache); err != nil {
		t.Fatal(err)
	}
	// A recoverable journal must remain untouched, including stale active state.
	hash, err := runtime.home.AuthHash()
	if err != nil {
		t.Fatal(err)
	}
	journal, err := json.Marshal(switcher.Journal{Version: 1, Operation: "use", TargetProfileID: active.ID, OldHash: "old", NewHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime.paths.Journal, journal, 0o600); err != nil {
		t.Fatal(err)
	}
	wantObservation, err := runtime.switcher().Observe()
	if err != nil || wantObservation.State != switcher.AccountStateExternalLogin {
		t.Fatal("fixture must observe the live account independently of recorded state")
	}
	before := make(map[string][]byte)
	for _, path := range []string{runtime.paths.Vault, runtime.paths.State, runtime.home.AuthPath, runtime.paths.UsageCache, runtime.paths.Journal} {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	options := lockingOptions(runtime)
	options.runtimeLoader = func(create bool) (runtimeState, error) {
		if create {
			t.Error("UI status requested runtime initialization")
		}
		return runtime, nil
	}
	processCalls := 0
	wantProcesses := []process.Info{{PID: 4242, Command: "codex"}}
	options.detectCodex = func() ([]process.Info, error) {
		processCalls++
		return wantProcesses, nil
	}
	var output bytes.Buffer
	options.Output = &output
	// An informational snapshot must also work while a writer holds the locks.
	lock, err := runtime.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := executeUIStatus(options); err != nil {
		t.Fatal(err)
	}
	var got uiStatusView
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal("UI status did not return a single JSON object")
	}
	if got.SchemaVersion != 1 || len(got.Accounts) != len(data.Profiles) {
		t.Fatal("incorrect schema version or account inventory")
	}
	for index, profile := range data.Profiles {
		want := toView(profile, profile.ID == active.ID)
		want.Usage = usageFromCache(cache, profile.ID, "", time.Now())
		if !reflect.DeepEqual(got.Accounts[index], want) {
			t.Fatal("account metadata, active marker, or cached usage changed")
		}
	}
	if !reflect.DeepEqual(got.Observation, wantObservation) {
		t.Fatal("bridge changed the public observation")
	}
	if processCalls != 1 || !reflect.DeepEqual(got.CodexProcesses, wantProcesses) {
		t.Fatal("bridge did not use the process detector result")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil || len(fields) != 4 {
		t.Fatal("unexpected top-level JSON fields")
	}
	var processes []map[string]json.RawMessage
	if err := json.Unmarshal(fields["codexProcesses"], &processes); err != nil || len(processes) != 1 || len(processes[0]) != 2 || processes[0]["pid"] == nil || processes[0]["command"] == nil {
		t.Fatal("process JSON must contain only PID and command name")
	}
	for _, canary := range []string{"synthetic-id", "synthetic-access", "synthetic-refresh", "auth_mode", "tokens", "access_token", "refresh_token", "id_token"} {
		if strings.Contains(output.String(), canary) {
			t.Fatal("UI status JSON exposed authentication material")
		}
	}
	for path, original := range before {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, after) {
			t.Fatal("UI status changed synthetic persistent data")
		}
	}
}

func TestUIStatusEmptyArrays(t *testing.T) {
	root := t.TempDir()
	runtime := lockingRuntime(t, filepath.Join(root, "switch"), filepath.Join(root, "codex"))
	if _, err := runtime.manager.Init(); err != nil {
		t.Fatal(err)
	}
	options := lockingOptions(runtime)
	options.detectCodex = func() ([]process.Info, error) { return nil, nil }
	var output bytes.Buffer
	options.Output = &output
	if err := executeUIStatus(options); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Accounts       json.RawMessage `json:"accounts"`
		CodexProcesses json.RawMessage `json:"codexProcesses"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || string(got.Accounts) != "[]" || string(got.CodexProcesses) != "[]" {
		t.Fatal("empty accounts and process lists must serialize as arrays")
	}
}

func TestUIStatusProcessDetectionFailure(t *testing.T) {
	const canary = "SYNTHETIC_PROCESS_ERROR_CANARY"
	options := lockingOptions(prepareInteractiveRuntime(t))
	options.detectCodex = func() ([]process.Info, error) {
		return []process.Info{{PID: 4242, Command: "codex"}}, errors.New(canary)
	}
	var output bytes.Buffer
	options.Output, options.Error = &output, &output
	err := executeUIStatus(options)
	if err == nil || !strings.Contains(err.Error(), "inspect running Codex processes failed") {
		t.Fatal("process detection failure must fail the bridge")
	}
	if output.Len() != 0 || strings.Contains(err.Error(), canary) || errors.Unwrap(err) != nil {
		t.Fatal("process failure exposed diagnostics or emitted a misleading status")
	}
}
