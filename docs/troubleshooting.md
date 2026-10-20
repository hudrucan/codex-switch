# Troubleshooting

Start with:

```bash
codex-switch doctor
```

The report contains no authentication material and is safe to inspect before
sharing. Review it manually because paths and account IDs may still be private.

## Credential store is auto or keyring

`codex-switch` needs the official file store for the active projection:

```bash
codex-switch init --enable-file-store
```

A timestamped `config.toml.codex-switch.bak.*` file is created first.

## WSL reports that secret-tool is unavailable

Use a WSL-capable build and rerun initialization. Source/development builds
follow the [manual installation instructions](../README.md#install-from-source);
trusted release builds can use `codex-switch update`.

```bash
codex-switch init --enable-file-store
```

WSL2 does not need `secret-tool`. `codex-switch` uses Windows DPAPI through the
Windows PowerShell executable and stores only encrypted bytes in the current
Windows user's registry.

If the updated command reports that PowerShell is unavailable, verify WSL
interoperability:

```bash
powershell.exe -NoLogo -NoProfile -Command '$PSVersionTable.PSVersion'
```

If that executable cannot run, enable Windows interoperability and the `/mnt/c`
mount in WSL, restart the distribution with `wsl.exe --shutdown` from Windows,
and retry. Installing `libsecret-tools` alone is not sufficient unless the WSL
distribution also runs a working Secret Service and DBus session.

## Codex is still running

Quit the desktop app and stop active `codex` CLI processes. The tool refuses the
switch because a running Codex process could refresh and rewrite the outgoing
credential after replacement.

`--allow-running` exists for recovery and expert use. It is not recommended for
ordinary switching.

## Active account is unmanaged

Inspect it and preserve it before switching:

```bash
codex-switch sync --check
codex-switch sync --as current
```

## Account list disagrees with a login performed in Codex

The active marker is derived from Codex's live `auth.json`. Check
the detected drift and reconcile the encrypted profile and local pointer:

```bash
codex-switch sync --check
codex-switch sync
```

If the live account is already a saved profile, no alias is required. This does
not rewrite Codex sessions, plugins, configuration, or UI state. Running
`codex-switch use <live-alias>` also repairs the state without requiring Codex
to close or restart.

## Token generations are ambiguous

Codex changed a refresh token but the saved and live timestamps cannot prove
which is newer. Inspect the conflict first. To intentionally preserve the
credentials currently used by Codex:

```bash
codex-switch sync --check
codex-switch sync --prefer-live
```

The ambiguity is intentionally not resolved by guessing.

## Account usage is unavailable

Confirm the official Codex executable is installed and current:

```bash
codex --version
codex-switch account usage <alias>
```

On macOS, a standalone `codex` command is optional when the official Codex or
ChatGPT desktop app is installed in `/Applications` or `~/Applications`.
Run `codex-switch doctor` to see the selected executable. Nonstandard locations
require `--codex-binary <path>` or `CODEX_BINARY`; see
[supported discovery paths](compatibility.md#codex-releases).

On Windows, a path under
`C:\Program Files\WindowsApps\OpenAI.Codex_*\app\resources\codex.exe` belongs to
the desktop app package. Windows does not allow `codex-switch` to launch that
bundled executable directly. Install the
[standalone Codex CLI](https://learn.chatgpt.com/docs/codex/cli), open a new
terminal, and verify that `codex --version` resolves to the standalone CLI. If
needed, select it explicitly with `--codex-binary <path>` or the `CODEX_BINARY`
environment variable. `codex-switch doctor` reports this case separately from a
completely missing CLI.

Usage queries require network access and a saved ChatGPT login. They do not work
for API-key-only or Amazon Bedrock authentication. A missing method on an older
Codex build is reported as partial when the other method still works; update
Codex if both usage methods are unavailable.

For an offline or temporarily failing service, inspect the last successful
snapshot without making a request:

```bash
codex-switch account list --cached
codex-switch account usage <alias> --cached
```

A stale cached snapshot is labeled `stale`; a failed refresh keeps that snapshot
and displays the complete redacted error rather than discarding useful data.
The tool retries a failed isolated query up to three times with backoff and
queries saved accounts sequentially to avoid competing Codex App Server startup
or authentication work. The last failure remains visible with `--cached` until
that account refreshes successfully.

## Interrupted switch journal

Run `codex-switch status`. Recovery compares the current `auth.json` hash with
both sides of the journal and either rolls state forward or discards an
uncommitted journal. If neither hash matches, preserve the files and open a
private support report.
