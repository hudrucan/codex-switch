package app

import (
	"errors"
	"time"

	"github.com/SilkageNet/codex-switch/internal/process"
	"github.com/SilkageNet/codex-switch/internal/switcher"
	"github.com/spf13/cobra"
)

type uiStatusView struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	Accounts       []accountView        `json:"accounts"`
	Observation    switcher.Observation `json:"observation"`
	CodexProcesses []process.Info       `json:"codexProcesses"`
}

func newUICommand(options *Options) *cobra.Command {
	command := &cobra.Command{Use: "ui", Short: "Read-only CLI bridge for the native UI"}
	command.AddCommand(newUIStatusCommand(options))
	return command
}

func newUIStatusCommand(options *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show account observation, cached usage, and running Codex processes",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			runtime, err := options.loadRuntime(false)
			if err != nil {
				return errors.New("load UI status runtime failed (diagnostics redacted)")
			}
			data, err := runtime.manager.Load()
			if err != nil {
				return errors.New("read UI status accounts failed (diagnostics redacted)")
			}
			// Observe directly: the existing status command performs recovery writes.
			observation, err := runtime.switcher().Observe()
			if err != nil {
				return errors.New("observe UI status failed (diagnostics redacted)")
			}
			cache, err := runtime.usageService(options.Version).Cached()
			if err != nil {
				return err
			}
			detect := options.detectCodex
			if detect == nil {
				detect = process.DetectCodex
			}
			processes, err := detect()
			if err != nil {
				return errors.New("inspect running Codex processes failed (diagnostics redacted)")
			}
			if processes == nil {
				processes = []process.Info{}
			}
			views := make([]accountView, 0, len(data.Profiles))
			now := time.Now()
			for _, profile := range data.Profiles {
				view := toView(profile, profile.ID == observation.ProfileID)
				view.Usage = usageFromCache(cache, profile.ID, "", now)
				views = append(views, view)
			}
			result := uiStatusView{SchemaVersion: 1, Accounts: views, Observation: observation, CodexProcesses: processes}
			return options.render(result, formatObservation(observation))
		},
	}
}
