[CmdletBinding()]
param([string]$Go = 'go', [switch]$Race)

. (Join-Path $PSScriptRoot 'windows-test-common.ps1')
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw 'This script runs native Windows tests, not cross-compiled tests.'
}
$target = @(& $Go env GOOS GOHOSTOS)
if ($LASTEXITCODE -ne 0 -or $target.Count -ne 2 -or $target[0] -ne 'windows' -or $target[1] -ne 'windows') {
    throw 'Native tests require a Windows Go host and target.'
}
$testTemp = New-LaodiPrivateTestDirectory
if (-not ('LaodiTestProcessOwner' -as [type])) {
    Add-Type -Path (Join-Path $PSScriptRoot 'test-process-owner.cs')
}
$ownerScope = $null
$previous = @{}
foreach ($name in @('TMP', 'TEMP', 'GOTMPDIR', 'LAODI_NATIVE_STARTUP_MONITOR_EXE')) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
Push-Location ([IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..')))
try {
    $ownerScope = [LaodiTestProcessOwner]::new()
    Write-Host "Native test previous default owner matched user: $($ownerScope.PreviousOwnerIsUser)"
    # Check ordinary, implicit object creation, not explicitly-owned fixtures.
    $probe = Join-Path $testTemp 'owner-probe'
    [void][IO.Directory]::CreateDirectory($probe)
    $probeFile = Join-Path $probe 'file'
    [IO.File]::WriteAllText($probeFile, 'synthetic')
    $userSID = [Security.Principal.WindowsIdentity]::GetCurrent().User
    foreach ($path in @($probe, $probeFile)) {
        $owner = (Get-Acl -LiteralPath $path).GetOwner([Security.Principal.SecurityIdentifier])
        if (-not $owner.Equals($userSID)) { throw 'Native test fixture did not inherit current-user ownership.' }
    }
    Remove-Item -LiteralPath $probe -Recurse -Force
    foreach ($name in @('TMP', 'TEMP', 'GOTMPDIR')) {
        [Environment]::SetEnvironmentVariable($name, $testTemp, 'Process')
    }
    # Exercise the login-startup backend with a real watcher and an isolated
    # shortcut folder. This does not opt into real Task Scheduler registrations.
    $startupMonitor = Join-Path $testTemp 'laodi-startup-fixture.exe'
    & $Go build -o $startupMonitor ./cmd/laodi
    if ($LASTEXITCODE -ne 0) { throw 'Could not build native startup monitor fixture.' }
    [Environment]::SetEnvironmentVariable('LAODI_NATIVE_STARTUP_MONITOR_EXE', $startupMonitor, 'Process')
    Write-Host "Native test temporary directory: $testTemp"
    $testArguments = @('test', '-count=1')
    if ($Race) { $testArguments += '-race' }
    & $Go @testArguments ./...
    if ($LASTEXITCODE -ne 0) { throw 'Native Windows tests failed.' }
    # Retain bounded native/legacy persistence diagnostics for the regression
    # that cannot be reproduced by a cross-compile on another OS.
    & $Go @testArguments -run '^TestWindowsStartupNativePersistenceSpecialPaths$' -v ./internal/laodi
    if ($LASTEXITCODE -ne 0) { throw 'Native shortcut persistence regression failed.' }
    & $Go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Windows Go vet failed.' }
} finally {
    try {
        Pop-Location
        foreach ($name in @('TMP', 'TEMP', 'GOTMPDIR', 'LAODI_NATIVE_STARTUP_MONITOR_EXE')) {
            [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
        }
        Remove-Item -LiteralPath $testTemp -Recurse -Force
    } finally {
        if ($null -ne $ownerScope) { $ownerScope.Dispose() }
    }
}
