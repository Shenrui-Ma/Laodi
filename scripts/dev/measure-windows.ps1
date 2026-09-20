[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$OutputPath,
    [string]$ProgressPath,
    [ValidateRange(5, 86400)][int]$DurationSeconds = 22,
    [ValidateRange(0, 100)][int]$ManifestCount = 100,
    [ValidateRange(1, 100000)][int]$FilesPerManifest = 1
)
. (Join-Path $PSScriptRoot 'windows-test-common.ps1')
if ($ManifestCount * $FilesPerManifest -gt 100000) { throw 'At most 100,000 synthetic entries are permitted.' }
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
if (-not $ProgressPath) { $ProgressPath = $OutputPath + '.progress.json' }
$ProgressPath = [IO.Path]::GetFullPath($ProgressPath)
$sha256 = (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLowerInvariant()
$version = Invoke-LaodiTestProcess -Executable $Binary -Arguments @('version')
if ($version.ExitCode -ne 0) { throw 'Cannot identify development binary.' }
$testRoot = New-LaodiPrivateTestDirectory
$evidence = Join-Path $testRoot 'checkpoints'
$manifests = Join-Path $evidence '0123456789ab\manifests'
$stateDir = Join-Path $testRoot 'state'
[void][IO.Directory]::CreateDirectory($manifests)
$files = [Collections.Generic.List[object]]::new()
for ($i = 0; $i -lt $FilesPerManifest; $i++) { $files.Add(@{ path = ".git/objects/SYNTHETIC-$i"; sizeBytes = 123 }) }
$payload = @{ schema = 'repo_snapshot_manifest/v2'; workspaceKey = '/SYNTHETIC/resource-test'; files = $files.ToArray() }
for ($i = 0; $i -lt $ManifestCount; $i++) { Write-LaodiTestJSON (Join-Path $manifests ($i.ToString('x64') + '.json')) $payload }
$samples = [Collections.Generic.List[object]]::new()
$sampleSeconds = [Math]::Max(1, [Math]::Ceiling($DurationSeconds / 2000.0))
$started = [DateTime]::UtcNow
$timer = [Diagnostics.Stopwatch]::StartNew()
$watch = Start-LaodiTestProcess -Executable $Binary -Arguments @('watch', '--root', $evidence, '--state-dir', $stateDir, '--build', '3.12.3.7463', '--interval', '2s', '--duration', ($DurationSeconds.ToString() + 's'))
$launchTime = $watch.Process.StartTime
$processIdentity = @{ monitor_pid = $watch.Process.Id; monitor_started_at = $launchTime.ToUniversalTime().ToString('o'); sampler_pid = $PID; sampler_started_at = (Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o') }
$maximumGap = 0.0
$previousElapsed = 0.0
$monitorLifetimeSeconds = $null
$durationVerified = $false
try {
    while (-not $watch.Process.HasExited) {
        if ($timer.Elapsed.TotalSeconds -gt $DurationSeconds + 30) { throw 'Resource probe exceeded its duration allowance.' }
        try {
            $sample = Get-Process -Id $watch.Process.Id -ErrorAction Stop
            if ($sample.StartTime -ne $launchTime) { throw 'Sampled process identity changed.' }
            $elapsed = $timer.Elapsed.TotalSeconds
            $maximumGap = [Math]::Max($maximumGap, $elapsed - $previousElapsed)
            $previousElapsed = $elapsed
            $samples.Add([pscustomobject]@{
                elapsed_seconds = $elapsed; recorded_at = [DateTime]::UtcNow.ToString('o'); cpu_seconds = $sample.TotalProcessorTime.TotalSeconds
                working_set_bytes = $sample.WorkingSet64; private_bytes = $sample.PrivateMemorySize64
                peak_working_set_bytes = $sample.PeakWorkingSet64
                handles = $sample.HandleCount; threads = $sample.Threads.Count; process_count = 1
            })
            $sample.Dispose()
            Write-LaodiTestJSON $ProgressPath @{
                schema_version = 1; status = 'running'; requested_seconds = $DurationSeconds; elapsed_seconds = $elapsed
                recorded_at = [DateTime]::UtcNow.ToString('o'); binary_sha256 = $sha256; process_identity = $processIdentity
                last_sample = $samples[$samples.Count - 1]; sample_count = $samples.Count
                verification_passed = $null; suspend_resume_verified = $false
            }
        } catch {
            if (-not $watch.Process.HasExited) { throw }
        }
        # Samples are bounded to approximately 2,000 even for a 24-hour run.
        # A long scheduling/suspend gap is recorded, never silently relabelled.
        Start-Sleep -Seconds $sampleSeconds
    }
    $completion = Complete-LaodiTestProcess -Child $watch -TimeoutMilliseconds 5000
    $timer.Stop()
    $watch.Process.Refresh()
    # The sampler can wake long after a child exited (including after suspend).
    # Its stopwatch alone therefore cannot prove that the monitor ran for the
    # requested duration. Use this exact child's OS start/exit timestamps too.
    $monitorLifetimeSeconds = ($watch.Process.ExitTime.ToUniversalTime() - $launchTime.ToUniversalTime()).TotalSeconds
    $durationVerified = $monitorLifetimeSeconds -ge $DurationSeconds -and $timer.Elapsed.TotalSeconds -ge $DurationSeconds
    $cpu = $watch.Process.TotalProcessorTime.TotalSeconds
    $peakWorkingSet = ($samples | Measure-Object -Property peak_working_set_bytes -Maximum).Maximum
    $state = [IO.File]::ReadAllText((Join-Path $stateDir 'state.json')) | ConvertFrom-Json
    $seenCount = @($state.seen.PSObject.Properties).Count
    $eventCount = @($state.events | Where-Object { $null -ne $_ }).Count
    $running = $null -ne $state.PSObject.Properties['running'] -and $state.running
    $passed = $durationVerified -and $completion.ExitCode -eq 0 -and $state.initialized -and -not $running -and $seenCount -eq $ManifestCount -and $samples.Count -gt 0
    if ((Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLowerInvariant() -ne $sha256) { $passed = $false }
    $result = [ordered]@{
        schema_version = 1; verification_passed = $passed; created_at = [DateTime]::UtcNow.ToString('o')
        version = $version.Stdout.Trim(); binary_sha256 = $sha256; os_version = [Environment]::OSVersion.Version.ToString(); architecture = $env:PROCESSOR_ARCHITECTURE
        process_identity = $processIdentity
        fixture = @{ manifest_count = $ManifestCount; files_per_manifest = $FilesPerManifest }
        measurement = @{ requested_seconds = $DurationSeconds; observed_wall_seconds = $timer.Elapsed.TotalSeconds; monitor_lifetime_seconds = $monitorLifetimeSeconds; duration_verified = $durationVerified; sample_interval_seconds = $sampleSeconds; sample_count = $samples.Count; longest_sample_gap_seconds = $maximumGap; suspend_resume_verified = $false }
        process_resources = @{ cpu_seconds = $cpu; cpu_percent_of_one_core = 100 * $cpu / $timer.Elapsed.TotalSeconds; os_peak_working_set_bytes_observed = $peakWorkingSet }
        sample_summary = @{
            working_set_bytes_max = ($samples | Measure-Object -Property working_set_bytes -Maximum).Maximum
            private_bytes_max = ($samples | Measure-Object -Property private_bytes -Maximum).Maximum
            handles_min = ($samples | Measure-Object -Property handles -Minimum).Minimum
            handles_max = ($samples | Measure-Object -Property handles -Maximum).Maximum
            process_count = 1
        }
        saved_state = @{ initialized = $state.initialized; running = $running; coverage = $state.coverage; seen_count = $seenCount; event_count = $eventCount }
        samples = $samples.ToArray(); exit_code = $completion.ExitCode
        limits = @('Monitor PID only; no Hook/notifier children launched in this probe', 'Periodic private-memory and handle samples can miss transient peaks', 'A completed duration alone does not establish sleep/resume or battery behavior', 'Synthetic fixtures; no real client, network, user settings or notification is exercised', 'Short measurements do not establish a 24-hour result or resource ceiling')
    }
    Write-LaodiTestJSON ([IO.Path]::GetFullPath($OutputPath)) $result
    Write-LaodiTestJSON $ProgressPath @{ schema_version = 1; status = 'complete'; verification_passed = $passed; recorded_at = [DateTime]::UtcNow.ToString('o'); process_identity = $processIdentity; binary_sha256 = $sha256; observed_wall_seconds = $timer.Elapsed.TotalSeconds; requested_seconds = $DurationSeconds; monitor_lifetime_seconds = $monitorLifetimeSeconds; duration_verified = $durationVerified }
    [ordered]@{ verification_passed = $passed; measurement = $result.measurement; process_resources = $result.process_resources; sample_summary = $result.sample_summary; saved_state = $result.saved_state } | ConvertTo-Json -Depth 8
    if (-not $passed) { throw 'Resource probe did not meet its expected state or process result.' }
} catch {
    $failure = @{ schema_version = 1; status = 'failed'; verification_passed = $false; recorded_at = [DateTime]::UtcNow.ToString('o'); process_identity = $processIdentity; binary_sha256 = $sha256; reason = 'resource_probe_did_not_complete'; observed_wall_seconds = $timer.Elapsed.TotalSeconds; requested_seconds = $DurationSeconds; monitor_lifetime_seconds = $monitorLifetimeSeconds; duration_verified = $durationVerified }
    Write-LaodiTestJSON $ProgressPath $failure
    Write-LaodiTestJSON $OutputPath $failure
    throw
} finally {
    if (-not $watch.Process.HasExited) { $watch.Process.Kill(); $watch.Process.WaitForExit() }
    $watch.Process.Dispose()
}
