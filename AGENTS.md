# AGENTS.md

## Project scope

`codex-switch` is a lightweight Go CLI for managing multiple ChatGPT/Codex accounts with one shared `CODEX_HOME`. It stores saved profiles in an encrypted vault and projects only the selected account's authentication into Codex's supported file store.

This fork may later include a small macOS menu bar companion. Keep the credential-management core independent of any UI.

## Communication and language

- **Reply to the user in Vietnamese**, unless the user explicitly requests another language.
- **Write code in English.** This includes identifiers, comments, tests, error messages, documentation, UI copy, commit messages, and PR descriptions.
- Explain technical findings and decisions concisely in Vietnamese. Keep commands, paths, symbols, and code snippets in their original technical form.
- Distinguish verified results from hypotheses. Never claim an audit, test, build, fix, or deployment succeeded unless it actually ran and was verified.

## Non-negotiable security boundaries

- Treat access tokens, refresh tokens, ID tokens, raw `auth.json` documents, vault keys, and encrypted vault backups as sensitive credentials.
- **Never read, print, copy, export, modify, or delete real user credentials** while developing, debugging, reviewing, or testing this repository.
- Never touch the user's real `~/.codex`, real `CODEX_HOME`, live `codex-switch` data directory, macOS Keychain items, Windows Credential Manager entries, or Linux Secret Service entries during tests.
- Do not run login, account import, reauthentication, account switching, vault rotation, synchronization, or updater commands against real profiles as part of development verification.
- Use isolated temporary `CODEX_HOME` and `CODEX_SWITCH_HOME` directories and synthetic authentication documents for all functional and regression tests. Use fake or in-memory secret stores where practical.
- Never put secrets in terminal output, logs, errors, telemetry, screenshots, issue reports, fixtures, commits, or remote tools.
- Never send credentials to the Desk C2C Hub connector. Its repository access is read-only; do not widen that boundary to support this project.
- Do not add telemetry, external API calls, local HTTP servers, proxy features, background daemons, or new privileged capabilities without explicit approval.

## Credential and state invariants

1. Keep one shared `CODEX_HOME`. Normal switching may change only its `auth.json` projection and the switcher's own encrypted vault/state. Preserve Codex sessions, databases, configuration, Skills, MCP settings, and UI state.
2. Let the official Codex CLI own the OAuth/login flow; do not reimplement login or intercept browser credentials.
3. Keep saved profiles encrypted at rest, with the key managed by the operating system's secret store. Never introduce a plaintext vault fallback.
4. Preserve unknown fields in supported authentication JSON. Reject unsupported authentication modes, invalid documents, ambiguous identities, and ambiguous token generations rather than guessing.
5. Determine the active account from live account/workspace identity and credential material, not a remembered alias or email address.
6. Coordinate **every** operation that can mutate vault data, active authentication, or related state with a consistent cross-process locking strategy. Avoid stale read-modify-write overwrites and lock-order deadlocks.
7. Preserve atomic replacement, restrictive file permissions, symlink/path protections, compare-before-replace checks, and switch recovery journals.
8. A failed or uncertain atomic write must not cause deletion of the only key capable of decrypting the vault already published to disk. Design key rotation for recoverability, including post-rename durability failures.
9. Token refresh reconciliation must check account identity and generation, retain the newest validated credentials when possible, and fail closed on uncertainty. Test failures and cancellation during refresh.
10. Never bypass the running-Codex process guard for normal switching. Do not use `--allow-running` in automation or UI flows.
11. Preserve CLI behavior, stored-data compatibility, and supported platforms unless a change is explicitly requested and covered by migration or regression tests.

## Development workflow

- Inspect the relevant source, tests, and `docs/` before editing. Read `CONTRIBUTING.md` for existing contribution requirements.
- Make the smallest deterministic change that addresses the task. Prefer focused commits and separate security fixes from refactors, dependency upgrades, and UI work.
- Preserve unrelated work. Never silently reset, discard, overwrite, or clean the working tree.
- Use a dedicated feature or fix branch for nontrivial changes. Do not rewrite shared history.
- Keep the existing Go module path `github.com/SilkageNet/codex-switch` unless a deliberate migration is requested; a GitHub fork does not by itself require changing the module path.
- Do not automatically update the installed `codex-switch` binary or invoke `codex-switch update`. The current updater targets upstream releases, not this fork.
- Do not modify `ChatGPT.app`, the bundled Codex application, system application bundles, or Desk C2C Hub source as part of switcher development.
- Do not commit, push, open PRs, create tags, publish releases, install tools, or deploy unless explicitly requested.
- Before any security-sensitive code change, identify the invariant, relevant failure paths, and a regression test that would catch the bug.

## Security hardening priorities

Address credential-integrity risks before adding UI or unattended behavior:

1. Audit and unify cross-process synchronization for all vault mutations: account add/import/reauth/rename/remove, sync, usage refresh, key rotation, and backup import.
2. Make vault key creation and rotation recoverable across write errors, especially errors occurring after destination replacement but before directory durability is confirmed.
3. Preserve refreshed credentials across recoverable App Server failures, timeouts, and retries without accepting another account's credentials.
4. Redact or safely normalize errors returned by subprocesses and RPC handlers before displaying or persisting them.
5. Review executable discovery, release updates, dependency integrity, build provenance, and platform-specific permission boundaries.

Treat a potential bug as a hypothesis until verified by source reasoning and, where feasible, a reproducible test. Never use real credentials to reproduce a security issue.

## macOS companion UI rules

- Prefer a small SwiftUI menu bar companion; do not turn the CLI into a credential-serving daemon.
- Read account identity and quota through documented **public, redacted CLI output**, for example `codex-switch current --json` and `codex-switch account list --cached --json`.
- Do not read the encrypted vault, raw `auth.json`, Keychain, or application data directly from the UI.
- Clearly label cached usage and its last refresh time. Do not silently treat stale quota as live data.
- Do not poll live usage automatically, rotate accounts automatically, or run background token refresh. A future explicit refresh feature requires separate security review.
- Switch only after an explicit user action, using the established CLI guard and showing any error. Do not use `--allow-running`, force switching, or terminate Codex automatically.
- Never inject controls into, patch, or replace files inside ChatGPT/Codex application bundles.
- Keep account switching and quota display optional; CLI users must not need the UI to operate safely.

## Tests and verification

- Tests that touch credentials or persistent state must create isolated temporary directories and use synthetic tokens, fake account metadata, and fake secret stores.
- Cover concurrent writers, lost updates, crash and partial-write recovery, token generation conflicts, process-guard behavior, symlink rejection, output redaction, and permission handling when relevant.
- Add fault injection around file writes, rename, sync, key-store updates/deletion, and interrupted App Server queries for the failure paths being changed.
- Run focused tests first. Before proposing a security-sensitive change as complete, run the applicable full checks:

```bash
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
git diff --check
```

- Run `pre-commit run --all-files` only if pre-commit is already installed and configured; do not install it implicitly.
- Do not run commands that operate on real credential stores or the current live Codex account as part of verification.
- Report exact checks executed, their results, any checks not run, changed files, compatibility impact, and residual risks.

## Documentation and handoff

- Keep `README.md` concise and user-focused. Put implementation, security, and compatibility details in `docs/`.
- Update `docs/security.md` or `docs/architecture.md` when changing a security boundary or state invariant.
- Never present source inspection alone as a completed runtime audit or claim this tool is categorically safer than alternatives without supporting evidence.
- End work with a concise Vietnamese handoff: what changed, why, verification evidence, remaining risks, and whether any commit or push occurred.
