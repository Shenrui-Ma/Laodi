# Windows client contract status

Status on 2026-09-19: **no Windows client build or live callback is verified**.
Windows hook installation fails before reading or writing client settings.
Finding a public executable, accepting a synthetic JSON event, and running a
synthetic shell command are separate checks; none establishes live integration.

## Discovery and identity

`laodi hooks discover` inspects a bounded list of standard LocalAppData and
Program Files installation candidates plus exact `zcode.exe`/`claude.exe` PATH
entries. It never launches a client or opens settings, sessions or checkpoints.
It reports public executable SHA-256 and fixed PE file version when available.
Reparse candidates, non-local/ambiguous paths and oversized files are rejected.
Its JSON can contain local executable paths; redact those before publishing.
Nonstandard locations, launcher scripts, Store packages and WSL are not covered.

The development host had no matching standard installation directory,
uninstall registry entry or PATH executable in the read-only discovery pass.
Registry inspection was a one-time development check; the CLI does not enumerate
registry entries or recursively search application data.

Windows `DetectBuild` returns `unknown`, even when a PE version happens to equal
the known macOS version. The scanner reports `unsupported_build` for discovered
Windows clients. Windows has no guessed default evidence root. `--build` remains
an explicit synthetic-fixture mechanism, not validation of a real client.

## Documented event preparation

The [Claude Code hook reference](https://code.claude.com/docs/en/hooks) documents
the `PowerShell` tool, its `tool_input.command` string, JSON stdin and async
command hooks. The observer can inspect those documented event bodies manually;
no Windows installer is enabled from documentation alone. The existing macOS
`Bash|Read` installation and shell command stay unchanged.

PowerShell read requests accept only direct `Get-Content` (or its full module
name), one path, and a small full-name option set. Single-quoted literal paths
handle Chinese, spaces, apostrophes and shell metacharacters. Expansions,
pipelines, scripts, aliases, providers, alternate streams and ambiguous options
remain `shell_command_not_parsed`. Requested access is never labelled a
confirmed read. Post-tool errors are inspected but retain `tool_failed`.

The native PowerShell 5.1 regression starts a child process against an invalid
synthetic credential file and checks the output detector. It does not send a
model request, install hooks or upload data. UTF-8 JSON is accepted; invalid
UTF-8 and UTF-16 input report an encoding coverage gap without guessing a
conversion. Raw commands, paths and output bodies are not persisted.

Native cmd `type`, Git Bash and WSL remain distinct unknown command boundaries.
Although [cmd type](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/type)
and [Get-Content](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.management/get-content)
are documented, a Windows client schema and executor must identify which shell
actually received the command before adding another parser.

## Required before enabling installation

- Inspect the actual installed client's public build/resources and record their
  hashes, exact executable identity and documented configuration location.
- Verify direct `exe` plus arguments where supported, raw JSON stdin encoding,
  async behavior, quiet zero-exit failures and paths with all special characters.
- Exercise an actual new-session callback using only synthetic project data;
  preserve every other setting and test ownership/rollback on that executor.
- Verify a real snapshot independently. If none occurs, keep that channel
  unsupported/unverified while reporting the separately tested tool channel.

State uses the current user's `FOLDERID_LocalAppData` from
[SHGetKnownFolderPath](https://learn.microsoft.com/en-us/windows/win32/api/shlobj_core/nf-shlobj_core-shgetknownfolderpath),
not Roaming or an unchecked environment path. Public file versions use
[GetFileVersionInfoW](https://learn.microsoft.com/en-us/windows/win32/api/winver/nf-winver-getfileversioninfow)
and [VerQueryValueW](https://learn.microsoft.com/en-us/windows/win32/api/winver/nf-winver-verqueryvaluew).
