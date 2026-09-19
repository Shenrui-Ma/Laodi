# Windows client contracts

Status on 2026-09-20: the following **public Windows binaries and Hook executor contracts are statically verified**. Native configuration merge/removal and synthetic event tests pass. ZCode's logged-in desktop session passed the live Read callback acceptance below. Claude Code's exact public executable passed a separate isolated real-client test using local model fixtures. These results verify the stated callback scopes; configuration success alone is not a delivery test.

| Client | Exact Windows version | Public program SHA-256 | Executor |
|---|---|---|---|
| ZCode x64 | 3.14.0.7681 | `564db1fd7b7c9cb71deca8178f7ffd865ace870fb0240e02c93a3300499118be` | Async command with Laodi as native shell bridge |
| Claude Code x64 | 2.1.278 | `006ea5c8638f67f10a5ae66bb232fd267c9f6af294e3f03f4cfcf1fd3f2cced8` | Async command plus direct `args` |

ZCode also requires `resources/glm/zcode.cjs` SHA-256 `8f5cfccf2a899b92e57bc2a5760b949c1a928f739652fffc9e6d07c24f11ba05`. That public runtime was inspected without reading settings, credentials or session history. Its command callback at character offset 5048538 passes the selected shell to the execution port; shell normalization at 2095178 preserves an explicit executable. [Node's shell contract](https://nodejs.org/api/child_process.html#shell-requirements) invokes non-cmd shells with `-c` and the command string. The async runner does not await the callback or interpret its stdout as a decision.

Laodi therefore sets `shell` to its stable native `laodi-host.exe`, and `command` to a versioned base64url JSON envelope containing only the adapter and state directory. Laodi's `-c` entry accepts that fixed protocol, forwards original JSON stdin to the observer, and exits silently. There is no arbitrary command interpreter, PowerShell wrapper, Git Bash or WSL dependency. Chinese, spaces, apostrophes, `%`, `!`, `&`, `$()` and parentheses stay data. Paths containing vendor `${...}` placeholders are rejected conservatively.

The [ZCode Hook reference](https://zcode.z.ai/en/docs/hooks) documents user settings at `~/.zcode/cli/config.json`, `hooks.enabled`, the three tool events and asynchronous command hooks. Its direct `process` executor is synchronous; Laodi does not mark it async. ZCode matches `Bash|Read`. Current tool execution on Windows may select CMD or Git Bash independently of the Hook shell; shell-read request parsing stays conservative until that tool's dialect is identified.

The exact 3.14 runtime's `Read` metadata retains that tool name. Its protocol `createRecord` creates a new app, runs the configuration loader and synchronously reads the user config again before constructing that session's Hook runner. Existing sessions retain their runner; a genuinely new session does not require restarting ZCode to read the changed configuration.

`TestWindowsZCodeNodeShellStableGUIBridge` independently passed with Node v24.19.0 and a real x64 GUI-subsystem Laodi PE. It reproduces the public runtime's `spawn(command, [], {shell, windowsHide: true, stdio: ['pipe','pipe','pipe']})` call in an isolated path containing Chinese text, spaces, apostrophe, `&`, `%`, `!` and `$()`. The actual stable launcher validates its protected version record and starts the version executable. Node supplies `-c` itself; a native probe verifies both argv shape and a SHA-256 match over the unmodified UTF-8 stdin bytes. The observer emits no stdout/stderr, exits 0 and queues one expected sensitive-access request. A separate delayed native probe exits 7 after the caller has continued. The distribution fixture uses an in-memory task scheduler and explicitly disables client discovery; no real tasks or client settings are changed.

This independent test covers the native Node/GUI executable/routing chain; the logged-in ZCode session has its own evidence below. Reproduce by setting `LAODI_TEST_NODE_EXE` and `LAODI_TEST_HOOK_EXE` (build with `-ldflags '-H=windowsgui'`), then running `go test ./internal/laodi -run '^TestWindowsZCodeNodeShellStableGUIBridge$' -v`. Optional `LAODI_TEST_ZCODE_SHELL_SUMMARY` writes only contract results.

The [Claude Code Hook reference](https://code.claude.com/docs/en/hooks) documents `command` plus `args` as direct executable invocation, async callbacks, `PowerShell` tool input and UTF-8 JSON stdin. The [official changelog](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md) introduced exec-form hooks in 2.1.139. Laodi pins the exact 2.1.278 Windows x64 bytes from the [official release manifest](https://downloads.claude.ai/claude-code-releases/2.1.278/manifest.json), not a version resource alone. Claude settings are `~/.claude/settings.json`; matches are `Bash|PowerShell|Read`. Future/unknown binaries require a new review and remain unsupported.

## ZCode logged-in desktop session

ZCode 3.14.0.7681 with GLM 5.3 Flash completed a genuinely new desktop session that used only `Read` on three isolated synthetic files: README, ordinary text and `.env` containing an explicitly invalid credential fixture. The UI task completion was matched with real callbacks recorded by the installed monitor; this is not based only on the model's final self-report. Earlier backend-busy and CAPTCHA-timeout attempts did not complete the acceptance sequence. The successful run followed manual user recovery, without automating or bypassing the CAPTCHA and without restarting the existing ZCode process.

The read-only summary `work/ordinary-install/live-hook-dev11-summary.json` records two new `zcode` events, both observed at `2026-09-19T17:33:58.4900638Z`:

| Observation | Rule count | Recorded notification status |
|---|---|---|
| `sensitive_tool_access_requested` | `env_file = 1` | `recorded_only` |
| `sensitive_tool_output_detected` | `credential_assignment = 1` | `not_configured` |

Both events have `baseline_existing = false`. Diagnostics are empty, `dropped_events = 0`, the consumed queue has zero files and no gap marker remains. The summary confirms unchanged monitor and client process identities and zero notification API calls during its read-only check. These are two observations of the synthetic `.env` read—one requested access and one credential-like output—not two separate credential leaks. Credential validity and remote delivery remain unknown.

This accepts real ZCode Read-to-Hook-to-monitor delivery for the exact pinned build. It does not establish visible Windows notification delivery: the recorded statuses above explicitly show recording only or no configured notifier. It also does not validate the unsupported Windows snapshot producer, remote upload, or other shell/tool dialects.

## Claude real executable with a local model fixture

`TestClaudeWindowsLoopbackCallbacks` launches that unmodified public Claude 2.1.278 executable with an isolated temporary HOME, USERPROFILE, CLAUDE_CONFIG_DIR and project. It uses restricted mode, explicit settings, no session persistence, an invalid synthetic API key and a deterministic HTTP server bound to loopback. Model responses come from this local fixture; there is no Claude login, paid model request or user-profile configuration read. Update/telemetry traffic is disabled and external HTTP proxy requests are rejected. The observed run made five loopback API requests, returned four tool results and made zero requests to the rejecting proxy. This is a real client's tool and Hook execution test, not an authenticated production-model session test.

The client actually ran three `Read` calls (synthetic credential text, a missing file and ordinary text) and one PowerShell `Get-Content -LiteralPath '中文.env'` call. It emitted eight raw UTF-8 callbacks: three PreToolUse Read, one PreToolUse PowerShell, two PostToolUse Read, one PostToolUse PowerShell and one PostToolUseFailure Read. The production observer recorded exactly **two sensitive-output findings**, one for each successful read of the invalid credential fixture. The ordinary read and missing-file failure produced no credential-output finding. These counts describe tool observations; they are not five findings, credential validity or evidence of remote upload.

Both the observer executable and an independent native callback probe were invoked through direct argv from a path containing Chinese text, spaces, an apostrophe, `&`, `%`, `!` and `$()`. Callback JSON retained Chinese characters. The probe delayed 900 ms then exited 7; the matching tool result arrived before the probe completed and the client still completed with exit 0. This verifies async fail-open behavior. The PowerShell command used a relative literal filename within that special-character working directory, not an absolute PowerShell command containing every metacharacter.

This test exposed and fixed a real observer defect: resolving the default user state before parsing an explicit `--state-dir` silently discarded callbacks under a restricted profile. The same real-client fixture produced no observer findings before the fix and the two expected findings after it. A focused native/race regression also removes HOME and USERPROFILE entirely. Installed callbacks now need only their explicit private state path.

Reproduction is opt-in: set `LAODI_TEST_CLAUDE_EXE` to the reviewed public binary, `LAODI_TEST_HOOK_EXE` to a newly built native Laodi binary, and `LAODI_TEST_CLAUDE_MOCK=1`; run `go test ./internal/laodi -run '^TestClaudeWindowsLoopbackCallbacks$' -v`. Optional `LAODI_TEST_CLAUDE_SUMMARY` writes only the counts and contract results. The harness persists no raw callback body or model request.

## Discovery and installation

`laodi hooks discover` inspects bounded standard LocalAppData/Program Files candidates, exact `zcode.exe`/`claude.exe` PATH entries and public image paths of matching running process names. It never reads command lines, process environments, sessions or checkpoints. Explicit `--client-exe` supports nonstandard locations and downloaded public test fixtures; fixtures are never added automatically. The output contains public executable paths, hashes and version metadata; discovery itself does not perform a live delivery test. Stopped nonstandard installations, scripts, Store packages and WSL are not inferred.

`install --client-exe PATH` (PowerShell bootstrap `-ClientExecutable`) uses a recognized contract only. Without an explicit path it selects discovered reviewed clients, rejecting ambiguous identities. Windows distribution installs hooks only after the owned monitor is healthy. The private `windows-integration.json` records the selected client contracts and home directory; commands refer to the stable launcher across upgrades. Upgrades preflight exact owned entries and refuse user edits. Direct `hooks install` also selects that stable host when the state directory contains a verified distribution.

Configuration files are bounded JSON with duplicate-key rejection. Native file handles reject reparse points, hardlinks and other-user write permissions. Existing client ACLs and foreign JSON fields are preserved; [ReplaceFileW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-replacefilew) preserves the target DACL instead of replacing it with Laodi's private ACL. State and receipts retain current-user/SYSTEM-only ACLs. An ownership receipt records only Laodi's entry, never a copy of other settings. Removal reconstructs its plan from that protected receipt even when the client or old executable is gone, removes only the exact owned entries, and retains unrelated configuration. Interrupted multi-file operations remain explicit and retain ownership evidence for inspection; they are not a cross-file transaction.

## Observation boundary

PowerShell read requests accept direct `Get-Content` (or its full module name), one path and a small full-name option set. Literal paths handle Chinese and shell metacharacters. Expansion, pipelines, scripts, aliases, providers, alternate streams and ambiguous options remain `shell_command_not_parsed`. Requested access is never labelled a confirmed read. Tool failures retain their failure status. Invalid UTF-8 and UTF-16 produce an encoding coverage gap; raw input, commands, paths and output bodies are not persisted.

Windows snapshot coverage is independent. The reviewed ZCode 3.14 resources use a different checkpoint producer from the macOS 3.12.3 upload format, so the legacy snapshot parser remains unsupported; see [the Windows snapshot contract](windows-zcode-snapshot-contract.md). A working Hook does not validate snapshots, remote upload, server retention or notification delivery.

The macOS installation command, event matcher and file backend are preserved. Native Windows tests use isolated private homes and synthetic content. Optional exact-binary tests require `LAODI_TEST_ZCODE_EXE` and never select a real user profile as their configuration home.
