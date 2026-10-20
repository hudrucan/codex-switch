# Architecture

## Invariants

1. A user has one live `CODEX_HOME`.
2. Account switching projects only `auth.json`.
3. Codex-owned sessions, databases, configuration, plugins, skills, and UI state
   are never copied between profile directories.
4. The official `codex login` command owns the login protocol.
5. Unknown authentication schemas and ambiguous token generations fail closed.
6. The live `auth.json` identity is authoritative. Persisted active state is a
   recovery hint and must never override a different live account ID.
7. Every mutation holds the vault lock from before reading mutable state until
   its writes finish. Operations using live authentication or related state
   also acquire the live-home lock, always after the vault lock.

## Components

- `authschema` validates the supported ChatGPT login shape while retaining the
  complete raw JSON document, including unknown fields.
- `codexlogin` runs official login in a temporary `CODEX_HOME` configured for
  file storage, then imports the resulting document.
- `secretstore` protects a small random vault key with the operating-system
  credential store. WSL uses a Windows PowerShell bridge to protect the key with
  current-user DPAPI and store only ciphertext in HKCU.
- `vault` encrypts all saved account profiles with XChaCha20-Poly1305.
- `switcher` observes the live identity, classifies external-login and token
  drift, safely synchronizes known profiles, prepares a journal, performs
  compare-before-replace, and records the selected profile.
- `codexusage` runs the official Codex App Server in an isolated temporary
  `CODEX_HOME` and reads the stable account, rate-limit, and token-usage methods.
- `accountusage` queries selected profiles sequentially, reconciles credential
  refresh generations, and coordinates with switching through the same locks.
- `usagecache` stores credential-free successful snapshots separately from the
  encrypted vault.
- `atomicfile` publishes complete files and refuses symlink destinations.
- `doctor` reports only redacted, non-secret local facts.

## Native UI boundary

The native UI consumes public JSON through short-lived CLI commands. It never
reads the vault, live authentication, switcher state, usage cache files, or
operating-system credential stores directly. `ui status --json` returns
`schemaVersion: 1`, an `accounts` array using the existing public account and
cached usage views, the unchanged public `observation`, and a `codexProcesses`
array containing only `pid` and `command`. Empty arrays remain arrays.

This status command uses read-only observation and the usage service's cache
API. It does not refresh usage, start an App Server, recover journals, or write
storage. Usage retains its freshness status and fetch timestamp. The response
is a best-effort snapshot without mutation locks; process detection failure
returns a redacted error instead of an apparently safe empty list.

Switching requires explicit user action through the normal guarded
`codex-switch use <alias> --json` command, without `--allow-running`. That command
rechecks safety and remains the final mutation authority.

The app uses its bundled `Contents/Helpers/codex-switch`, or an absolute
`CODEX_SWITCH_CLI_PATH` override. There is no PATH search or shell. After symlink
resolution, the executable and its parent directories must belong to the current
user or root and must not be group/other writable. Non-executable files,
setuid/setgid executables, and non-regular files are rejected. Invalid overrides
fail without fallback. These checks trust the current user; they do not verify
a code signature.

`macos/CodexSwitch` contains Codex Switch, a SwiftUI `MenuBarExtra` agent app.
Opening its menu reads cached status first. If any account has no valid cache
timestamp, a future timestamp, or a snapshot older than five minutes, that open
event can invoke `codex-switch account list --refresh --json` and reload status.
An empty account list never triggers refresh. Closing the menu before the status
read finishes prevents automatic refresh; an already-started refresh finishes
normally so CLI credential recovery and process cleanup remain intact.

Automatic and explicit Refresh Usage share the app's operation exclusion and a
60-second cooldown from completion. Consecutive refresh or status-reload failures
delay the next attempt by one, two, then five minutes (capped); a successful
refresh and reload resets that sequence. The UI retains its previous successful
data on failure. Deadlines are checked on user actions/menu opens, without timers,
queued retries, or background polling. These limits are in memory per app process
and reset on relaunch; independent CLI invocations retain existing behavior and
cross-process locks. Passing a cached reset time never fabricates fresh quota.

On Unix, process discovery uses the `ps` short executable-name column (`ucomm`)
so macOS path-column truncation cannot turn `ChatGPT` into `Ch`. The public
process view preserves the case distinction between GUI names and terminal
`codex`. GUI termination still requires native bundle identity and launch-date
verification. Native ownership lookup may route a helper or bundled `codex`
worker to that GUI only when its full ancestry is stable, all executable paths
resolve inside the GUI bundle, and none has a controlling terminal. Ancestry,
executable paths, process start times and native app identity are reverified.
Only the deduplicated GUI roots receive termination requests; children are never
signaled directly. Previously observed children that briefly outlive the GUI
are only waited on. An orphan left after the bounded wait, an independent CLI,
or unverifiable metadata requires manual closure; switching still requires an
empty fresh CLI process list.

For supported GUI apps, switching follows explicit Quit confirmation, native
graceful termination, and bounded CLI status verification. If Codex remains,
Force Quit requires a second confirmation and fresh CLI/native app identity
checks before each target is terminated. Terminal or unverifiable processes
require manual closure and Retry. A reused PID never transfers consent. Every
final switch uses the normal CLI process guard, including races after the
informational status read. After termination requests, cached status is checked
at most 12 times, waiting 500 ms between checks. A still-running status subprocess
receives a termination request after 5 seconds; mutating CLI commands finish
under their own lifecycle controls. See the [native app guide](../macos/CodexSwitch/README.md)
for building and menu behavior.

## Locking and mutation model

All vault writers acquire `<vault path>.lock`. Operations using live
authentication or home-related state then acquire
`CODEX_HOME/.codex-switch.lock`. The vault lock serializes writers sharing a
vault even when they select different Codex homes. The home lock also protects
one live projection when commands select different switcher data directories.
Both use nonblocking platform locks: a contending command returns a busy error
and can be retried. Failure to acquire the home lock releases the vault lock;
successful operations release
both in reverse order. Lock files remain in place to keep a stable lock target
across atomic vault replacement.

Account add/reauth, rename, portable backup import (merge and replace), and the
rotate-key command use only the vault lock because they do not read or write
live authentication or home-related state. Rename and rotate-key therefore
continue to work with a valid vault when `CODEX_HOME` is missing, without
creating that home. This is a fixed choice based on the resources used by each
command, never a fallback after a home-lock error. Import-current, remove,
initialization of Codex configuration, switching, deactivation, sync, journal
recovery, and usage refresh acquire both locks through the same helper. A
home-lock failure aborts those operations and releases the vault lock.

Official login, backup file reading, passphrase input, and backup decryption
finish before acquiring the transaction lock. Add/reauth then reload the vault
under its lock before applying alias and replacement rules. Reauth also takes a
short vault lock before login to capture the existing target's stable profile
ID and alias, then releases it for login. Its write transaction requires that
same ID to still own the unchanged alias; removal, rename (including a case-only
rename), or alias reassignment aborts without saving credentials. Reauth never
creates a profile; account add remains a separate insertion path. Backup import
reloads and validates the current vault under the lock for both merge and
explicit replacement; merge retains concurrent changes and rejects current
alias conflicts. `--replace` still intentionally replaces the validated vault.

Internal vault `Save` and `RotateKey` require their caller to hold the vault
lock; they do not reacquire it. Initialization acquires the vault lock before
checking whether a vault exists and releases it before any transaction lock is
acquired. This avoids nested acquisition during runtime setup. Read-only
observation and backup export do not mutate the vault and do not acquire the
operation lock.

All concurrent writers must use this locking protocol. Binaries that omit vault
locking must not perform mutations concurrently with this CLI.

## Profile identity and lookup semantics

Internal profile IDs are exact, case-sensitive identifiers. Aliases are unique
case-insensitively, but may legally equal another profile's ID. Resolution uses
the namespace implied by the operation:

- User-facing selectors may use `Find`, which returns the first profile whose
  ID matches exactly or whose alias matches case-insensitively.
- Persisted or generated profile IDs must use exact `FindByID`. Internal IDs
  never fall back into the alias namespace, including when the ID is missing.
- After successful creation, alias ownership is deterministic; `FindByAlias`
  retrieves the newly added profile without interpreting the alias as an ID.

Active state, observations, reconciliation, and usage selection/persistence use
internal IDs. User account selectors and switch targets retain alias/ID lookup
compatibility. Journal recovery records its target ID only after hash checks;
reauthentication validates its captured ID and unchanged alias. Source is the
authoritative inventory of lookup call sites.

## Vault/key lifecycle

```text
hold vault lock
  -> decrypt the current envelope and capture its key ID (rotation only)
  -> generate and store a new key
  -> read back and verify the stored key
  -> write and sync the encrypted temporary file
  -> atomically replace the vault
  -> confirm platform directory durability
  -> read back and decrypt the new envelope using the credential store
  -> verify the new key ID
  -> delete only the previous key
```

`atomicfile.Write` returns a `WriteError` on failure, exposing whether
replacement was not attempted, was attempted with an uncertain outcome, or
completed before a later failure. Only a nil write error confirms all durability
steps completed. Cleanup failures preserve the original error and classification.

Any error before old-key deletion retains stored keys, even when a failed write
appears to precede publication. Initialization and missing-vault `Save` use the
same protocol. Interrupted publication leaves no in-memory recovery dependency:
the next load reads the envelope's key ID and uses its retained key. Successful
rotation after a recoverable failure deletes only the immediately previous key;
older orphaned keys are retained. A cleanup error after verified commit reports
the committed state explicitly, so a retry never assumes the old key is active.
See `security.md` for durability assumptions and retained-orphan limitations.

## Switch transaction and recovery

```text
acquire vault and home locks
  -> recover stale journal
  -> read and hash live auth
  -> identify the live account independently of recorded state
  -> reconcile live refresh generation into vault
  -> decrypt and validate target
  -> if target is already live, repair state without replacing auth.json
  -> otherwise verify Codex is stopped
  -> persist prepared journal
  -> compare live hash again
  -> atomically replace auth.json
  -> persist active state
  -> remove journal
```

The journal contains only profile IDs, hashes, and timestamps. If the process
stops after replacement but before state persistence, recovery compares the live
file with both hashes and completes the state transition.

## Live-state reconciliation

```text
read live auth and recorded state
  -> parse identity from explicit top-level and OpenAI auth namespace claims
  -> require account and known workspace continuity against document and profile
  -> prefer one exact credential-material match
  -> classify in-sync, external login, refresh, unmanaged, or ambiguous
  -> for sync: compare the live hash again
  -> adopt only a provably newer live generation
  -> persist the derived active pointer
```

Read-oriented commands use the observation immediately, so a stale recorded
profile never receives the active marker. Observation itself does not modify
state or credentials; usage refresh may persist a validated newer token
generation. `sync` performs explicit reconciliation
writes under the shared lock. Unknown and multiple matches are never assigned
by email or alias, and conflicting token generations require `--prefer-live`.
Known workspace evidence must remain present and equal in live credentials, even
when token material matches exactly. Unknown workspace evidence may become known
only with an otherwise unambiguous identity. Observation never fills missing
live workspace evidence from saved metadata. Generation preference cannot
override account/workspace mismatch.

Workspace claims (`workspace_id`, `workspaceId`, `chatgpt_workspace_id`) and
`email` are read only at the top level or directly inside
`https://api.openai.com/auth`. Equal non-empty evidence is accepted; conflicting
or structurally invalid supported evidence is rejected with a fixed error.
Unrelated nested claims are ignored and the complete raw document is preserved.

## Isolated usage query

```text
acquire shared operation lock
  -> decrypt and validate selected profile(s)
  -> create one temporary CODEX_HOME per profile
  -> write only that profile plus file-store config
  -> initialize the official Codex App Server
  -> read account/read, account/rateLimits/read, and account/usage/read in order
  -> retain quota reset times and any earned-reset count and expiration metadata
  -> terminate the owned server lifecycle and reap the wrapper on every exit path
  -> recover and validate its final authentication file
  -> delete the temporary home
  -> reconcile any newer credential generation, including one produced by a failed query
  -> atomically save credential-free usage snapshots
```

The live account selection never changes. If Codex rotates a refresh token while
answering the query, identity and generation checks run before the new document
is saved. For an active profile, compare-before-replace protects the live
projection from a concurrent Codex write. Transient failures are retried with
backoff using the newest validated credential generation returned by the prior
attempt. Multi-account refreshes are intentionally sequential so independent
App Server processes do not compete for startup or authentication resources.

All returns after App Server startup share one cleanup/recovery path. Unix
launches the server in its own process group. Windows starts it suspended,
assigns it to a kill-on-close job without breakaway permission, and only then
resumes its initial thread. Job assignment or ambiguous startup fails closed.
Cancellation closes protocol pipes and terminates the owned group/job. Cleanup
reaps the wrapper and confirms the group/job is empty before reading final
authentication. Confirmation is bounded and independent of the cancelled query
context. If it fails, no auth is recovered and the private temporary home is
retained; it is not removed while the lifecycle remains uncertain. Only a
regular file within the temporary home is read, with the configured authentication size limit. Schema, account, known
workspace, and generation checks run even when initialization, RPC, decoding,
process exit, or cancellation failed. A known workspace must not disappear or
change. An unknown original workspace may become known; generation ordering
uses credential material and timestamp evidence. Older or ambiguous generations
are rejected. A recovered candidate accompanies the original operation failure
and never makes failed usage retrieval successful. Explicitly marked partial results are supported
when one usage method succeeds.

Retry inputs advance only after validation against the most recently accepted
document. Failed attempts cannot replace that document with older, conflicting,
or malformed credentials. Cancellation in backoff or before another attempt
returns the already validated candidate. Under the operation locks, credential
reconciliation and vault persistence precede cache loading/writing. Invalid
auxiliary state prevents uncertain live-file projection, but does not discard a
validated vault update. Active auth uses compare-before-replace; credential
persistence uses the atomic publication and key-recovery protocol.

An explicit list refresh defers observation and cache preflight until after the
refresh service has reconciled credentials. Explicit usage selectors (alias or
`--all`) may proceed from validated vault profiles when observation fails.
Usage without a selector still requires a valid live observation, and cached
commands never invoke the runner. Auxiliary corruption is reported, not repaired
or interpreted as an active identity; a command can therefore fail after safely
persisting refreshed credentials. Post-refresh observation errors are redacted.

RPC error messages/data and subprocess stderr are excluded from public
diagnostics. Retries, cache failures, and CLI usage views share a bounded
allowlist grammar; read-only cache loads redact unsafe messages in memory
without rewriting the cache. See `security.md` for the diagnostic contract.

## Release/update/install flow

`main` passes version plus repository/channel linker stamps into the updater.
Source builds default to development; the trusted release workflow supplies
the release channel. Version output and the update result's `installed`, `latest`,
`current`, and `updated` keys retain their public schemas. The trusted release
repository is an explicit constant, never derived from the Go module path or
from release metadata. See [distribution policy and trust limits](security.md#binary-distribution-and-update-trust)
for provenance, artifact validation, redirect rules, and publication controls.

```text
validate installed provenance (mutating update only)
  -> bounded fork metadata, stable tag, exact origin and unique assets
  -> bounded unique checksum manifest for the exact platform archive
  -> bounded archive with exact size and SHA-256
  -> validate every archive entry and recover one root executable
  -> lock the independent executable target
  -> validate target type, permissions, identity and content
  -> stage/sync/atomically replace through the atomic writer
```

Before GoReleaser runs, `scripts/verify-release-source.sh` validates the stable
tag, tag/HEAD agreement, and clean worktree, then explicitly fetches origin main
and checks tag ancestry against the refreshed `refs/remotes/origin/main`. Any
fetch or ancestry failure aborts publication. This establishes source reachability,
not human review or publisher authentication.

Shell and PowerShell install scripts delegate to a verifier in the same reviewed
checkout, using Python 3.9+ and its standard library. The installer independently
validates release origin, assets, checksums, and archives before staging and
atomically replacing a target. It never executes archive contents or falls back
to upstream releases.

## Data locations

`CODEX_HOME` resolution:

1. `--codex-home`
2. `CODEX_HOME`
3. `~/.codex`

`codex-switch` data resolution:

1. `--home`
2. `CODEX_SWITCH_HOME`
3. the operating system's user configuration directory

Only tests and advanced portable installations should normally override these
paths.

The usage cache is `usage-cache.v1.json` inside the resolved `codex-switch` data
directory. It contains no authentication documents.
