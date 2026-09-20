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
$previous = @{}
foreach ($name in @('TMP', 'TEMP', 'GOTMPDIR')) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
Push-Location ([IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..')))
try {
    foreach ($name in @('TMP', 'TEMP', 'GOTMPDIR')) {
        [Environment]::SetEnvironmentVariable($name, $testTemp, 'Process')
    }
    Write-Host "Native test temporary directory: $testTemp"
    $testArguments = @('test', '-count=1')
    if ($Race) { $testArguments += '-race' }
    & $Go @testArguments ./...
    if ($LASTEXITCODE -ne 0) { throw 'Native Windows tests failed.' }
    & $Go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Windows Go vet failed.' }
} finally {
    Pop-Location
    foreach ($name in @('TMP', 'TEMP', 'GOTMPDIR')) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
    Remove-Item -LiteralPath $testTemp -Recurse -Force
}
