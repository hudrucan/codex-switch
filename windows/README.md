# Codex Switch for Windows

A small WPF tray companion for the existing Go CLI. Click its system-tray icon
to see accounts, cached quota, reset times, and cache timestamps. Select an
account to switch without opening a terminal or changing directory.

The layout follows the macOS companion, with a white outline tray icon and the
original purple application icon. Only the saved-account list scrolls when needed.

The UI never reads authentication, vaults, cache files, or credential stores.
It runs its bundled CLI through public JSON, with the normal process guard.
It does not poll, close applications, switch automatically,
register a startup task, or install a service.

## Build and run

Development requires Go (see `go.mod`) and .NET SDK 10 on Windows. No third-party
UI or test packages are required.

```powershell
dotnet run --project windows/CodexSwitch.Tests -c Release
dotnet build windows/CodexSwitch.Tray -c Release
.\windows\build.ps1
```

The build script creates a new `dist/windows-win-x64` directory and refuses to
overwrite an existing build. Use `-OutputDirectory C:\path\to\new-folder` or
`-Runtime win-arm64` when needed. The self-contained output includes .NET;
end users do not need Go or .NET installed. Keep the complete directory together.
The script only builds; it does not install, launch, sign, or publish the app.

Run `CodexSwitch.Tray.exe` from the output directory. Windows may initially place
the icon under the hidden-icons arrow. Drag it into the visible tray if desired.
Only one normal instance runs per Windows user. Click outside the popup or press
Esc to hide it. Right-click the tray icon for Exit, which is disabled while a CLI
operation is in progress so a switch can finish its recovery and persistence.

The output directory and its ancestors must satisfy the executable ACL checks.
A shared development drive with broad Modify permissions will be rejected even
if the helper executable itself is present. For normal use, copy the complete
output to a private per-user directory such as `%LOCALAPPDATA%\CodexSwitch`,
then create a shortcut there. Do not weaken permissions on unrelated directories
or relax the resolver to accommodate a shared build location.

## Accounts and switching

Initialize and add accounts with the existing CLI. A launchable official Codex
CLI is still needed for login and live usage; the tray's read-only cached view
and account switching do not depend on a versioned desktop CLI path.

On Windows, the official standalone installer provides a stable command path at
`%LOCALAPPDATA%\Programs\OpenAI\Codex\bin`. Reopen PowerShell and restart the
tray after installation so they inherit the updated user PATH. Verify with
`Get-Command codex` and `codex --version`.

Opening the menu reads cached status. Click Refresh Usage to update all saved
accounts sequentially and see progress. Failed accounts keep their cached usage;
completed updates are retained. A 60-second cooldown applies after success;
repeated failures increase the delay to 120 then 300 seconds. Hiding the popup
does not cancel refresh. Other commands and Quit stay disabled until it finishes.

Choose an account to switch. If Codex is running, close it yourself and select
Retry Switch. After switching, select Open Codex to launch it. If the launcher
cannot be found, open Codex or ChatGPT from Start.

Unmanaged, ambiguous, conflicting, unsupported, or unreadable state blocks menu
switching. Aliases beginning with `-`, or colliding with another profile's ID or
alias, must be renamed through the CLI. Account additions remain CLI operations.

`account add` saves a new profile without adopting the login already active in
Codex. If that live login is unmanaged, save it once with
`codex-switch account import-current <alias>`, then reopen the menu.

## Synthetic previews and verification

```powershell
dotnet run --project windows/CodexSwitch.Tray -- --demo
dotnet run --project windows/CodexSwitch.Tray -- --preview C:\path\to\preview.png
```

Both modes use in-memory accounts without reading live profiles. Preview mode
renders a PNG and exits; demo mode opens the tray popup.

Preview accepts an optional `active`, `unmanaged`, or `many` scenario followed
by `light` or `dark`, for example `--preview C:\path\to\unmanaged.png unmanaged
light`.

The source repository's `docs/architecture.md` describes the CLI bridge;
`docs/security.md` documents executable validation, redaction, lifecycle rules,
and verification limits. See `docs/troubleshooting.md` for CLI errors.
