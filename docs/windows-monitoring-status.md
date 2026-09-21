# Windows monitoring status

`laodi status` and `laodi incidents --format agent-summary` expose independent
checks under `monitoring`. These checks do not combine into a claim that every
tool action, current session, or upload is covered.

| Field | Meaning |
| --- | --- |
| `background` | `verified_running` requires a recent heartbeat, matching PID creation time and the selected version's executable. Other values mean that identity or freshness could not be verified. |
| `clients[].contract` | Rechecks the exact reviewed program bytes recorded at installation. Changed or unknown programs never inherit approval from an earlier receipt. |
| `clients[].hooks` | Checks owned Hook entries and whether configuration enables them. Configuration alone does not prove that a running client has reloaded it. |
| `clients[].callback` | Historical, best-effort metadata from observer invocations. A caller is not authenticated; a synthetic invocation also appears here. Check a fresh client session separately. |
| `notifications` | Separates application preference, a read-only Windows authorization query, and visibility. Visibility remains `unconfirmed`; this command sends nothing. |

Normal callbacks, including complex commands the parser cannot understand, can
now leave coverage metadata even when they produce no risk event. Closed
categories distinguish parser limits (`shell_command_not_parsed`, unsupported
tools/events) from incomplete inspection (`inspection_incomplete`). These
samples do not generate additional incidents or notifications. Existing risk
and malformed-input handling is unchanged.

Metadata contains category names and UTC timestamps only. It contains no command,
path, tool output, credential, session ID, or invocation count. Each category is
updated at most once per minute, in a private directory separate from the risk
queue. Sampling can be missed under contention or filesystem failure. Missing or
old samples therefore mean delivery is unverified, not that the client is safe,
inactive, or has never called the observer. Old samples are retained across
updates and do not attest to the current client version or session.

Existing installations start collecting this metadata only after they run the
new observer. Existing risk-event source counts retain their original meaning;
zero events cannot be used to count installed or working integrations.

## Startup recovery boundary

Startup fallback still starts the monitor at login and supports explicit
installation repair. Its protocol-1 stable launcher does not automatically
restart a crashed child. A safe supervisor needs a separately reviewed migration
that preserves user stop/disable intent, ownership, bounded retry and uninstall
behavior. The Task Scheduler backend retains its existing bounded failure retry.

The native Startup regression runs a real stable launcher and versioned monitor
inside synthetic directories. It checks child crashes, repeated installation,
explicit recovery, version switching and removal. It does not verify a user's
actual logout/login or explain an unrelated machine's scheduler access denial.

## Installation permission diagnostics

New private files and temporary Startup shortcuts explicitly receive the
current user's ownership and protected permissions when created. This avoids
accidentally inheriting an administrative token's default owner; installation
does not change the token, repair existing foreign files, or relax state checks.

Hook configuration rejection reports fixed check-stage, principal-role,
permission and inheritance categories without account names, SIDs or settings
contents. Ordinary deny entries and ineffective inherit-only entries do not
grant access and are no longer rejected as writable. Other principals' effective
write permissions, including writes inherited by future hook files, remain
rejected. Send the concise diagnostic for an affected machine before deciding
whether a configuration-specific repair is appropriate; do not recursively
rewrite a user profile or client directory's permissions.
