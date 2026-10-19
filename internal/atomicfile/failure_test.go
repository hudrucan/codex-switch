package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var (
	errInjectedWrite   = errors.New("injected write failure")
	errInjectedCleanup = errors.New("injected cleanup failure")
)

type faultFile struct {
	*os.File
	stage string
}

func (file faultFile) Chmod(mode os.FileMode) error {
	if file.stage == "chmod" {
		return errInjectedWrite
	}
	return file.File.Chmod(mode)
}

func (file faultFile) Write(data []byte) (int, error) {
	if file.stage == "write" {
		return 0, errInjectedWrite
	}
	if file.stage == "short-write" {
		return file.File.Write(data[:len(data)/2])
	}
	return file.File.Write(data)
}

func (file faultFile) Sync() error {
	if file.stage == "sync-file" || file.stage == "cleanup" || file.stage == "cleanup-close" {
		return errInjectedWrite
	}
	return file.File.Sync()
}

func (file faultFile) Close() error {
	err := file.File.Close()
	if file.stage == "close-file" {
		return errInjectedWrite
	}
	if file.stage == "cleanup-close" {
		return errInjectedCleanup
	}
	return err
}

func TestWriteFailurePublicationState(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, stage := range []string{"create", "chmod", "write", "short-write", "sync-file", "close-file", "replace-before", "replace-after", "sync-parent", "cleanup", "cleanup-close"} {
			name := "create/" + stage
			if existing {
				name = "replace/" + stage
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "destination")
				if existing {
					if err := Write(path, []byte("previous"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				operations := writeOperations{
					createTemp: func(dir, pattern string) (temporaryFile, error) {
						if stage == "create" {
							return nil, errInjectedWrite
						}
						file, err := os.CreateTemp(dir, pattern)
						if err != nil {
							return nil, err
						}
						return faultFile{file, stage}, nil
					},
					replace: func(source, destination string) error {
						if stage == "replace-before" {
							return errInjectedWrite
						}
						if err := replace(source, destination); err != nil {
							return err
						}
						if stage == "replace-after" {
							return errInjectedWrite
						}
						return nil
					},
					syncParent: func(parent string) error {
						if stage == "sync-parent" {
							return errInjectedWrite
						}
						return syncParent(parent)
					},
					remove: func(path string) error {
						if stage == "cleanup" {
							return errInjectedCleanup
						}
						return os.Remove(path)
					},
				}
				err := write(path, []byte("replacement"), 0o600, operations)
				var failure *WriteError
				if !errors.As(err, &failure) {
					t.Fatalf("expected a classified write error, got %v", err)
				}
				wantState := ReplacementNotAttempted
				if stage == "replace-before" || stage == "replace-after" {
					wantState = ReplacementUncertain
				}
				if stage == "sync-parent" {
					wantState = ReplacementCompleted
				}
				if failure.Replacement != wantState {
					t.Fatal("write reported the wrong replacement state")
				}
				cause := errInjectedWrite
				if stage == "short-write" {
					cause = io.ErrShortWrite
				}
				if !errors.Is(err, cause) {
					t.Fatal("write lost its original error")
				}
				if (stage == "cleanup" || stage == "cleanup-close") && !errors.Is(err, errInjectedCleanup) {
					t.Fatal("write hid its cleanup error")
				}
				published := stage == "replace-after" || stage == "sync-parent"
				data, readErr := os.ReadFile(path)
				switch {
				case published:
					if readErr != nil || string(data) != "replacement" {
						t.Fatal("published replacement was lost")
					}
				case existing:
					if readErr != nil || string(data) != "previous" {
						t.Fatal("pre-replacement failure changed the destination")
					}
				default:
					if !errors.Is(readErr, os.ErrNotExist) {
						t.Fatal("pre-replacement failure created a destination")
					}
				}
				if (existing || published) && runtime.GOOS != "windows" {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0o600 {
						t.Fatal("write failed to preserve restrictive permissions")
					}
				}
				files, err := filepath.Glob(filepath.Join(root, ".codex-switch-*.tmp"))
				if err != nil {
					t.Fatal(err)
				}
				if stage == "cleanup" {
					if len(files) != 1 {
						t.Fatal("expected the injected cleanup failure to leave its temporary file")
					}
				} else if len(files) != 0 {
					t.Fatal("write left an unexpected temporary file")
				}
			})
		}
	}
}
