package atomicfile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const DefaultReadLimit int64 = 4 << 20

type ReplacementState uint8

const (
	ReplacementNotAttempted ReplacementState = iota
	ReplacementUncertain
	ReplacementCompleted
)

// WriteError distinguishes failure before replacement from an uncertain
// replacement or a published destination whose durability was not confirmed.
// A nil error is the only confirmation that all write steps completed.
type WriteError struct {
	Err         error
	Replacement ReplacementState
}

func (err *WriteError) Error() string { return err.Err.Error() }
func (err *WriteError) Unwrap() error { return err.Err }

type temporaryFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type writeOperations struct {
	createTemp func(string, string) (temporaryFile, error)
	replace    func(string, string) error
	syncParent func(string) error
	remove     func(string) error
}

func ReadLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s is too large (%d bytes; maximum %d)", path, info.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte read limit", path, limit)
	}
	return data, nil
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func FileHash(path string) (string, error) {
	data, err := ReadLimited(path, DefaultReadLimit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing", nil
		}
		return "", err
	}
	return Hash(data), nil
}

func Write(path string, data []byte, mode os.FileMode) error {
	return write(path, data, mode, writeOperations{
		createTemp: func(dir, pattern string) (temporaryFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		replace: replace, syncParent: syncParent, remove: os.Remove,
	})
}

func write(path string, data []byte, mode os.FileMode, operations writeOperations) (result error) {
	state := ReplacementNotAttempted
	defer func() {
		if result != nil {
			result = &WriteError{Err: result, Replacement: state}
		}
	}()
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination: %w", err)
	}

	tmp, err := operations.createTemp(parent, ".codex-switch-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			if err := tmp.Close(); err != nil {
				result = errors.Join(result, fmt.Errorf("close temporary file: %w", err))
			}
		}
		if state != ReplacementCompleted {
			if err := operations.remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove temporary file: %w", err))
			}
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if written, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	} else if written != len(data) {
		return fmt.Errorf("write temporary file: %w", io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	// A replacement call that returns an error can have an uncertain outcome.
	// Do not infer that the destination is unchanged merely from that error.
	state = ReplacementUncertain
	if err := operations.replace(tmpPath, path); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	state = ReplacementCompleted
	if err := operations.syncParent(parent); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}
