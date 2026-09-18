# macOS notification helper

Small, no-window `LSUIElement` bundle using Apple's UserNotifications framework. It accepts a fixed event kind and opaque ID, never raw project names, paths, log text or notification bodies. It has no network client and does not interact with Agent processes.

Build from the repository root:

```sh
sh platform/macos/notifier/build.sh
```

Requires the macOS SDK/clang from Xcode Command Line Tools. No package dependency is added. Output defaults to the ignored `platform/macos/notifier/build/LaodiNotify.app`; an alternative output directory may be passed as the first argument. Building does not install, register a background service, request permission or send a notification. The development bundle identifier `local.laodi.notify` is not a release signing identity.

Only explicitly run these after reviewing their effects:

```text
LaodiNotify.app/Contents/MacOS/LaodiNotify --status
LaodiNotify.app/Contents/MacOS/LaodiNotify --request-permission
LaodiNotify.app/Contents/MacOS/LaodiNotify --send --id incident_123 --kind snapshot-history
```

`--status` queries without prompting. `--request-permission` is the sole permission-request path and may show a macOS dialog. `--send` never requests permission; it requires existing authorization. It sends no sound, uses no critical/time-sensitive notification and cannot bypass Focus. Never automatically run either side-effecting command from a build, query, doctor check or test suite.

The output schema and required end-to-end validation are documented in [NOTIFICATIONS.md](../../../docs/NOTIFICATIONS.md). Compilation is not proof of notification delivery or permission UX. Release requires a stable bundle location and identity, proper signing/distribution, and real authorization/delivery tests. This task does not perform those actions.
