package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/SilkageNet/codex-switch/internal/app"
	"github.com/SilkageNet/codex-switch/internal/cliupdate"
)

var version = "dev"
var releaseRepository string
var buildChannel = "development"

func main() {
	if version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	command := app.NewCommand(version, cliupdate.Provenance{Repository: releaseRepository, Channel: buildChannel})
	if err := command.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
