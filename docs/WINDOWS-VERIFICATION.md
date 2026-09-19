# Windows development verification

Date: 2026-09-19–20 (UTC+8). Baseline: `main` / `v0.3.0-preview.2`, commit
`3f0e33e6844a7bbaad55febe19c5d36ee6beb259`. This is a development implementation,
**not a declaration that Windows release acceptance is complete**. The private
handoff, account configuration, credentials, original logs and user paths are
not included in this repository.

## Platform and support boundary

| Platform/channel | Result | Limit |
|---|---|---|
| Windows 11 x64, 10.0.26200, ordinary interactive user | Native core, task, local install/update/remove exercised | One existing user environment, not a clean second account |
| Go 1.27.1 windows/amd64 | Native tests and vet; existing MinGW GCC for race tests | Build tools only; not required by installed Go executables |
| Local fixed NTFS/ReFS | Backend explicitly requires local fixed filesystem and restricted DACL | This host exercised NTFS; ReFS has no physical-volume acceptance |
| Windows 10, ARM64, WSL, Git Bash | Not supported/verified by this milestone | Not inferred from a successful x64 build |
| macOS | Existing backend retained; Darwin arm64 test binaries cross-compile and vet passes | macOS CI execution is still required; cross-compilation is not native regression |
| Real Windows client callback | Unverified; automatic Hook installation refuses before reading settings | No matching standard/PATH client found; nonstandard installation requires its actual path |
| Real background snapshot | Not triggered / not verified | No model request or global configuration capture induced |

The exact client identity and executor boundary are in
[WINDOWS-CLIENT-CONTRACT.md](WINDOWS-CLIENT-CONTRACT.md). Windows does not reuse
the macOS known-build identity. Explicit `--build` use in these tests identifies
synthetic evidence only. Monitoring does not block uploads; memory-only sends
remain outside coverage.

## Implemented behavior

- `FOLDERID_LocalAppData` chooses the default state root. State, inbox, receipts
  and staging use current-user/SYSTEM DACLs; parent user-directory ACLs are not
  modified. Native handles check file identity, links and reparse attributes.
- `LockFileEx` provides a real nonblocking process lock. Separate distribution,
  service and Hook management locks preserve the existing lock order. Queue
  capacity, per-entry bounds and approximately 100 ms contention budget remain.
- A per-installation, per-user scheduled task uses an interactive token and
  least privilege. Battery/idle settings, unlimited execution time, IgnoreNew
  and one-minute failure backoff are explicit. Complete owned task XML and
  receipts must match before management; no passwords are stored.
- A private native event requests graceful stop. PID creation time and executable
  file identity are checked before signalling. The monitor writes stopped state;
  no executable-name kill, Agent restart or forced termination is used.
- Immutable `versions/<tag>` payloads and an authenticated-by-local-receipt
  current-version record route stable entry points. This is local integrity,
  not cryptographic publisher authentication. Short-lived old children and old
  version directories may remain; active executables are not overwritten.
- Upgrade writes recovery material before stopping the monitor, switches the
  small record with native same-volume replacement, checks a fresh heartbeat,
  and restores the old selection on failure. Schema 1 and existing queue/state
  remain compatible. Launcher protocol 1 deliberately defers stable-launcher
  replacement; a future launcher update requires a separate migration design.
- Recovery holds the service/Hook management locks and checks task ownership
  before stopping anything. Health checks bind the heartbeat to the selected
  process's creation time, including a same-version restart. An unchanged live
  installation accepts its recent heartbeat without forcing a restart.
- Native state publication retries transient sharing/access-denied errors for
  at most 300 ms. A retained reader regression checks eventual publication; a
  persistent sharing conflict still fails explicitly and preserves old state.
- Hook routing errors are silent and fail open without executing an unverified
  fallback. Interactive commands still report invalid installation records.
- `install.ps1` and native `laodi update --version TAG` use official HTTPS release
  hosts, bounded transfers, strict tags and SHA-256. ZIP validation precedes
  extraction and rejects aliases, escapes, links, duplicate/case-colliding names
  and oversized payloads. Checksums and package from one origin prove
  consistency, not a publisher signature.

The root README and its macOS installation command remain unchanged. There is
no public Windows release URL advertised as usable yet. No Authenticode signing
is applied. No Defender, SmartScreen, TLS or execution-policy setting is changed.

## Expected, actual, evidence and limits

| Case ID | Expected | Actual/evidence | Limit |
|---|---|---|---|
| WIN-BASELINE | Identify native blockers | Clean baseline native tests fail on Unix permissions/locks, shell helpers and unprivileged symlink creation | Separate baseline checkout; failures were not hidden |
| WIN-CORE | Private state and one writer | Native store/inbox/DACL/ADS/hardlink/junction/case-alias tests pass, including child exit lock release | No second-user impersonation, disk-full or physical power-loss test |
| WIN-TASK | Least-privilege task starts and owns only itself | Native create/query/delete and full install/watch/graceful-stop/remove tests pass; edited/foreign task tests refuse changes | Policy denial is injected; sleep/logoff/battery/multi-user lifecycle not physically exercised |
| WIN-INSTALL | One-command local package install | PowerShell bootstrap verifies local package hash and installs into isolated state; hidden task publishes a heartbeat | Local development package, not an official hosted release or clean-user test |
| WIN-UPGRADE | A→B and B→B | `v0.4.0-windows.test1`→`test2` selects B; B→B keeps PID and process creation time | Development builds; no client configuration installed |
| WIN-ROLLBACK | Failed B retains usable A | Deliberately exiting synthetic version host causes health failure; exit 1, old version/heartbeat restored, journal cleared | Process failure, not actual machine power loss |
| WIN-RECOVERY | Journal survives interrupted stages | Synthetic journal-published/pointer-switched/new-started cases recover before retry; removal cannot erase pending recovery | Boundary-state reconstruction, not crash injection at every filesystem instruction |
| WIN-ARCHIVE | Reject hostile archives before running code | Go archive tests plus 11 PowerShell bootstrap cases reject traversal, ADS, device/alias names, duplicate/case collisions and wrong checksum | Expanded-size limits also enforced; not a general ZIP implementation |
| WIN-REMOVE | Remove only owned integration, preserve history | Native task/receipt removed; process receipt gone; saved state stopped and history retained | Executables/immutable versions retained for safe later cleanup |
| WIN-PRIVACY | No raw credential output persisted | Sequential synthetic replay verifies redacted state and summaries | Synthetic invalid credential only; no real API key used |

Final instrumented run: `go test -race -json ./...` passed **306 tests/subtests**
with **82 skips** and no failures. Skips are reported, not counted as passes:
macOS distribution/service/configuration mechanisms, Unix permission semantics,
and two opt-in native Task Scheduler integration cases. Native `go vet ./...`
passes. Darwin arm64 vet and both test binaries cross-compile; no macOS native
test was run on this Windows host.

The separate uninstrumented final run enabled both native Task Scheduler cases
with the final hidden monitor executable: **308 tests/subtests passed, 80
skipped, zero failures**. The two counts describe overlapping suites, not 614
independent cases. The PowerShell bootstrap also rejected all 11 hostile inputs
against the final `dev8` package. The Skill's PowerShell command discovery was
checked against that executable; frontmatter and reference links were reviewed
manually because the bundled validator lacked PyYAML. No dependency was added.

The final local PowerShell package sequence was `v0.4.0-windows.dev7` first
install, `dev7` to `dev8` upgrade, then `dev8` reinstall. It ran from
`2026-09-19T16:16:33Z` to `16:17:11Z`. Reinstall preserved PID and creation time;
a deliberately exiting new host returned exit 1 and restored the selected
`dev8` monitor with a fresh heartbeat. Exact task/process receipts were removed
afterward and stopped historical state remained. Earlier native coverage also
verified reinstalling a newer version after removing its owned background task.

## Classification and tool replay

Native Windows PowerShell 5.1 runs `scripts/dev/verify-windows.ps1`. Final
sequential replay completed at `2026-09-19T16:20:24Z`, using development binary
`v0.4.0-windows.dev8`, SHA-256
`5b854d51d21e3173bada618d6e92caa0935c2585a2dbd7b4b1efd97ef12a2a6f`.
The ten independent expected event records produced **TP=10, FN=0, FP=0**;
five explicit negative assertions produced **TN=5**. These are separate raw
counts, not an overall accuracy or all-client detection-rate claim.

The replay checks Git-object vs ordinary-workspace classification, main vs extra
manifest association, attempts vs acceptance, large failure counts not becoming
attempts, sensitive access vs invalid credential output, failed tool output,
negative outputs, privacy redaction and restart deduplication (zero duplicate
events). First observable and first observed persistence times are recorded per
case in the local evidence. This does not reproduce a host's real upload.

An additional **32-process simultaneous Hook burst** saved 11 credential event
records and lost 21 in the first measurement. The final run used a stdin release
barrier and saved 4, losing 28 to bounded contention:
**TP=4, FN=28, FP=0; TN not applicable**. The sticky `hook_inbox_gap_recorded`
diagnostic persisted correctly. Gap visibility passes its safety contract;
individual delivery is not lossless. Sequential and burst denominators must not
be merged or the losses omitted. This is a release/performance limitation to
evaluate with real client traffic.

Synthetic Git init/add/commit/status/diff/log all exit 0; subsequent HEAD and
index checks match. Observed process-inclusive durations were approximately
47–85 ms in the final run. There is no unmonitored baseline comparison, so these values do not
measure added monitor overhead. No running Agent was restarted.

## Resources

Development binary SHA-256 `6e1a96b080f528ac7fe9a90d103f55b6cf526136a702b8aa1adef56050df7422`
was sampled natively with `Get-Process` for about 22 seconds per fixture.
Values cover the monitor PID only; Hook/notifier bursts
and the launcher parent are not included in this table.

| Fixture | CPU seconds | Maximum sampled working set | Maximum sampled private bytes | Maximum sampled handles |
|---|---:|---:|---:|---:|
| Empty evidence root | 0.1875 | 15,773,696 | 50,343,936 | 266 |
| 100 manifests | 0.375 | 20,062,208 | 54,358,016 | 296 |
| One manifest, 42,411 entries | 0.25 | 20,131,840 | 54,804,480 | 290 |

The separate final 32-Hook burst observed 33 total processes including the
monitor, 491,216,896 bytes summed working set, 1,624,043,520 bytes summed private
memory, 7,561 handles and 2.109375 cumulative Hook CPU seconds. These sampled
totals demonstrate why a per-process bound is not a bound on arbitrary client
concurrency. No notification process ran in this resource measurement.

Samples can miss transient peaks. Startup is included; CPU seconds are cumulative
process time, not a percentage of total system capacity. These short observations
do not prove a memory ceiling or 24-hour stability. `measure-windows.ps1` accepts
`-DurationSeconds 86400`, writes bounded progress samples and records scheduling
gaps; sleep/resume is never inferred from elapsed duration alone.

The final 24-hour run started at `2026-09-19T16:17:27Z` (2026-09-20 00:17 UTC+8),
using `v0.4.0-windows.dev8`, SHA-256
`5b854d51d21e3173bada618d6e92caa0935c2585a2dbd7b4b1efd97ef12a2a6f`, with 100
synthetic manifests. It is **still running, not passed** at this report's commit.
Sampler/monitor identity, unchanged hash, progressing samples and empty error
output were checked. A local follow-up checks completion/failure and will update
this report. Do not infer sleep/resume acceptance from that future duration.

Earlier interrupted runs do not count toward 24 hours. One failed before monitor
startup; one dev5 run was deliberately stopped after about 704.6 seconds to
switch to final code. Its original harness had omitted the duration condition;
that result was invalidated and retained locally with the correction. The fixed
harness requires the monitor's own start-to-exit lifetime and observed wall time
to meet the requested duration. A 5-second run passed at 5.118 seconds, while an
otherwise healthy, exit-0 early stop at 1.182 seconds correctly failed.

## Notification and remaining release gates

The helper uses the system .NET Framework compiler/runtime and WinRT metadata;
there are no NuGet or other third-party dependencies. It is a short-lived GUI
subsystem executable, with stable AUMID, current-user shortcut/COM activation and
the existing Laodi logo. See [its platform documentation](../platform/windows/notifier/README.md).

Initial exact registration/query/removal works. The first synthetic notification
attempt on this host failed **before `Show`**, at `CreateToastNotifier`, with
`0x80070490`; it was not submitted to the OS and is not a visible-notification
success. Later creation became available while the settings query still returned
the same error. Automatic approval review then rejected the scheduled synthetic
`Show` action with only `blocked by policy`; it was not executed or retried through
another route. Test registrations and tasks were removed with ownership checks.
The helper remains experimental and is not connected by the Windows installer.
API acceptance, notification settings, Do Not Disturb and human-visible
presentation remain separate facts. No permission prompt is expected on Windows.

Outstanding gates include a real supported client's new-session callback,
visible notification confirmation, clean second-user DACL/install acceptance,
sleep/battery/logoff/multi-user tests, 24-hour completion, macOS native CI,
official release publication, full transfer-failure testing and every-stage
crash/disk-full recovery. The current branch must not be labelled fully accepted
Windows support until those gates have evidence. Global configuration testing
remains unperformed and requires an independent logged-in test user.

## Reproduction

Run the native Go tests and vet, then build a development package:

```powershell
go test ./...
go vet ./...
# Requires a native C compiler for the race detector, only on developer/CI hosts.
go test -race ./...
./scripts/release/build-windows.ps1 -Version v0.4.0-windows.test
```

Use a separate state directory for synthetic workflows. Do not point a fixture
writer at actual client checkpoints. Generated packages live under ignored
`dist/`; test outputs belong outside the repository or in ignored local storage.
Review staged changes for user paths/configuration before publication.
