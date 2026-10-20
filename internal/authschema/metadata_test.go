package authschema

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestMetadataUsesOnlyExplicitConsistentClaimLocations(t *testing.T) {
	const canary = "SYNTHETIC_IDENTITY_CANARY_DO_NOT_OUTPUT"
	for _, test := range []struct {
		name, claims, workspace, email string
		invalid                        bool
	}{
		{name: "namespace", claims: `{"email":"a@example.invalid","https://api.openai.com/auth":{"workspace_id":"a"}}`, workspace: "a", email: "a@example.invalid"},
		{name: "top-level", claims: `{"workspace_id":"a","email":"a@example.invalid"}`, workspace: "a", email: "a@example.invalid"},
		{name: "camel-case", claims: `{"workspaceId":"a"}`, workspace: "a"},
		{name: "chatgpt", claims: `{"https://api.openai.com/auth":{"chatgpt_workspace_id":"a","email":"a@example.invalid"}}`, workspace: "a", email: "a@example.invalid"},
		{name: "equal-evidence", claims: `{"workspace_id":"a","workspaceId":"a","https://api.openai.com/auth":{"workspace_id":"a"}}`, workspace: "a"},
		{name: "equal-duplicate-member", claims: `{"workspace_id":"a","workspace_id":"a"}`, workspace: "a"},
		{name: "empty-evidence", claims: `{"workspace_id":"","workspaceId":"a"}`, workspace: "a"},
		{name: "unrelated-nesting", claims: `{"unrelated":[{"workspace_id":"` + canary + `","email":"` + canary + `"}]}`},
		{name: "conflict", claims: `{"workspace_id":"a","https://api.openai.com/auth":{"workspace_id":"` + canary + `"}}`, invalid: true},
		{name: "duplicate-member-conflict", claims: `{"workspace_id":"a","workspace_id":"` + canary + `"}`, invalid: true},
		{name: "duplicate-namespace-conflict", claims: `{"https://api.openai.com/auth":{"workspace_id":"a"},"https://api.openai.com/auth":{"workspace_id":"` + canary + `"}}`, invalid: true},
		{name: "invalid-workspace", claims: `{"workspace_id":{"` + canary + `":1}}`, invalid: true},
		{name: "null-workspace", claims: `{"workspace_id":null}`, invalid: true},
		{name: "invalid-namespace", claims: `{"https://api.openai.com/auth":["` + canary + `"]}`, invalid: true},
		{name: "conflicting-email", claims: `{"email":"a@example.invalid","https://api.openai.com/auth":{"email":"` + canary + `"}}`, invalid: true},
		{name: "invalid-email", claims: `{"email":["` + canary + `"]}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(test.claims)) + ".signature"
			raw := testAuth("account-a", "synthetic-refresh", "2026-08-20T00:00:00Z", token)
			// Unknown outer fields and nested token claims stay byte-for-byte intact.
			raw = append(raw[:len(raw)-1], []byte(`,"unknown":{"preserve":[1,2,3]}}`)...)
			document, err := Parse(raw)
			if test.invalid {
				if err == nil || err.Error() != "authentication identity claims are invalid or conflicting" || strings.Contains(err.Error(), canary) {
					t.Fatal("ambiguous identity was accepted or its error was not redacted")
				}
				return
			}
			if err != nil || document.WorkspaceID != test.workspace || document.Email != test.email {
				t.Fatal("supported metadata was not resolved deterministically")
			}
			if !bytes.Equal(document.Raw, raw) || document.Tokens.IDToken != token || document.Tokens.RefreshToken != "synthetic-refresh" {
				t.Fatal("parsing changed raw authentication or token material")
			}
		})
	}
}
