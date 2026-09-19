[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$HelperA,
    [Parameter(Mandatory=$true)][string]$HelperB,
    [Parameter(Mandatory=$true)][string]$StableHost,
    [Parameter(Mandatory=$true)][string]$OutputPath
)
# Own-AUMID integration test. No --send call, scheduled task or OS policy change.
. (Join-Path $PSScriptRoot '..\..\..\scripts\dev\windows-test-common.ps1')
$HelperA=(Resolve-Path -LiteralPath $HelperA).Path
$HelperB=(Resolve-Path -LiteralPath $HelperB).Path
$StableHost=(Resolve-Path -LiteralPath $StableHost).Path
$root=New-LaodiPrivateTestDirectory
$versionA=Join-Path $root 'versions\v0.0.0-synthetic-a'
$versionB=Join-Path $root 'versions\v0.0.0-synthetic-b'
[void][IO.Directory]::CreateDirectory($versionA)
[void][IO.Directory]::CreateDirectory($versionB)
Copy-Item -LiteralPath $HelperA -Destination (Join-Path $versionA 'LaodiNotify.exe')
Copy-Item -LiteralPath $HelperB -Destination (Join-Path $versionB 'LaodiNotify.exe')
Copy-Item -LiteralPath $StableHost -Destination (Join-Path $root 'laodi-host.exe')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\..\..\assets\laodi-logo.png') -Destination $root
$a=Join-Path $versionA 'LaodiNotify.exe'
$b=Join-Path $versionB 'LaodiNotify.exe'
$receipt=Join-Path $root 'notification-install.json'
$pending=Join-Path $root 'notification-install.pending.json'
$icon=Join-Path $root 'notification-icon.ico'
$shortcut=Join-Path ([Environment]::GetFolderPath('Programs')) 'Laodi-skills.lnk'
$appKey='Software\Classes\AppUserModelId\dev.laodi.guardian'
$summary=[ordered]@{schema_version=1;verification_passed=$false;assertions=0;notification_send_invocations=0;registration_changes_scope='exact own AUMID, CLSID and shortcut';status=$null;cleaned=$false}
function Invoke-Notice([string]$Helper,[string]$Action) {
    $result=Invoke-LaodiTestProcess -Executable $Helper -Arguments @('--state-dir',$root,$Action)
    $body=$result.Stdout|ConvertFrom-Json
    return [pscustomobject]@{ExitCode=$result.ExitCode;Body=$body}
}
function Assert-Notice([bool]$Condition,[string]$Message) {
    $summary.assertions++
    if(-not $Condition){throw $Message}
}
try {
    $created=Invoke-Notice $a '--register'
    Assert-Notice ($created.ExitCode -eq 0 -and $created.Body.ok) 'Registration failed or another installation owns the AUMID.'
    $originalReceipt=[IO.File]::ReadAllBytes($receipt)
    $status=Invoke-Notice $a '--status'
    $summary.status=$status.Body
    Assert-Notice ($status.ExitCode -eq 0 -and $status.Body.registration -eq 'verified') 'Owned registration status failed.'
    $second=Invoke-Notice $b '--register'
    Assert-Notice ($second.ExitCode -eq 0) 'New version helper rejected stable ownership.'
    Assert-Notice ([Convert]::ToBase64String($originalReceipt) -ceq [Convert]::ToBase64String([IO.File]::ReadAllBytes($receipt))) 'Version switch changed notification identity receipt.'

    $originalIcon=[IO.File]::ReadAllBytes($icon)
    try {
        [IO.File]::AppendAllText($icon,'SYNTHETIC-USER-EDIT')
        $refused=Invoke-Notice $b '--unregister'
        Assert-Notice ($refused.ExitCode -ne 0 -and (Test-Path -LiteralPath $receipt) -and (Test-Path -LiteralPath $shortcut)) 'Changed icon was removed.'
    } finally { [IO.File]::WriteAllBytes($icon,$originalIcon) }

    $key=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($appKey,$true)
    try {
        $key.SetValue('SyntheticForeignValue','preserve')
        $refused=Invoke-Notice $b '--unregister'
        Assert-Notice ($refused.ExitCode -ne 0 -and (Test-Path -LiteralPath $shortcut)) 'Changed registration was removed.'
    } finally { $key.DeleteValue('SyntheticForeignValue');$key.Dispose() }

    # Model interruption with only a subset of the exact recorded registry
    # values published; pending ownership must recover without overwriting.
    [IO.File]::Move($receipt,$pending)
    [IO.File]::Delete($shortcut)
    $key=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($appKey,$true)
    try{$key.DeleteValue('CustomActivator')}finally{$key.Dispose()}
    $recovered=Invoke-Notice $b '--register'
    Assert-Notice ($recovered.ExitCode -eq 0 -and (Test-Path -LiteralPath $receipt) -and -not(Test-Path -LiteralPath $pending)) 'Partial registration did not recover.'

    # Interrupted removal can lack both shortcut and extracted icon, while the
    # pending receipt still protects the remaining registry configuration.
    [IO.File]::Move($receipt,$pending)
    [IO.File]::Delete($shortcut)
    [IO.File]::Delete($icon)
    $removed=Invoke-Notice $b '--unregister'
    Assert-Notice ($removed.ExitCode -eq 0 -and -not(Test-Path -LiteralPath $pending)) 'Partial removal did not recover.'
    $summary.verification_passed=$true
} finally {
    if((Test-Path -LiteralPath $receipt) -or (Test-Path -LiteralPath $pending)){
        $cleanup=Invoke-Notice $b '--unregister'
        if($cleanup.ExitCode -ne 0){throw 'Exact notification cleanup failed; receipt retained for review.'}
    }
    $key=[Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($appKey)
    $summary.cleaned= -not(Test-Path -LiteralPath $receipt) -and -not(Test-Path -LiteralPath $pending) -and -not(Test-Path -LiteralPath $shortcut) -and $null -eq $key
    if($key){$key.Dispose()}
    Write-LaodiTestJSON ([IO.Path]::GetFullPath($OutputPath)) $summary
}
$summary|ConvertTo-Json -Depth 8
if(-not $summary.cleaned){throw 'Owned notification artifacts were not fully cleaned.'}
