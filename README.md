# codex-switch

`codex-switch` is a small, cross-platform account manager for local Codex clients.
It keeps one shared `CODEX_HOME` and switches only the active authentication
projection, so projects, sessions, plugins, skills, MCP configuration, and local
UI state stay in place.

> [!IMPORTANT]
> This is an independent compatibility tool, not an OpenAI product. The active
> credential is projected to the officially supported file store at
> `$CODEX_HOME/auth.json`. Treat that file like a password.

## Security model

- A random 256-bit vault key is stored in macOS Keychain, Windows Credential
  Manager, Linux Secret Service, or Windows DPAPI when running inside WSL2.
- Account bundles are encrypted at rest with XChaCha20-Poly1305.
- Only the active account is present in the Codex plaintext file store.
- Tokens are never printed by commands, JSON output, or diagnostics.
- Switches use a cross-process lock, compare-before-replace checks, and a
  recovery journal.
- The tool fails closed when the operating-system credential store is
  unavailable. It never falls back to a plaintext multi-account store.

The threat model trusts the current operating-system user. It reduces accidental
leaks, cross-user access, and unsafe writes; it cannot protect credentials from a
malicious process already running as the same user.

## Install from source

```bash
git clone https://github.com/hudrucan/codex-switch.git
cd codex-switch
go build -trimpath ./cmd/codex-switch
```

This fork distributes as `hudrucan/codex-switch`; the Go module path remains
`github.com/SilkageNet/codex-switch`. Source/development builds require manual
updates. `update --check` checks the fork without proving self-update eligibility.

When a reviewed fork release is available, run `sh scripts/install.sh` or
`./scripts/install.ps1` from that reviewed checkout. Both installers require
Python 3.9+ and verify artifacts before installation.
`CODEX_SWITCH_VERSION` selects a stable version (default: latest), and
`CODEX_SWITCH_INSTALL_DIR` selects the destination. Missing fork releases fail
closed; installers never fall back to upstream. See [distribution security](docs/security.md#binary-distribution-and-update-trust).

Adding accounts and querying live usage require a launchable official Codex CLI.
macOS can use the CLI bundled with Codex/ChatGPT; Windows requires the standalone
CLI. `CODEX_BINARY` and `--codex-binary` select an explicit executable. The Linux
archive also supports WSL2. See [platform requirements](docs/compatibility.md).

## Quick start

```bash
# Initialize the encrypted vault and enable Codex's file credential store.
codex-switch init --enable-file-store

# Preserve the login already active in Codex.
codex-switch account import-current personal

# Add another account through the official Codex login flow.
codex-switch account add work --device-auth

# Inspect and switch. Close Codex before switching, then restart it.
codex-switch account list
codex-switch account usage work
codex-switch use work
codex-switch current

# Detect or reconcile a login performed directly in Codex.
codex-switch sync --check
codex-switch sync
```

Use `codex-switch doctor` before reporting a problem. Machine-readable output is
available on commands with `--json`.

## Usage without switching

`codex-switch` can inspect every saved account through the official Codex App
Server without making that account active:

```bash
# Query the active managed account now.
codex-switch account usage

# Query one saved account, or all accounts in a reliable sequence.
codex-switch account usage work
codex-switch account usage --all

# Refresh all rows in the compact account table.
codex-switch account list --refresh

# Work offline with the last successful snapshots.
codex-switch account list --cached
codex-switch account usage work --cached
```

Normal `account list` calls refresh only missing snapshots or snapshots older
than five minutes. Explicit `--refresh` queries every account; `--cached` stays
offline. Failed refreshes preserve the last successful values and show a
redacted error. See [usage recovery and diagnostics](docs/troubleshooting.md#account-usage-is-unavailable).

The compact list shows the next automatic reset for each quota window. Its
`RESETS` column is separate: it reports earned manual rate-limit resets and the
nearest known expiration, when Codex provides them. `account usage <alias>`
shows exact local reset times plus each returned earned-reset grant and expiry.
The authoritative earned-reset count can be present even when the service omits
or caps its detail rows.

## Commands

```text
codex-switch init
codex-switch current
codex-switch status
codex-switch sync [--check] [--prefer-live] [--as <alias>]
codex-switch doctor
codex-switch use <alias>
codex-switch deactivate
codex-switch select

codex-switch account add <alias>
codex-switch account import-current <alias>
codex-switch account list [--refresh|--cached]
codex-switch account usage [alias] [--all] [--cached]
codex-switch account show <alias>
codex-switch account rename <old> <new>
codex-switch account reauth <alias>
codex-switch account remove <alias>

codex-switch vault export --output backup.cxs
codex-switch vault import --input backup.cxs
codex-switch vault rotate-key
codex-switch update [--check]
```

## Logins changed outside codex-switch

The live `$CODEX_HOME/auth.json` is the source of truth for the active account.
`current`, `status`, `account list`, `account show`, and the default
`account usage` selection inspect that live identity instead of trusting the
last account recorded by `codex-switch`.

After a login directly in Codex, use `sync --check` to inspect and `sync` to
adopt a provably newer credential generation and repair the active pointer.
Preserve an unmanaged login with `sync --as <alias>`. Ambiguous generations
remain unchanged unless you explicitly choose `sync --prefer-live`; see
[token generation conflicts](docs/troubleshooting.md#token-generations-are-ambiguous).

Running `codex-switch use <alias>` when that alias is already live performs the
same safe reconciliation without rewriting `auth.json`, closing Codex, or
requiring a restart. A real switch to a different account still requires Codex
to be stopped.

## What a switch changes

Normal account switches modify only:

- `$CODEX_HOME/auth.json`
- `codex-switch`'s own state and encrypted vault

An account-usage query may persist an officially refreshed credential generation
back to the encrypted vault. If the queried profile is active, the same
generation is safely reconciled into `$CODEX_HOME/auth.json`; no account selection
or Codex-owned state changes.

Initialization may make a one-time, backed-up change to
`$CODEX_HOME/config.toml` to set `cli_auth_credentials_store = "file"`.
`codex-switch` does not rewrite session history or Codex configuration during a
switch.

## Documentation

- [Architecture](docs/architecture.md)
- [Security](docs/security.md)
- [Compatibility](docs/compatibility.md)
- [Troubleshooting](docs/troubleshooting.md)
- [macOS menu-bar app](macos/CodexSwitch/README.md)
- [Windows tray app](windows/README.md)

The design was informed by the MIT-licensed
[CC Switch](https://github.com/farion1231/cc-switch), especially its handling of
Codex CLI refresh-token rotation. This project is an independent, narrower
implementation and does not include CC Switch's provider routing or proxy stack.

## License

MIT
