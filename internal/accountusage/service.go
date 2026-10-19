package accountusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexhome"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
	appconfig "github.com/SilkageNet/codex-switch/internal/config"
	"github.com/SilkageNet/codex-switch/internal/filelock"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/switcher"
	"github.com/SilkageNet/codex-switch/internal/usagecache"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

const (
	queryTimeout          = 20 * time.Second
	defaultQueryAttempts  = 3
	defaultRetryBaseDelay = 250 * time.Millisecond
)

type QueryRunner interface {
	Query(context.Context, json.RawMessage) (codexusage.Snapshot, json.RawMessage, error)
}

type Service struct {
	Home   codexhome.Home
	Paths  appconfig.Paths
	Vault  *vault.Manager
	Runner QueryRunner

	queryAttempts  int
	retryBaseDelay time.Duration
}

type Result struct {
	ProfileID string              `json:"profileId"`
	Snapshot  codexusage.Snapshot `json:"snapshot,omitempty"`
	Error     string              `json:"error,omitempty"`
	Attempts  int                 `json:"attempts,omitempty"`
}

type queryResult struct {
	profileID string
	snapshot  codexusage.Snapshot
	auth      json.RawMessage
	err       error
	attempts  int
}

func (service Service) Cached() (usagecache.Cache, error) {
	return usagecache.Load(service.Paths.UsageCache)
}

func (service Service) Refresh(ctx context.Context, profileIDs []string) (map[string]Result, error) {
	if service.Runner == nil {
		return nil, errors.New("account usage runner is not configured")
	}
	lock, err := filelock.AcquireOperation(service.Paths.Vault, service.Home.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()

	data, err := service.Vault.Load()
	if err != nil {
		return nil, err
	}
	selected := make([]vault.Profile, 0, len(profileIDs))
	seen := make(map[string]bool, len(profileIDs))
	for _, id := range profileIDs {
		profile, findErr := data.Find(id)
		if findErr != nil {
			return nil, findErr
		}
		if !seen[profile.ID] {
			selected = append(selected, *profile)
			seen[profile.ID] = true
		}
	}

	queried := make([]queryResult, 0, len(selected))
	for _, profile := range selected {
		snapshot, updatedAuth, attempts, queryErr := service.queryWithRetry(ctx, profile.Auth)
		queried = append(queried, queryResult{profileID: profile.ID, snapshot: snapshot, auth: updatedAuth, err: queryErr, attempts: attempts})
	}

	results := make(map[string]Result, len(selected))
	candidates := make(map[string]json.RawMessage, len(selected))
	cache, err := usagecache.Load(service.Paths.UsageCache)
	if err != nil {
		return nil, err
	}
	for _, result := range queried {
		entry := Result{ProfileID: result.profileID, Attempts: result.attempts}
		if result.err != nil {
			entry.Error = result.err.Error()
		} else {
			entry.Snapshot = result.snapshot
		}
		if len(result.auth) > 0 {
			candidates[result.profileID] = result.auth
		}
		results[result.profileID] = entry
	}

	data, err = service.Vault.Load()
	if err != nil {
		return nil, err
	}
	state, stateErr := appstate.Load(service.Paths.State)
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return nil, stateErr
	}
	observation, observationErr := (switcher.Service{Home: service.Home, Paths: service.Paths, Vault: service.Vault}).Observe()
	if observationErr != nil {
		return nil, observationErr
	}
	activeProfileID := ""
	if observation.Managed {
		activeProfileID = observation.ProfileID
	}
	vaultChanged := false
	for profileID, candidateRaw := range candidates {
		profile, findErr := data.Find(profileID)
		if findErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "profile was removed while usage was being queried")
			results[profileID] = entry
			delete(cache.Profiles, profileID)
			continue
		}
		candidate, parseErr := authschema.Parse(candidateRaw)
		if parseErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "refreshed credentials are invalid")
			results[profileID] = entry
			continue
		}
		if candidate.Tokens.AccountID != profile.AccountID || (profile.WorkspaceID != "" && candidate.WorkspaceID != "" && candidate.WorkspaceID != profile.WorkspaceID) {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "refreshed credentials do not match the saved account")
			results[profileID] = entry
			continue
		}
		if results[profileID].Error == "" {
			cache.Profiles[profileID] = results[profileID].Snapshot
		}
		saved, parseErr := authschema.Parse(profile.Auth)
		if parseErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "saved credentials became invalid")
			results[profileID] = entry
			continue
		}
		decision, compareErr := authschema.CompareGeneration(saved, candidate)
		if compareErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, credentialError(compareErr))
			results[profileID] = entry
			continue
		}
		if decision != authschema.GenerationAdoptLive {
			continue
		}
		profile.Auth = append(json.RawMessage(nil), candidate.Raw...)
		profile.Email = candidate.Email
		profile.WorkspaceID = candidate.WorkspaceID
		if refreshed, ok := candidate.GenerationTime(); ok {
			profile.TokenUpdatedAt = refreshed
		}
		vaultChanged = true

		if activeProfileID == profile.ID {
			changed, syncErr := service.reconcileActive(profile, candidate, &state)
			if syncErr != nil {
				entry := results[profileID]
				entry.Error = appendError(entry.Error, syncErr.Error())
				results[profileID] = entry
			} else if changed {
				vaultChanged = true
			}
		}
	}
	if vaultChanged {
		if err := service.Vault.Save(data); err != nil {
			return nil, err
		}
	}
	validProfiles := make(map[string]bool, len(data.Profiles))
	for _, profile := range data.Profiles {
		validProfiles[profile.ID] = true
	}
	for profileID := range cache.Profiles {
		if !validProfiles[profileID] {
			delete(cache.Profiles, profileID)
		}
	}
	for profileID, result := range results {
		if result.Error == "" {
			delete(cache.Failures, profileID)
			continue
		}
		cache.Failures[profileID] = usagecache.Failure{
			AttemptedAt: time.Now().UTC(),
			Message:     result.Error,
			Attempts:    result.Attempts,
		}
	}
	for profileID := range cache.Failures {
		if !validProfiles[profileID] {
			delete(cache.Failures, profileID)
		}
	}
	if err := usagecache.Save(service.Paths.UsageCache, cache); err != nil {
		return nil, err
	}
	return results, nil
}

func (service Service) queryWithRetry(ctx context.Context, auth json.RawMessage) (codexusage.Snapshot, json.RawMessage, int, error) {
	attemptLimit := service.queryAttempts
	if attemptLimit <= 0 {
		attemptLimit = defaultQueryAttempts
	}
	baseDelay := service.retryBaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}
	currentAuth := append(json.RawMessage(nil), auth...)
	var latestAuth json.RawMessage
	var lastErr error
	for attempt := 1; attempt <= attemptLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return codexusage.Snapshot{}, nil, attempt - 1, err
		}
		queryContext, cancel := context.WithTimeout(ctx, queryTimeout)
		snapshot, updatedAuth, err := service.Runner.Query(queryContext, currentAuth)
		cancel()
		if len(updatedAuth) > 0 {
			latestAuth = append(latestAuth[:0], updatedAuth...)
			currentAuth = append(currentAuth[:0], updatedAuth...)
		}
		if err == nil {
			if len(updatedAuth) == 0 {
				updatedAuth = latestAuth
			}
			return snapshot, updatedAuth, attempt, nil
		}
		lastErr = err
		if attempt == attemptLimit {
			break
		}
		delay := baseDelay << (attempt - 1)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return codexusage.Snapshot{}, latestAuth, attempt, ctx.Err()
		case <-timer.C:
		}
	}
	return codexusage.Snapshot{}, latestAuth, attemptLimit, fmt.Errorf("query failed after %d attempts: %w", attemptLimit, lastErr)
}

func (service Service) reconcileActive(profile *vault.Profile, candidate authschema.Document, state *appstate.State) (bool, error) {
	liveRaw, err := service.Home.ReadAuth()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read active credentials after refresh: %w", err)
	}
	liveHash := atomicfile.Hash(liveRaw)
	live, err := authschema.Parse(liveRaw)
	if err != nil {
		return false, fmt.Errorf("validate active credentials after refresh: %w", err)
	}
	if live.Tokens.AccountID != candidate.Tokens.AccountID ||
		(live.WorkspaceID != "" && candidate.WorkspaceID != "" && live.WorkspaceID != candidate.WorkspaceID) {
		return false, errors.New("active account changed while usage was being queried; refreshed credentials were kept only in the vault")
	}
	decision, err := authschema.CompareGeneration(candidate, live)
	if err != nil {
		return false, errors.New(credentialError(err))
	}
	switch decision {
	case authschema.GenerationAdoptLive:
		profile.Auth = append(json.RawMessage(nil), live.Raw...)
		profile.Email = live.Email
		profile.WorkspaceID = live.WorkspaceID
		if refreshed, ok := live.GenerationTime(); ok {
			profile.TokenUpdatedAt = refreshed
		}
		if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: profile.ID, AuthHash: liveHash}); err != nil {
			return false, fmt.Errorf("record active credentials: %w", err)
		}
		*state = appstate.State{Version: 1, ActiveProfileID: profile.ID, AuthHash: liveHash}
		return true, nil
	case authschema.GenerationUseSaved:
		currentHash, hashErr := service.Home.AuthHash()
		if hashErr != nil {
			return false, hashErr
		}
		if currentHash != liveHash {
			return false, errors.New("active credentials changed while usage was being queried; no active file was replaced")
		}
		if err := service.Home.WriteAuth(candidate.Raw); err != nil {
			return false, fmt.Errorf("publish refreshed active credentials: %w", err)
		}
		publishedHash, err := service.Home.AuthHash()
		if err != nil {
			return false, err
		}
		if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: profile.ID, AuthHash: publishedHash}); err != nil {
			return false, fmt.Errorf("record refreshed active credentials: %w", err)
		}
		*state = appstate.State{Version: 1, ActiveProfileID: profile.ID, AuthHash: publishedHash}
	case authschema.GenerationSame:
		if state.ActiveProfileID != profile.ID || state.AuthHash != liveHash {
			if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: profile.ID, AuthHash: liveHash}); err != nil {
				return false, fmt.Errorf("record active credentials: %w", err)
			}
			*state = appstate.State{Version: 1, ActiveProfileID: profile.ID, AuthHash: liveHash}
		}
	}
	return false, nil
}

func credentialError(err error) string {
	if errors.Is(err, authschema.ErrAmbiguousGeneration) {
		return "refreshed Token generations are ambiguous; reauthenticate this profile"
	}
	return err.Error()
}

func appendError(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}
