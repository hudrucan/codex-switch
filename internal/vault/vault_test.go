package vault

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/secretstore"
)

func TestVaultRoundTripAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.enc")
	secrets := secretstore.NewMemoryStore()
	manager := New(path, secrets)
	data, err := manager.Init()
	if err != nil {
		t.Fatal(err)
	}
	profile := NewProfile("work", "test", json.RawMessage(`{"auth_mode":"chatgpt"}`), "account-a", "", "person@example.com", data.UpdatedAt)
	if err := data.Add(profile, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save(data); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("person@example.com")) || bytes.Contains(encoded, []byte("account-a")) {
		t.Fatal("vault leaked plaintext account metadata")
	}
	loaded, err := manager.Load()
	if err != nil || len(loaded.Profiles) != 1 || loaded.Profiles[0].Alias != "work" {
		t.Fatalf("unexpected loaded vault: %#v, %v", loaded, err)
	}
	if err := manager.RotateKey(); err != nil {
		t.Fatal(err)
	}
	rotated, err := manager.Load()
	if err != nil || len(rotated.Profiles) != 1 {
		t.Fatalf("rotated vault failed: %#v, %v", rotated, err)
	}
}

func TestPortableExportImport(t *testing.T) {
	data := Data{Version: 1, Profiles: []Profile{{ID: "id", Alias: "personal", AccountID: "account"}}}
	encoded, err := Export(data, []byte("a long test passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("personal")) {
		t.Fatal("portable backup leaked plaintext")
	}
	loaded, err := Import(encoded, []byte("a long test passphrase"))
	if err != nil || len(loaded.Profiles) != 1 || loaded.Profiles[0].Alias != "personal" {
		t.Fatalf("unexpected import: %#v, %v", loaded, err)
	}
	if _, err := Import(encoded, []byte("wrong passphrase")); err == nil {
		t.Fatal("wrong passphrase should fail")
	}
}

func TestDuplicateAlias(t *testing.T) {
	data := Data{Version: 1}
	if err := data.Add(Profile{Alias: "Work"}, false); err != nil {
		t.Fatal(err)
	}
	if err := data.Add(Profile{Alias: "work"}, false); err == nil {
		t.Fatal("aliases must be case-insensitively unique")
	}
}

func TestFindByIDDoesNotResolveCollidingAlias(t *testing.T) {
	data := Data{Profiles: []Profile{
		{ID: "other-id", Alias: "TARGET-ID"},
		{ID: "target-id", Alias: "work"},
	}}
	profile, err := data.FindByID("target-id")
	if err != nil || profile.ID != "target-id" {
		t.Fatal("internal ID lookup was redirected by an alias")
	}
	alias, err := data.Find("target-id")
	if err != nil || alias.ID != "other-id" {
		t.Fatal("user-facing case-insensitive alias lookup changed")
	}
	for _, value := range []string{"work", "TARGET-ID", "missing"} {
		if _, err := data.FindByID(value); err == nil {
			t.Fatal("internal ID lookup accepted an alias or inexact ID")
		}
	}
}

func TestFindByAliasDoesNotResolveCollidingID(t *testing.T) {
	data := Data{Profiles: []Profile{
		{ID: "created", Alias: "existing"},
		{ID: "new-profile-id", Alias: "created"},
	}}
	for _, alias := range []string{"created", "CREATED"} {
		profile, err := data.FindByAlias(alias)
		if err != nil || profile.ID != "new-profile-id" {
			t.Fatal("alias-only lookup returned another profile by ID")
		}
	}
	if _, err := data.FindByAlias("new-profile-id"); err == nil {
		t.Fatal("alias-only lookup accepted a profile ID")
	}
	for selector, expected := range map[string]string{"created": "created", "EXISTING": "created", "new-profile-id": "new-profile-id"} {
		profile, err := data.Find(selector)
		if err != nil || profile.ID != expected {
			t.Fatal("normal user-facing alias/ID selector behavior changed")
		}
	}
	profile, err := data.FindByID("created")
	if err != nil || profile.Alias != "existing" {
		t.Fatal("exact-ID lookup changed after adding alias-only lookup")
	}
}

func TestValidateAlias(t *testing.T) {
	for _, alias := range []string{"", " work", "work\n"} {
		if err := ValidateAlias(alias); err == nil {
			t.Fatalf("alias %q should be rejected", alias)
		}
	}
	if err := ValidateAlias("work-account"); err != nil {
		t.Fatal(err)
	}
}
