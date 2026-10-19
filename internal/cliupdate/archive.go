package cliupdate

import (
	"errors"
	"path"
	"strings"
)

type archiveState struct {
	expected string
	seen     map[string]bool
	total    int64
	binary   []byte
}

func (state *archiveState) check(name string, directory bool, size int64) error {
	clean := strings.TrimSuffix(name, "/")
	key := strings.ToLower(clean)
	if clean == "" || path.Clean(clean) != clean || strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") || strings.ContainsAny(clean, "\\:\x00\r\n") || state.seen[key] || len(state.seen) >= 256 {
		return errors.New("unsafe or duplicate archive path")
	}
	for _, component := range strings.Split(clean, "/") {
		if !safeAssetName(component) {
			return errors.New("unsafe archive path")
		}
	}
	state.seen[key] = true
	if directory {
		if clean != "LICENSES" || size != 0 {
			return errors.New("unexpected archive directory")
		}
		return nil
	}
	if name != clean {
		return errors.New("invalid archive file path")
	}
	license := strings.HasPrefix(clean, "LICENSES/") && path.Base(clean) != "codex-switch" && path.Base(clean) != "codex-switch.exe"
	if clean != state.expected && clean != "LICENSE" && clean != "README.md" && clean != "THIRD_PARTY_NOTICES.md" && !license {
		return errors.New("unexpected archive file")
	}
	if size <= 0 || size > maxBinaryBytes || state.total+size > maxDownloadBytes {
		return errors.New("archive entry exceeds its size limit")
	}
	state.total += size
	return nil
}

func (state *archiveState) finish() ([]byte, error) {
	if len(state.binary) == 0 {
		return nil, errors.New("expected executable is missing from archive")
	}
	return state.binary, nil
}
