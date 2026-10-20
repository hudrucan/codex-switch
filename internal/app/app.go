package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/cliupdate"
	"github.com/SilkageNet/codex-switch/internal/codexhome"
	"github.com/SilkageNet/codex-switch/internal/doctor"
	"github.com/SilkageNet/codex-switch/internal/launcher"
	"github.com/SilkageNet/codex-switch/internal/process"
	"github.com/SilkageNet/codex-switch/internal/switcher"
	"github.com/spf13/cobra"
)

type Options struct {
	CodexHome  string
	AppHome    string
	CodexBin   string
	JSON       bool
	Version    string
	Provenance cliupdate.Provenance
	Input      io.Reader
	Output     io.Writer
	Error      io.Writer

	runtimeLoader func(bool) (runtimeState, error)
	login         func(bool) (authschema.Document, error)
	detectCodex   func() ([]process.Info, error)
}

func NewCommand(version string, provenance ...cliupdate.Provenance) *cobra.Command {
	options := &Options{Version: version, Input: os.Stdin, Output: os.Stdout, Error: os.Stderr}
	if len(provenance) == 1 {
		options.Provenance = provenance[0]
	}
	root := &cobra.Command{
		Use:           "codex-switch",
		Short:         "Securely switch ChatGPT accounts in one shared CODEX_HOME",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(options.Input)
	root.SetOut(options.Output)
	root.SetErr(options.Error)
	root.PersistentFlags().StringVar(&options.CodexHome, "codex-home", "", "Codex home (defaults to CODEX_HOME or ~/.codex)")
	root.PersistentFlags().StringVar(&options.AppHome, "home", "", "codex-switch data directory")
	root.PersistentFlags().StringVar(&options.CodexBin, "codex-binary", "", "path to the official codex executable")
	root.PersistentFlags().BoolVar(&options.JSON, "json", false, "emit machine-readable JSON without secrets")

	root.AddCommand(
		newInitCommand(options),
		newCurrentCommand(options),
		newStatusCommand(options),
		newUICommand(options),
		newSyncCommand(options),
		newDoctorCommand(options),
		newUseCommand(options),
		newDeactivateCommand(options),
		newSelectCommand(options),
		newAccountCommand(options),
		newVaultCommand(options),
		newUpdateCommand(options),
		newVersionCommand(options),
		newCompletionCommand(root),
	)
	return root
}

func newInitCommand(options *Options) *cobra.Command {
	var enableFileStore bool
	command := &cobra.Command{
		Use:   "init",
		Short: "Initialize the encrypted account vault",
		RunE: func(*cobra.Command, []string) error {
			runtime, err := options.loadRuntime(true)
			if err != nil {
				return err
			}
			lock, err := runtime.acquireLock()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			mode, err := runtime.home.CredentialStore()
			if err != nil {
				return err
			}
			backup := ""
			if mode != codexhome.StoreFile {
				if !enableFileStore {
					return fmt.Errorf("codex credential store is %s; rerun with --enable-file-store to enable safe account projection", mode)
				}
				backup, err = runtime.home.EnableFileStore()
				if err != nil {
					return err
				}
			}
			result := map[string]any{"initialized": true, "codexHome": runtime.home.Path, "vault": runtime.paths.Vault, "credentialStore": "file"}
			if backup != "" {
				result["configBackup"] = backup
			}
			return options.render(result, fmt.Sprintf("Initialized codex-switch for %s", runtime.home.Path))
		},
	}
	command.Flags().BoolVar(&enableFileStore, "enable-file-store", false, "set cli_auth_credentials_store=file with a backup")
	return command
}

func newUseCommand(options *Options) *cobra.Command {
	var allowRunning bool
	var launch bool
	command := &cobra.Command{
		Use:   "use <alias>",
		Short: "Atomically switch the active Codex account",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			result, err := runtime.switcher().Use(args[0], allowRunning)
			if err != nil {
				return err
			}
			if launch {
				if err := launcher.Codex(); err != nil {
					return fmt.Errorf("account switched but Codex could not be launched: %w", err)
				}
			}
			message := fmt.Sprintf("Active account: %s. Restart Codex to apply it.", result.Alias)
			if !result.Changed {
				message = fmt.Sprintf("Account %s is already active. No restart is required.", result.Alias)
				if result.CredentialsUpdated || result.StateRepaired {
					message = fmt.Sprintf("Account %s is already active. Reconciled changes made outside codex-switch; no restart is required.", result.Alias)
				}
			}
			return options.render(result, message)
		},
	}
	command.Flags().BoolVar(&allowRunning, "allow-running", false, "switch even when a Codex process is detected")
	command.Flags().BoolVar(&launch, "launch", false, "launch Codex after switching")
	return command
}

func newDeactivateCommand(options *Options) *cobra.Command {
	var allowRunning bool
	command := &cobra.Command{
		Use:   "deactivate",
		Short: "Remove the active auth projection without deleting saved accounts",
		RunE: func(*cobra.Command, []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			if err := runtime.switcher().Deactivate(allowRunning); err != nil {
				return err
			}
			return options.render(map[string]bool{"deactivated": true}, "Removed the active Codex authentication projection.")
		},
	}
	command.Flags().BoolVar(&allowRunning, "allow-running", false, "deactivate even when a Codex process is detected")
	return command
}

func newCurrentCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the active account",
		RunE: func(*cobra.Command, []string) error {
			view, err := options.currentView()
			if err != nil {
				return err
			}
			return options.render(view, formatView(view))
		},
	}
}

func newStatusCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Recover interrupted state and show the active account",
		RunE: func(*cobra.Command, []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			if err := runtime.switcher().Recover(); err != nil {
				return err
			}
			observation, err := runtime.switcher().Observe()
			if err != nil {
				return err
			}
			result := map[string]any{"codexHome": runtime.home.Path, "accountState": observation}
			if view, viewErr := viewFromObservation(observation); viewErr == nil {
				result["active"] = view
			}
			message := fmt.Sprintf("CODEX_HOME: %s\n%s", runtime.home.Path, formatObservation(observation))
			return options.render(result, message)
		},
	}
}

func newSyncCommand(options *Options) *cobra.Command {
	var check bool
	var preferLive bool
	var alias string
	command := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile codex-switch with the active Codex login",
		RunE: func(*cobra.Command, []string) error {
			if check && (preferLive || alias != "") {
				return errors.New("--check cannot be combined with --prefer-live or --as")
			}
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			if check {
				observation, err := runtime.switcher().Observe()
				if err != nil {
					return err
				}
				return options.render(observation, formatObservation(observation))
			}
			result, err := runtime.switcher().Sync(switcher.SyncOptions{Alias: alias, PreferLive: preferLive})
			if err != nil {
				return err
			}
			message := "Account state is already in sync."
			switch {
			case result.Imported:
				message = fmt.Sprintf("Imported and synchronized the active Codex login as %q.", result.Alias)
			case result.CredentialsUpdated && result.StateRepaired:
				message = fmt.Sprintf("Synchronized credentials and repaired the active account as %s.", result.Alias)
			case result.CredentialsUpdated:
				message = fmt.Sprintf("Synchronized the active credentials for %s.", result.Alias)
			case result.StateRepaired:
				if result.Alias == "" {
					message = "Cleared stale active-account state because Codex is logged out."
				} else {
					message = fmt.Sprintf("Repaired the active account as %s; Codex credentials were not replaced.", result.Alias)
				}
			}
			return options.render(result, message)
		},
	}
	command.Flags().BoolVar(&check, "check", false, "inspect account drift without changing state")
	command.Flags().BoolVar(&preferLive, "prefer-live", false, "adopt the active Codex credentials when generations conflict")
	command.Flags().StringVar(&alias, "as", "", "import an unmanaged active login using this alias")
	return command
}

func newDoctorCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run redacted local diagnostics",
		RunE: func(*cobra.Command, []string) error {
			runtime, err := options.loadRuntimeForDoctor()
			if err != nil {
				return err
			}
			report := doctor.Run(runtime.home, runtime.paths, runtime.manager, runtime.bin, runtime.binErr)
			if options.JSON {
				return options.render(report, "")
			}
			for _, check := range report.Checks {
				_, _ = fmt.Fprintf(options.Output, "%-8s %-20s %s\n", strings.ToUpper(check.Status), check.Name, check.Message)
			}
			if !report.OK {
				return errors.New("doctor found blocking problems")
			}
			return nil
		},
	}
}

func newSelectCommand(options *Options) *cobra.Command {
	var allowRunning bool
	command := &cobra.Command{
		Use:   "select",
		Short: "Interactively select an account",
		RunE: func(*cobra.Command, []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			data, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			if len(data.Profiles) == 0 {
				return errors.New("no accounts saved")
			}
			for index, profile := range data.Profiles {
				_, _ = fmt.Fprintf(options.Output, "%d) %s\n", index+1, profile.Alias)
			}
			_, _ = fmt.Fprint(options.Output, "Select account: ")
			line, err := bufio.NewReader(options.Input).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			choice, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil || choice < 1 || choice > len(data.Profiles) {
				return errors.New("invalid account selection")
			}
			result, err := runtime.switcher().Use(data.Profiles[choice-1].Alias, allowRunning)
			if err != nil {
				return err
			}
			message := fmt.Sprintf("Active account: %s. Restart Codex to apply it.", result.Alias)
			if !result.Changed {
				message = fmt.Sprintf("Account %s is already active. No restart is required.", result.Alias)
			}
			return options.render(result, message)
		},
	}
	command.Flags().BoolVar(&allowRunning, "allow-running", false, "switch even when a Codex process is detected")
	return command
}

func newVersionCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the codex-switch version",
		RunE: func(*cobra.Command, []string) error {
			return options.render(map[string]string{"version": options.Version}, options.Version)
		},
	}
}

func newUpdateCommand(options *Options) *cobra.Command {
	var check bool
	command := &cobra.Command{
		Use:   "update",
		Short: "Securely update codex-switch from its latest GitHub release",
		RunE: func(*cobra.Command, []string) error {
			result, err := cliupdate.Run(context.Background(), options.Version, check, options.Provenance)
			if err != nil {
				return err
			}
			message := fmt.Sprintf("codex-switch %s is available.", result.Latest)
			if result.Current {
				message = fmt.Sprintf("codex-switch %s is already current.", result.Latest)
			} else if result.Updated {
				message = fmt.Sprintf("Updated codex-switch to %s.", result.Latest)
			}
			return options.render(result, message)
		},
	}
	command.Flags().BoolVar(&check, "check", false, "check for an update without installing it")
	return command
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(command *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(command.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(command.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(command.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(command.OutOrStdout())
			default:
				return errors.New("unsupported shell")
			}
		},
	}
}
