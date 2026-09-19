# Windows notification helper

`build.ps1` builds an x64, windowless helper from the Windows 11 system .NET
Framework compiler and Windows Runtime metadata. There are no NuGet packages,
SDK downloads, or user-installed runtimes. This implementation therefore has a
system .NET Framework 4.8 dependency; it is not a C++ Windows SDK binary.

The selected unpackaged-app route is Microsoft's **COM activation** route:
one current-user Start-menu shortcut declares a stable AUMID and activator
CLSID. Owned HKCU AppUserModelId metadata provides the display name, logo and
activator; the matching CLSID LocalServer32 starts this helper's short-lived
`INotificationActivationCallback` server. ToastGeneric carries a fixed status
activation. Activation acknowledges the notification and leaves incidents
available through `laodi incidents`; it does not start a desktop framework or
alter an Agent. There is no protocol handler or stub CLSID. See [Microsoft's
desktop notification sample](https://github.com/WindowsNotifications/desktop-toasts/blob/master/CPP-WINRT/DesktopToastsCppWinRtApp/DesktopNotificationManagerCompat.cpp)
and [shortcut properties](https://learn.microsoft.com/en-us/windows/win32/properties/props-system-appusermodel-toastactivatorclsid).

This helper is **experimental and opt-in**. It is not connected automatically
by the Windows installer, and packaging it does not establish notification
support. To test explicitly, place `LaodiNotify.exe` and the unchanged `assets/laodi-logo.png` into the
protected, stable installation root. Run `--register` there once. Repeated
registration verifies the exact shortcut hash and owned registry configuration;
`--unregister` refuses to delete user-edited registrations. The helper uses a
private sidecar ownership receipt. Its stable AUMID is `dev.laodi.guardian`.

`--status`, `--send --id EVENT_ID --kind KIND`, `--register`, and `--unregister`
write one bounded JSON result to stdout. `--send` reports `accepted_by_os` only
after `Show` succeeds. OS notification settings distinguish enabled, app/user
disabled, policy disabled and other disabled states. Focus Assist / Do Not
Disturb and human visibility remain explicitly unknown. There is no permission
dialog and no claim that an accepted toast was seen.

The helper accepts fixed templates only, never arbitrary notification text,
paths, credential content or project names. `--test-template` validates the
synthetic Chinese template without sending a notification. Sending an actual
synthetic notification requires the explicit `--send` command and one human
visibility check. Windows 10, ARM64, activation from other accounts, and policy
variations are not yet verified.

On the development Windows 11 machine, native build, private registration,
idempotence, shortcut identity, refusal to remove edited shortcut/registry
configuration and exact cleanup passed. One authorized synthetic send at
2026-09-19 15:46:40 UTC failed in `CreateToastNotifier` with `0x80070490` before
`Show`. A subsequent ordinary-user scheduled diagnostic successfully registered
the identity and constructed a notifier, but its `Setting` query returned the
same HRESULT. Both direct and scheduled helpers reported no package identity.
The scheduled-context attempt to complete the one synthetic `Show` was then
rejected by automatic execution policy before running; it was not retried.
All synthetic registration and diagnostic tasks were removed with exact
ownership checks. No `Show` was completed and no human visibility confirmation
was requested. Visible notifications, COM activation, disabled settings and
Focus Assist behavior remain **unverified**.
