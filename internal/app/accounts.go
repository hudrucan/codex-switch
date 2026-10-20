package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/SilkageNet/codex-switch/internal/authschema"
	"github.com/SilkageNet/codex-switch/internal/codexlogin"
	"github.com/SilkageNet/codex-switch/internal/codexusage"
	appstate "github.com/SilkageNet/codex-switch/internal/state"
	"github.com/SilkageNet/codex-switch/internal/switcher"
	"github.com/SilkageNet/codex-switch/internal/usagecache"
	"github.com/SilkageNet/codex-switch/internal/vault"
	"github.com/spf13/cobra"
)

func newAccountCommand(options *Options) *cobra.Command {
	command := &cobra.Command{Use: "account", Aliases: []string{"accounts"}, Short: "Manage ChatGPT account profiles"}
	command.AddCommand(
		newAccountImportCommand(options),
		newAccountAddCommand(options, false),
		newAccountListCommand(options),
		newAccountUsageCommand(options),
		newAccountShowCommand(options),
		newAccountRenameCommand(options),
		newAccountRemoveCommand(options),
		newAccountAddCommand(options, true),
	)
	return command
}

func newAccountImportCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "import-current <alias>",
		Short: "Import the account currently active in Codex",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runtime, err := options.loadRuntime(true)
			if err != nil {
				return err
			}
			lock, err := runtime.acquireLock()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			raw, err := runtime.home.ReadAuth()
			if err != nil {
				return fmt.Errorf("read active Codex login: %w", err)
			}
			document, err := authschema.Parse(raw)
			if err != nil {
				return err
			}
			profile, err := addDocument(runtime.manager, args[0], "import-current", document)
			if err != nil {
				return err
			}
			hash, err := runtime.home.AuthHash()
			if err == nil {
				_ = appstate.Save(runtime.paths.State, appstate.State{ActiveProfileID: profile.ID, AuthHash: hash})
			}
			return options.render(toView(profile, true), fmt.Sprintf("Imported current account as %q", profile.Alias))
		},
	}
}

func newAccountAddCommand(options *Options, reauth bool) *cobra.Command {
	var deviceAuth bool
	use := "add <alias>"
	short := "Add an account through the official Codex login flow"
	if reauth {
		use = "reauth <alias>"
		short = "Replace a profile through the official Codex login flow"
	}
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runtime, err := options.loadRuntime(true)
			if err != nil {
				return err
			}
			if runtime.binErr != nil {
				return runtime.binErr
			}
			if runtime.bin == "" {
				return errors.New("official Codex executable not found; install Codex or pass --codex-binary")
			}
			var target reauthTarget
			if reauth {
				target, err = runtime.captureReauthTarget(args[0])
				if err != nil {
					return err
				}
			}
			runner := codexlogin.Runner{Binary: runtime.bin, Stdin: options.Input, Stdout: options.Output, Stderr: options.Error}
			login := runner.Login
			if options.login != nil {
				login = options.login
			}
			document, err := login(deviceAuth)
			if err != nil {
				return err
			}
			lock, err := runtime.acquireVaultLock()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			var profile vault.Profile
			if reauth {
				profile, err = reauthDocument(runtime.manager, target, args[0], document)
			} else {
				profile, err = addDocument(runtime.manager, args[0], "codex-login", document)
			}
			if err != nil {
				return err
			}
			return options.render(toView(profile, false), fmt.Sprintf("Saved account %q", profile.Alias))
		},
	}
	command.Flags().BoolVar(&deviceAuth, "device-auth", false, "use the official Codex device-code flow")
	return command
}

func newAccountListCommand(options *Options) *cobra.Command {
	var refresh bool
	var cached bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List saved accounts and their usage",
		RunE: func(*cobra.Command, []string) error {
			if refresh && cached {
				return errors.New("--refresh and --cached cannot be used together")
			}
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			data, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			var observation switcher.Observation
			var cache usagecache.Cache
			// Explicit refresh selects profiles from the validated vault. Auxiliary
			// state is inspected afterward so it cannot prevent credential recovery.
			if !refresh {
				observation, err = runtime.switcher().Observe()
				if err != nil {
					return codexusage.RedactError(err)
				}
				cache, err = runtime.usageService(options.Version).Cached()
				if err != nil {
					return err
				}
			}
			activeProfileID := observation.ProfileID
			refreshIDs := make([]string, 0, len(data.Profiles))
			if !cached {
				for _, profile := range data.Profiles {
					snapshot, ok := cache.Profiles[profile.ID]
					if refresh || !ok || usageStatus(snapshot.FetchedAt, time.Now()) != "fresh" {
						refreshIDs = append(refreshIDs, profile.ID)
					}
				}
			}
			refreshErrors := map[string]string{}
			if len(refreshIDs) > 0 {
				results, refreshErr := runtime.usageService(options.Version).Refresh(context.Background(), refreshIDs)
				if refreshErr != nil {
					for _, id := range refreshIDs {
						refreshErrors[id] = refreshErr.Error()
					}
				} else {
					for id, result := range results {
						refreshErrors[id] = result.Error
					}
				}
				cache, err = runtime.usageService(options.Version).Cached()
				if err != nil {
					return err
				}
				observation, err = runtime.switcher().Observe()
				if err != nil {
					return codexusage.RedactError(err)
				}
				activeProfileID = observation.ProfileID
			}
			views := make([]accountView, 0, len(data.Profiles))
			for _, profile := range data.Profiles {
				view := toView(profile, profile.ID == activeProfileID)
				view.Usage = usageFromCache(cache, profile.ID, refreshErrors[profile.ID], time.Now())
				views = append(views, view)
			}
			if options.JSON {
				return options.render(views, "")
			}
			if len(views) == 0 {
				_, _ = fmt.Fprintln(options.Output, "No accounts saved.")
				return nil
			}
			writer := tabwriter.NewWriter(options.Output, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(writer, "\tALIAS\tIDENTITY\tPLAN\tLIMITS\tRESETS\tTOKENS\tUPDATED\tERROR")
			for _, view := range views {
				marker := " "
				if view.Active {
					marker = "*"
				}
				identity := view.Email
				if identity == "" {
					identity = view.AccountID
				}
				plan, limits, resets, tokens, updated := summarizeUsage(view.Usage, time.Now())
				_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", marker, view.Alias, identity, plan, limits, resets, tokens, updated, compactUsageError(view.Usage))
			}
			if err := writer.Flush(); err != nil {
				return err
			}
			for _, view := range views {
				if view.Usage != nil && view.Usage.Error != "" {
					_, _ = fmt.Fprintf(options.Output, "Refresh error for %s: %s\n", view.Alias, view.Usage.Error)
				}
			}
			if notice := observationNotice(observation); notice != "" {
				_, _ = fmt.Fprintln(options.Output, notice)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&refresh, "refresh", false, "refresh every account before listing")
	command.Flags().BoolVar(&cached, "cached", false, "show cached usage without contacting Codex services")
	return command
}

func newAccountUsageCommand(options *Options) *cobra.Command {
	var all bool
	var cached bool
	command := &cobra.Command{
		Use:   "usage [alias]",
		Short: "Show account limits and token usage without switching",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return errors.New("an alias and --all cannot be used together")
			}
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			data, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			observation, observationErr := runtime.switcher().Observe()
			if observationErr != nil && (cached || (!all && len(args) == 0)) {
				return codexusage.RedactError(observationErr)
			}
			activeProfileID := observation.ProfileID
			profiles := make([]vault.Profile, 0, len(data.Profiles))
			switch {
			case all:
				profiles = append(profiles, data.Profiles...)
			case len(args) == 1:
				profile, findErr := data.Find(args[0])
				if findErr != nil {
					return findErr
				}
				profiles = append(profiles, *profile)
			default:
				if !observation.HasLive {
					return errors.New("no managed account is active; pass an alias or --all")
				}
				if !observation.Managed {
					return errors.New("the active account is unmanaged or ambiguous; pass a saved alias")
				}
				profile, findErr := data.FindByID(activeProfileID)
				if findErr != nil {
					return errors.New("the active account is unmanaged; pass a saved alias")
				}
				profiles = append(profiles, *profile)
			}
			if len(profiles) == 0 {
				return errors.New("no accounts saved")
			}
			refreshErrors := map[string]string{}
			if !cached {
				ids := make([]string, 0, len(profiles))
				for _, profile := range profiles {
					ids = append(ids, profile.ID)
				}
				results, refreshErr := runtime.usageService(options.Version).Refresh(context.Background(), ids)
				if refreshErr != nil {
					if len(profiles) == 1 {
						return refreshErr
					}
					for _, id := range ids {
						refreshErrors[id] = refreshErr.Error()
					}
				} else {
					for id, result := range results {
						refreshErrors[id] = result.Error
					}
				}
			}
			if !cached {
				observation, err = runtime.switcher().Observe()
				if err != nil {
					return codexusage.RedactError(err)
				}
				activeProfileID = observation.ProfileID
			}
			cache, err := runtime.usageService(options.Version).Cached()
			if err != nil {
				return err
			}
			views := make([]accountView, 0, len(profiles))
			for _, profile := range profiles {
				view := toView(profile, profile.ID == activeProfileID)
				view.Usage = usageFromCache(cache, profile.ID, refreshErrors[profile.ID], time.Now())
				if len(profiles) == 1 && view.Usage.Status == "unavailable" {
					if view.Usage.Error != "" {
						return errors.New(view.Usage.Error)
					}
					return errors.New("no cached usage is available; retry without --cached")
				}
				views = append(views, view)
			}
			if options.JSON {
				if len(views) == 1 {
					return options.render(views[0], "")
				}
				return options.render(views, "")
			}
			for index, view := range views {
				if index > 0 {
					_, _ = fmt.Fprintln(options.Output)
				}
				_, _ = fmt.Fprintln(options.Output, formatUsage(view, time.Now()))
			}
			return nil
		},
	}
	command.Flags().BoolVar(&all, "all", false, "show usage for every saved account")
	command.Flags().BoolVar(&cached, "cached", false, "show cached usage without contacting Codex services")
	return command
}

func newAccountShowCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <alias>",
		Short: "Show non-secret account metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			data, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			profile, err := data.Find(args[0])
			if err != nil {
				return err
			}
			observation, err := runtime.switcher().Observe()
			if err != nil {
				return err
			}
			view := toView(*profile, profile.ID == observation.ProfileID)
			return options.render(view, formatView(view))
		},
	}
}

func newAccountRenameCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename an account alias",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			lock, err := runtime.acquireVaultLock()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			data, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			if err := vault.ValidateAlias(args[1]); err != nil {
				return err
			}
			profile, err := data.Find(args[0])
			if err != nil {
				return err
			}
			if existing, findErr := data.Find(args[1]); findErr == nil && existing.ID != profile.ID {
				return fmt.Errorf("account alias %q already exists", args[1])
			}
			profile.Alias = args[1]
			if err := runtime.manager.Save(data); err != nil {
				return err
			}
			return options.render(map[string]string{"old": args[0], "new": args[1]}, fmt.Sprintf("Renamed %q to %q", args[0], args[1]))
		},
	}
}

func newAccountRemoveCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <alias>",
		Short: "Remove an inactive account from the encrypted vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return err
			}
			lock, err := runtime.acquireLock()
			if err != nil {
				return err
			}
			defer func() { _ = lock.Close() }()
			data, err := runtime.manager.Load()
			if err != nil {
				return err
			}
			profile, err := data.Find(args[0])
			if err != nil {
				return err
			}
			observation, err := runtime.switcher().Observe()
			if err != nil {
				return err
			}
			if profile.ID == observation.ProfileID {
				return errors.New("cannot remove the active account; switch or deactivate first")
			}
			if observation.State == switcher.AccountStateAmbiguous && profile.AccountID == observation.AccountID {
				return errors.New("cannot remove a profile that may be active while the live account match is ambiguous")
			}
			removed, err := data.Remove(args[0])
			if err != nil {
				return err
			}
			if err := runtime.manager.Save(data); err != nil {
				return err
			}
			return options.render(map[string]string{"removed": removed.Alias}, fmt.Sprintf("Removed %q", removed.Alias))
		},
	}
}

func addDocument(manager *vault.Manager, alias, source string, document authschema.Document) (vault.Profile, error) {
	data, err := manager.Load()
	if err != nil {
		return vault.Profile{}, err
	}
	updatedAt, _ := document.GenerationTime()
	profile := vault.NewProfile(alias, source, document.Raw, document.Tokens.AccountID, document.WorkspaceID, document.Email, updatedAt)
	if err := data.Add(profile, false); err != nil {
		return vault.Profile{}, err
	}
	if err := manager.Save(data); err != nil {
		return vault.Profile{}, err
	}
	saved, err := data.FindByAlias(alias)
	if err != nil {
		return vault.Profile{}, err
	}
	return *saved, nil
}

type reauthTarget struct {
	ID    string
	Alias string
}

func (runtime runtimeState) captureReauthTarget(alias string) (reauthTarget, error) {
	if err := vault.ValidateAlias(alias); err != nil {
		return reauthTarget{}, err
	}
	lock, err := runtime.acquireVaultLock()
	if err != nil {
		return reauthTarget{}, err
	}
	defer func() { _ = lock.Close() }()
	data, err := runtime.manager.Load()
	if err != nil {
		return reauthTarget{}, err
	}
	// Match aliases explicitly: Data.Find also accepts profile IDs.
	for _, profile := range data.Profiles {
		if strings.EqualFold(profile.Alias, alias) {
			return reauthTarget{ID: profile.ID, Alias: profile.Alias}, nil
		}
	}
	return reauthTarget{}, fmt.Errorf("account profile %q not found; use account add to create a profile", alias)
}

// reauthDocument runs under the caller's vault lock and replaces only the
// original profile if its alias still matches the pre-login snapshot.

func reauthDocument(manager *vault.Manager, target reauthTarget, alias string, document authschema.Document) (vault.Profile, error) {
	data, err := manager.Load()
	if err != nil {
		return vault.Profile{}, err
	}
	for index, current := range data.Profiles {
		if current.ID != target.ID {
			continue
		}
		if current.Alias != target.Alias || !strings.EqualFold(current.Alias, alias) {
			break
		}
		updatedAt, _ := document.GenerationTime()
		profile := vault.NewProfile(alias, "reauth", document.Raw, document.Tokens.AccountID, document.WorkspaceID, document.Email, updatedAt)
		profile.ID = current.ID
		profile.CreatedAt = current.CreatedAt
		data.Profiles[index] = profile
		if err := manager.Save(data); err != nil {
			return vault.Profile{}, err
		}
		return profile, nil
	}
	return vault.Profile{}, errors.New("reauthentication target changed during login; no credentials were saved; retry reauth for the intended profile")
}
