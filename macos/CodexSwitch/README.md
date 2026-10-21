# Codex Switch

Codex Switch is a native SwiftUI menu-bar app for macOS 13 or later. It has no
Dock icon, background polling, server, or direct credential/storage access.
The Go CLI remains independently usable and owns all credential management.

Open `Package.swift` in Xcode, or use the installed Apple toolchain:

```sh
swift build --package-path macos/CodexSwitch
swift test --package-path macos/CodexSwitch
xcrun swift-format lint --strict --recursive macos/CodexSwitch/Sources macos/CodexSwitch/Tests
```

There are no third-party Swift dependencies. Bundle identity, minimum OS, agent
configuration, and supported application bundle identifiers are centralized in
`Configuration/Info.plist`. The Swift package also declares macOS 13 as its
deployment target. `Sources/CodexSwitchCore` contains the typed CLI client,
process execution, observable model, and native application adapter;
`Sources/CodexSwitchApp` contains the `MenuBarExtra` UI. Tests use synthetic
JSON and protocol doubles; they do not use real accounts or terminate apps.

## CLI selection and local bundle build

By default the app uses exactly `Contents/Helpers/codex-switch` inside its own
bundle. Development can explicitly override this with the absolute environment
variable `CODEX_SWITCH_CLI_PATH`. There is no PATH search or shell invocation.
The resolved executable and its parent directories must be owned by the current
user or root and must not be group/other writable. Invalid overrides fail
without fallback. See the [executable selection policy](../../docs/architecture.md#native-ui-boundary).

To create a local bundle from a separately built, reviewed CLI, run from the
repository root (the output directory must be new and its parent must exist):

```sh
bash macos/CodexSwitch/scripts/build-app.sh /absolute/path/to/codex-switch /absolute/path/to/new-build-output
```

The script builds the Swift executable and copies that CLI into a new
`Codex Switch.app`. It does not launch, install, archive, notarize, or perform
distribution signing. `CODEX_SWITCH_BUILD_ROOT` optionally selects a Swift
scratch directory. Use a private, non-shared output location before running the
app. To override the bundled CLI during local development, launch the built
bundle's executable with `CODEX_SWITCH_CLI_PATH` set; running it will read the
configured live account through the CLI, so development verification must use
test doubles instead.

The bundle includes `Resources/AppIcon.icns`, using the same mark as the menu
bar. To regenerate it, run from `macos/CodexSwitch`:

```sh
mkdir -p .build
xcrun swiftc -swift-version 6 -parse-as-library Sources/CodexSwitchApp/MenuBarIcon.swift scripts/render-app-icon.swift -o .build/render-app-icon
.build/render-app-icon .build/AppIcon.iconset
iconutil -c icns .build/AppIcon.iconset -o Resources/AppIcon.icns
```

## Menu and switching behavior

The menu shows the current account and saved accounts with quota percentages,
local reset times, banked reset counts and cache labels. Missing reset times or
counts show as unavailable rather than zero. Banked resets use the reported
available count, independently of how many detail rows the service returns.
Saved-account details use four left-aligned lines with compact 12-hour reset
times; each account's cache timestamp stays with that account.
The menu follows the system appearance and keeps process details collapsed by
default. Its monochrome icon shows a person with two switching arrows.

Opening the menu while idle first reads `codex-switch ui status --json`. The
menu labels usage as cached, shows its last fetch time, and distinguishes stale,
partial, and unavailable data. Percentages show remaining quota, derived from
the CLI's `usedPercent`. Unknown JSON fields are tolerated; status schemas other
than version 1 are rejected. Unmanaged, ambiguous, conflicting, or unknown
observations do not receive a safe active marker and block menu switching.

If any account's cache is older than five minutes or its update time is missing,
invalid, or in the future, opening the menu also refreshes usage once. Closing the
menu before the cached read completes prevents this refresh. There are no refresh timers
or queued retries while the menu is open or closed, and passing a reset time
never assumes quota has returned to 100%.

Both this automatic refresh and **Refresh Usage** invoke
`codex-switch account list --refresh --json`, followed by a status read. They share
a 60-second cooldown from completion. Consecutive failures delay another attempt
by one, two, then five minutes, capped at five; success resets the sequence.
Reopen the menu or click Refresh Usage after the delay to try again. These limits
last for the app process and do not throttle independent CLI commands.
An already-started refresh finishes safely even if the menu closes. A
command failure, per-account refresh error, or subsequent status error preserves
the previous successful menu data. Operations and confirmation flows exclude
concurrent refreshes and switches. The menu stays locked through the final cached
status read after switching. Refresh progress replaces the icon and label in the
Refresh Usage row; other operations show a small spinner and status in the
footer. Both keep the menu height stable and leave cached account data visible
without an overlay. Errors are fixed local messages; raw stderr and decoding
diagnostics are never displayed or logged.

Choosing another account first obtains fresh status. With no Codex process
reported, it calls exactly `codex-switch use <alias> --json`. Aliases beginning
with `-` or colliding with another profile ID require renaming through the CLI.
If the guarded command fails, status is re-read; a running-Codex race returns to
the quit flow without a bypass or automatic retry.

When a supported GUI is running, **Quit Codex & Switch** asks for confirmation
and waits for Codex and its workers to exit. **Force Quit Codex** requires a
separate confirmation and warns about unsaved work. A new GUI instance requires
fresh consent. Terminal `codex` processes or unverifiable applications require
manual closure followed by **Retry Switch**. Switching remains guarded until
the CLI reports no Codex processes. Cancel is available while awaiting
confirmation; controls are disabled during in-flight operations. See the
[process verification rules](../../docs/architecture.md#native-ui-boundary).

**Open Codex** independently uses `NSWorkspace`, preferring the configured Codex
bundle identifier and then ChatGPT, then reads cached status once to update the
process indicator. It never switches accounts or automatically reopens Codex
after Quit & Switch. No AppleScript, Automation entitlement, shell, or arbitrary
PID kill is used for application termination.

Refresh feedback shows the requested account count and elapsed time, then
changes to loading updated status when the CLI refresh finishes. This is elapsed
time, not per-account progress. A fixed-height hint keeps cached quota visible
and explains that closing the menu lets the current refresh finish. The elapsed
display does not poll the CLI or start additional refreshes.

Real GUI launch, menu lifecycle, and native quit/force-quit behavior require
manual acceptance with a reviewed bundle; unit tests verify the state machine
through doubles.
