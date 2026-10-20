# Compatibility

## Supported authentication

The authentication adapter accepts ChatGPT login documents with:

- `auth_mode = "chatgpt"`
- `tokens.account_id`
- `tokens.access_token`
- `tokens.refresh_token`
- optional `tokens.id_token`
- optional RFC3339 `last_refresh`

Unknown fields are retained because the complete raw document is encrypted and
projected. API-key profiles are unsupported.

## Credential store

The active Codex credential store must be `file`. Run:

```bash
codex-switch init --enable-file-store
```

The command creates a timestamped backup before making a surgical top-level
`config.toml` edit. Normal account switches do not edit `config.toml`.

## WSL2

The Linux build detects WSL through the standard WSL environment and Microsoft
kernel markers. On WSL it prefers Windows PowerShell and current-user DPAPI over
Linux Secret Service:

- the generated vault key is encrypted for the current Windows user;
- only the DPAPI ciphertext is stored under
  `HKCU\Software\SilkageNet\codex-switch\secrets`;
- the key is sent to the static PowerShell bridge over standard input and is not
  placed in command-line arguments;
- no plaintext fallback file is created in the WSL filesystem.

Windows interoperability and the default `/mnt/c` mount must be enabled. Both
Windows PowerShell 5.1 (`powershell.exe`) and PowerShell 7 (`pwsh.exe`) are
recognized. If `secret-tool` is also available, it remains a compatibility
fallback so vaults created by earlier Linux builds can still be read and
rotated.

## Codex releases

Isolated account-usage queries were validated with Codex CLI
`0.148.0-alpha.21`. Login is delegated to the installed official CLI, and usage
uses the documented stable App Server protocol without private OAuth or usage
endpoints.

On Windows, these operations require the standalone Codex CLI. The executable
inside the Microsoft Store/MSIX desktop package is private to that package and
cannot be spawned by an external process. `codex-switch` skips that entry while
searching `PATH` and returns an actionable error when no standalone CLI is
available. A standalone CLI can also be selected with `CODEX_BINARY` or
`--codex-binary`.

On macOS, discovery checks `PATH`, `/Applications`, and `~/Applications`. Both
the retained `Codex.app/Contents/Resources/codex` compatibility path and the
nested `ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex`
layout are supported, along with their direct/nested bundle variants.

Usage querying initializes `codex app-server` and calls:

- `account/read`
- `account/rateLimits/read`
- `account/usage/read`

When returned by `account/rateLimits/read`, the client also preserves
`rateLimitResetCredits.availableCount` and its optional grant/expiration detail
rows. Older services that omit this optional object remain supported.

If one usage method is unavailable, the other is still cached and marked
partial. If both are unavailable, update the installed Codex client. These
methods require a ChatGPT/Codex service login; API-key-only and Amazon Bedrock
profiles are not supported by `codex-switch`.

On an unknown or malformed schema, `codex-switch` stops before overwriting the
live file. Add a redacted fixture and a versioned adapter before broadening the
accepted shape.

## Upstream references

- OpenAI authentication documentation: https://developers.openai.com/codex/auth
- OpenAI Codex App Server documentation:
  https://learn.chatgpt.com/docs/app-server
- OpenAI Codex CLI documentation: https://learn.chatgpt.com/docs/codex/cli
- CC Switch managed Codex OAuth implementation:
  https://github.com/farion1231/cc-switch/tree/v3.20.0
