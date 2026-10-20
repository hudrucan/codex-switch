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
user or root and must not be group/other writable. Setuid/setgid executables,
non-executables, and shared writable directories such as `/tmp` are rejected.
An invalid explicit override fails instead of falling back to another binary.
These checks assume the current OS user is trusted; they are not code-signature
verification or a defense against that user replacing their own files.

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

## Menu and switching behavior

Opening the menu while idle reads only `codex-switch ui status --json`. The
menu labels usage as cached, shows its last fetch time, and distinguishes stale,
partial, and unavailable data. Percentages show remaining quota, derived from
the CLI's `usedPercent`. Unknown JSON fields are tolerated; status schemas other
than version 1 are rejected. Unmanaged, ambiguous, conflicting, or unknown
observations do not receive a safe active marker and block menu switching.

Only **Refresh Usage** invokes
`codex-switch account list --refresh --json`, followed by a status read. A
command failure, per-account refresh error, or subsequent status error preserves
the previous successful menu data. Operations and confirmation flows exclude
concurrent refreshes and switches. Errors are fixed local messages; raw stderr
and decoding diagnostics are never displayed or logged.

Choosing another account first obtains fresh status. With no Codex process
reported, it calls exactly `codex-switch use <alias> --json`. Aliases beginning
with `-` or colliding with another profile ID require renaming through the CLI.
If the guarded command fails, status is re-read; a running-Codex race returns to
the quit flow without a bypass or automatic retry.

For CLI-reported `Codex`/`ChatGPT` GUI applications with a supported native bundle
identity and launch date, the app asks for **Quit Codex & Switch** confirmation.
It re-reads process status before each native termination request, checks app
instance identity, and requests graceful termination with `NSRunningApplication`.
It then checks status at most 12 times with a 500 ms wait between checks. Cached
status child processes receive a termination request after 5 seconds; mutating
CLI commands are allowed to finish under their own lifecycle controls.

If Codex remains, **Force Quit Codex** requires a separate explicit confirmation
and warns about unsaved work. Each target must still appear in freshly read CLI
status and have the same native app instance, bundle identifier, and launch
date. A new process or reused PID requires fresh consent. Only after the CLI
reports no Codex processes does the normal guarded switch run. Terminal `codex`
processes or unverifiable applications are never signaled: the user closes them
and chooses **Retry Switch**. Cancel is available while awaiting confirmation;
quit/open/refresh/switch controls are disabled during in-flight operations.

**Open Codex** independently uses `NSWorkspace`, preferring the configured Codex
bundle identifier and then ChatGPT. It never switches accounts. No AppleScript,
Automation entitlement, shell, or arbitrary PID kill is used for application
termination.

Distribution signing/notarization, release/installation automation, icons,
login items, automatic refresh, and automated account rotation are outside this
implementation. Real GUI launch, menu lifecycle, and native quit/force-quit
behavior still require an explicit manual acceptance run with a reviewed bundle;
unit tests verify the state machine through doubles.
