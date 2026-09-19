# Windows development entry point. A Windows release is not advertised until the
# verification gate in docs/WINDOWS-VERIFICATION.md is satisfied.
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][ValidatePattern('^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')][string]$Version,
    [string]$StateDir = '',
	[string]$ClientExecutable = '',
    [switch]$DryRun,
    [switch]$NoNotifications,
    [ValidateSet('text','json')][string]$Format = 'text',
    # Local packages are explicit development inputs, never a download fallback.
    [string]$PackagePath = '',
    [string]$ExpectedSHA256 = ''
)
$ErrorActionPreference = 'Stop'
if ($Version.Length -gt 128) { throw 'Release tag is too long' }
foreach ($part in (($Version -split '-',2 | Select-Object -Skip 1) -split '\.')) { if ($part -match '^0[0-9]+$') { throw 'Invalid numeric prerelease identifier' } }
Add-Type -AssemblyName System.Net.Http
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
function Get-OfficialBytes([string]$Address,[int64]$Limit) {
    $handler = [Net.Http.HttpClientHandler]::new()
    $handler.AllowAutoRedirect = $false
    $client = [Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(75)
    $downloadDeadline = [Threading.CancellationTokenSource]::new()
    $downloadDeadline.CancelAfter(75000)
    try {
        for ($redirect = 0; $redirect -le 5; $redirect++) {
            $uri = [Uri]$Address
            if ($uri.Scheme -ne 'https' -or $uri.UserInfo -or -not $uri.IsDefaultPort -or $uri.Host -notin @('github.com','release-assets.githubusercontent.com','objects.githubusercontent.com')) { throw 'Untrusted release URL or redirect' }
            $response = $client.GetAsync($uri,[Net.Http.HttpCompletionOption]::ResponseHeadersRead,$downloadDeadline.Token).GetAwaiter().GetResult()
            try {
                if ([int]$response.StatusCode -in @(301,302,303,307,308)) { $Address = [Uri]::new($uri,$response.Headers.Location).AbsoluteUri; continue }
                if ([int]$response.StatusCode -ne 200) { throw "Release download returned HTTP $([int]$response.StatusCode)" }
                if ($response.Content.Headers.ContentLength -gt $Limit) { throw 'Release download exceeds limit' }
                $stream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
                $output = [IO.MemoryStream]::new()
                try {
                    $buffer = New-Object byte[] 65536
                    while (($count = $stream.ReadAsync($buffer,0,$buffer.Length,$downloadDeadline.Token).GetAwaiter().GetResult()) -gt 0) {
                        if ($output.Length + $count -gt $Limit) { throw 'Release download exceeds limit' }
                        $output.Write($buffer,0,$count)
                    }
                    return ,$output.ToArray()
                } finally { $stream.Dispose(); $output.Dispose() }
            } finally { $response.Dispose() }
        }
        throw 'Too many release redirects'
    } finally { $downloadDeadline.Dispose(); $client.Dispose(); $handler.Dispose() }
}
function Get-SHA256([byte[]]$Bytes) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-','').ToLowerInvariant() } finally { $sha.Dispose() }
}
$asset = "Laodi-skills-$Version-windows-amd64.zip"
if ($PackagePath) {
    $file = Get-Item -LiteralPath $PackagePath
    if ($file.Length -gt 134217728 -or $file.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Invalid local development archive' }
    if ($ExpectedSHA256 -cnotmatch '^[a-f0-9]{64}$') { throw 'Local archive requires an explicit lowercase SHA256' }
    $archiveBytes = [IO.File]::ReadAllBytes($file.FullName)
    $checksum = $ExpectedSHA256
} else {
    $base = "https://github.com/Shenrui-Ma/Laodi-skills/releases/download/$Version/"
    $checksums = [Text.Encoding]::UTF8.GetString((Get-OfficialBytes ($base + 'SHA256SUMS-windows') 65536))
    $matching = @($checksums -split '\r?\n' | Where-Object { $_ -cmatch ('^[a-f0-9]{64}  ' + [Regex]::Escape($asset) + '$') })
    if ($matching.Count -ne 1) { throw 'Release checksum entry missing or ambiguous' }
    $checksum = $matching[0].Substring(0,64)
    $archiveBytes = Get-OfficialBytes ($base + $asset) 134217728
}
if ((Get-SHA256 $archiveBytes) -cne $checksum) { throw 'Release checksum mismatch; existing installation unchanged' }
$localRoot = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
$localRoot = [IO.Path]::GetFullPath($localRoot)
if ($localRoot -notmatch '^[A-Za-z]:\\' -or $localRoot.Substring(2).Contains(':')) { throw 'Private staging requires a local drive path without streams' }
$drive = [IO.DriveInfo]::new([IO.Path]::GetPathRoot($localRoot))
if ($drive.DriveType -ne [IO.DriveType]::Fixed -or $drive.DriveFormat -notin @('NTFS','ReFS')) { throw 'Private staging requires a local fixed NTFS or ReFS volume' }
for ($ancestor = [IO.DirectoryInfo]::new($localRoot); $null -ne $ancestor; $ancestor = $ancestor.Parent) {
    if ($ancestor.Exists -and ($ancestor.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Private staging rejects reparse points and junctions' }
}
$stage = [IO.Path]::GetFullPath((Join-Path $localRoot ('Laodi-stage-' + [Guid]::NewGuid().ToString('N'))))
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
$acl = [Security.AccessControl.DirectorySecurity]::new()
$acl.SetAccessRuleProtection($true,$false)
$acl.SetOwner($sid)
foreach ($identity in @($sid,[Security.Principal.SecurityIdentifier]::new('S-1-5-18'))) {
    $rule = [Security.AccessControl.FileSystemAccessRule]::new($identity,'FullControl','ContainerInherit,ObjectInherit','None','Allow')
    $acl.AddAccessRule($rule)
}
# .NET Framework's CreateDirectory ACL overload creates the restricted object
# atomically; do not create a broadly inherited directory and tighten it later.
if ($PSVersionTable.PSEdition -eq 'Core') {
    [IO.FileSystemAclExtensions]::Create([IO.DirectoryInfo]::new($stage),$acl)
} else { [IO.Directory]::CreateDirectory($stage,$acl) | Out-Null }
$stream = $null
$zip = $null
try {
    $createdACL = Get-Acl -LiteralPath $stage
    if (-not $createdACL.AreAccessRulesProtected -or $createdACL.GetOwner([Security.Principal.SecurityIdentifier]).Value -cne $sid.Value) { throw 'Private staging ACL ownership or inheritance mismatch' }
    foreach ($entryACL in $createdACL.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier])) {
        if ($entryACL.IdentityReference.Value -cnotin @($sid.Value,'S-1-5-18') -or $entryACL.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) { throw 'Private staging ACL grants unexpected access' }
    }
    $stream = [IO.MemoryStream]::new($archiveBytes,$false)
    $zip = [IO.Compression.ZipArchive]::new($stream,[IO.Compression.ZipArchiveMode]::Read)
    if ($zip.Entries.Count -lt 5 -or $zip.Entries.Count -gt 8) { throw 'Unexpected ZIP entry count' }
    $allowed = @('laodi.exe','laodi-host.exe','LaodiNotify.exe','laodi-logo.png','SKILL.md','usage.md','windows-manifest.json')
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    [int64]$expanded = 0
    foreach ($entry in $zip.Entries) {
        $mode = (([int64]$entry.ExternalAttributes -band 4294967295) -shr 16) -band 61440
        if ($entry.FullName -cnotin $allowed -or -not $seen.Add($entry.FullName) -or $mode -notin @(0,32768) -or ($entry.ExternalAttributes -band 1040)) { throw 'Unsafe, duplicate or unexpected ZIP entry' }
        $expanded += $entry.Length
        if ($entry.Length -gt 67108864 -or $expanded -gt 134217728) { throw 'ZIP expanded size limit exceeded' }
    }
    foreach ($entry in $zip.Entries) {
        $destination = Join-Path $stage $entry.FullName
        $inputStream = $entry.Open()
        $outputStream = [IO.FileStream]::new($destination,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None,65536,[IO.FileOptions]::WriteThrough)
        try {
            $buffer = New-Object byte[] 65536
            [int64]$written = 0
            while (($count = $inputStream.Read($buffer,0,$buffer.Length)) -gt 0) {
                $written += $count
                if ($written -gt $entry.Length -or $written -gt 67108864) { throw 'ZIP entry exceeds declared size' }
                $outputStream.Write($buffer,0,$count)
            }
            if ($written -ne $entry.Length) { throw 'ZIP entry length mismatch' }
            $outputStream.Flush($true)
        } finally { $inputStream.Dispose(); $outputStream.Dispose() }
        if ((Get-Item -LiteralPath $destination).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Extracted reparse point rejected' }
    }
    $manifest = Get-Content -LiteralPath (Join-Path $stage 'windows-manifest.json') -Raw | ConvertFrom-Json
    if ($manifest.schema -ne 1 -or $manifest.protocol -ne 1 -or $manifest.version -cne $Version) { throw 'Package version or protocol mismatch' }
    if (@($manifest.files.PSObject.Properties).Count -ne $zip.Entries.Count - 1) { throw 'Package file count mismatch' }
    foreach ($required in @('laodi.exe','laodi-host.exe','laodi-logo.png','SKILL.md')) {
        if ($required -cnotin @($manifest.files.PSObject.Properties.Name)) { throw 'Required executable or asset is absent from the payload manifest' }
    }
    foreach ($property in $manifest.files.PSObject.Properties) {
        if ($property.Name -cnotin $allowed -or $property.Name -ceq 'windows-manifest.json' -or $property.Value -cnotmatch '^[a-f0-9]{64}$') { throw 'Invalid payload manifest' }
        if ((Get-FileHash -LiteralPath (Join-Path $stage $property.Name) -Algorithm SHA256).Hash.ToLowerInvariant() -cne $property.Value) { throw 'Payload hash mismatch' }
    }
    $arguments = @('install','--source-dir',$stage,'--format',$Format)
    if ($StateDir) { $arguments += @('--state-dir',$StateDir) }
	if ($ClientExecutable) { $arguments += @('--client-exe',$ClientExecutable) }
    if ($DryRun) { $arguments += '--dry-run' }
    if ($NoNotifications) { $arguments += '--no-notifications' }
    & (Join-Path $stage 'laodi.exe') @arguments
    if ($LASTEXITCODE -ne 0) { throw "Installer failed (exit $LASTEXITCODE); inspect its result" }
} finally {
    if ($null -ne $zip) { $zip.Dispose() }
    if ($null -ne $stream) { $stream.Dispose() }
    $resolvedStage = [IO.Path]::GetFullPath($stage)
    if ([IO.Path]::GetDirectoryName($resolvedStage) -eq [IO.Path]::GetFullPath($localRoot) -and [IO.Path]::GetFileName($resolvedStage) -match '^Laodi-stage-[a-f0-9]{32}$') {
        # Packages are flat. Delete only direct files and then the empty
        # directory, so a planted junction never triggers recursive traversal.
        try {
            $stageItem = Get-Item -LiteralPath $resolvedStage -Force
            if ($stageItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Stage became a reparse point; retained' }
            foreach ($child in Get-ChildItem -LiteralPath $resolvedStage -Force) {
                if ($child.PSIsContainer -or ($child.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Unexpected stage directory or reparse point; retained' }
                Remove-Item -LiteralPath $child.FullName -Force
            }
            Remove-Item -LiteralPath $resolvedStage -Force
        } catch { Write-Warning 'Private temporary package could not be fully cleaned; existing installation was not removed' }
    }
}
