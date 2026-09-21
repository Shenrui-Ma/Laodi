[CmdletBinding()]
param([string]$Go = 'go', [switch]$Race)

# Offline component evidence only: never install or launch the downloaded client.
# The immutable installer digest was recorded from the official 3.11.2 manifest:
# https://cdn-zcode.z.ai/zcode/electron/releases/3.11.2/windows-x64/latest.yml
. (Join-Path $PSScriptRoot 'windows-test-common.ps1')
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Requires native Windows.' }
$node = (Get-Command node -CommandType Application -ErrorAction Stop).Source
$python = (Get-Command python -CommandType Application -ErrorAction Stop).Source
$sevenZip = (Get-Command 7z -CommandType Application -ErrorAction Stop).Source
if (-not ('LaodiTestProcessOwner' -as [type])) { Add-Type -Path (Join-Path $PSScriptRoot 'test-process-owner.cs') }
$ownerScope = [LaodiTestProcessOwner]::new()
$directory = $null
$previous = @{}
foreach ($name in @('TEMP','TMP','GOTMPDIR','LAODI_ARCHIVE_NODE','LAODI_ARCHIVE_COMPONENT','LAODI_WINDOWS_ARCHIVE_APP')) {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
Push-Location ([IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..')))
try {
    $directory = New-LaodiPrivateTestDirectory
    foreach ($name in @('TEMP','TMP','GOTMPDIR')) { [Environment]::SetEnvironmentVariable($name, $directory, 'Process') }
    $installer = Join-Path $directory 'original-installer.exe'
    $url = 'https://cdn-zcode.z.ai/zcode/electron/releases/3.11.2/windows-x64/ZCode-3.11.2-win-x64.exe'
    $expectedSize = 163369016L
    $expectedHash = 'y6Z+hBQrAgXoErb03YEBTSx7nOtsC6RhrCJGSeN0f0APvGslgoNrZgwPbeJsrHMllFbT9F+MxEu8b9kxvFqrzg=='
    Add-Type -AssemblyName System.Net.Http
    $handler = [Net.Http.HttpClientHandler]::new()
    $handler.AllowAutoRedirect = $false
    $client = [Net.Http.HttpClient]::new($handler)
    $cancel = [Threading.CancellationTokenSource]::new([TimeSpan]::FromMinutes(5))
    $response = $null; $downloadStream = $null; $output = $null
    try {
        $response = $client.GetAsync($url, [Net.Http.HttpCompletionOption]::ResponseHeadersRead, $cancel.Token).GetAwaiter().GetResult()
        if ([int]$response.StatusCode -ne 200) { throw 'Official installer download did not return HTTP 200.' }
        if ($null -ne $response.Content.Headers.ContentLength -and $response.Content.Headers.ContentLength -ne $expectedSize) { throw 'Unexpected installer size.' }
        $downloadStream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $output = [IO.File]::Open($installer, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
        $buffer = [byte[]]::new(1048576); $total = 0L
        while (($count = $downloadStream.ReadAsync($buffer, 0, $buffer.Length, $cancel.Token).GetAwaiter().GetResult()) -gt 0) {
            $total += $count
            if ($total -gt $expectedSize) { throw 'Installer exceeded its pinned size.' }
            $output.Write($buffer, 0, $count)
        }
        if ($total -ne $expectedSize) { throw 'Installer was truncated.' }
    } finally {
        if ($null -ne $output) { $output.Dispose() }
        if ($null -ne $downloadStream) { $downloadStream.Dispose() }
        if ($null -ne $response) { $response.Dispose() }
        $cancel.Dispose(); $client.Dispose(); $handler.Dispose()
    }
    $stream = [IO.File]::OpenRead($installer); $hasher = [Security.Cryptography.SHA512]::Create()
    try { $actualHash = [Convert]::ToBase64String($hasher.ComputeHash($stream)) } finally { $stream.Dispose(); $hasher.Dispose() }
    if ($actualHash -cne $expectedHash) { throw 'Official installer SHA-512 did not match the pinned manifest.' }
    $outer = Join-Path $directory 'container'
    # Select archive members without running the NSIS installer. Do not assume
    # a particular NSIS temporary directory; require exactly one x64 payload.
    & $sevenZip x '-y' ('-o' + $outer) $installer '*.7z' '-r' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not inspect the installer container.' }
    $payloads = @(Get-ChildItem -LiteralPath $outer -Recurse -File -Filter 'app-64.7z')
    if ($payloads.Count -ne 1) { throw 'Installer did not contain exactly one x64 payload; no tests ran.' }
    $appDirectory = Join-Path $directory 'public-program'
    & $sevenZip x '-y' ('-o' + $appDirectory) $payloads[0].FullName 'ZCode.exe' 'resources/app.asar' 'resources/glm/zcode.cjs' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not extract the three public program identity files.' }
    $env:LAODI_WINDOWS_ARCHIVE_APP = Join-Path $appDirectory 'ZCode.exe'
    $moduleDirectory = Join-Path $directory 'component'
    & $python scripts/dev/prepare_windows_archive_component.py (Join-Path $appDirectory 'resources/app.asar') $moduleDirectory
    if ($LASTEXITCODE -ne 0) { throw 'Original producer extraction failed.' }
    $env:LAODI_ARCHIVE_NODE = $node
    $env:LAODI_ARCHIVE_COMPONENT = Join-Path $moduleDirectory 'original-component.mjs'
    $tests = @('TestWindowsArchiveProtectionOriginalIdentity','TestWindowsArchiveOriginalProducerABA')
    $testArguments = @('test','-count=1','-json','-run', ('^(' + ($tests -join '|') + ')$'))
    if ($Race) { $testArguments += '-race' }
    $events = @(& $Go @testArguments ./internal/laodi)
    if ($LASTEXITCODE -ne 0) { $events | Write-Host; throw 'Original component native tests failed.' }
    $records = @($events | ForEach-Object { $_ | ConvertFrom-Json })
    foreach ($test in $tests) {
        $passes = @($records | Where-Object { $_.PSObject.Properties.Name -contains 'Test' -and $_.Test -ceq $test -and $_.Action -ceq 'pass' })
        if ($passes.Count -ne 1) { throw "Required original component test did not pass (skip is not acceptance): $test" }
    }
    Write-Host 'Original Windows producer: hash-verified package; isolated baseline, pending, new-workspace and incremental denial/restoration passed. No full client or official service was exercised.'
} finally {
    Pop-Location
    foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    try { if ($null -ne $directory) { Remove-Item -LiteralPath $directory -Recurse -Force } } finally { $ownerScope.Dispose() }
}
