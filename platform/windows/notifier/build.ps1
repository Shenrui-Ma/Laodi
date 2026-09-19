param([string]$OutputDirectory = (Join-Path $PSScriptRoot '..\..\..\bin\windows-notifier'))
$ErrorActionPreference = 'Stop'
$framework = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319'
$compiler = Join-Path $framework 'csc.exe'
$runtimeFacade = Join-Path $env:WINDIR 'Microsoft.NET\assembly\GAC_MSIL\System.Runtime\v4.0_4.0.0.0__b03f5f7f11d50a3a\System.Runtime.dll'
$metadata = Join-Path $env:WINDIR 'System32\WinMetadata'
$references = @((Join-Path $metadata 'Windows.UI.winmd'), (Join-Path $metadata 'Windows.Data.winmd'), (Join-Path $metadata 'Windows.Foundation.winmd'), (Join-Path $framework 'System.Runtime.WindowsRuntime.dll'), $runtimeFacade)
foreach ($dependency in (@($compiler) + $references)) { if (-not (Test-Path -LiteralPath $dependency -PathType Leaf)) { throw "Required Windows/.NET Framework build component missing: $dependency" } }
$null = New-Item -ItemType Directory -Force -Path $OutputDirectory
$OutputDirectory = (Resolve-Path -LiteralPath $OutputDirectory).Path
$logo = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..\..\assets\laodi-logo.png')).Path
Copy-Item -LiteralPath $logo -Destination (Join-Path $OutputDirectory 'laodi-logo.png') -Force
Add-Type -AssemblyName System.Drawing
$sourceImage = [Drawing.Image]::FromFile($logo)
$iconBitmap = [Drawing.Bitmap]::new($sourceImage, [Drawing.Size]::new(128,128))
$icon = [Drawing.Icon]::FromHandle($iconBitmap.GetHicon())
$iconPath = Join-Path $OutputDirectory 'laodi-logo.ico'
$iconStream = [IO.File]::Create($iconPath)
try { $icon.Save($iconStream) } finally { $iconStream.Dispose(); $icon.Dispose(); $iconBitmap.Dispose(); $sourceImage.Dispose() }
$arguments = @('/nologo','/target:winexe','/platform:x64','/optimize+','/utf8output',('/out:' + (Join-Path $OutputDirectory 'LaodiNotify.exe')),('/win32icon:' + $iconPath), '/r:System.Web.Extensions.dll')
$arguments += $references | ForEach-Object { '/r:' + $_ }
$arguments += Join-Path $PSScriptRoot 'Main.cs'
& $compiler @arguments
if ($LASTEXITCODE -ne 0) { throw "Notifier build failed with exit code $LASTEXITCODE" }
Write-Output (Join-Path $OutputDirectory 'LaodiNotify.exe')
