[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$StableLauncher,
    [Parameter(Mandatory=$true)][string]$OutputPath
)
# Invokes only the owned COM callback. No toast API, Show, scheduled task,
# registration change, user-input payload, or process termination is involved.
. (Join-Path $PSScriptRoot '..\..\..\scripts\dev\windows-test-common.ps1')
$StableLauncher=(Resolve-Path -LiteralPath $StableLauncher).Path
if([IO.Path]::GetFileName($StableLauncher) -cne 'laodi-host.exe'){throw 'Expected the stable laodi-host.exe launcher.'}
$root=[IO.Path]::GetDirectoryName($StableLauncher)
$current=[IO.File]::ReadAllText((Join-Path $root 'current-version.json'))|ConvertFrom-Json
$receipt=Join-Path $root 'notification-install.json'
$receiptHash=(Get-FileHash -LiteralPath $receipt -Algorithm SHA256).Hash
$helper=Join-Path $root ('versions\'+$current.manifest.version+'\LaodiNotify.exe')
$expectedHash=$current.manifest.files.'LaodiNotify.exe'
if((Get-FileHash -LiteralPath $helper -Algorithm SHA256).Hash -ine $expectedHash){throw 'Selected helper differs from protected version selection.'}
function Get-OwnedHelpers {
    foreach($process in @(Get-Process -Name 'LaodiNotify' -ErrorAction SilentlyContinue)){
        try{if($process.Path -ieq $helper){$process}}catch{}
    }
}
if(@(Get-OwnedHelpers).Count -ne 0){throw 'Selected helper is already running; wait for its bounded lifetime before activation test.'}
$started=[DateTime]::UtcNow
$result=Invoke-LaodiTestProcess -Executable $StableLauncher -Arguments @('--test-activation') -TimeoutMilliseconds 16000
$body=$result.Stdout|ConvertFrom-Json
$servers=@(Get-OwnedHelpers)
$verified=$result.ExitCode -eq 0 -and $body.ok -and $body.action -eq 'test-activation' -and $body.activation -eq 'callback_completed' -and $body.delivery -eq 'not_requested' -and $body.notification_api_calls -eq 0
$freshServer=$servers.Count -eq 1 -and $servers[0].StartTime.ToUniversalTime() -ge $started
$receiptUnchanged=(Get-FileHash -LiteralPath $receipt -Algorithm SHA256).Hash -ceq $receiptHash
$summary=[ordered]@{
    schema_version=1
    verification_passed=($verified -and $freshServer -and $receiptUnchanged)
    started_utc=$started.ToString('o')
    completed_utc=[DateTime]::UtcNow.ToString('o')
    callback=$body
    selected_payload_verified=$freshServer
    com_server_process_count=$servers.Count
    registration_receipt_unchanged=$receiptUnchanged
    notification_api_calls=0
    visible_click_verified=$false
    server_lifetime_limit_seconds=15
}
Write-LaodiTestJSON ([IO.Path]::GetFullPath($OutputPath)) $summary
$summary|ConvertTo-Json -Depth 8
if(-not $summary.verification_passed){throw 'Owned COM callback chain was not verified; evidence retained.'}
