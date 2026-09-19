# Native Windows development harness

These scripts run native Windows executables from Windows PowerShell 5.1 or
PowerShell 7. They need a built development binary; the Git workflow also needs
Git for Windows. These are developer checks, not user runtime dependencies.
They use no Python, WSL, Git Bash, downloaded modules or policy bypass.

```powershell
powershell.exe -NoProfile -File scripts/dev/verify-windows.ps1 `
  -Binary bin/laodi.exe -OutputPath ../windows-replay.json

powershell.exe -NoProfile -File scripts/dev/measure-windows.ps1 `
  -Binary bin/laodi.exe -OutputPath ../windows-idle.json `
  -ManifestCount 0 -DurationSeconds 22

powershell.exe -NoProfile -File scripts/dev/measure-windows.ps1 `
  -Binary bin/laodi.exe -OutputPath ../windows-100-manifests.json `
  -ManifestCount 100 -FilesPerManifest 1 -DurationSeconds 22

powershell.exe -NoProfile -File scripts/dev/measure-windows.ps1 `
  -Binary bin/laodi.exe -OutputPath ../windows-large-manifest.json `
  -ManifestCount 1 -FilesPerManifest 42411 -DurationSeconds 22
```

Use `-Git` to select an explicit Git executable if its installation differs
from the default. Synthetic workspaces are fresh private temporary directories
whose ACL grants the current user and SYSTEM access. They are retained locally
for inspection; no real client configuration or evidence root is used. Reports
contain measurements and classifications, not raw source paths or credentials.

The replay checks separate Git/ordinary/extra-manifest stages, a large failure
count with no attempt, silent raw UTF-8 hook stdin, positive and negative tool
fixtures, native concurrent hook processes, normal Git work, unchanged HEAD and
index, graceful exit and restart deduplication. A concurrency burst can exceed
the existing approximately 100 ms lock budget. The report separates delivered
records (TP), missing records (FN), visible gap validation and lossless delivery.
Passing the safety check never turns a gap into a detected credential event.

Resource sampling covers CPU seconds, working set, private bytes, handles and
process count. The replay additionally samples the entire concurrent Hook batch
and monitor and sums child CPU time. The resource-only probe has no Hook or
notification children. Periodic samples can miss transient peaks. Git timing
includes process startup and is not a controlled overhead benchmark.

## A 24-hour run

Use the same resource command with `-DurationSeconds 86400`. Choose an immutable
copy of the final binary before launching. For a hidden background run use
PowerShell `Start-Process -WindowStyle Hidden`, with exact quoted paths for the
PowerShell script, binary and report. Do not use a client process as the target.

`-ProgressPath` overrides the default `<OutputPath>.progress.json`. The script
atomically updates this progress record at most 44 seconds apart during active
execution, records both sampler and monitor PID/start time and the binary hash,
then writes final success or failure. Sampling stays around 2,000 records even
over 24 hours. A suspended machine naturally creates a longer gap; a long gap
does not by itself verify resume correctness. The script records that limitation.

Wait for the complete report before claiming 24-hour acceptance. Inspect exit
status, unchanged binary hash, final stopped state and sampling gaps. Sleep,
battery, logout and multi-user cases need their own observed evidence. This
harness does not register a service, send notifications, contact a model, or
validate real Windows client callbacks.
