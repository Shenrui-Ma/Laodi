# Windows development entry point. A Windows release is not advertised until the
# verification gate in docs/WINDOWS.md is satisfied.
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
# A packaged desktop host can redirect AppData even when its child has no
# package identity. Only hand the verified installer to the current desktop's
# Shell; keep the real installer's path/owner checks and transaction intact.
function Initialize-LaodiDesktopBridge {
    if ('LaodiBootstrap.Desktop' -as [type]) { return }
    Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Text;
using System.Reflection;
using System.Diagnostics;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Principal;
namespace LaodiBootstrap {
 public static class Desktop {
  [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern uint GetFinalPathNameByHandleW(IntPtr handle,StringBuilder path,uint count,uint flags);
  [DllImport("user32.dll",SetLastError=true)] static extern uint GetWindowThreadProcessId(IntPtr window,out uint pid);
  [DllImport("kernel32.dll",SetLastError=true)] static extern IntPtr OpenProcess(uint access,bool inherit,uint pid);
  [DllImport("advapi32.dll",SetLastError=true)] static extern bool OpenProcessToken(IntPtr process,uint access,out IntPtr token);
  [DllImport("advapi32.dll",SetLastError=true)] static extern bool GetTokenInformation(IntPtr token,int kind,out uint value,uint size,out uint needed);
  [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
  [ComImport,Guid("85CB6900-4D95-11CF-960C-0080C7F4EE85"),InterfaceType(ComInterfaceType.InterfaceIsIDispatch)]
  interface ShellWindows { [return:MarshalAs(UnmanagedType.IDispatch)] object FindWindowSW(ref object location,ref object root,int kind,out int window,int options); }
  [UnmanagedFunctionPointer(CallingConvention.StdCall)] delegate int QueryInterface(IntPtr self,ref Guid iid,out IntPtr value);
  [UnmanagedFunctionPointer(CallingConvention.StdCall)] delegate int QueryService(IntPtr self,ref Guid service,ref Guid iid,out IntPtr value);
  [UnmanagedFunctionPointer(CallingConvention.StdCall)] delegate int ActiveView(IntPtr self,out IntPtr view);
  [UnmanagedFunctionPointer(CallingConvention.StdCall)] delegate int ItemObject(IntPtr self,uint item,ref Guid iid,out IntPtr value);
  static Delegate Method(IntPtr self,int slot,Type type) { return Marshal.GetDelegateForFunctionPointer(Marshal.ReadIntPtr(Marshal.ReadIntPtr(self),slot*IntPtr.Size),type); }
  static void Check(int hr) { Marshal.ThrowExceptionForHR(hr); }
  static void Release(object value) { if(value!=null && Marshal.IsComObject(value))Marshal.ReleaseComObject(value); }
  static bool Elevated(IntPtr token) { uint value,needed;if(!GetTokenInformation(token,20,out value,4,out needed))throw new Win32Exception();return value!=0; }
  public static string FinalFile(string path) {
   using(var file=new FileStream(path,FileMode.Open,FileAccess.Read,FileShare.Read)) {
    var text=new StringBuilder(32768);
    uint count=GetFinalPathNameByHandleW(file.SafeFileHandle.DangerousGetHandle(),text,32768,0);
    if(count==0)throw new Win32Exception();
    if(count>=32768)throw new IOException("Installer path exceeds Windows limit");
    string result=text.ToString();
    if(!result.StartsWith(@"\\?\",StringComparison.Ordinal) || result.Length<7 || result[5]!=':')throw new IOException("Installer requires a local drive path");
    return result.Substring(4);
   }
  }
  // IShellWindows -> desktop IServiceProvider -> IShellBrowser -> IShellView
  // -> desktop automation Application. Creating Shell.Application directly
  // would execute inside the caller's redirected environment instead.
  // https://devblogs.microsoft.com/oldnewthing/20131118-00/?p=2643
  public static void Launch(string executable,string arguments,string directory) {
   object windows=null,desktop=null,folder=null,application=null;
   IntPtr provider=IntPtr.Zero,browser=IntPtr.Zero,view=IntPtr.Zero,dispatch=IntPtr.Zero;
   try {
    windows=Activator.CreateInstance(Type.GetTypeFromCLSID(new Guid("9BA05972-F6A8-11CF-A442-00A0C90A8F39"),true));
    object location=0,root=null; int window;
    desktop=((ShellWindows)windows).FindWindowSW(ref location,ref root,8,out window,1);
    if(desktop==null || window==0)throw new InvalidOperationException("An interactive Windows desktop is required for automatic installation");
    uint pid; if(GetWindowThreadProcessId(new IntPtr(window),out pid)==0)throw new Win32Exception();
    using(var shell=Process.GetProcessById((int)pid))using(var caller=Process.GetCurrentProcess()) {
     if(shell.SessionId!=caller.SessionId)throw new InvalidOperationException("Desktop belongs to a different session");
    }
    IntPtr process=OpenProcess(0x1000,false,pid),token=IntPtr.Zero;
    if(process==IntPtr.Zero)throw new Win32Exception();
    try {
     if(!OpenProcessToken(process,8,out token))throw new Win32Exception();
     using(var identity=new WindowsIdentity(token))using(var caller=WindowsIdentity.GetCurrent()) {
      if(identity.User!=caller.User)throw new InvalidOperationException("Desktop belongs to a different user");
      if(Elevated(token) && !Elevated(caller.Token))throw new InvalidOperationException("Desktop would elevate the installer");
     }
    } finally { if(token!=IntPtr.Zero)CloseHandle(token);CloseHandle(process); }
    IntPtr unknown=Marshal.GetIUnknownForObject(desktop);
    try { var iid=new Guid("6D5140C1-7436-11CE-8034-00AA006009FA");Check(((QueryInterface)Method(unknown,0,typeof(QueryInterface)))(unknown,ref iid,out provider)); } finally {Marshal.Release(unknown);}
    var service=new Guid("4C96BE40-915C-11CF-99D3-00AA004AE837");var browserIID=new Guid("000214E2-0000-0000-C000-000000000046");
    Check(((QueryService)Method(provider,3,typeof(QueryService)))(provider,ref service,ref browserIID,out browser));
    Check(((ActiveView)Method(browser,15,typeof(ActiveView)))(browser,out view));
    var dispatchIID=new Guid("00020400-0000-0000-C000-000000000046");
    Check(((ItemObject)Method(view,15,typeof(ItemObject)))(view,0,ref dispatchIID,out dispatch));
    folder=Marshal.GetObjectForIUnknown(dispatch);
    application=folder.GetType().InvokeMember("Application",BindingFlags.GetProperty,null,folder,null);
    application.GetType().InvokeMember("ShellExecute",BindingFlags.InvokeMethod,null,application,new object[]{executable,arguments,directory,"open",0});
   } finally {
    if(dispatch!=IntPtr.Zero)Marshal.Release(dispatch);if(view!=IntPtr.Zero)Marshal.Release(view);if(browser!=IntPtr.Zero)Marshal.Release(browser);if(provider!=IntPtr.Zero)Marshal.Release(provider);
    Release(application);Release(folder);Release(desktop);Release(windows);
   }
  }
 }
}
'@
}
function Invoke-LaodiDesktopInstaller([string]$Payload,[string[]]$InstallerArguments,[string]$Bridge,[string]$InstallerHash,[ref]$RetainStage,[ValidateRange(1,300)][int]$WaitSeconds=300) {
    # Pass data as JSON, never as executable PowerShell text. The wrapper and
    # payload are held read-only until completion. A timeout is indeterminate:
    # retain both private directories and never launch a second installation.
    $request = [ordered]@{ executable=(Join-Path $Payload 'laodi.exe'); arguments=$InstallerArguments; sha256=$InstallerHash; user_sid=$sid.Value }
    [IO.File]::WriteAllText((Join-Path $Bridge 'request.json'),($request | ConvertTo-Json -Depth 4),[Text.UTF8Encoding]::new($false))
    $PhysicalBridge=Split-Path -Parent ([LaodiBootstrap.Desktop]::FinalFile((Join-Path $Bridge 'request.json')))
    $worker = @'
$ErrorActionPreference='Stop'
$exitCode=1
$output=''
$errors=''
try {
    # A PowerShell 7 host can export an incompatible PSModulePath to 5.1.
    foreach($module in @('Microsoft.PowerShell.Utility','Microsoft.PowerShell.Management')) {
        Import-Module (Join-Path $PSHOME ('Modules\'+$module+'\'+$module+'.psd1')) -ErrorAction Stop
    }
    $request=Get-Content -LiteralPath (Join-Path $PSScriptRoot 'request.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -cne $request.user_sid) { throw 'Desktop installer user changed' }
    if ((Get-FileHash -LiteralPath $request.executable -Algorithm SHA256).Hash.ToLowerInvariant() -cne $request.sha256) { throw 'Desktop installer checksum changed' }
    function Quote-Argument([string]$value) {
        # Windows C runtime argument quoting; no command interpreter is used.
        $quoted=[Text.StringBuilder]::new('"');$slashes=0
        foreach($character in $value.ToCharArray()) {
            if($character -eq '\'){$slashes++;continue}
            if($character -eq '"'){[void]$quoted.Append(('\'*($slashes*2+1))).Append('"')}
            else {[void]$quoted.Append(('\'*$slashes)).Append($character)}
            $slashes=0
        }
        [void]$quoted.Append(('\'*($slashes*2))).Append('"');return $quoted.ToString()
    }
    $info=[Diagnostics.ProcessStartInfo]::new()
    $info.FileName=$request.executable
    $info.Arguments=(@($request.arguments | ForEach-Object {Quote-Argument $_}) -join ' ')
    $info.WorkingDirectory=Split-Path -Parent $request.executable
    $info.UseShellExecute=$false;$info.CreateNoWindow=$true
    $info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
    $info.StandardOutputEncoding=[Text.UTF8Encoding]::new($false)
    $info.StandardErrorEncoding=[Text.UTF8Encoding]::new($false)
    $process=[Diagnostics.Process]::new();$process.StartInfo=$info
    try {
        if(-not $process.Start()){throw 'Desktop installer did not start'}
        $stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $output=$stdout.GetAwaiter().GetResult();$errors=$stderr.GetAwaiter().GetResult();$exitCode=$process.ExitCode
    } finally {$process.Dispose()}
} catch {$errors=$_.Exception.Message}
$result=[ordered]@{schema=1;exit_code=$exitCode;stdout=$output;stderr=$errors}
$text=$result | ConvertTo-Json -Depth 3 -Compress
$temporary=Join-Path $PSScriptRoot 'result.pending'
[IO.File]::WriteAllText($temporary,$text,[Text.UTF8Encoding]::new($false))
[IO.File]::Move($temporary,(Join-Path $PSScriptRoot 'result.json'))
'@
    [IO.File]::WriteAllText((Join-Path $Bridge 'worker.ps1'),$worker,[Text.UTF8Encoding]::new($false))
    $pins=[Collections.Generic.List[IDisposable]]::new()
    try {
        foreach($file in @((Get-ChildItem -LiteralPath $Payload -File).FullName) + @((Join-Path $Bridge 'request.json'),(Join-Path $Bridge 'worker.ps1'))) {
            $pins.Add([IO.File]::Open($file,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read))
        }
        $powershell=Join-Path ([Environment]::GetFolderPath('System')) 'WindowsPowerShell\v1.0\powershell.exe'
        $workerPath=Join-Path $PhysicalBridge 'worker.ps1'
        if($workerPath.Contains('"')){throw 'Invalid desktop installer path'}
        $command='-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File "'+$workerPath+'"'
        # Mark uncertain before COM dispatch: a failing RPC can still have
        # launched its process. Do not clean up files out from under it.
        $RetainStage.Value=$true
        [LaodiBootstrap.Desktop]::Launch($powershell,$command,$PhysicalBridge)
        $deadline=[DateTime]::UtcNow.AddSeconds($WaitSeconds)
        $resultPath=Join-Path $Bridge 'result.json'
        while(-not (Test-Path -LiteralPath $resultPath)) {
            if([DateTime]::UtcNow -ge $deadline){throw 'Desktop installation did not report completion before its deadline; do not retry while it may still be running'}
            Start-Sleep -Milliseconds 200
        }
        $resultFile=Get-Item -LiteralPath $resultPath
        if($resultFile.Length -gt 1048576 -or ($resultFile.Attributes -band [IO.FileAttributes]::ReparsePoint)) {throw 'Invalid desktop installer result'}
        if([LaodiBootstrap.Desktop]::FinalFile($resultPath) -ine (Join-Path $PhysicalBridge 'result.json')){throw 'Desktop installer result path changed'}
        $result=Get-Content -LiteralPath $resultPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if($result.schema -ne 1 -or ($result.exit_code -isnot [int] -and $result.exit_code -isnot [long]) -or $result.exit_code -lt [int]::MinValue -or $result.exit_code -gt [int]::MaxValue -or $result.stdout -isnot [string] -or $result.stderr -isnot [string]){throw 'Incomplete desktop installer result'}
        $RetainStage.Value=$false
        return $result
    } finally {foreach($pin in $pins){$pin.Dispose()}}
}
$asset = "Laodi-$Version-windows-amd64.zip"
if ($PackagePath) {
    $file = Get-Item -LiteralPath $PackagePath
    if ($file.Length -gt 134217728 -or $file.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Invalid local development archive' }
    if ($ExpectedSHA256 -cnotmatch '^[a-f0-9]{64}$') { throw 'Local archive requires an explicit lowercase SHA256' }
    $archiveBytes = [IO.File]::ReadAllBytes($file.FullName)
    $checksum = $ExpectedSHA256
} else {
    $base = "https://github.com/Shenrui-Ma/Laodi/releases/download/$Version/"
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
$bridgeRoot = $null
$retainStage = $false
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
    Initialize-LaodiDesktopBridge
    $physicalStage=Split-Path -Parent ([LaodiBootstrap.Desktop]::FinalFile((Join-Path $stage 'laodi.exe')))
    $arguments = @('install','--source-dir',$physicalStage,'--format',$Format)
    if ($StateDir) { $arguments += @('--state-dir',$StateDir) }
	if ($ClientExecutable) { $arguments += @('--client-exe',$ClientExecutable) }
    if ($DryRun) { $arguments += '--dry-run' }
    if ($NoNotifications) { $arguments += '--no-notifications' }
    if($physicalStage -ine $stage) {
        if($Format -eq 'text'){Write-Host 'Laodi: completing installation through the current Windows desktop; please wait.'}
        $bridgeRoot=Join-Path $localRoot ('Laodi-stage-'+[Guid]::NewGuid().ToString('N'))
        if($PSVersionTable.PSEdition -eq 'Core'){[IO.FileSystemAclExtensions]::Create([IO.DirectoryInfo]::new($bridgeRoot),$acl)}
        else {[IO.Directory]::CreateDirectory($bridgeRoot,$acl) | Out-Null}
        $result=Invoke-LaodiDesktopInstaller $physicalStage $arguments $bridgeRoot $manifest.files.'laodi.exe' ([ref]$retainStage)
        if($result.stdout){Write-Output $result.stdout.TrimEnd([char[]]"`r`n")}
        if($result.stderr){[Console]::Error.WriteLine($result.stderr.TrimEnd([char[]]"`r`n"))}
        $installerExit=$result.exit_code
    } else {
        & (Join-Path $stage 'laodi.exe') @arguments
        $installerExit=$LASTEXITCODE
    }
    if ($installerExit -ne 0) { throw "Installer failed (exit $installerExit); inspect its result" }
} finally {
    if ($null -ne $zip) { $zip.Dispose() }
    if ($null -ne $stream) { $stream.Dispose() }
    if($retainStage){Write-Warning 'Desktop installation outcome is not confirmed; private staging was retained. Do not start a second installation until its result is known.'}
    foreach($cleanupStage in @($stage,$bridgeRoot)) {
        if(-not $cleanupStage -or $retainStage){continue}
        $resolvedStage = [IO.Path]::GetFullPath($cleanupStage)
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
}
