package usagecache

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
)

type Cache struct {
	Version  int                            `json:"version"`
	Profiles map[string]codexusage.Snapshot `json:"profiles"`
	Failures map[string]Failure             `json:"failures,omitempty"`
}

type Failure struct {
	AttemptedAt time.Time `json:"attemptedAt"`
	Message     string    `json:"message"`
	Attempts    int       `json:"attempts,omitempty"`
}

func Load(path string) (Cache, error) {
	data, err := atomicfile.ReadLimited(path, 16<<20)
	if errors.Is(err, os.ErrNotExist) {
		return Cache{Version: 1, Profiles: map[string]codexusage.Snapshot{}, Failures: map[string]Failure{}}, nil
	}
	if err != nil {
		return Cache{}, errors.New("read usage cache failed")
	}
	var cache Cache
	if err := json.Unmarshal(data, &cache); err != nil {
		return Cache{}, errors.New("usage cache is invalid")
	}
	if cache.Version != 1 {
		return Cache{}, errors.New("usage cache is invalid")
	}
	if cache.Profiles == nil {
		cache.Profiles = map[string]codexusage.Snapshot{}
	}
	if cache.Failures == nil {
		cache.Failures = map[string]Failure{}
	}
	normalizeFailures(&cache)
	return cache, nil
}

func Save(path string, cache Cache) error {
	cache.Version = 1
	if cache.Profiles == nil {
		cache.Profiles = map[string]codexusage.Snapshot{}
	}
	if cache.Failures == nil {
		cache.Failures = map[string]Failure{}
	}
	normalizeFailures(&cache)
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return errors.New("encode usage cache failed")
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return errors.New("write usage cache failed")
	}
	return nil
}

func normalizeFailures(cache *Cache) {
	for id, failure := range cache.Failures {
		failure.Message = codexusage.NormalizeErrorMessage(failure.Message)
		cache.Failures[id] = failure
	}
}
