package cliupdate

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/filelock"
)

func replaceExecutable(binary []byte) error {
	target, err := os.Executable()
	if err != nil {
		return errors.New("cannot locate update target")
	}
	return replaceTarget(target, binary, atomicfile.Write)
}

// Keep the old executable at its original path until one atomic replacement.
// Windows may refuse to replace a running executable; fail without a rename gap.
func replaceTarget(target string, binary []byte, write func(string, []byte, os.FileMode) error) error {
	if len(binary) == 0 || len(binary) > maxBinaryBytes {
		return errors.New("invalid replacement executable size")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || !filepath.IsAbs(target) || filepath.Clean(target) != resolved {
		return errors.New("unsafe update target path")
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return errors.New("unsafe update target type")
	}
	lockPath := target + ".update.lock"
	if lockInfo, err := os.Lstat(lockPath); err == nil {
		if !lockInfo.Mode().IsRegular() {
			return errors.New("unsafe update lock")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect update lock")
	}
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return errors.New("cannot lock update target")
	}
	defer func() { _ = lock.Close() }()
	before, err := atomicfile.ReadLimited(target, maxBinaryBytes)
	if err != nil {
		return errors.New("cannot inspect update target")
	}
	current, err := os.Lstat(target)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("update target changed")
	}
	currentBytes, err := atomicfile.ReadLimited(target, maxBinaryBytes)
	if err != nil || atomicfile.Hash(before) != atomicfile.Hash(currentBytes) {
		return errors.New("update target changed")
	}
	if err := write(target, binary, info.Mode().Perm()); err != nil {
		var failure *atomicfile.WriteError
		if !errors.As(err, &failure) || failure.Replacement != atomicfile.ReplacementNotAttempted {
			return errors.New("executable replacement outcome or durability is uncertain; inspect the installed version before retrying")
		}
		return errors.New("executable replacement failed; existing executable was retained")
	}
	return nil
}
