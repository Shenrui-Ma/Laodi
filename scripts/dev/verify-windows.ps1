[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$OutputPath,
    [string]$Git = 'C:\Program Files\Git\cmd\git.exe'
)
. (Join-Path $PSScriptRoot 'windows-test-common.ps1')
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$Git = (Resolve-Path -LiteralPath $Git).Path
$started = [DateTime]::UtcNow
$sha256 = (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLowerInvariant()
$versionResult = Invoke-LaodiTestProcess -Executable $Binary -Arguments @('version')
if ($versionResult.ExitCode -ne 0) { throw 'Cannot identify development binary.' }
$testRoot = New-LaodiPrivateTestDirectory
$evidence = Join-Path $testRoot 'checkpoints'
$stateDir = Join-Path $testRoot 'state'
$statePath = Join-Path $stateDir 'state.json'
$workspace = Join-Path $evidence '0123456789ab'
$project = Join-Path $testRoot 'synthetic-project'
$emptyHome = Join-Path $testRoot 'empty-home'
foreach ($path in @($evidence, $project, $emptyHome)) { [void][IO.Directory]::CreateDirectory($path) }
$a = 'a' * 64; $b = 'b' * 64; $c = 'c' * 64
$key = '/SYNTHETIC/private-project'
$measurements = [Collections.Generic.List[object]]::new()
$gitResults = [Collections.Generic.List[object]]::new()
$watchArguments = @('watch', '--root', $evidence, '--state-dir', $stateDir, '--build', '3.12.3.7463', '--interval', '1s', '--duration', '20s')
$watch = Start-LaodiTestProcess -Executable $Binary -Arguments $watchArguments
try {
    $null = Wait-LaodiTestEvents -Child $watch -StatePath $statePath -Expected @{} -ExpectedTotal 0
    $observable = [DateTime]::UtcNow
    Write-LaodiTestJSON (Join-Path $workspace ('manifests\' + $a + '.json')) @{
        schema = 'repo_snapshot_manifest/v2'; workspaceKey = $key
        files = @(@{ path = '.git/objects/pack/SYNTHETIC.pack'; sizeBytes = 257 }, @{ path = '.git/lfs/objects/SYNTHETIC'; sizeBytes = 31 })
    }
    Write-LaodiTestJSON (Join-Path $workspace ('extra-manifests\' + $b + '.json')) @{
        schema = 'repo_snapshot_extra_manifest/v1'
        groups = @(@{ groupId = 'global-configs'; files = @(@{ path = 'SYNTHETIC-settings.json'; sizeBytes = 10; contentHash = $c }) })
    }
    Write-LaodiTestJSON (Join-Path $workspace 'state.json') @{
        workspaceKey = $key; workspacePath = $key; failureCount = 564
        pendingUpload = @{ nextManifestHash = $a; nextExtraManifestHash = $b; attemptCount = 0 }
    }
    $saved = Wait-LaodiTestEvents -Child $watch -StatePath $statePath -Expected @{ sensitive_manifest_match = 1; global_config_manifest_match = 1; upload_attempt_recorded = 0 } -ExpectedTotal 2
    $measurements.Add(@{ case_id = 'WIN-REPLAY-01'; expected = 'Two manifests; failureCount is not an upload attempt'; actual = 'passed'; observable_at = $observable.ToString('o'); persisted_by = [DateTime]::UtcNow.ToString('o'); events = 2 })
    $observable = [DateTime]::UtcNow
    Write-LaodiTestJSON (Join-Path $workspace 'state.json') @{
        workspaceKey = $key; workspacePath = $key
        activeUpload = @{ nextManifestHash = $a; nextExtraManifestHash = $b; attemptCount = 1 }
    }
    $saved = Wait-LaodiTestEvents -Child $watch -StatePath $statePath -Expected @{ upload_attempt_recorded = 1; global_config_upload_attempt_recorded = 1 } -ExpectedTotal 4
    $measurements.Add(@{ case_id = 'WIN-REPLAY-02'; expected = 'Separate main and extra attempts'; actual = 'passed'; observable_at = $observable.ToString('o'); persisted_by = [DateTime]::UtcNow.ToString('o'); events = 2 })
    $observable = [DateTime]::UtcNow
    Write-LaodiTestJSON (Join-Path $workspace ('manifests\' + $c + '.json')) @{
        schema = 'repo_snapshot_manifest/v2'; workspaceKey = $key
        files = @(@{ path = 'docs/SYNTHETIC.txt'; sizeBytes = 100 })
    }
    Write-LaodiTestJSON (Join-Path $workspace 'state.json') @{
        workspaceKey = $key; workspacePath = $key; lastAcceptedManifestHash = $c; lastAcceptedExtraManifestHash = $b
        activeUpload = @{ nextManifestHash = $a; attemptCount = 1 }
    }
    $saved = Wait-LaodiTestEvents -Child $watch -StatePath $statePath -Expected @{ workspace_snapshot_upload_acceptance_recorded = 1; global_config_upload_acceptance_recorded = 1; upload_acceptance_recorded = 0 } -ExpectedTotal 7
    $measurements.Add(@{ case_id = 'WIN-REPLAY-03'; expected = 'Ordinary accepted record does not accept pending Git manifest'; actual = 'passed'; observable_at = $observable.ToString('o'); persisted_by = [DateTime]::UtcNow.ToString('o'); events = 3 })

    $fake = 'L4p9Vz2Rk7Wq8Mn3Bc6Tf1Yu0Hs5XeAa'
    $hookCases = @(
        @{ id = 'access'; event = 'PreToolUse'; input = @{ command = "Get-Content -LiteralPath 'C:\SYNTHETIC\.env'" }; output = $null },
        @{ id = 'output'; event = 'PostToolUse'; input = @{}; output = @{ stdout = 'API_KEY=' + $fake } },
        @{ id = 'failure'; event = 'PostToolUseFailure'; input = @{}; output = $null; error = 'API_KEY=' + $fake },
        @{ id = 'hash'; event = 'PostToolUse'; input = @{}; output = @{ stdout = ('SHA256=' + $a) } },
        @{ id = 'uuid'; event = 'PostToolUse'; input = @{}; output = @{ stdout = 'API_KEY=550e8400-e29b-41d4-a716-446655440000' } },
        @{ id = 'placeholder'; event = 'PostToolUse'; input = @{}; output = @{ stdout = 'API_KEY=your_example_placeholder_value' } }
    )
    $observable = [DateTime]::UtcNow
    foreach ($case in $hookCases) {
        $payload = @{ session_id = 'SYNTHETIC-session'; tool_use_id = ('SYNTHETIC-' + $case.id); hook_event_name = $case.event; tool_name = 'PowerShell'; tool_input = $case.input }
        if ($null -ne $case.output) { $payload.tool_response = $case.output }
        if ($case.ContainsKey('error')) { $payload.error = $case.error }
        $bytes = [Text.UTF8Encoding]::new($false).GetBytes((ConvertTo-Json -InputObject $payload -Depth 10 -Compress))
        $hook = Invoke-LaodiTestProcess -Executable $Binary -Arguments @('hook', '--adapter', 'claude-code', '--state-dir', $stateDir) -InputBytes $bytes
        if ($hook.ExitCode -ne 0 -or $hook.Stdout -ne '' -or $hook.Stderr -ne '') { throw 'Hook changed process output or exit status.' }
    }
    $saved = Wait-LaodiTestEvents -Child $watch -StatePath $statePath -Expected @{ sensitive_tool_access_requested = 1; sensitive_tool_output_detected = 2 } -ExpectedTotal 10
    $measurements.Add(@{ case_id = 'WIN-HOOK-01'; expected = 'Access plus two output events; three negatives remain quiet'; actual = 'passed'; observable_at = $observable.ToString('o'); persisted_by = [DateTime]::UtcNow.ToString('o'); events = 3; real_callback = $false })

    $observable = [DateTime]::UtcNow
    $concurrent = [Collections.Generic.List[object]]::new()
    $hookBytes = [Collections.Generic.List[object]]::new()
    $stressSamples = [Collections.Generic.List[object]]::new()
    $hookCPU = 0.0
    try {
        for ($i = 0; $i -lt 32; $i++) {
            $payload = @{ session_id = 'SYNTHETIC-concurrent'; tool_use_id = ('SYNTHETIC-' + $i); hook_event_name = 'PostToolUse'; tool_name = 'PowerShell'; tool_response = @{ stdout = 'API_KEY=' + $fake } }
            $bytes = [Text.UTF8Encoding]::new($false).GetBytes((ConvertTo-Json -InputObject $payload -Depth 10 -Compress))
            # Launch first, then release stdin together, exercising actual
            # concurrent processes rather than 32 completed sequential calls.
            $concurrent.Add((Start-LaodiTestProcess -Executable $Binary -Arguments @('hook', '--adapter', 'claude-code', '--state-dir', $stateDir) -KeepInputOpen))
            $hookBytes.Add($bytes)
        }
        $stressSamples.Add((Get-LaodiChildResources ($concurrent.ToArray() + @($watch))))
        for ($i = 0; $i -lt $concurrent.Count; $i++) {
            $concurrent[$i].Process.StandardInput.BaseStream.Write($hookBytes[$i], 0, $hookBytes[$i].Length)
            $concurrent[$i].Process.StandardInput.Close()
        }
        $stressDeadline = [DateTime]::UtcNow.AddSeconds(8)
        do {
            $stressSamples.Add((Get-LaodiChildResources ($concurrent.ToArray() + @($watch))))
            $active = @($concurrent | Where-Object { -not $_.Process.HasExited }).Count
            if ($active -eq 0) { break }
            if ([DateTime]::UtcNow -gt $stressDeadline) { throw 'Concurrent hook batch exceeded its deadline.' }
            Start-Sleep -Milliseconds 20
        } while ($true)
        foreach ($child in $concurrent) {
            $hook = Complete-LaodiTestProcess -Child $child
            if ($hook.ExitCode -ne 0 -or $hook.Stdout -ne '' -or $hook.Stderr -ne '') { throw 'Concurrent hook changed output or exit status.' }
            $hookCPU += $child.Process.TotalProcessorTime.TotalSeconds
        }
    } finally {
        foreach ($child in $concurrent) {
            if (-not $child.Process.HasExited) { $child.Process.Kill(); $child.Process.WaitForExit() }
            $child.Process.Dispose()
        }
    }
    # Wait two monitor ticks after all producers finish. The queue intentionally
    # keeps its ~100 ms lock bound; any loss must remain an explicit gap.
    Start-Sleep -Milliseconds 2200
    $saved = [IO.File]::ReadAllText($statePath) | ConvertFrom-Json
    $counts = Get-LaodiTestCounts $saved
    $delivered = $counts['sensitive_tool_output_detected'] - 2
    $missed = 32 - $delivered
    $gapRecorded = $counts.ContainsKey('hook_coverage_degraded') -and @($saved.diagnostics | Where-Object { $_.code -eq 'hook_inbox_gap_recorded' }).Count -gt 0
    if ($delivered -lt 0 -or $delivered -gt 32 -or ($missed -gt 0 -and -not $gapRecorded)) { throw 'Concurrent hook loss was silent or event counts were invalid.' }
    $expectedFinalEvents = @($saved.events).Count
    $stressResult = @{ launched_hook_processes = 32; TP = $delivered; FN = $missed; FP = 0; gap_recorded = $gapRecorded; lossless_delivery = ($missed -eq 0); safety_contract_passed = $true
        hook_cpu_seconds_sum = $hookCPU; maximum_process_count_observed_including_monitor = ($stressSamples | Measure-Object -Property process_count -Maximum).Maximum
        working_set_bytes_sum_max_observed = ($stressSamples | Measure-Object -Property working_set_bytes_sum -Maximum).Maximum
        private_bytes_sum_max_observed = ($stressSamples | Measure-Object -Property private_bytes_sum -Maximum).Maximum
        handles_sum_max_observed = ($stressSamples | Measure-Object -Property handles_sum -Maximum).Maximum
        resource_scope = '32 hook children plus monitor sampled; hook CPU excludes monitor and sampler; no notifier was configured; transient peaks may be missed' }
    $measurements.Add(@{ case_id = 'WIN-HOOK-02'; expected = 'Bounded-lock contention either delivers records or persists a visible gap'; actual = 'safety_contract_passed'; observable_at = $observable.ToString('o'); persisted_by = [DateTime]::UtcNow.ToString('o'); delivered_events = $delivered; missing_events = $missed; real_callback = $false })

    $gitEnvironment = @{
        GIT_CONFIG_NOSYSTEM = '1'; GIT_CONFIG_GLOBAL = 'NUL'; GIT_AUTHOR_NAME = 'Synthetic'; GIT_AUTHOR_EMAIL = 'test@example.invalid'
        GIT_COMMITTER_NAME = 'Synthetic'; GIT_COMMITTER_EMAIL = 'test@example.invalid'
        GIT_CONFIG_COUNT = '3'; GIT_CONFIG_KEY_0 = 'core.hooksPath'; GIT_CONFIG_VALUE_0 = $emptyHome
        GIT_CONFIG_KEY_1 = 'commit.gpgsign'; GIT_CONFIG_VALUE_1 = 'false'; GIT_CONFIG_KEY_2 = 'core.autocrlf'; GIT_CONFIG_VALUE_2 = 'false'
    }
    $gitCommands = @(@('init', '-q', ('--template=' + $emptyHome)), @('add', 'app.txt'), @('commit', '-qm', 'synthetic baseline'), @('status', '--porcelain'), @('diff', '--no-ext-diff'), @('log', '-1', '--format=%s'))
    [IO.File]::WriteAllText((Join-Path $project 'app.txt'), "SYNTHETIC baseline`n", [Text.UTF8Encoding]::new($false))
    foreach ($arguments in $gitCommands) {
        $clock = [Diagnostics.Stopwatch]::StartNew()
        $result = Invoke-LaodiTestProcess -Executable $Git -Arguments $arguments -Directory $project -Environment $gitEnvironment
        $clock.Stop()
        if ($result.ExitCode -ne 0) { throw ('Synthetic Git operation failed: ' + $arguments[0]) }
        $gitResults.Add(@{ operation = $arguments[0]; exit_code = $result.ExitCode; elapsed_ms = $clock.Elapsed.TotalMilliseconds })
    }
    $headBefore = (Invoke-LaodiTestProcess -Executable $Git -Arguments @('rev-parse', 'HEAD') -Directory $project -Environment $gitEnvironment).Stdout
    $indexBefore = (Get-FileHash -LiteralPath (Join-Path $project '.git\index') -Algorithm SHA256).Hash
    $completion = Complete-LaodiTestProcess -Child $watch -TimeoutMilliseconds 30000
    if ($completion.ExitCode -ne 0) { throw 'Synthetic monitor exited with failure.' }
    $saved = [IO.File]::ReadAllText($statePath) | ConvertFrom-Json
    $running = $null -ne $saved.PSObject.Properties['running'] -and $saved.running
    if ($running -or @($saved.events).Count -ne $expectedFinalEvents) { throw 'Final state has unexpected events or was not gracefully stopped.' }
    $headAfter = (Invoke-LaodiTestProcess -Executable $Git -Arguments @('rev-parse', 'HEAD') -Directory $project -Environment $gitEnvironment).Stdout
    $indexAfter = (Get-FileHash -LiteralPath (Join-Path $project '.git\index') -Algorithm SHA256).Hash
    if ($headBefore -ne $headAfter -or $indexBefore -ne $indexAfter) { throw 'Synthetic source HEAD/index changed after normal development.' }
    $restart = Invoke-LaodiTestProcess -Executable $Binary -Arguments @('watch', '--root', $evidence, '--state-dir', $stateDir, '--build', '3.12.3.7463', '--interval', '1s', '--duration', '2s')
    if ($restart.ExitCode -ne 0) { throw 'Synthetic monitor restart failed.' }
    $afterRestart = [IO.File]::ReadAllText($statePath) | ConvertFrom-Json
    if (@($afterRestart.events).Count -ne $expectedFinalEvents) { throw 'Restart duplicated events.' }
    $summary = Invoke-LaodiTestProcess -Executable $Binary -Arguments @('incidents', '--state-dir', $stateDir, '--format', 'agent-summary')
    if ($summary.ExitCode -ne 0) { throw 'Incident summary failed.' }
    foreach ($private in @($key, 'SYNTHETIC', $a, $b, $c, $fake, $testRoot)) {
        if ($summary.Stdout.Contains($private)) { throw 'Incident summary exposed raw synthetic source data.' }
    }
    foreach ($file in Get-ChildItem -LiteralPath $stateDir -File -Recurse) {
        if ([IO.File]::ReadAllText($file.FullName).Contains($fake)) { throw 'Raw synthetic credential was persisted.' }
    }
    $result = [ordered]@{
        schema_version = 1; verification_passed = $true; started_at = $started.ToString('o'); completed_at = [DateTime]::UtcNow.ToString('o')
        os_version = [Environment]::OSVersion.Version.ToString(); architecture = $env:PROCESSOR_ARCHITECTURE; version = $versionResult.Stdout.Trim(); binary_sha256 = $sha256
        cases = $measurements.ToArray(); sequential_event_classification = @{ TP = 10; FN = 0; FP = 0; TN = 5; denominator = '10 independently identified expected positive event records; 5 negative assertions (3 outputs, failure count, pending Git acceptance)' }
        concurrent_hook_stress = $stressResult
        events = $expectedFinalEvents; restart_duplicate_events = 0; git_operations = $gitResults.ToArray(); source_head_index_preserved = $true
        raw_credential_not_persisted = $true; summary_redacted = $true; monitor_exit = $completion.ExitCode
        notification_api_result = 'not_configured'; notification_user_visible = 'not_tested'; actual_client_callback = 'not_tested'; actual_snapshot = 'not_triggered'
        limits = @('verification_passed validates sequential replay and explicit contention-gap safety, not lossless stress delivery', 'Synthetic CLI replay only; no real client was launched', 'No notification was sent', 'No all-client detection-rate claim', 'Git durations include child startup; no baseline overhead comparison', '24-hour soak, sleep, multi-user and real update acceptance are separate requirements')
    }
    if ((Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLowerInvariant() -ne $sha256) { throw 'Test binary changed during verification.' }
    Write-LaodiTestJSON ([IO.Path]::GetFullPath($OutputPath)) $result
    $result | ConvertTo-Json -Depth 15
} finally {
    if (-not $watch.Process.HasExited) { $watch.Process.WaitForExit(25000) | Out-Null }
    if (-not $watch.Process.HasExited) { $watch.Process.Kill(); $watch.Process.WaitForExit() }
    $watch.Process.Dispose()
}
