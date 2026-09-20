[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')][string]$Version,
    [string]$Go = 'go',
    [string]$OutputRoot = '',
    [switch]$SkipChecks
)
$ErrorActionPreference = 'Stop'
if ($Version.Length -gt 128) { throw 'Release tag is too long' }
foreach ($part in (($Version -split '-',2 | Select-Object -Skip 1) -split '\.')) { if ($part -match '^0[0-9]+$') { throw 'Invalid numeric prerelease identifier' } }
$target = @(& $Go env GOOS GOARCH)
if ($LASTEXITCODE -ne 0 -or $target.Count -ne 2 -or $target[0] -ne 'windows' -or $target[1] -ne 'amd64') { throw 'Windows development packages require a windows/amd64 Go target' }
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
if (-not $OutputRoot) { $OutputRoot = Join-Path $repo 'dist' }
$releaseRoot = Join-Path ([IO.Path]::GetFullPath($OutputRoot)) $Version
$payload = Join-Path $releaseRoot 'payload'
if (Test-Path -LiteralPath $releaseRoot) { throw 'Output version already exists; choose a new output directory.' }
New-Item -ItemType Directory -Path $payload -Force | Out-Null
Push-Location $repo
try {
    $commit = (& git rev-parse --verify HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $commit -cnotmatch '^[a-f0-9]{40}$') { throw 'Cannot identify release source commit' }
    $trackedChanges = @(& git status --porcelain --untracked-files=no)
    if ($LASTEXITCODE -ne 0 -or $trackedChanges.Count -ne 0) { throw 'Commit tracked source changes before building a release candidate' }
    if (-not $SkipChecks) {
        & (Join-Path $repo 'scripts/dev/test-windows.ps1') -Go $Go
    }
    & $Go build -buildvcs=true -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $payload 'laodi.exe') ./cmd/laodi
    if ($LASTEXITCODE -ne 0) { throw 'CLI build failed' }
    & $Go build -buildvcs=true -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version" -o (Join-Path $payload 'laodi-host.exe') ./cmd/laodi
    if ($LASTEXITCODE -ne 0) { throw 'Hidden host build failed' }
    foreach ($binary in @('laodi.exe','laodi-host.exe')) {
        $metadata = @(& $Go version -m (Join-Path $payload $binary))
        if ($LASTEXITCODE -ne 0 -or -not ($metadata -match ('vcs.revision=' + [Regex]::Escape($commit) + '$')) -or -not ($metadata -match 'vcs.modified=false$')) { throw "Executable provenance differs from clean release commit: $binary" }
    }
    Copy-Item -LiteralPath (Join-Path $repo 'assets/laodi-logo.png') -Destination $payload
    & (Join-Path $repo 'platform/windows/notifier/build.ps1') -OutputDirectory $payload
    if (-not $?) { throw 'Windows notification helper build failed' }
    $skill = [IO.File]::ReadAllText((Join-Path $repo 'skills/laodi/SKILL.md')).Replace('references/usage.md','usage.md')
    [IO.File]::WriteAllText((Join-Path $payload 'SKILL.md'), $skill, [Text.UTF8Encoding]::new($false))
    Copy-Item -LiteralPath (Join-Path $repo 'skills/laodi/references/usage.md') -Destination $payload
    $hashes = [ordered]@{}
    Get-ChildItem -LiteralPath $payload -File | Sort-Object Name | ForEach-Object { $hashes[$_.Name] = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    $manifest = [ordered]@{schema=1;version=$Version;protocol=1;files=$hashes} | ConvertTo-Json -Depth 5
    [IO.File]::WriteAllText((Join-Path $payload 'windows-manifest.json'), $manifest, [Text.UTF8Encoding]::new($false))
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = Join-Path $releaseRoot "Laodi-$Version-windows-amd64.zip"
    [IO.Compression.ZipFile]::CreateFromDirectory($payload,$archive,[IO.Compression.CompressionLevel]::Optimal,$false)
    $sum = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'SHA256SUMS-windows'), "$sum  $([IO.Path]::GetFileName($archive))`n", [Text.UTF8Encoding]::new($false))
    $manifestSHA256 = (Get-FileHash -LiteralPath (Join-Path $payload 'windows-manifest.json') -Algorithm SHA256).Hash.ToLowerInvariant()
    $info = [ordered]@{version=$Version;commit=$commit;manifest_sha256=$manifestSHA256;platform='windows/amd64';go=(& $Go version);authenticode='unsigned';runtime_dependencies='Windows 11 built-in APIs';release_gate='pending live client, visible notification, clean-user and 24h verification'} | ConvertTo-Json
    [IO.File]::WriteAllText((Join-Path $releaseRoot 'build-info-windows.json'),$info,[Text.UTF8Encoding]::new($false))
    Write-Output $archive
} finally { Pop-Location }
