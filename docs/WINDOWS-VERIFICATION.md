# Windows development verification

Date: 2026-09-19–20 (UTC+8). Baseline: `main` / `v0.3.0-preview.2`, commit
`3f0e33e6844a7bbaad55febe19c5d36ee6beb259`. Final local development package:
`v0.4.0-windows.dev12`. This is implementation and test evidence, **not completed
Windows release acceptance**. The private handoff, user paths, account settings,
original client logs and credentials are not included.

## Platform and coverage

| Platform/channel | Evidence | Boundary |
|---|---|---|
| Windows 11 x64, 10.0.26200, ordinary user, NTFS | Native execution, background task, upgrades and real ZCode callbacks | One existing account; clean second-user acceptance remains open |
| Go 1.27.1 windows/amd64 | Native race tests and vet | Go and MinGW are development tools, not product dependencies |
| ZCode 3.14.0.7681 / GLM-5.3-Flash | Logged-in new-session Read callbacks produced two expected findings | No claim for every tool, model or client version |
| Claude Code 2.1.278 | Unmodified public executable, eight callbacks with a loopback model fixture | No authenticated production-model session |
| Windows ZCode background snapshots | Current 3.14 producer has an unsupported Git-checkpoint schema | No verified Windows upload parser/root; not merely waiting for an upload trigger |
| macOS | Backend preserved; Darwin arm64 vet and both test binaries cross-compile | Native macOS CI still required |
| ReFS, Windows 10, ARM64, WSL, Git Bash | No physical acceptance | Not inferred from Windows 11 x64 results |

Exact client hashes and executor contracts are in
[WINDOWS-CLIENT-CONTRACT.md](WINDOWS-CLIENT-CONTRACT.md). The independent snapshot
review is in [windows-zcode-snapshot-contract.md](windows-zcode-snapshot-contract.md).
The known macOS upload format is used only under explicit isolated synthetic test
roots. Monitoring does not block uploads; memory-only sends remain outside coverage.

PowerShell `Get-Content` has a verified subset. ZCode's Windows `Bash` event does
not carry its actual shell dialect, while the client can select CMD or Git Bash.
Its Hook shell is only the callback executor. Therefore bare `type .env` cannot
reliably be classified as a CMD read request: in Bash it queries a command.
CMD/Git Bash/WSL request parsing remains unsupported; output detection is separate.
Complex or ambiguous commands are coverage gaps, not asserted file reads.

## Implemented behavior

- Current-user LocalAppData state, inbox, receipts and staging use restricted
  current-user/SYSTEM DACLs. Parent ACLs stay unchanged. Native handles reject
  reparse paths, junctions, hardlinks, ADS and aliases; storage must be local and
  fixed. Native `LockFileEx` enforces one writer and releases on process exit.
- Bounded Hook input, unmodified UTF-8 forwarding, private HMAC identifiers,
  256-entry inbox, 16 KiB entry limit and approximately 100 ms contention bound
  remain. State commits precede ACK. Raw commands, output and credential values
  are not persisted. ZCode uses a silent native `-c` bridge; Claude uses argv.
- Verified client integration preserves foreign JSON fields and ACLs, records
  exact owned entries and rejects user edits. Unknown bytes are not granted a
  known contract by display version. Removal preserves unrelated configuration.
- Per-user/per-installation Task Scheduler registration uses an interactive
  token and least privilege, no password, explicit battery/idle settings,
  unlimited duration, IgnoreNew and one-minute failure backoff. Management
  requires matching XML/receipts. Graceful stop checks PID creation time and
  executable identity before signalling a private native event; no kill by name.
- Immutable version directories and a protected version record route stable
  launchers. Recovery, fresh-heartbeat checks and bounded sharing retries restore
  the old version on failure. State, deduplication, inbox, HMAC, root and
  integration identities survive updates. Healthy same-version installs keep
  the process. Stable-launcher replacement needs a separate protocol migration;
  active executables are not overwritten.
- Packaged notifications have stable AUMID, current-user shortcut/COM registration
  and the existing logo. Enable/disable is read per event without restarting or
  replaying history. Registration follows successful installation health; locked
  preflight rejects dropping a registered helper or changing stable artwork.
  Partial registration and pending upgrade recovery remain explicit and owned.
- `install.ps1` and `laodi update --version TAG` enforce official HTTPS origins,
  time/size bounds, tags, hashes and strict ZIP paths before extraction. No third-
  party runtime is installed; the helper uses system .NET/WinRT. Same-origin
  hashes prove consistency, not publisher signing. Packages remain unsigned
  development artifacts; no official Windows release was published.
- Final-handle path checks reject MSIX-redirected or mixed real/virtual installs.
  Registration verifies newly owned registry keys resolve to the ordinary user
  Classes hive. Host isolation settings are not changed.

Root README, macOS installer, notifier and release workflow are unchanged from
the baseline. Windows has a separate build-only workflow. No Defender,
SmartScreen, execution policy, TLS or client permissions were changed.

## Native validation and installation

The complete integration `go test -race -json ./...` run passed **405 tests/subtests,
88 skipped, 0 failed**. Skips include macOS/Unix cases and opt-in native fixtures;
they are not passes. Native `go vet ./...` passed. Subsequent final notification
and path guards passed focused race regressions and vet. Overlapping suites must
not be summed into independent case counts. Offline helper tests passed 90
protocol assertions using fake delegates and zero notification API calls.

| Case | Result | Limit |
|---|---|---|
| Private state/inbox/file identity | Native DACL/link/alias/reparse/ADS/lock replacement and process-exit tests pass | No second ordinary-user access test |
| Task management and graceful stop | Exact ownership, edited/foreign refusal, stopped-state persistence pass | Sleep/battery/logoff not physically tested |
| Archive/bootstrap | Go archive cases and 11 hostile PowerShell bootstrap inputs rejected | No formal hosted Windows release |
| Transfer failures | Loopback TLS redirect/network/truncation/bounds tests pass | Not an official release download |
| Transaction recovery | Updater child terminated at eight stages; interrupted recovery and sharing-conflict rollback pass | No physical power-loss/full-disk test |
| Notification transaction | Failed health never registers new helper; missing helper/artwork rejected before selection; locks and preference cases pass | Show/visibility separate |
| Removal | Exact owned integrations removed, historical state retained | Immutable executable versions retained for safe later cleanup |

The first apparent default install was redirected by the Codex MSIX host and
invisible to Task Scheduler. It was not a successful ordinary install. Final
validation ran the reviewed local package in the ordinary interactive user
context through exact-owned temporary test tasks. All temporary tasks were
removed only after their XML matched.

The actual default installation passed dev9→dev10→dev11→dev12 plus same-version
reinstalls. Final dev11→dev12→dev12 validation ran from
`2026-09-19T17:42:17Z` to `17:42:43Z`. The two real events, HMAC, configuration,
Hook entries, integration/task receipts, root identity and stable host hashes
were retained. Repeating dev12 preserved PID/creation time. Registered notification
identity survived A→B→B; a real COM callback completed without Show. The formal
monitor remains running. The user's ZCode process was never restarted.

## Actual callbacks and privacy

After model availability/CAPTCHA problems were resolved by the user, a new logged-
in ZCode session used Read on three isolated synthetic files: README, ordinary
text and an `.env` containing an invalid test credential. UI completion and actual
monitor records agree. At `2026-09-19T17:33:58.4900638Z`, exactly two new
`source=zcode` records appeared: `sensitive_tool_access_requested` / `env_file`
and `sensitive_tool_output_detected` / `credential_assignment`, count 1 each.
Notification states were respectively `recorded_only` and `not_configured`.
Diagnostics were empty, dropped events zero, and the queue drained with no gap.
These observations do not establish an overall detection rate.

An ordinary-context read-only privacy check at `17:40:26Z` found no fixed invalid
fixture value in current state or pending queue. Field names matched the closed
schema; Hook metadata held only fixed categories or hashes. Zero pending files
existed at that snapshot; this does not claim inspection of every historical
queue file. No real API key was used or exposed.

Claude's isolated real-executable test observed eight Read/PowerShell callbacks,
including a failed Read, and two expected output findings. UTF-8/special-character
argv survived; a delayed independent probe exiting 7 did not block client results.
Its deterministic model was local, with no login or paid request. An independent
Node test verified ZCode's native shell/GUI host/version routing chain. These
fixtures are distinct from the authenticated ZCode session above.

## Synthetic replay and concurrency

Windows PowerShell 5.1 replay of dev12 completed `2026-09-19T17:40:52Z`.
CLI SHA-256: `5b8d7bf399327327cad362e7d1679049a1a7c981875cfb4305a4db34f6403178`.
Sequential classification: **TP=10, FN=0, FP=0, TN=5** (ten independent expected
events, five negative assertions). Cases distinguish Git/ordinary workspaces,
main/extra stages, attempts/acceptance, high failure counts/attempts, access/output,
failed output and negative hash/UUID/placeholders. Restart duplicates: zero.
Raw credential text was absent and summaries redacted.

A separate 32-process simultaneous stdin-barrier burst produced **TP=5, FN=27,
FP=0; TN not applicable**. The sticky `hook_inbox_gap_recorded` diagnostic persisted.
This meets bounded failure reporting, **not lossless delivery**. The short lock
budget and durable writes remain; concurrency loss is a known performance limit.
Sequential and burst denominators must not be merged. The burst observed up to
33 processes, 343,674,880 bytes summed working set, 1,595,645,952 bytes summed
private memory, 4,934 handles and 1.046875 cumulative Hook CPU seconds; no notifier.

Synthetic Git init/add/commit/status/diff/log returned 0 and preserved HEAD/index.
Durations including startup were about 48–98 ms without an unmonitored baseline;
these are not added-overhead measurements. No fake record was written into real
client checkpoints.

## Resources and long run

Final dev12 monitor-only native samples, approximately 22 seconds each:

| Fixture | CPU seconds | Maximum sampled working set | Maximum sampled private bytes | Maximum sampled handles |
|---|---:|---:|---:|---:|
| Empty evidence root | 0.078125 | 16,306,176 | 51,073,024 | 262 |
| 100 manifests | 0.890625 | 20,774,912 | 55,656,448 | 292 |
| One manifest, 42,411 entries | 0.296875 | 20,180,992 | 54,562,816 | 288 |

Samples may miss peaks. Startup is included; CPU is cumulative process time.
These are not ceilings or 24-hour results and exclude notifier children.

Final dev12 24-hour sampling started `2026-09-19T17:43:21Z`
(2026-09-20 01:43 UTC+8), using the exact hash above and 100 synthetic manifests.
The user subsequently cancelled the 24-hour requirement and requested functional
verification only. The exact synthetic monitor was gracefully stopped and its
follow-up automation paused; the actual installed monitor remains running.
**The long run is cancelled, not passed and not still pending.** Earlier dev5/dev8
short runs also do not establish 24-hour stability. The harness requires both OS
start-to-exit lifetime and wall duration; a healthy early exit fails its original
duration criterion. No sleep/resume result is inferred.

## Notifications and remaining acceptance

Ordinary-context registration and direct COM callback passed. Final registration
is retained. Early `Setting`
queries returned `0x80070490` and correctly reported `authorization: unknown`.
The user's later ordinary-PowerShell manual `notifications test` returned
`delivery: accepted_by_os`, `authorization: enabled`, and verified registration.
The user subsequently found the notification in Notification Center, reported
Do Not Disturb off, and supplied a screenshot of the visible banner after another
manual test. The Laodi icon and Chinese title/body are visibly correct. Both API
acceptance and human-visible delivery have therefore passed for this machine.
The program still reports Focus Assist as unknown; it does not infer this from
API acceptance. After confirmation, `notifications enable` set the preference
to true without sending a test notification. Ordinary-context status showed
enabled/verified; monitor/client identities, registration and both existing events
were unchanged. No historical event was replayed. See
[notification documentation](../platform/windows/notifier/README.md).

An earlier synthetic send failed before Show. Automatic approval review rejected
a later scheduled Show attempt with only `blocked by policy`; it was not executed
or retried through another automated route. The user subsequently ran the final
command manually and supplied the successful API result above. The first manual
command had omitted its arguments and displayed help; that was not a send.
The screenshot/user confirmation provides the visibility evidence separately
from COM/offline tests. Real notification click activation, disabled-mode and
Do Not Disturb suppression behavior remain untested.

Functional delivery does not claim 24-hour stability; that test was explicitly
removed from this task by the user. Other unperformed release acceptance includes
clean ordinary-user install and cross-user DACL isolation, sleep/battery/logoff/
multi-user lifecycle, real task-crash restart, physical disk-full recovery, native
macOS CI and a reviewed formal Windows release. Real Windows snapshot upload
monitoring remains unsupported for inspected 3.14 bytes. Global configuration
testing was not performed; it needs an independent logged-in test account and is
outside current-user synthetic-project scope.

## Reproduction

Run native tests/vet and optionally race tests with a development C compiler, then
`scripts/release/build-windows.ps1 -Version v0.4.0-windows.test`. Use
`scripts/dev/verify-windows.ps1` and `measure-windows.ps1` with isolated test state.
Opt-in exact-client fixtures are documented in the client contract. Outputs belong
in ignored `dist/` or outside the repo. Use ordinary PowerShell for default install;
virtualized MSIX paths are rejected. Do not publish raw local/client evidence.
