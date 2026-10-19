# Security model

## Protected assets

- ChatGPT access tokens
- ChatGPT refresh tokens
- ID tokens and their account metadata
- portable vault backups

Saved profiles are encrypted with XChaCha20-Poly1305. A separate random key is
stored in macOS Keychain, Windows Credential Manager, or Linux Secret Service.
The key is never stored next to the ciphertext.

On WSL2, where a Linux desktop Secret Service is commonly unavailable, the key
is protected by Windows DPAPI for the current Windows user. The resulting
ciphertext is stored in HKCU, separate from the encrypted vault in the WSL
filesystem. The embedded PowerShell bridge is static; secret values travel over
standard input, never process arguments, and bridge diagnostics are redacted.

The active profile must be readable by Codex and is therefore projected into
the officially supported plaintext file store. That file is created with mode
`0600` on Unix. On Windows it lives in the current user's profile and is
published with a replace-existing operation.

## Threat boundaries

The current operating-system user is trusted. A malicious process running as
that user can observe or use the same credentials Codex can use. The project
focuses on:

- preventing multi-account secrets from entering ordinary JSON/config files;
- avoiding accidental logs, shell history, issue attachments, and repository
  commits;
- preventing other local users from reading stored files;
- preventing partial, stale, concurrent, and path-traversing writes.

## Operational rules

- Commands never expose a token retrieval operation.
- JSON output is intentionally based on dedicated public view types.
- Usage queries create per-profile temporary homes with mode `0700` and
  credential files with mode `0600`, then remove them after the App Server exits.
- The usage cache contains rate limits, earned-reset counts and metadata,
  aggregate token statistics, timestamps, and public account metadata only. It
  never contains authentication documents.
- A token refreshed during an isolated query is accepted only after account,
  workspace, and refresh-generation checks. Active-file updates use a
  compare-before-replace check under the shared operation lock. A validated
  newer generation is preserved even when the accompanying usage request fails,
  so retries do not fall back to a rotated token.
- Active-account detection uses the live account and workspace identifiers;
  stale recorded state, aliases, and email addresses cannot redirect live
  credentials into another saved profile. Synchronization rechecks the live
  file hash before committing derived state.
- Real credentials are forbidden in tests and fixtures.
- The Linux desktop implementation fails closed when Secret Service is absent.
  WSL fails closed when neither the Windows DPAPI bridge nor Secret Service is
  available; it never creates a plaintext key fallback.
- Portable backups require a passphrase of at least 12 characters and use
  Argon2id before XChaCha20-Poly1305 encryption.
- Authentication documents larger than the configured limit are rejected.
- A symlink at a managed destination is rejected instead of followed.

## Failed usage queries and diagnostic redaction

After an isolated App Server starts, every return first terminates/reaps it and
then reads its final authentication file before removing the temporary home.
Recovery requires a regular, size-limited authentication file, a supported schema,
the original account identity, and the original workspace when that workspace
was known. Missing or changed known workspace evidence fails closed. Existing
token-material/timestamp generation semantics are preserved: only a validated
newer generation advances retry inputs and saved credentials; older, conflicting,
cross-account, cross-workspace, malformed, and ambiguous candidates are rejected.

A valid recovered refresh does not hide an RPC, protocol, process, timeout, or
cancellation failure. Usage snapshots remain unsuccessful while credentials can
still be reconciled into the encrypted vault and, when safe, active auth. Retry
backoff and pre-attempt cancellation retain the latest validated candidate.
Cache failures and unreadable active state cannot prevent the earlier vault
reconciliation; uncertain active state is never used to guess a projection.

Refresh selection and persistence resolve internal profile IDs exactly, without
alias fallback. A legal alias matching another profile's ID cannot redirect a
query, suppress a selected profile, or receive another profile's credentials.
User-provided aliases retain their existing lookup behavior.

The same exact-ID rule applies to switcher reconciliation and recorded aliases:
`state.ActiveProfileID` and observed profile IDs are never interpreted as aliases.
Observe, Use, and Sync therefore cannot redirect profile A's live credentials,
workspace metadata, or active state into a profile B whose alias matches A's ID.
A missing recorded ID leaves the recorded alias unknown. User switch targets
and account CLI selectors retain their existing lookup behavior. After profile
creation, add/import-current and unmanaged `Sync --as` use alias-only lookup to
retrieve the profile just added. An alias equal to an existing ID cannot redirect
returned metadata or the new active pointer. Such namespace collisions remain
valid vault data. The production call-site classification is in `architecture.md`.

Explicit `account list --refresh` and `account usage <alias>`/`--all` select
validated vault profiles before auxiliary failures can abort recovery. Corrupt
cache, active state, or live authentication can still produce a redacted command
error after credential persistence; those files are not silently repaired.
Invalid active state never supplies an implicit selection or permits live
projection. `--cached` remains offline and does not trigger recovery writes.

Raw RPC error messages/data are discarded, and subprocess stderr is sent to a
discard sink. Public errors are restricted to fixed classifications, fixed method
names, and numeric RPC codes in a bounded range. They preserve `errors.Is`
without exposing untrusted causes through formatting or unwrapping. Error
strings are at most 1024 bytes. Usage-cache load/save and CLI usage views apply
the same strict allowlist to failure messages, including caches from older
versions; arbitrary JSON field names and decoder diagnostics are not echoed.
Existing unsafe messages are not rewritten on a read-only cache load, but are
redacted in memory and on the next save. Public JSON keys and vault formats do
not change; diagnostic text is intentionally less detailed.

Recovery cannot reconstruct a refresh that was never written, was truncated, or
cannot be identity/generation-validated. A failed vault write or unavailable
credential store still limits persistence; P1 retains its key-recovery behavior.
Cleanup is best effort when the operating system cannot kill/reap a process or
remove files; such failure is reported without reading credentials from an
unconfirmed running child. Tests use synthetic helper processes and do not
verify live OAuth or every Codex App Server version.

## Vault key publication and recovery

Vault creation and rotation store a fresh key before publishing its encrypted
envelope. The key is read back and compared with the generated value before
replacement. After the atomic write reports success, the manager reads and
decrypts the published envelope using the credential store and checks its key
identifier before deleting the previous key. Rotation uses the old identifier
from the same envelope that was decrypted, rather than a separate file read.

Atomic write errors distinguish replacement not attempted, replacement outcome
uncertain, and replacement completed with a later failure. A failed replacement
call is treated as uncertain even if it usually leaves the destination
unchanged. Success requires temporary-file sync and close, platform replacement,
and the platform's parent-directory durability step. Temporary-file close and
removal failures are included in the error instead of silently discarded.

No key-store rollback deletion runs on a failed or interrupted write, including
initial creation. Both old and new keys are retained when publication or
verification is uncertain; a fresh process uses the surviving envelope's key
identifier to decrypt it. This needs no new vault format or plaintext recovery
file. A later rotation can proceed from whichever valid envelope survived.
Initialization checks that the vault path itself is absent before creating an
empty vault; a missing key-store resource cannot trigger reinitialization of an
existing vault.

If deletion of the previous key fails or has an uncertain outcome, the command
returns an error explicitly stating that rotation committed but cleanup could
not be confirmed. The current vault still has its verified new key. Failed or
interrupted operations can leave orphaned keys in the operating-system store;
they are deliberately not garbage-collected automatically. A leftover encrypted
temporary file is not selected as the active vault.

These guarantees assume the credential store preserves acknowledged writes and
the filesystem honors its sync/replacement primitives. Read-back verification
detects immediately missing or incorrect keys; it cannot guarantee hardware
durability or availability of an offline credential store. Unix syncs the parent
directory; Windows retains the existing write-through `MoveFileEx` replacement
without a separate directory sync. Fault-injection tests model software failure
and interruption boundaries, including helper-process exit before and after
publication using synthetic persistent stores. They do not simulate physical
power loss or storage corruption.

## Platform credential-store adapters

The macOS implementation invokes the system `security` utility without a shell.
Because that utility treats `-w` without a value as an interactive prompt rather
than reading standard input, the adapter supplies the vault key directly to
`-w`. The value can therefore be visible briefly to processes running as the
same operating-system user, which is inside this project's trust boundary.
Linux sends values to `secret-tool` over standard input. Windows calls the
native Credential Manager API directly. WSL invokes Windows PowerShell without
a profile and uses current-user DPAPI plus a hashed registry value name.

## Binary distribution and update trust

The binary distribution trust root is the GitHub repository
`hudrucan/codex-switch`, independently of the unchanged upstream Go module path.
Source builds default to development provenance. GoReleaser OSS uses
`envOrDefault` to keep ordinary builds and snapshots in development unless
`CODEX_SWITCH_BUILD_CHANNEL` is explicitly supplied. The trusted release
workflow supplies `release` only to its GoReleaser step. Mutating update
requires a canonical stable installed version, the exact fork repository stamp,
and the release channel stamp. A source build with a module-derived version still
cannot self-update. Read-only `--check` queries the fork without claiming that
the current executable is installable. There is no automatic upstream migration.
No existing fork release or successful live installation is asserted by this
hardening work; absent releases fail closed.

Provenance stamps are build-policy markers, not signatures. A user who builds
with arbitrary linker flags can forge them. Release publication and GitHub
repository/workflow control are trusted, together with HTTPS certificate roots.
Checksums from the same release detect corruption and bind the exact archive;
they do not independently authenticate the publisher. The workflow creates
GitHub attestations and SBOMs, but the updater does not verify attestations.
Runtime attestation verification would need a separate trusted verifier and
policy (such as a GitHub CLI/Sigstore dependency); it is deliberately not faked
or replaced by a custom signing protocol.

Only canonical stable `vMAJOR.MINOR.PATCH` releases are installable. Drafts,
prereleases, duplicate JSON keys/assets/checksum records, empty artifacts, size
disagreements, and foreign origins are rejected. Metadata is limited to 2 MiB,
checksums to 1 MiB, archives to 128 MiB, and each executable to 64 MiB. Archive
entries are limited to 256 with 128 MiB total file data. Only a root executable
and the release's license/README/notice files are allowed. Paths, duplicates,
links, special entries, and extended tar headers cannot select an executable.
Tar input is restricted to USTAR, as emitted for the short paths in this release
configuration; GNU/PAX extensions are not interpreted as an alternate selector.
The entire archive is checked, including entries after the executable.

HTTP clients send no credentials, cookies, or inherited proxy authentication.
Initial requests use the exact fork API/download URLs. Metadata redirects are
rejected. A download may redirect once from its fork GitHub URL to HTTPS on
`release-assets.githubusercontent.com`, under the official release-asset path;
arbitrary hosts, ports, userinfo, and subsequent redirects are rejected. This host
is documented in GitHub's [runner network requirements](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).
New GitHub download hosts require policy review rather than automatic trust.
Remote bodies and local filesystem failures are not echoed as diagnostics.

Verification finishes before any executable write. The old self-update library
used predictable `.new`/`.old` paths, truncated a pre-existing temporary path,
did not sync its staged file, and moved the old executable away before publishing
the new one. The updater now uses the existing P1 atomic writer: random exclusive
temporary files, file sync, one platform replacement, and directory durability.

The target must be a regular, non-symlink file with a canonical parent path;
setuid/setgid targets are rejected. A separate `<executable>.update.lock` guards
updates and installation, without accessing any account/vault state. Existing
permissions and compare-before-replace checks are retained. Windows may refuse
replacement of a running executable: failure retains the old path, with no
rename-away fallback. Post-replacement durability errors report uncertainty;
they do not delete the published executable. Same-user filesystem races and
hardware durability remain platform trust assumptions.

Shell and PowerShell installers invoke a single standard-library Python 3.9+
verifier from the same reviewed checkout. This explicit installer prerequisite
replaces fragile shell JSON/checksum parsing and keeps the two platforms on one
validation policy; it adds no Go or CLI runtime dependency. Missing Python fails
before download. Installers extract only verified bytes into a random temporary
file beside the destination, never run archive content, and atomically replace
after validation. Symlink destinations and uncertain concurrent changes fail
closed. They do not request elevation automatically.

Release builds verify module integrity and clean, tag-matching source instead of
running `go mod tidy`. Before building, the workflow explicitly fetches
`refs/heads/main` from checkout's `origin` into `refs/remotes/origin/main` and
requires the tag commit to be its ancestor. A failed fetch, missing branch, or
unreachable commit aborts the release, even if a stale local reference exists.
This verifies source reachability in freshly fetched main history; it does not
prove that a human reviewed a commit. GitHub repository and workflow control
remain the trust root. Repository administrators must protect main with review
requirements and restrict creation, movement, and deletion of `v*` release tags
to authorized release actors. Those repository-side controls are operational
requirements, not settings enforced or changed by this source patch.

Linker metadata is deterministic; builds without the explicit workflow marker
remain development builds. Direct GitHub Actions are pinned to public commits
resolved during this audit; tool versions are explicit and Dependabot already monitors Go and
Actions weekly. Pins reduce mutable-reference exposure, but hosted runners,
action transitive code/downloaders, Go's checksum infrastructure, and GitHub
release control remain trusted. Builds are not claimed to be fully hermetic or
independently reproducible. No release is performed by local tests.
