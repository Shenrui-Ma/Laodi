[CmdletBinding()]
param([Parameter(Mandatory=$true)][string]$Package,[Parameter(Mandatory=$true)][string]$Version,[Parameter(Mandatory=$true)][string]$WorkDir)
$ErrorActionPreference='Stop'
$repo=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$WorkDir=[IO.Path]::GetFullPath($WorkDir)
New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$bootstrapPackage=(Resolve-Path -LiteralPath $Package).Path
$bootstrapResults=[Collections.Generic.List[object]]::new()
$bootstrapCases=@('../outside','C:/outside','//server/share','laodi.exe:stream','NUL','laodi.exe.','laodi.exe ','LAODI.EXE','laodi.exe','folder/file')
for ($caseIndex=0;$caseIndex -lt $bootstrapCases.Count;$caseIndex++) {
    $casePath=Join-Path $WorkDir ("malicious-$caseIndex.zip")
    Copy-Item -LiteralPath $bootstrapPackage -Destination $casePath
    $caseZip=[IO.Compression.ZipFile]::Open($casePath,[IO.Compression.ZipArchiveMode]::Update)
    try { $entry=$caseZip.CreateEntry($bootstrapCases[$caseIndex]);$writer=[IO.StreamWriter]::new($entry.Open());try{$writer.Write('synthetic')}finally{$writer.Dispose()} } finally {$caseZip.Dispose()}
    $caseHash=(Get-FileHash -LiteralPath $casePath -Algorithm SHA256).Hash.ToLowerInvariant()
    $rejected=$false
    try { & (Join-Path $repo 'install.ps1') -Version $Version -PackagePath $casePath -ExpectedSHA256 $caseHash -StateDir (Join-Path $WorkDir 'must-not-install') -DryRun -NoNotifications -Format json | Out-Null }
    catch { $rejected=$true }
    if (-not $rejected) { throw "Bootstrap accepted malicious case $caseIndex" }
    $bootstrapResults.Add([pscustomobject]@{id="zip-$caseIndex";result='rejected';expected='reject before executable launch'})
}
try { & (Join-Path $repo 'install.ps1') -Version $Version -PackagePath $bootstrapPackage -ExpectedSHA256 ('0'*64) -DryRun -NoNotifications | Out-Null;throw 'Expected checksum rejection' }
catch { if ($_.Exception.Message -eq 'Expected checksum rejection') {throw} }
$bootstrapResults.Add([pscustomobject]@{id='checksum';result='rejected';expected='existing installation unchanged'})
if (Test-Path -LiteralPath (Join-Path $WorkDir 'must-not-install')) {throw 'Invalid archive changed installation'}
$bootstrapResults | ConvertTo-Json -Depth 4
