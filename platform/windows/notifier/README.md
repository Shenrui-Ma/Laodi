# Windows notification helper

`build.ps1` produces the x64, windowless `LaodiNotify.exe` and copies the existing
`assets/laodi-logo.png`. It uses the Windows 11 system .NET Framework compiler,
Windows Runtime metadata, and inbox .NET Framework 4.8 at runtime. No NuGet
package, Windows App SDK runtime, SDK download, or user-installed runtime is
required. The generated ICO is a build intermediate and is removed. Original
code is MIT; the existing logo retains its separate asset terms.

## Identity and update routing

The implementation uses the documented unpackaged desktop **COM activation**
route. One current-user Start-menu shortcut declares AUMID `dev.laodi.guardian`
and activator CLSID `{f5f9e6b4-e191-4d36-8f66-6423d1382e71}`. Exact owned HKCU
AppUserModelId metadata names the logo and activator; the matching CLSID
LocalServer32 invokes the stable `laodi-host.exe --notification-activate`.
The shortcut invokes `laodi-host.exe --notification-open`. No stub CLSID,
protocol handler, service, elevation, or user password is used.

The stable host verifies the protected current-version record and payload,
then delegates to that version's helper. All helper invocations include
`--state-dir INSTALL_ROOT`. `--notification-activate` maps to `--com-server`;
`--notification-open` maps to `--activate status`. Monitor notification requests
use stable-host `--send --id EVENT_ID --kind KIND`; they route through the same
version selection. Only fixed notification kinds and bounded opaque IDs are
accepted. Source paths, credentials and caller-supplied notification copy are
never accepted or included in a toast.

Receipt schema 2 binds the stable host, stable PNG and their hashes, generated
ICO hash, complete shortcut hash, AUMID and exact COM registration. It does not
bind the versioned helper path. Therefore A→B changes the helper selected by the
protected version record without rewriting notification identity, modifying
Agent configuration or replacing a running helper. A rollback selects A again.
Changing stable host/artwork requires an explicit future launcher migration.

A current-user private mutex serializes registration management. A protected
pending receipt is written before publishing the icon, shortcut or registry
values. Interrupted registration/removal can resume by checking all surviving
artifacts against that receipt. Modified or foreign entries are retained and
reported; the helper does not delete foreign registry siblings or shortcut
content. Default Windows uninstall removes only verified notification metadata.

Handle-based file paths and the final native registry-key names are checked
before registration is committed. A packaged caller can redirect AppData or
new HKCU writes despite reporting no process package identity. Redirected
registrations are rejected and exact pending artifacts rolled back; use an
ordinary Windows terminal for installation. Existing parent-key visibility
alone does not establish that newly written COM registration is external.

See Microsoft's [desktop COM notification sample](https://github.com/WindowsNotifications/desktop-toasts/blob/master/CPP-WINRT/DesktopToastsCppWinRtApp/DesktopNotificationManagerCompat.cpp)
and [ToastActivatorCLSID property](https://learn.microsoft.com/en-us/windows/win32/properties/props-system-appusermodel-toastactivatorclsid).
New Windows App SDK applications use a newer NuGet API; this project retains
the inbox desktop route to meet its no-additional-dependency requirement.

## Protocol and status

Supported actions are `--register`, `--unregister`, `--status`,
`--send --id EVENT_ID --kind KIND`, `--test-template`, `--test-activation`, `--com-server`, and
`--activate status`. JSON schema 1 separates registration integrity, OS
notification setting, API delivery, and human visibility. Go enforces a 16 KiB
output bound, hidden child processes and deadlines. API failure never loses the
monitor's separately persisted event.

`--status` is read-only. A valid receipt gives `registration: verified`; an
unavailable Windows settings query gives `authorization: unknown` plus the
exact `authorization_error_code`. This is neither notification denial nor proof
that notification delivery works. Known app/user/policy/manifest disabling is
reported distinctly using the [ToastNotifier.Setting contract](https://learn.microsoft.com/en-us/uwp/api/windows.ui.notifications.toastnotifier.setting).
WinRT can project a failing HRESULT as `System.Exception`, not only
`COMException`; both representations preserve the failure evidence.

`--send` reports `accepted_by_os` only after `Show` returns successfully. A known
disabled setting prevents a send; an unknown setting remains unknown while
`Show` determines API acceptance or failure. Human visibility is always
`unconfirmed`; Focus Assist / Do Not Disturb is not inferred from acceptance.
No permission dialog or notification-setting change is performed. Settings URI:
`ms-settings:notifications`.

## Verification

`build.ps1 -Test` runs offline protocol assertions with fake platform
delegates: thirteen Chinese templates, fixed activation/logo, input limits,
disabled states, failed status queries, failed Show semantics and COM interface
contracts. It performs no registration changes and no notification API calls.
`test-registration.ps1`, run from an ordinary Windows terminal, performs explicitly scoped native registration tests:
versioned helpers A→B preserve the receipt; modified icon/registry values are
refused; partial registration and removal recover; exact cleanup succeeds.
It never invokes `--send` or creates a scheduled task.

`test-activation.ps1 -StableLauncher INSTALL_ROOT\laodi-host.exe -OutputPath REPORT`
requires an already registered installation. It invokes `--test-activation`,
which verifies registration, creates only the owned local COM class and calls
its fixed callback. It checks that the selected version helper was newly
started and the receipt stayed unchanged. It creates no ToastNotifier and
calls no notification API. The COM server exits after its bounded 15-second
lifetime, preserving enough time for RPC to return. This checks executable
routing and the callback interface; it does not verify a human toast click.

Release acceptance remains separate from these offline tests. A clean user environment, interactive client callbacks, visible notifications, startup/session behavior and long-running observation must be recorded before Windows release publication. See [Windows status](../../../docs/WINDOWS.md).
