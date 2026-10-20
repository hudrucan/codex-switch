package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SilkageNet/codex-switch/internal/switcher"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

type accountView struct {
	ID              string     `json:"id"`
	Alias           string     `json:"alias"`
	AccountID       string     `json:"accountId"`
	WorkspaceID     string     `json:"workspaceId,omitempty"`
	Email           string     `json:"email,omitempty"`
	Source          string     `json:"source"`
	Active          bool       `json:"active"`
	AuthenticatedAt time.Time  `json:"authenticatedAt"`
	LastUsedAt      time.Time  `json:"lastUsedAt,omitempty"`
	Usage           *usageView `json:"usage,omitempty"`
}

func viewFromObservation(observation switcher.Observation) (accountView, error) {
	if !observation.HasLive {
		return accountView{}, errors.New("no account is active")
	}
	if observation.State == switcher.AccountStateAmbiguous {
		return accountView{}, errors.New("multiple saved profiles match the active Codex login; run 'codex-switch sync --check'")
	}
	if observation.Managed {
		return accountView{
			ID:              observation.ProfileID,
			Alias:           observation.Alias,
			AccountID:       observation.AccountID,
			WorkspaceID:     observation.WorkspaceID,
			Email:           observation.Email,
			Source:          observation.Source,
			Active:          true,
			AuthenticatedAt: observation.AuthenticatedAt,
			LastUsedAt:      observation.LastUsedAt,
		}, nil
	}
	return accountView{AccountID: observation.AccountID, WorkspaceID: observation.WorkspaceID, Email: observation.Email, Active: true, Alias: "unmanaged"}, nil
}

func formatObservation(observation switcher.Observation) string {
	identity := observation.Email
	if identity == "" {
		identity = observation.AccountID
	}
	switch observation.State {
	case switcher.AccountStateInSync:
		return fmt.Sprintf("Active account: %s (%s)\nState: in sync", observation.Alias, identity)
	case switcher.AccountStateExternalLogin, switcher.AccountStateExternalLoginWithRefresh:
		return fmt.Sprintf("Active account: %s (%s)\nState: Codex was logged in outside codex-switch; run 'codex-switch sync'", observation.Alias, identity)
	case switcher.AccountStateCredentialRefresh:
		return fmt.Sprintf("Active account: %s (%s)\nState: Codex credentials were refreshed; run 'codex-switch sync'", observation.Alias, identity)
	case switcher.AccountStateStateDrift:
		return fmt.Sprintf("Active account: %s (%s)\nState: local account metadata is stale; run 'codex-switch sync'", observation.Alias, identity)
	case switcher.AccountStateCredentialConflict:
		return fmt.Sprintf("Active account: %s (%s)\nState: credential generations conflict; inspect before using 'codex-switch sync --prefer-live'", observation.Alias, identity)
	case switcher.AccountStateUnmanaged:
		return fmt.Sprintf("Active account: unmanaged (%s)\nState: run 'codex-switch sync --as <alias>' to preserve it", identity)
	case switcher.AccountStateAmbiguous:
		return fmt.Sprintf("Active account: ambiguous (%s)\nState: multiple saved profiles match; no automatic changes are allowed", identity)
	case switcher.AccountStateLoggedOut:
		return "Active account: none\nState: Codex logged out outside codex-switch; run 'codex-switch sync'"
	default:
		return "Active account: none\nState: in sync"
	}
}

func observationNotice(observation switcher.Observation) string {
	switch observation.State {
	case switcher.AccountStateExternalLogin, switcher.AccountStateExternalLoginWithRefresh:
		recorded := observation.RecordedAlias
		if recorded == "" {
			recorded = "none"
		}
		return fmt.Sprintf("Note: Codex is actually using %s, not the last recorded account %s; run 'codex-switch sync'.", observation.Alias, recorded)
	case switcher.AccountStateCredentialRefresh:
		return fmt.Sprintf("Note: Codex refreshed %s outside codex-switch; run 'codex-switch sync'.", observation.Alias)
	case switcher.AccountStateStateDrift:
		return fmt.Sprintf("Note: local state for %s is stale; run 'codex-switch sync'.", observation.Alias)
	case switcher.AccountStateCredentialConflict:
		return "Warning: live and saved credential generations conflict; run 'codex-switch sync --check'."
	case switcher.AccountStateUnmanaged:
		return "Note: the active Codex login is unmanaged; run 'codex-switch sync --as <alias>' to preserve it."
	case switcher.AccountStateAmbiguous:
		return "Warning: multiple saved profiles match the active Codex login; run 'codex-switch sync --check'."
	case switcher.AccountStateLoggedOut:
		return "Note: Codex is logged out but stale active-account state remains; run 'codex-switch sync'."
	default:
		return ""
	}
}

// addDocument runs inside the caller's vault lock and reloads current state.

func toView(profile vault.Profile, active bool) accountView {
	return accountView{ID: profile.ID, Alias: profile.Alias, AccountID: profile.AccountID, WorkspaceID: profile.WorkspaceID, Email: profile.Email, Source: profile.Source, Active: active, AuthenticatedAt: profile.AuthenticatedAt, LastUsedAt: profile.LastUsedAt}
}

func formatView(view accountView) string {
	identity := view.Email
	if identity == "" {
		identity = view.AccountID
	}
	if view.Alias == "" {
		return identity
	}
	return fmt.Sprintf("%s (%s)", view.Alias, identity)
}

func (options *Options) render(value any, text string) error {
	if options.JSON {
		encoder := json.NewEncoder(options.Output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	if text != "" {
		_, err := fmt.Fprintln(options.Output, text)
		return err
	}
	return nil
}
