package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexhome"
	appconfig "github.com/SilkageNet/codex-switch/internal/config"
	"github.com/SilkageNet/codex-switch/internal/process"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/vault"
)

type Journal struct {
	Version         int       `json:"version"`
	Operation       string    `json:"operation"`
	TargetProfileID string    `json:"targetProfileId,omitempty"`
	OldHash         string    `json:"oldHash"`
	NewHash         string    `json:"newHash"`
	PreparedAt      time.Time `json:"preparedAt"`
}

type Service struct {
	Home  codexhome.Home
	Paths appconfig.Paths
	Vault *vault.Manager
}

type Result struct {
	Changed            bool   `json:"changed"`
	CredentialsUpdated bool   `json:"credentialsUpdated"`
	StateRepaired      bool   `json:"stateRepaired"`
	ProfileID          string `json:"profileId"`
	Alias              string `json:"alias"`
}

func (service Service) Use(alias string, allowRunning bool) (Result, error) {
	lock, err := service.acquireLock()
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Close() }()
	if err := service.recoverUnlocked(); err != nil {
		return Result{}, err
	}
	snapshot, err := service.observeUnlocked()
	if err != nil {
		return Result{}, err
	}
	target, err := snapshot.data.Find(alias)
	if err != nil {
		return Result{}, err
	}
	targetDocument, err := authschema.Parse(target.Auth)
	if err != nil {
		return Result{}, fmt.Errorf("target account is invalid: %w", err)
	}

	credentialsUpdated := false
	if snapshot.hasLive {
		if snapshot.observation.State == AccountStateAmbiguous {
			return Result{}, errors.New("multiple saved profiles match the active Codex login; run 'codex-switch sync --check' for details")
		}
		if !snapshot.observation.Managed {
			return Result{}, errors.New("the active Codex login is not managed; preserve it with 'codex-switch sync --as <alias>' before switching")
		}
		currentProfile, findErr := snapshot.data.FindByID(snapshot.observation.ProfileID)
		if findErr != nil {
			return Result{}, findErr
		}
		credentialsUpdated, _, err = updateProfileFromLive(currentProfile, snapshot.live, false)
		if err != nil {
			return Result{}, err
		}
		if currentProfile.ID == target.ID {
			stateRepaired := snapshot.state.ActiveProfileID != target.ID || snapshot.state.AuthHash != snapshot.liveHash
			if !credentialsUpdated && !stateRepaired {
				return Result{Changed: false, ProfileID: target.ID, Alias: target.Alias}, nil
			}
			if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
				return Result{}, err
			}
			if credentialsUpdated {
				if err := service.Vault.Save(snapshot.data); err != nil {
					return Result{}, err
				}
			}
			if stateRepaired {
				if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
					return Result{}, err
				}
				if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: target.ID, AuthHash: snapshot.liveHash}); err != nil {
					return Result{}, err
				}
			}
			return Result{Changed: false, CredentialsUpdated: credentialsUpdated, StateRepaired: stateRepaired, ProfileID: target.ID, Alias: target.Alias}, nil
		}
	}

	if err := service.ensureStopped(allowRunning); err != nil {
		return Result{}, err
	}
	if err := service.ensureLiveHash(snapshot.liveHash); err != nil {
		return Result{}, err
	}
	target.LastUsedAt = time.Now().UTC()
	if err := service.Vault.Save(snapshot.data); err != nil {
		return Result{}, fmt.Errorf("update target account metadata: %w", err)
	}
	publishedBytes := append(append([]byte(nil), targetDocument.Raw...), '\n')
	newHash := atomicfile.Hash(publishedBytes)
	journal := Journal{Version: 1, Operation: "use", TargetProfileID: target.ID, OldHash: snapshot.liveHash, NewHash: newHash, PreparedAt: time.Now().UTC()}
	if err := service.writeJournal(journal); err != nil {
		return Result{}, err
	}

	currentHash, err := service.Home.AuthHash()
	if err != nil {
		return Result{}, err
	}
	if currentHash != snapshot.liveHash {
		return Result{}, errors.New("codex credentials changed during the switch; no file was replaced, retry after closing Codex")
	}
	if err := service.Home.WriteAuth(targetDocument.Raw); err != nil {
		return Result{}, err
	}
	actualHash, err := service.Home.AuthHash()
	if err != nil {
		return Result{}, err
	}
	if actualHash != newHash {
		return Result{}, errors.New("published auth.json did not match the target account")
	}
	if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: target.ID, AuthHash: actualHash}); err != nil {
		return Result{}, fmt.Errorf("auth switched but active state could not be recorded; run doctor: %w", err)
	}
	if err := os.Remove(service.Paths.Journal); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("auth switched but journal cleanup failed: %w", err)
	}
	return Result{Changed: true, CredentialsUpdated: credentialsUpdated, StateRepaired: snapshot.state.ActiveProfileID != target.ID, ProfileID: target.ID, Alias: target.Alias}, nil
}

func (service Service) Deactivate(allowRunning bool) error {
	lock, err := service.acquireLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := service.recoverUnlocked(); err != nil {
		return err
	}
	if err := service.ensureStopped(allowRunning); err != nil {
		return err
	}
	data, err := service.Vault.Load()
	if err != nil {
		return err
	}
	liveDocument, hasLive, err := service.readLive()
	if err != nil {
		return err
	}
	if hasLive {
		currentProfile, findErr := identifyCurrent(&data, liveDocument)
		if findErr != nil {
			return findErr
		}
		if currentProfile == nil {
			return errors.New("the active Codex login is not managed; preserve it with 'codex-switch sync --as <alias>' before deactivating")
		}
		if err := reconcileProfile(service.Vault, data, currentProfile, liveDocument); err != nil {
			return err
		}
	}
	oldHash, err := service.Home.AuthHash()
	if err != nil {
		return err
	}
	journal := Journal{Version: 1, Operation: "deactivate", OldHash: oldHash, NewHash: "missing", PreparedAt: time.Now().UTC()}
	if err := service.writeJournal(journal); err != nil {
		return err
	}
	if err := service.Home.DeleteAuth(); err != nil {
		return err
	}
	if err := appstate.Save(service.Paths.State, appstate.State{}); err != nil {
		return err
	}
	return os.Remove(service.Paths.Journal)
}

func (service Service) Recover() error {
	if err := service.Home.Ensure(); err != nil {
		return err
	}
	lock, err := service.acquireLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	return service.recoverUnlocked()
}

func (service Service) recoverUnlocked() error {
	encoded, err := atomicfile.ReadLimited(service.Paths.Journal, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal Journal
	if err := json.Unmarshal(encoded, &journal); err != nil || journal.Version != 1 {
		return errors.New("switch recovery journal is invalid; move it aside and run doctor")
	}
	actualHash, err := service.Home.AuthHash()
	if err != nil {
		return err
	}
	switch actualHash {
	case journal.OldHash:
		return os.Remove(service.Paths.Journal)
	case journal.NewHash:
		if journal.Operation == "use" {
			if err := appstate.Save(service.Paths.State, appstate.State{ActiveProfileID: journal.TargetProfileID, AuthHash: actualHash}); err != nil {
				return err
			}
		} else {
			if err := appstate.Save(service.Paths.State, appstate.State{}); err != nil {
				return err
			}
		}
		return os.Remove(service.Paths.Journal)
	default:
		return errors.New("auth.json matches neither side of an interrupted switch; refusing automatic recovery")
	}
}

func (service Service) writeJournal(journal Journal) error {
	encoded, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(service.Paths.Journal, append(encoded, '\n'), 0o600)
}

func (service Service) ensureStopped(allowRunning bool) error {
	if allowRunning {
		return nil
	}
	running, err := process.DetectCodex()
	if err != nil {
		return err
	}
	if len(running) == 0 {
		return nil
	}
	return fmt.Errorf("codex is still running (PID %d, %s); close it before switching or explicitly pass --allow-running", running[0].PID, running[0].Command)
}

func (service Service) readLive() (authschema.Document, bool, error) {
	data, err := service.Home.ReadAuth()
	if errors.Is(err, os.ErrNotExist) {
		return authschema.Document{}, false, nil
	}
	if err != nil {
		return authschema.Document{}, false, err
	}
	document, err := authschema.Parse(data)
	return document, err == nil, err
}

func identifyCurrent(data *vault.Data, live authschema.Document) (*vault.Profile, error) {
	accountMatches := data.FindByAccount(live.Tokens.AccountID, "")
	exactMatches := make([]*vault.Profile, 0, len(accountMatches))
	matches := make([]*vault.Profile, 0, len(accountMatches))
	for _, profile := range accountMatches {
		document, err := profileIdentity(profile, live)
		if err != nil {
			continue
		}
		matches = append(matches, profile)
		if authschema.SameMaterial(document, live) {
			exactMatches = append(exactMatches, profile)
		}
	}
	if len(exactMatches) == 1 {
		return exactMatches[0], nil
	}
	if len(exactMatches) > 1 {
		return nil, errors.New("multiple managed profiles exactly match the active ChatGPT credentials")
	}
	// Missing live workspace evidence cannot disambiguate a changed generation
	// from other profiles of the same account, even if one has unknown metadata.
	if live.WorkspaceID == "" && len(accountMatches) > 1 {
		return nil, authschema.ErrIdentityMismatch
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, errors.New("multiple managed profiles match the active ChatGPT account; no profile can be selected safely")
	}
	if len(accountMatches) != 0 {
		return nil, authschema.ErrIdentityMismatch
	}
	return nil, nil
}

func reconcileProfile(manager *vault.Manager, data vault.Data, profile *vault.Profile, live authschema.Document) error {
	updated, _, err := updateProfileFromLive(profile, live, false)
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}
	return manager.Save(data)
}
