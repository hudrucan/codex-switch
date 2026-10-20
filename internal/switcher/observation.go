package switcher

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/filelock"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

type AccountState string

const (
	AccountStateInSync                   AccountState = "in_sync"
	AccountStateExternalLogin            AccountState = "external_login"
	AccountStateCredentialRefresh        AccountState = "credential_refresh"
	AccountStateExternalLoginWithRefresh AccountState = "external_login_with_refresh"
	AccountStateCredentialConflict       AccountState = "credential_conflict"
	AccountStateStateDrift               AccountState = "state_drift"
	AccountStateUnmanaged                AccountState = "unmanaged"
	AccountStateAmbiguous                AccountState = "ambiguous"
	AccountStateLoggedOut                AccountState = "logged_out"
	AccountStateNoActive                 AccountState = "no_active"
)

type CredentialState string

const (
	CredentialStateCurrent    CredentialState = "current"
	CredentialStateLiveNewer  CredentialState = "live_newer"
	CredentialStateSavedNewer CredentialState = "saved_newer"
	CredentialStateAmbiguous  CredentialState = "ambiguous"
)

type Observation struct {
	State             AccountState    `json:"state"`
	CredentialState   CredentialState `json:"credentialState,omitempty"`
	HasLive           bool            `json:"hasLive"`
	Managed           bool            `json:"managed"`
	NeedsSync         bool            `json:"needsSync"`
	ProfileID         string          `json:"profileId,omitempty"`
	Alias             string          `json:"alias,omitempty"`
	AccountID         string          `json:"accountId,omitempty"`
	WorkspaceID       string          `json:"workspaceId,omitempty"`
	Email             string          `json:"email,omitempty"`
	Source            string          `json:"source,omitempty"`
	AuthenticatedAt   time.Time       `json:"authenticatedAt,omitempty"`
	LastUsedAt        time.Time       `json:"lastUsedAt,omitempty"`
	RecordedProfileID string          `json:"recordedProfileId,omitempty"`
	RecordedAlias     string          `json:"recordedAlias,omitempty"`
}

type SyncOptions struct {
	Alias      string
	PreferLive bool
}

type SyncResult struct {
	Changed            bool         `json:"changed"`
	CredentialsUpdated bool         `json:"credentialsUpdated"`
	StateRepaired      bool         `json:"stateRepaired"`
	Imported           bool         `json:"imported"`
	DetectedState      AccountState `json:"detectedState"`
	ProfileID          string       `json:"profileId,omitempty"`
	Alias              string       `json:"alias,omitempty"`
}

type observationSnapshot struct {
	observation Observation
	data        vault.Data
	state       appstate.State
	live        authschema.Document
	liveHash    string
	hasLive     bool
}

func (service Service) Observe() (Observation, error) {
	snapshot, err := service.observeUnlocked()
	if err != nil {
		return Observation{}, err
	}
	return snapshot.observation, nil
}

func (service Service) observeUnlocked() (observationSnapshot, error) {
	data, err := service.Vault.Load()
	if err != nil {
		return observationSnapshot{}, err
	}
	state, err := appstate.Load(service.Paths.State)
	if err != nil {
		return observationSnapshot{}, err
	}
	snapshot := observationSnapshot{data: data, state: state, liveHash: "missing"}
	raw, err := service.Home.ReadAuth()
	if errors.Is(err, os.ErrNotExist) {
		snapshot.observation = observationWithoutLive(data, state)
		return snapshot, nil
	}
	if err != nil {
		return observationSnapshot{}, err
	}
	live, err := authschema.Parse(raw)
	if err != nil {
		return observationSnapshot{}, err
	}
	snapshot.live = live
	snapshot.liveHash = atomicfile.Hash(raw)
	snapshot.hasLive = true
	snapshot.observation = observeLive(data, state, live, snapshot.liveHash)
	return snapshot, nil
}

func observationWithoutLive(data vault.Data, state appstate.State) Observation {
	observation := Observation{State: AccountStateNoActive}
	if state.ActiveProfileID == "" {
		return observation
	}
	observation.State = AccountStateLoggedOut
	observation.NeedsSync = true
	observation.RecordedProfileID = state.ActiveProfileID
	if profile, err := data.FindByID(state.ActiveProfileID); err == nil {
		observation.RecordedAlias = profile.Alias
	}
	return observation
}

func observeLive(data vault.Data, state appstate.State, live authschema.Document, liveHash string) Observation {
	observation := Observation{
		HasLive:           true,
		NeedsSync:         true,
		AccountID:         live.Tokens.AccountID,
		WorkspaceID:       live.WorkspaceID,
		Email:             live.Email,
		RecordedProfileID: state.ActiveProfileID,
	}
	if state.ActiveProfileID != "" {
		if profile, err := data.FindByID(state.ActiveProfileID); err == nil {
			observation.RecordedAlias = profile.Alias
		}
	}
	profile, err := identifyCurrent(&data, live)
	if err != nil {
		observation.State = AccountStateAmbiguous
		return observation
	}
	if profile == nil {
		observation.State = AccountStateUnmanaged
		return observation
	}

	observation.Managed = true
	observation.ProfileID = profile.ID
	observation.Alias = profile.Alias
	observation.Source = profile.Source
	observation.AuthenticatedAt = profile.AuthenticatedAt
	observation.LastUsedAt = profile.LastUsedAt
	if observation.Email == "" {
		observation.Email = profile.Email
	}
	saved, parseErr := profileIdentity(profile, live)
	if parseErr != nil {
		observation.State = AccountStateCredentialConflict
		observation.CredentialState = CredentialStateAmbiguous
		return observation
	}
	decision, compareErr := authschema.ValidateRefresh(saved, live)
	switch {
	case errors.Is(compareErr, authschema.ErrAmbiguousGeneration):
		observation.CredentialState = CredentialStateAmbiguous
		observation.State = AccountStateCredentialConflict
		return observation
	case compareErr != nil:
		observation.CredentialState = CredentialStateAmbiguous
		observation.State = AccountStateCredentialConflict
		return observation
	case decision == authschema.GenerationAdoptLive:
		observation.CredentialState = CredentialStateLiveNewer
	case decision == authschema.GenerationUseSaved:
		observation.CredentialState = CredentialStateSavedNewer
		observation.State = AccountStateCredentialConflict
		return observation
	default:
		observation.CredentialState = CredentialStateCurrent
	}

	externalLogin := state.ActiveProfileID != profile.ID
	switch {
	case externalLogin && observation.CredentialState == CredentialStateLiveNewer:
		observation.State = AccountStateExternalLoginWithRefresh
	case externalLogin:
		observation.State = AccountStateExternalLogin
	case observation.CredentialState == CredentialStateLiveNewer:
		observation.State = AccountStateCredentialRefresh
	default:
		if state.AuthHash != liveHash {
			observation.State = AccountStateStateDrift
		} else {
			observation.State = AccountStateInSync
			observation.NeedsSync = false
		}
	}
	return observation
}

func (service Service) Sync(options SyncOptions) (SyncResult, error) {
	lock, err := service.acquireLock()
	if err != nil {
		return SyncResult{}, err
	}
	defer func() { _ = lock.Close() }()
	if err := service.recoverUnlocked(); err != nil {
		return SyncResult{}, err
	}
	snapshot, err := service.observeUnlocked()
	if err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{DetectedState: snapshot.observation.State}
	if !snapshot.hasLive {
		if options.Alias != "" {
			return result, errors.New("codex is not logged in; --as requires an active ChatGPT login")
		}
		if snapshot.state.ActiveProfileID == "" && snapshot.state.AuthHash == "" {
			return result, nil
		}
		if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
			return result, err
		}
		if err := appstate.Save(service.Paths.State, appstate.State{}); err != nil {
			return result, err
		}
		result.Changed = true
		result.StateRepaired = true
		return result, nil
	}

	if snapshot.observation.State == AccountStateAmbiguous {
		return result, errors.New("multiple saved profiles match the active Codex login; no state was changed")
	}
	if !snapshot.observation.Managed {
		if options.Alias == "" {
			return result, errors.New("the active Codex login is unmanaged; run 'codex-switch sync --as <alias>' to preserve it")
		}
		updatedAt, _ := snapshot.live.GenerationTime()
		profile := vault.NewProfile(options.Alias, "sync", snapshot.live.Raw, snapshot.live.Tokens.AccountID, snapshot.live.WorkspaceID, snapshot.live.Email, updatedAt)
		if err := snapshot.data.Add(profile, false); err != nil {
			return result, err
		}
		if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
			return result, err
		}
		if err := service.Vault.Save(snapshot.data); err != nil {
			return result, err
		}
		if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
			return result, err
		}
		saved, err := snapshot.data.FindByAlias(options.Alias)
		if err != nil {
			return result, err
		}
		if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: saved.ID, AuthHash: snapshot.liveHash}); err != nil {
			return result, err
		}
		result.Changed = true
		result.CredentialsUpdated = true
		result.StateRepaired = true
		result.Imported = true
		result.ProfileID = saved.ID
		result.Alias = saved.Alias
		return result, nil
	}
	if options.Alias != "" {
		return result, errors.New("--as can only be used when the active Codex login is unmanaged")
	}

	profile, err := snapshot.data.FindByID(snapshot.observation.ProfileID)
	if err != nil {
		return result, err
	}
	credentialsUpdated, decision, err := updateProfileFromLive(profile, snapshot.live, options.PreferLive)
	if err != nil {
		return result, err
	}
	if decision == authschema.GenerationUseSaved && !options.PreferLive {
		return result, errors.New("the saved credentials are newer than the live Codex login; rerun with --prefer-live to replace them")
	}
	stateRepaired := snapshot.state.ActiveProfileID != profile.ID || snapshot.state.AuthHash != snapshot.liveHash
	if !credentialsUpdated && !stateRepaired {
		result.ProfileID = profile.ID
		result.Alias = profile.Alias
		return result, nil
	}
	if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
		return result, err
	}
	if credentialsUpdated {
		if err := service.Vault.Save(snapshot.data); err != nil {
			return result, err
		}
	}
	if stateRepaired {
		if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
			return result, err
		}
		if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: profile.ID, AuthHash: snapshot.liveHash}); err != nil {
			return result, err
		}
	}
	result.Changed = true
	result.CredentialsUpdated = credentialsUpdated
	result.StateRepaired = stateRepaired
	result.ProfileID = profile.ID
	result.Alias = profile.Alias
	return result, nil
}

// Both the saved document and persisted metadata can carry known workspace
// evidence. Generation preference cannot authorize losing either identity.
func profileIdentity(profile *vault.Profile, live authschema.Document) (authschema.Document, error) {
	saved, err := authschema.Parse(profile.Auth)
	if err != nil {
		return authschema.Document{}, fmt.Errorf("saved active account is invalid: %w", err)
	}
	if err := authschema.ValidateIdentity(profile.AccountID, profile.WorkspaceID, live); err != nil {
		return authschema.Document{}, err
	}
	if err := authschema.ValidateIdentity(saved.Tokens.AccountID, saved.WorkspaceID, live); err != nil {
		return authschema.Document{}, err
	}
	return saved, nil
}

func updateProfileFromLive(profile *vault.Profile, live authschema.Document, preferLive bool) (bool, authschema.GenerationDecision, error) {
	saved, err := profileIdentity(profile, live)
	if err != nil {
		return false, authschema.GenerationSame, err
	}
	decision, err := authschema.ValidateRefresh(saved, live)
	if errors.Is(err, authschema.ErrAmbiguousGeneration) {
		if !preferLive {
			return false, authschema.GenerationSame, errors.New("the active Codex credentials changed but their generation is ambiguous; rerun 'codex-switch sync --prefer-live' to adopt the live login")
		}
		decision = authschema.GenerationAdoptLive
	} else if err != nil {
		return false, authschema.GenerationSame, err
	}
	shouldAdopt := decision == authschema.GenerationAdoptLive || preferLive && decision == authschema.GenerationUseSaved
	if !shouldAdopt {
		return false, decision, nil
	}
	profile.Auth = append([]byte(nil), live.Raw...)
	profile.Email = live.Email
	profile.WorkspaceID = live.WorkspaceID
	if refreshed, ok := live.GenerationTime(); ok {
		profile.TokenUpdatedAt = refreshed
	}
	return true, decision, nil
}

func (service Service) ensureLiveHash(expected string) error {
	actual, err := service.Home.AuthHash()
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("codex credentials changed while account state was being synchronized; active state was not committed, retry")
	}
	return nil
}

func (service Service) acquireLock() (*filelock.Operation, error) {
	return filelock.AcquireOperation(service.Paths.Vault, service.Home.Path)
}
