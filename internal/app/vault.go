package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/vault"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newVaultCommand(options *Options) *cobra.Command {
	command := &cobra.Command{Use: "vault", Short: "Back up and maintain the encrypted account vault"}
	var exportOutput string
	exportCommand := &cobra.Command{
		Use:   "export",
		Short: "Create a passphrase-encrypted portable backup",
		RunE: func(*cobra.Command, []string) error {
			if exportOutput == "" {
				return errors.New("--output is required")
			}
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			data, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			passphrase, err := options.readPassphrase("Export passphrase: ", true)
			if err != nil {
				return err
			}
			encoded, err := vault.Export(data, passphrase)
			if err != nil {
				return err
			}
			if err := atomicfile.Write(exportOutput, append(encoded, '\n'), 0o600); err != nil {
				return err
			}
			return options.render(map[string]string{"output": exportOutput}, "Exported encrypted vault backup to "+exportOutput)
		},
	}
	exportCommand.Flags().StringVarP(&exportOutput, "output", "o", "", "backup output path")

	var importInput string
	var replace bool
	importCommand := &cobra.Command{
		Use:   "import",
		Short: "Import a passphrase-encrypted portable backup",
		RunE: func(*cobra.Command, []string) error {
			if importInput == "" {
				return errors.New("--input is required")
			}
			runtime, err := options.loadRuntime(true)
			if err != nil {
				return err
			}
			encoded, err := atomicfile.ReadLimited(importInput, 64<<20)
			if err != nil {
				return err
			}
			passphrase, err := options.readPassphrase("Import passphrase: ", false)
			if err != nil {
				return err
			}
			imported, err := vault.Import(encoded, passphrase)
			if err != nil {
				return err
			}
			lock, err := runtime.acquireVaultLock()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			current, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			if !replace {
				for _, profile := range imported.Profiles {
					if err := current.Add(profile, false); err != nil {
						return fmt.Errorf("merge imported profile %q: %w", profile.Alias, err)
					}
				}
				imported = current
			}
			if err := runtime.manager.Save(imported); err != nil {
				return err
			}
			return options.render(map[string]int{"profiles": len(imported.Profiles)}, fmt.Sprintf("Imported %d account profile(s)", len(imported.Profiles)))
		},
	}
	importCommand.Flags().StringVarP(&importInput, "input", "i", "", "backup input path")
	importCommand.Flags().BoolVar(&replace, "replace", false, "replace the local vault instead of merging")

	rotateCommand := &cobra.Command{
		Use:   "rotate-key",
		Short: "Rotate the operating-system-protected vault key",
		RunE: func(*cobra.Command, []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			lock, err := runtime.acquireVaultLock()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			if err := runtime.manager.RotateKey(); err != nil {
				return err
			}
			return options.render(map[string]bool{"rotated": true}, "Rotated the vault key.")
		},
	}
	command.AddCommand(exportCommand, importCommand, rotateCommand)
	return command
}

func (options *Options) readPassphrase(prompt string, confirm bool) ([]byte, error) {
	file, ok := options.Input.(*os.File)
	if ok && term.IsTerminal(int(file.Fd())) {
		_, _ = fmt.Fprint(options.Error, prompt)
		value, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(options.Error)
		if err != nil {
			return nil, err
		}
		if confirm {
			_, _ = fmt.Fprint(options.Error, "Confirm passphrase: ")
			second, err := term.ReadPassword(int(file.Fd()))
			_, _ = fmt.Fprintln(options.Error)
			if err != nil {
				return nil, err
			}
			if string(value) != string(second) {
				return nil, errors.New("passphrases do not match")
			}
		}
		return value, nil
	}
	line, err := bufio.NewReader(options.Input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}
