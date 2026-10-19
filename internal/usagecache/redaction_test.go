package usagecache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cacheCanary = "SYNTHETIC_CACHE_SECRET_CANARY_DO_NOT_OUTPUT"

func TestCacheRedactsNewAndLegacyFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	cache := Cache{Version: 1, Failures: map[string]Failure{"profile": {Message: cacheCanary}}}
	if err := Save(path, cache); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(encoded), cacheCanary) {
		t.Fatal("cache persisted untrusted diagnostics")
	}
	legacy := `{"version":1,"profiles":{},"failures":{"profile":{"message":"` + cacheCanary + `"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(loaded)
	if err != nil || strings.Contains(string(public), cacheCanary) || loaded.Failures["profile"].Message == "" {
		t.Fatal("legacy cache diagnostics reached public data")
	}
	if err := Save(path, loaded); err != nil {
		t.Fatal(err)
	}
	encoded, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(encoded), cacheCanary) {
		t.Fatal("cache rewrite retained legacy diagnostics")
	}
}

func TestCacheErrorsDoNotEchoJSONFieldsOrPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	raw := `{"version":1,"profiles":{},"failures":{"` + cacheCanary + `":{"message":42}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || strings.Contains(err.Error(), cacheCanary) {
		t.Fatal("cache decode error exposed an untrusted field")
	}
	directory := filepath.Join(t.TempDir(), cacheCanary)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(directory, Cache{}); err == nil || strings.Contains(err.Error(), cacheCanary) {
		t.Fatal("cache write error exposed an untrusted path")
	}
}
