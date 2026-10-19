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

## Mutation locking

All vault writers acquire `<vault path>.lock`. Operations using live
authentication or home-related state then acquire
`CODEX_HOME/.codex-switch.lock`. The vault lock serializes writers sharing a
vault even when they select different Codex homes. The home lock also protects
one live projection when commands select different switcher data directories.
Both use the existing nonblocking platform lock implementation: a contending
command fails with the existing busy error and can be retried. Failure to
acquire the home lock releases the vault lock; successful operations release
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

### Profile lookup audit

Internal profile IDs are exact, case-sensitive identifiers. An alias may legally
equal another profile's ID, including a case-insensitive match; alias ordering
must never affect internal resolution. The production lookup sites are classified
below. `Find` retains its existing user-facing alias/ID behavior; `FindByID` has no
alias fallback. `FindByAlias` resolves only the case-insensitive alias namespace.
After successful `Add(profile, false)`, aliases are unique and this alias-only
lookup retrieves the newly created profile, never an existing profile by ID.

| Production site | Lookup input | Classification and resolution |
| --- | --- | --- |
| `app.newAccountUsageCommand`, explicit selector | `args[0]` | User alias; retain `Find` |
| `app.newAccountShowCommand` | `args[0]` | User alias; retain `Find` |
| `app.newAccountRenameCommand`, current alias | `args[0]` | User alias; retain `Find` |
| `app.newAccountRenameCommand`, conflict check | `args[1]` | Proposed user alias; retain `Find` |
| `app.newAccountRemoveCommand` | `args[0]` | User alias; retain `Find` |
| `app.addDocument`, after `Add` | `alias` | Identity of the newly created profile by its unique alias; `FindByAlias` |
| `switcher.Service.Use`, target | `alias` | User switch target; retain `Find` |
| `switcher.Service.Sync`, after `Add` | `options.Alias` | Identity of the newly imported profile by its unique alias; `FindByAlias` |
| `app.newAccountUsageCommand`, implicit selector | `activeProfileID` | Observed internal ID; `FindByID` |
| `accountusage.Service.Refresh`, selection | `id` from `profileIDs` | Internal ID; `FindByID` |
| `accountusage.Service.Refresh`, persistence | `profileID` from query results | Internal ID; `FindByID` |
| `switcher.observationWithoutLive` | `state.ActiveProfileID` | Persisted internal ID; `FindByID` |
| `switcher.observeLive` | `state.ActiveProfileID` | Persisted internal ID; `FindByID` |
| `switcher.Service.Use`, live reconciliation | `snapshot.observation.ProfileID` | Observed internal ID; `FindByID` |
| `switcher.Service.Sync`, managed reconciliation | `snapshot.observation.ProfileID` | Observed internal ID; `FindByID` |

Recovery copies `Journal.TargetProfileID` into recorded state after hash checks;
it does not perform a name lookup. Subsequent observation resolves that state
with `FindByID`. Reauthentication checks its captured ID and alias explicitly.
A missing recorded ID does not acquire an alias from another profile.

All concurrent writers must use this locking protocol; older binaries that omit
vault locking must not run mutations concurrently with the updated CLI.

## Vault key lifecycle

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

`atomicfile.Write` keeps its error-returning API. A `WriteError` exposes whether
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
- `accountusage` queries up to four profiles concurrently, reconciles credential
  refresh generations, and coordinates with switching through the same lock.
- `usagecache` stores credential-free successful snapshots separately from the
  encrypted vault.
- `atomicfile` publishes complete files and refuses symlink destinations.
- `doctor` reports only redacted, non-secret local facts.

## Switch transaction

```text
acquire lock
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
  -> match account_id plus workspace_id against encrypted profiles
  -> prefer one exact credential-material match
  -> classify in-sync, external login, refresh, unmanaged, or ambiguous
  -> for sync: compare the live hash again
  -> adopt only a provably newer live generation
  -> persist the derived active pointer
```

Read-oriented commands use the observation immediately, so a stale recorded
profile never receives the active marker. Observation itself does not modify
state or credentials; the existing live usage-refresh path may still persist a
validated newer token generation. `sync` performs explicit reconciliation
writes under the shared lock. Unknown and multiple matches are never assigned
by email or alias, and conflicting token generations require `--prefer-live`.

## Isolated usage query

```text
acquire shared operation lock
  -> decrypt and validate selected profile(s)
  -> create one temporary CODEX_HOME per profile
  -> write only that profile plus file-store config
  -> initialize the official Codex App Server
  -> read account/read, account/rateLimits/read, and account/usage/read in order
  -> retain quota reset times and any earned-reset count and expiration metadata
  -> stop and reap the server on every exit path after startup
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

All returns after App Server startup share one cleanup/recovery path. Cancellation
closes protocol pipes and terminates the child; its exit is reaped before reading
the final authentication file. Only a regular file within the temporary home is
read, with the existing authentication size limit. Schema, account, known
workspace, and generation checks run even when initialization, RPC, decoding,
process exit, or cancellation failed. A known workspace must not disappear or
change. An unknown original workspace may become known; generation ordering
itself is unchanged. Older or ambiguous generations are rejected. A recovered
candidate accompanies the original operation failure and never makes failed
usage retrieval successful. Existing explicitly marked partial usage results
remain supported when one usage method succeeds.

Retry inputs advance only after validation against the most recently accepted
document. Failed attempts cannot replace that document with older, conflicting,
or malformed credentials. Cancellation in backoff or before another attempt
returns the already validated candidate. Under the P0 operation lock, credential
reconciliation and vault persistence precede cache loading/writing. Invalid
auxiliary state prevents uncertain live-file projection, but does not discard a
validated vault update. Compare-before-replace still protects active auth, and
P1 atomic-write/key handling is unchanged.

Within usage refresh, internal profile identifiers use exact-ID lookup during
selection and again after vault reload for persistence. They never fall back to
the alias namespace, even when an alias legally equals another profile ID.
The CLI still resolves explicit user aliases with its existing lookup rules;
implicit active usage selection resolves the observed profile ID exactly.

An explicit list refresh defers observation and cache preflight until after the
refresh service has reconciled credentials. Explicit usage selectors (alias or
`--all`) may proceed from validated vault profiles when observation fails.
Usage without a selector still requires a valid live observation, and cached
commands never invoke the runner. Auxiliary corruption is reported, not repaired
or interpreted as an active identity; a command can therefore fail after safely
persisting refreshed credentials. Post-refresh observation errors are redacted.

RPC error messages/data and stderr are not used as public diagnostics. Errors
expose fixed operation/method names, bounded numeric RPC codes, and fixed failure
classifications; context and original-error identity remain testable with
`errors.Is`. Error formatting, retries, cache failure messages, and the final CLI
usage view share a bounded allowlist grammar. Legacy cache messages outside that
grammar are normalized on load and on the next save, without changing the JSON
schema. Reading a cache does not rewrite its file.

## Release and installation flow

`main` passes version plus repository/channel linker stamps into the updater.
The Go source defaults to development. GoReleaser OSS reads
`CODEX_SWITCH_BUILD_CHANNEL` with `envOrDefault`, defaulting to development when
absent. Only the trusted release workflow supplies `release` to its GoReleaser
step; ordinary source builds, GoReleaser invocations, and snapshots without that
marker remain development builds. These linker stamps are policy markers, not
signatures. Version output and the update result's `installed`, `latest`, `current`, and `updated` keys are
unchanged. The trusted release repository is an explicit constant, never derived
from the Go module path or from release metadata.

```text
validate installed provenance (mutating update only)
  -> bounded fork metadata, stable tag, exact origin and unique assets
  -> bounded unique checksum manifest for the exact platform archive
  -> bounded archive with exact size and SHA-256
  -> validate every archive entry and recover one root executable
  -> lock the independent executable target
  -> validate target type, permissions, identity and content
  -> stage/sync/atomically replace through the existing atomic writer
```

Before GoReleaser runs, `scripts/verify-release-source.sh` validates the stable
tag, tag/HEAD agreement, and clean worktree, then explicitly fetches origin main
and checks tag ancestry against the refreshed `refs/remotes/origin/main`. Any
fetch or ancestry failure aborts publication. Checkout is bound to the fork by
the workflow repository condition. Local regression tests use temporary Git
repositories and local bare origins, with no network or production tags. This
source check depends on repository/workflow control and required release-tag
restrictions; it cannot independently prove review or authenticate a publisher.

Tests inject a local TLS transport and replacement callback; production URL
policy still evaluates the original GitHub URL before any connection. The tests
never contact GitHub or replace the running executable. Install scripts delegate
to `scripts/install.py`, which uses the same policy and Python's standard library,
and installer tests use synthetic archives and temporary destinations. Windows
wrapper execution and actual hosted release publication require separate platform
verification. See `security.md` for the checksum/authenticity boundary and failure
states.

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
