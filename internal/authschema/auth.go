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

	email, workspace, err := tokenMetadata(wire.Tokens.IDToken)
	if err != nil {
		return Document{}, err
	}
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
	if err := ValidateIdentity(saved.Tokens.AccountID, saved.WorkspaceID, candidate); err != nil {
		return GenerationUseSaved, err
	}
	return CompareGeneration(saved, candidate)
}

// ValidateIdentity requires known identity evidence to survive reconciliation.
// Callers with persisted metadata also validate that evidence before generation.
func ValidateIdentity(accountID, workspaceID string, candidate Document) error {
	if accountID != candidate.Tokens.AccountID || (workspaceID != "" && workspaceID != candidate.WorkspaceID) {
		return ErrIdentityMismatch
	}
	return nil
}

func tokenMetadata(token string) (string, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", "", nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", nil
	}
	var email, workspace string
	if !json.Valid(payload) || readMetadata(payload, true, &email, &workspace) != nil {
		return "", "", errors.New("authentication identity claims are invalid or conflicting")
	}
	return email, workspace, nil
}

// Only top-level claims and the OpenAI auth namespace supply metadata. Reading
// members individually also detects conflicting duplicate JSON members.
func readMetadata(raw []byte, namespace bool, email, workspace *string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrIdentityMismatch
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return ErrIdentityMismatch
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return ErrIdentityMismatch
		}
		var destination *string
		switch key {
		case "workspace_id", "workspaceId", "chatgpt_workspace_id":
			destination = workspace
		case "email":
			destination = email
		case "https://api.openai.com/auth":
			if namespace && readMetadata(value, false, email, workspace) != nil {
				return ErrIdentityMismatch
			}
		}
		if destination != nil {
			var claim string
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &claim) != nil || (*destination != "" && claim != "" && *destination != claim) {
				return ErrIdentityMismatch
			}
			if claim != "" {
				*destination = claim
			}
		}
	}
	return nil
}
