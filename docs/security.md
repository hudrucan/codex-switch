# Security model

## Protected assets

- ChatGPT access tokens, refresh tokens, and ID tokens
- Authentication documents and their account/workspace metadata
- Vault encryption keys, encrypted vaults, and portable backups

## Threat and trust boundaries

The current operating-system user is trusted. A malicious process running as
that user can observe or use the same credentials Codex can use. The project
focuses on preventing multi-account secrets from entering ordinary JSON/config
files, avoiding accidental disclosure in diagnostics or repository artifacts,
restricting access by other local users, and preventing partial, stale,
concurrent, and path-traversing writes.

The official Codex CLI owns login and the App Server usage protocol. The
operating-system credential store and filesystem supply key storage and atomic
publication primitives. Binary distribution additionally trusts the configured
GitHub repository/workflows and HTTPS certificate roots, as described below.

## Credential and storage guarantees

Saved profiles are encrypted with XChaCha20-Poly1305. A separate random key is
stored in macOS Keychain, Windows Credential Manager, or Linux Secret Service;
it is never stored next to the ciphertext. WSL can use current-user Windows
DPAPI with ciphertext in HKCU, separate from the encrypted vault in WSL.
There is no plaintext vault/key fallback.

The active profile is projected into Codex's supported plaintext file store.
The file uses mode `0600` on Unix and lives in the current user's profile on
Windows, published with replace-existing semantics. Switching changes only
`auth.json` and switcher-owned vault/state; sessions, databases, configuration,
Skills, MCP settings, and UI state remain in the shared `CODEX_HOME`.

Unknown fields in supported authentication JSON are preserved. Unsupported
modes, invalid schemas, ambiguous identities/generations, and documents above
the configured size limit are rejected. Managed destinations reject symlinks.
Identity metadata comes only from explicit top-level or OpenAI auth namespace
claims, never arbitrary nested fields. Supported workspace/email evidence must
be structurally valid and consistent, including duplicate members. Raw unknown
fields are preserved.

Portable backups require a passphrase of at least 12 characters and use Argon2id
before XChaCha20-Poly1305 encryption. Commands provide no token retrieval API;
public JSON uses dedicated view types, and the usage cache contains no auth
documents or tokens.

## Mutation and concurrency guarantees

Every vault mutation holds the cross-process vault lock before reading mutable
state. Operations using live authentication or related state also acquire the
home lock, always after the vault lock, and release locks in reverse order.
Home-lock failure aborts the operation and releases the vault lock; it never
silently falls back to vault-only synchronization. Vault-only commands such as
rename and key rotation can operate without an existing `CODEX_HOME`.

Interactive login, passphrase input, and backup preparation occur outside the
transaction locks. The transaction reloads and validates current vault state.
Reauthentication captures the intended stable ID and alias before login, then
requires the same ID to still own the unchanged alias before replacement.
Removal, rename, and alias reassignment fail closed. Backup merge preserves
concurrent changes; explicit replacement still validates the current vault.

Internal IDs use exact `FindByID` without alias fallback. User-facing selectors
retain alias/ID lookup, and post-creation alias-only resolution retrieves the
new profile deterministically. Legal alias/ID collisions cannot redirect
internal selection, credentials, metadata, or active state to another profile.

Active identity comes from live account/workspace and credential material,
never a remembered alias or email. Observation is read-only. Mutating sync uses
compare-before-replace and the switch recovery journal; normal switching also
requires the running-Codex process guard. Recognized Codex/ChatGPT executable
identity cannot be suppressed by ordinary argument text; the guard excludes
only its own PID. Known workspace evidence in either saved auth or persisted
profile metadata must survive live reconciliation. Missing/different workspace
evidence fails closed before generation comparison, including `--prefer-live`.
Observations do not invent missing live workspace evidence. Ambiguous identity
and generation evidence fail closed. See `architecture.md` for transaction and
lookup rules.

## Usage refresh and recovery guarantees

Usage queries use per-profile temporary homes with mode `0700` and auth files
with mode `0600` on Unix. App Server startup creates an owned process group on
Unix or a Windows kill-on-close job. Windows executes no wrapper code before
job assignment; unsupported assignment/startup fails closed. Every exit path
terminates the owned group/job, reaps the direct child, and confirms lifecycle
termination before reading final auth and removing the temporary home. Failed
confirmation returns a redacted cleanup error, recovers no auth, and retains the
private temporary home.
Recovery accepts only a regular, size-limited file with a supported schema,
the original account, and the original workspace when known. Missing or changed
known workspace evidence fails closed. Only a validated newer generation
advances retry inputs and saved credentials; older, conflicting, cross-account,
cross-workspace, malformed, or ambiguous candidates are rejected.

Recovered credentials accompany the original RPC, protocol, process, timeout,
or cancellation error. They never turn a failed usage query into successful
usage retrieval. Retry backoff and pre-attempt cancellation retain the latest
validated candidate. Credential reconciliation and vault persistence precede
cache loading/writing; unavailable cache or invalid auxiliary active state
cannot discard an otherwise safe vault refresh. Active auth projection also
requires a valid live identity and compare-before-replace under both locks.

Explicit `account list --refresh` and `account usage <alias>`/`--all` select
validated vault profiles before auxiliary failures can abort recovery. Corrupt
cache, active state, or live auth may still produce a redacted error after
credential persistence; those files are not silently repaired. Invalid state
never supplies an implicit selection or permits live projection. `--cached`
remains offline and does not trigger recovery writes. The cache stores only
usage snapshots, reset metadata, aggregate token statistics, public account
metadata, timestamps, and normalized failures.

## Diagnostic and redaction guarantees

Raw RPC messages/data are discarded and subprocess stderr goes to a discard
sink. Public errors use fixed classifications/method names and bounded numeric
RPC codes, preserving `errors.Is` without exposing untrusted causes through
formatting or unwrapping. Strings are bounded to 1024 bytes.

Retries, usage-cache load/save, and CLI usage views share a strict allowlist for
failure messages. Arbitrary JSON field names and decoder diagnostics are not
echoed. Unsafe messages from older caches are redacted in memory on load and on
the next save; a read-only load does not rewrite the cache. Public JSON schemas,
vault formats, and storage formats remain compatible; error text deliberately
omits untrusted diagnostics.

## Vault/key durability

Vault creation and rotation store a fresh key before publishing its encrypted
envelope. The new key and published vault must both pass read-back verification
before the previous key is deleted. Any write or verification failure retains
the keys, including initial creation, uncertain replacement, and failed
post-replacement durability checks. Recovery uses the surviving envelope's key
ID without plaintext backups or in-memory recovery state. See the
[publication protocol and write-error states](architecture.md#vaultkey-lifecycle).

An empty vault is created only if the vault path is absent; an unavailable key
cannot cause an existing vault to be reinitialized.

If previous-key deletion fails or has an uncertain outcome, the error states
that rotation committed but cleanup was not confirmed. The current vault has
its verified new key. Failed/interrupted operations may leave orphaned keys;
automatic garbage collection is deliberately absent. A leftover encrypted
temporary file is never selected as the active vault.

## Platform secret-store boundaries

macOS invokes the system `security` utility without a shell and supplies the
vault key directly to `-w`; that utility treats `-w` without a value as an
interactive prompt. The key can briefly be visible to processes running as the
same user, within the stated trust boundary.

Linux sends values to `secret-tool` on standard input and fails closed without
Secret Service. Windows calls Credential Manager directly. WSL invokes a static
Windows PowerShell bridge without a profile, uses current-user DPAPI and a
hashed registry value name, and sends secrets over standard input. Bridge
output is redacted. WSL fails closed when neither its DPAPI bridge nor Secret
Service is available.

## Binary distribution and update trust

The binary distribution trust root is `hudrucan/codex-switch`, independently of
the Go module path. Source builds default to development provenance. GoReleaser
OSS uses `envOrDefault` for `CODEX_SWITCH_BUILD_CHANNEL`, defaulting to development;
the trusted release workflow supplies `release` only to its GoReleaser step.
Mutating update requires a canonical stable installed version and the exact
fork repository and release channel stamps. A source build with a module-derived
version cannot self-update. Read-only `--check` queries the fork without claiming
update eligibility. Missing fork releases fail closed; there is no upstream
migration/fallback.

Linker provenance is a policy marker, not a signature; arbitrary linker flags
can forge it. Same-release checksums detect corruption and bind an exact archive,
but do not independently authenticate the publisher. The workflow creates GitHub
attestations and SBOMs; the runtime updater does not verify attestations. Such
verification requires a separate trusted verifier and policy.

Only canonical stable `vMAJOR.MINOR.PATCH` releases are installable. Drafts,
prereleases, duplicate JSON keys/assets/checksum records, empty artifacts, size
mismatches, and foreign origins are rejected. Metadata is limited to 2 MiB,
checksums to 1 MiB, archives to 128 MiB, and each executable to 64 MiB. Archives
are limited to 256 entries and 128 MiB total file data. Only a root executable
and approved license/README/notice files are accepted. Paths, duplicates, links,
special entries, and extended tar headers cannot select an executable. Tar
input is restricted to USTAR; gzip trailers and trailing tar/gzip data are
validated. Validation includes every entry, even after the executable.

HTTP sends no credentials, cookies, or inherited proxy authentication. Initial
URLs must identify the exact fork API/download origin. Metadata redirects are
rejected. A download may redirect once from its fork GitHub URL to HTTPS on
`release-assets.githubusercontent.com` under its official release-asset path.
Arbitrary hosts, ports, userinfo, and subsequent redirects are rejected. GitHub
documents that host in its [runner network requirements](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).
New download hosts require policy review. Remote bodies and local filesystem
failures are excluded from diagnostics.

Executable updates validate all artifacts before one atomic replacement of the
existing executable. Staging uses random exclusive temporary files, file sync,
platform replacement, and directory durability. Targets must be regular files
with canonical parent paths; symlinks and setuid/setgid targets are rejected.
A separate `<executable>.update.lock` coordinates updates/installations without
account/vault access. Permissions and compare-before-replace are preserved.
Post-replacement durability errors report uncertainty and retain the published
executable. No rename-away fallback creates an absent executable path.

Shell and PowerShell installers run a standard-library Python 3.9+ verifier from
the same reviewed checkout, without a Go/CLI runtime dependency. Missing Python
fails before download. Only verified bytes are staged beside the destination
and atomically replaced; archive content is never executed. Symlink destinations
and uncertain concurrent changes fail closed. Installers do not request
elevation automatically.

The release workflow verifies module integrity, a clean tag-matching worktree,
and tag ancestry against `refs/remotes/origin/main` explicitly refreshed from
`origin`'s `refs/heads/main`. Failed fetch, missing branch, or unreachable commit
aborts release even if a stale reference exists. This proves source reachability
in fetched main history, not human review. GitHub repository/workflow control is
the trust root. Administrators must protect main with review requirements and
restrict creation, movement, and deletion of `v*` tags to authorized release
actors. These are required repository-side operational controls, not settings
enforced by this CLI. Direct Actions are pinned to commits, tool versions are
explicit, and Dependabot monitors Go and Actions weekly.

## Windows tray UI

The optional Windows UI consumes public CLI JSON without directly reading auth,
vaults, state/cache files, or operating-system credential stores. It uses
`Helpers/codex-switch.exe` beside the app, or an absolute local
`CODEX_SWITCH_CLI_PATH` override without fallback. Discovery rejects UNC and
reparse paths and checks executable/ancestor ownership and replacement ACLs.
The current user, SYSTEM, Administrators, and TrustedInstaller are trusted.
Creating siblings under an ancestor does not grant replacement rights over its
protected children. Deny rules do not relax allow-rule rejection, so restrictive
custom ACLs may be rejected. These checks do not authenticate a publisher or
eliminate same-user races.

Commands use separate arguments without a shell. Public stdout is capped at four
million characters; stderr is drained and discarded. Only read-only status has a
five-second UI timeout. Switch and refresh commands finish under the CLI's locks,
credential reconciliation, and recovery controls; hiding the popup does not
cancel them, and Quit is disabled while busy. The UI verifies profile, alias,
account/workspace identity, freshness, and error-free usage before accepting a
refresh result. A failed final observation disables switching until status can
be read again. It never schedules refreshes or passes `--allow-running`, and
adds no credential/storage format.

Close & Switch requires a separate explicit confirmation. Read-only process
metadata maps CLI-reported PIDs to a GUI in the Store package
`OpenAI.Codex_2p2nqsd0c76g0`, owned by the current user/session. Ancestry, package
or bundled/known-sidecar paths, and process start times must agree on reread.
Confirmation binds the GUI PID, start time, executable, and window; the complete
account/process observation is checked before each close request. A new or reused
GUI needs fresh confirmation. Windows receives only a normal window-close request,
never a kill or force-quit. Known children may be waited for after their GUI exits,
but are never signaled. Unknown processes or a close timeout require manual exit;
the normal CLI process guard remains authoritative. No real app is closed in tests.

Synthetic preview/demo modes never construct the real CLI client. Regression
checks use temporary homes, fake clients, and a synthetic subprocess without
opening credential stores. They do not establish live OAuth compatibility or
native tray behavior. Distribution still requires manual checks of Explorer
restart, keyboard navigation, high contrast, mixed-DPI monitors, taskbar positions,
and the installed executable's ACLs.

## Residual risks and limitations

- Credential-store acknowledged writes and filesystem sync/replacement primitives
  are trusted. Read-back detects immediately missing/incorrect keys, but cannot
  guarantee physical storage durability or offline credential-store availability.
  Unix syncs the parent directory; Windows uses write-through `MoveFileEx` without
  a separate directory sync.
- A refresh never written, truncated, or lacking identity/generation evidence
  cannot be recovered. Failed vault writes or an unavailable store may prevent
  persistence. Cleanup is best effort if the OS cannot stop/reap a process or
  remove files. Failures are reported without raw diagnostics; auth is not read
  from an unconfirmed process lifecycle, and its temporary home is retained.
  Unix process groups contain descendants that inherit the group; deliberate
  session/group escape by a same-user executable is outside this boundary.
  Executable discovery still trusts the selected official CLI/wrapper. Windows
  job/thread restrictions can prevent usage queries rather than permit unsafe
  uncontained execution.
- Windows may refuse replacement of a running executable; failure retains the
  old path. Same-user filesystem races remain within the local trust boundary.
- Hosted runners, transitive Action code/downloaders, Go checksum infrastructure,
  GitHub release control, and TLS remain trusted. Builds are not claimed to be
  fully hermetic or independently reproducible.
- Synthetic offline tests do not verify live OAuth, every App Server version,
  physical power loss/storage corruption, native-platform behavior on other
  systems, or actual hosted release publication. Real credentials are forbidden
  in development tests and fixtures.
