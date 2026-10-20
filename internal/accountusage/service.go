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
	retryWait      func(context.Context, time.Duration) error
}

type Result struct {
	ProfileID string              `json:"profileId"`
	Snapshot  codexusage.Snapshot `json:"snapshot,omitempty"`
	Error     string              `json:"error,omitempty"`
	Attempts  int                 `json:"attempts,omitempty"`
}

func (service Service) Cached() (usagecache.Cache, error) {
	return usagecache.Load(service.Paths.UsageCache)
}

func (service Service) Refresh(ctx context.Context, profileIDs []string) (output map[string]Result, resultErr error) {
	defer func() { resultErr = codexusage.RedactError(resultErr) }()
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
		profile, findErr := data.FindByID(id)
		if findErr != nil {
			return nil, findErr
		}
		if !seen[profile.ID] {
			selected = append(selected, *profile)
			seen[profile.ID] = true
		}
	}

	results := make(map[string]Result, len(selected))
	candidates := make(map[string]json.RawMessage, len(selected))
	successful := make(map[string]codexusage.Snapshot)
	for _, profile := range selected {
		snapshot, updatedAuth, attempts, queryErr := service.queryWithRetry(ctx, profile.Auth)
		entry := Result{ProfileID: profile.ID, Attempts: attempts}
		if queryErr != nil {
			entry.Error = codexusage.ErrorMessage(queryErr)
		} else {
			entry.Snapshot = snapshot
		}
		if len(updatedAuth) > 0 {
			candidates[profile.ID] = updatedAuth
		}
		results[profile.ID] = entry
	}

	data, err = service.Vault.Load()
	if err != nil {
		return nil, err
	}
	state, stateErr := appstate.Load(service.Paths.State)
	var reconciliationErr error
	if stateErr != nil {
		reconciliationErr = stateErr
	}
	observation, observationErr := (switcher.Service{Home: service.Home, Paths: service.Paths, Vault: service.Vault}).Observe()
	if observationErr != nil {
		reconciliationErr = observationErr
	}
	activeProfileID := ""
	if observation.Managed && reconciliationErr == nil {
		activeProfileID = observation.ProfileID
	}
	vaultChanged := false
	for profileID, candidateRaw := range candidates {
		profile, findErr := data.FindByID(profileID)
		if findErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "profile was removed while usage was being queried")
			results[profileID] = entry
			continue
		}
		candidate, parseErr := authschema.Parse(candidateRaw)
		if parseErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "refreshed credentials are invalid")
			results[profileID] = entry
			continue
		}
		if authschema.ValidateIdentity(profile.AccountID, profile.WorkspaceID, candidate) != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "refreshed credentials do not match the saved account or workspace")
			results[profileID] = entry
			continue
		}
		saved, parseErr := authschema.Parse(profile.Auth)
		if parseErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "saved credentials became invalid")
			results[profileID] = entry
			continue
		}
		decision, compareErr := authschema.ValidateRefresh(saved, candidate)
		if compareErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, credentialError(compareErr))
			results[profileID] = entry
			continue
		}
		if decision == authschema.GenerationUseSaved {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "refreshed credentials are older than the validated generation")
			results[profileID] = entry
			continue
		}
		if results[profileID].Error == "" {
			successful[profileID] = results[profileID].Snapshot
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

		if reconciliationErr != nil {
			entry := results[profileID]
			entry.Error = appendError(entry.Error, "active credential reconciliation failed (diagnostics redacted)")
			results[profileID] = entry
		}
		if activeProfileID == profile.ID {
			changed, syncErr := service.reconcileActive(profile, candidate, &state)
			if syncErr != nil {
				entry := results[profileID]
				entry.Error = appendError(entry.Error, "active credential reconciliation failed (diagnostics redacted)")
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
	cache, err := usagecache.Load(service.Paths.UsageCache)
	if err != nil {
		return nil, errors.New("usage cache update failed after credential reconciliation")
	}
	for id, snapshot := range successful {
		cache.Profiles[id] = snapshot
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
	current, err := authschema.Parse(auth)
	if err != nil {
		return codexusage.Snapshot{}, nil, 0, errors.New("saved credentials became invalid")
	}
	var latestAuth json.RawMessage
	var lastErr error
	wait := service.retryWait
	if wait == nil {
		wait = waitRetry
	}
	for attempt := 1; attempt <= attemptLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return codexusage.Snapshot{}, latestAuth, attempt - 1, codexusage.RedactError(errors.Join(lastErr, err))
		}
		queryContext, cancel := context.WithTimeout(ctx, queryTimeout)
		snapshot, candidateRaw, queryErr := service.Runner.Query(queryContext, current.Raw)
		contextErr := queryContext.Err()
		cancel()
		if queryErr == nil && contextErr != nil {
			queryErr = contextErr
		}
		if len(candidateRaw) > 0 {
			candidate, parseErr := authschema.Parse(candidateRaw)
			if parseErr != nil {
				queryErr = errors.Join(queryErr, errors.New("refreshed credentials are invalid"))
			} else {
				decision, validationErr := authschema.ValidateRefresh(current, candidate)
				switch {
				case validationErr != nil:
					queryErr = errors.Join(queryErr, errors.New(credentialError(validationErr)))
				case decision == authschema.GenerationUseSaved:
					queryErr = errors.Join(queryErr, errors.New("refreshed credentials are older than the validated generation"))
				case decision == authschema.GenerationAdoptLive:
					current = candidate
					latestAuth = append(json.RawMessage(nil), candidate.Raw...)
				}
			}
		}
		if queryErr == nil {
			return snapshot, current.Raw, attempt, nil
		}
		lastErr = codexusage.RedactError(queryErr)
		if attempt == attemptLimit {
			break
		}
		if err := wait(ctx, baseDelay<<(attempt-1)); err != nil {
			return codexusage.Snapshot{}, latestAuth, attempt, codexusage.RedactError(errors.Join(lastErr, err))
		}
	}
	return codexusage.Snapshot{}, latestAuth, attemptLimit, codexusage.RetryError(attemptLimit, lastErr)
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
		(live.WorkspaceID != candidate.WorkspaceID) {
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
		return "refreshed token generations are ambiguous (reauthentication required)"
	}
	return codexusage.ErrorMessage(err)
}

func appendError(existing, next string) string {
	if existing == "" {
		return codexusage.NormalizeErrorMessage(next)
	}
	return codexusage.NormalizeErrorMessage(existing + "; " + next)
}
