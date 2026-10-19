package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/cliupdate"
)

func TestSourceUpdateFailsBeforeAccountRuntime(t *testing.T) {
	for _, version := range []string{"dev", "1.2.3"} {
		t.Run(version, func(t *testing.T) {
			var output bytes.Buffer
			options := &Options{Version: version, Output: &output, Error: &output, JSON: true,
				runtimeLoader: func(bool) (runtimeState, error) {
					t.Fatal("update accessed the account runtime")
					return runtimeState{}, nil
				},
				Provenance: cliupdate.Provenance{},
			}
			command := newUpdateCommand(options)
			command.SilenceErrors, command.SilenceUsage = true, true
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), "trusted fork release build") || output.Len() != 0 {
				t.Fatal("source build self-update did not fail closed")
			}
		})
	}
}
