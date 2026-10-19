package authschema

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrAmbiguousGeneration = errors.New("authentication token generations are ambiguous")
var ErrIdentityMismatch = errors.New("refreshed credentials do not match the saved account or workspace")

type Tokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

type wireDocument struct {
	AuthMode string `json:"auth_mode"`
	Tokens   Tokens `json:"tokens"`
	Refresh  string `json:"last_refresh"`
}

type Document struct {
	Raw         json.RawMessage `json:"-"`
	AuthMode    string          `json:"authMode"`
	Tokens      Tokens          `json:"-"`
	LastRefresh string          `json:"lastRefresh,omitempty"`
	Email       string          `json:"email,omitempty"`
	WorkspaceID string          `json:"workspaceId,omitempty"`
}

type PublicInfo struct {
	AuthMode    string `json:"authMode"`
	AccountID   string `json:"accountId"`
	Email       string `json:"email,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	LastRefresh string `json:"lastRefresh,omitempty"`
}

func Parse(data []byte) (Document, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return Document{}, errors.New("auth.json is not valid JSON")
	}
	var wire wireDocument
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return Document{}, errors.New("auth.json has an unsupported structure")
	}
	if wire.AuthMode != "chatgpt" {
		return Document{}, errors.New("unsupported auth_mode; only ChatGPT login is supported")
	}
	if strings.TrimSpace(wire.Tokens.AccountID) == "" {
		return Document{}, errors.New("ChatGPT auth is missing tokens.account_id")
	}
	if strings.TrimSpace(wire.Tokens.AccessToken) == "" {
		return Document{}, errors.New("ChatGPT auth is missing tokens.access_token")
	}
	if strings.TrimSpace(wire.Tokens.RefreshToken) == "" {
		return Document{}, errors.New("ChatGPT auth is missing tokens.refresh_token")
	}

	email, workspace := tokenMetadata(wire.Tokens.IDToken)
	return Document{
		Raw:         append(json.RawMessage(nil), trimmed...),
		AuthMode:    wire.AuthMode,
		Tokens:      wire.Tokens,
		LastRefresh: wire.Refresh,
		Email:       email,
		WorkspaceID: workspace,
	}, nil
}

func (document Document) Public() PublicInfo {
	return PublicInfo{
		AuthMode:    document.AuthMode,
		AccountID:   document.Tokens.AccountID,
		Email:       document.Email,
		WorkspaceID: document.WorkspaceID,
		LastRefresh: document.LastRefresh,
	}
}

func (document Document) GenerationTime() (time.Time, bool) {
	if document.LastRefresh == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, document.LastRefresh)
	return parsed, err == nil
}

func SameMaterial(first, second Document) bool {
	return first.Tokens.AccountID == second.Tokens.AccountID &&
		first.Tokens.IDToken == second.Tokens.IDToken &&
		first.Tokens.AccessToken == second.Tokens.AccessToken &&
		first.Tokens.RefreshToken == second.Tokens.RefreshToken
}

type GenerationDecision int

const (
	GenerationSame GenerationDecision = iota
	GenerationUseSaved
	GenerationAdoptLive
)

func CompareGeneration(saved, live Document) (GenerationDecision, error) {
	if saved.Tokens.AccountID != live.Tokens.AccountID {
		return GenerationUseSaved, ErrIdentityMismatch
	}
	if SameMaterial(saved, live) {
		return GenerationSame, nil
	}
	savedTime, savedOK := saved.GenerationTime()
	liveTime, liveOK := live.GenerationTime()
	if !savedOK || !liveOK || savedTime.Equal(liveTime) {
		return GenerationSame, ErrAmbiguousGeneration
	}
	if liveTime.After(savedTime) {
		return GenerationAdoptLive, nil
	}
	return GenerationUseSaved, nil
}

// ValidateRefresh preserves generation ordering and requires a known workspace
// to remain present and unchanged in refreshed credentials.
func ValidateRefresh(saved, candidate Document) (GenerationDecision, error) {
	if saved.Tokens.AccountID != candidate.Tokens.AccountID || (saved.WorkspaceID != "" && saved.WorkspaceID != candidate.WorkspaceID) {
		return GenerationUseSaved, ErrIdentityMismatch
	}
	return CompareGeneration(saved, candidate)
}

func tokenMetadata(token string) (string, string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	email := findString(claims, "email")
	workspace := findString(claims, "workspace_id", "workspaceId", "chatgpt_workspace_id")
	return email, workspace
}

func findString(value any, keys ...string) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range keys {
			if candidate, ok := typed[key].(string); ok && candidate != "" {
				return candidate
			}
		}
		for _, child := range typed {
			if candidate := findString(child, keys...); candidate != "" {
				return candidate
			}
		}
	case []any:
		for _, child := range typed {
			if candidate := findString(child, keys...); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}
