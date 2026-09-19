# Windows release candidates

`.github/workflows/release-windows.yml` is a manually dispatched, **build-only**
workflow. It runs native Go race tests and vet, builds and tests the notification
helper offline, packages the CLI/hidden host/notifier, and independently verifies
the ZIP checksum and every payload hash. Its retained Actions artifact contains
only the Windows ZIP, `SHA256SUMS-windows`, and `build-info-windows.json`.

Select the reviewed branch or commit and a version when dispatching it. The
version is passed as data to the packaging script's strict version validator.
The workflow has only `contents: read`; it has no tag-push trigger, release API
step, or publishing job. Actions artifacts are verification outputs, not a
supported release channel, and may be accessible to repository users. They
expire after 14 days.

Before adding Windows assets to a public release, record successful interactive
real-client delivery for each advertised adapter, visible notification behavior,
clean-user/second-user isolation, install/remove/update and interrupted-update
recovery, startup/session behavior, and at least 24 hours of actual observation.
Headless CI and synthetic subprocess adapters cannot certify these acceptance
conditions. Report unavailable client/schema coverage explicitly; the reviewed
ZCode 3.14 checkpoint format is documented separately in
`windows-zcode-snapshot-contract.md`.

The existing macOS release workflow remains the sole creator of GitHub releases.
A future Windows publishing change must be separately reviewed and gated by the
recorded acceptance evidence. It should verify the target tag and existing
release, then add Windows-specific asset names without replacing macOS assets
or racing another `gh release create`. This workflow intentionally does not
automate that future step or claim that unsigned packages pass SmartScreen.
