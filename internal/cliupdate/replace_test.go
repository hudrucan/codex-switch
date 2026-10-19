package cliupdate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/filelock"
)

func TestReplacementPreservesExistingExecutableOnFailure(t *testing.T) {
	for _, stage := range []string{"success", "before-write", "after-write", "locked", "symlink", "parent-symlink", "lock-symlink"} {
		t.Run(stage, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "codex-switch")
			old := []byte("synthetic-old-binary")
			next := []byte("synthetic-new-binary")
			if err := os.WriteFile(target, old, 0o755); err != nil {
				t.Fatal(err)
			}
			actual := target
			if stage == "lock-symlink" {
				if err := os.Symlink(target, target+".update.lock"); err != nil {
					t.Skip("symlinks unavailable")
				}
			}
			if stage == "symlink" {
				actual = filepath.Join(root, "link")
				if err := os.Symlink(target, actual); err != nil {
					t.Skip("symlinks unavailable")
				}
			}
			if stage == "parent-symlink" {
				link := filepath.Join(root, "parent")
				if err := os.Symlink(root, link); err != nil {
					t.Skip("symlinks unavailable")
				}
				actual = filepath.Join(link, "codex-switch")
			}
			if stage == "locked" {
				lock, err := filelock.Acquire(target + ".update.lock")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Close() }()
			}
			write := func(path string, data []byte, mode os.FileMode) error {
				if stage == "before-write" {
					return &atomicfile.WriteError{Err: errors.New("synthetic path secret"), Replacement: atomicfile.ReplacementNotAttempted}
				}
				if err := atomicfile.Write(path, data, mode); err != nil {
					return err
				}
				if stage == "after-write" {
					return &atomicfile.WriteError{Err: errors.New("synthetic path secret"), Replacement: atomicfile.ReplacementCompleted}
				}
				return nil
			}
			err = replaceTarget(actual, next, write)
			data, readErr := os.ReadFile(target)
			if readErr != nil || (err == nil) != (stage == "success") || (err != nil && strings.Contains(err.Error(), "synthetic path secret")) {
				t.Fatal("unsafe replacement outcome")
			}
			expected := old
			if stage == "success" || stage == "after-write" {
				expected = next
			}
			if !bytes.Equal(data, expected) {
				t.Fatal("failed replacement lost the usable executable")
			}
			info, statErr := os.Stat(target)
			if statErr != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o755) {
				t.Fatal("replacement did not preserve executable permissions")
			}
		})
	}
}
