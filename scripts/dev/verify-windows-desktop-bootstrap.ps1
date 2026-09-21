[CmdletBinding()]
param([string]$Go='go',[switch]$Desktop)
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'windows-test-common.ps1')
$repo=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseFile((Join-Path $repo 'install.ps1'),[ref]$tokens,[ref]$errors)
if($errors.Count){throw $errors[0]}
foreach($name in @('Initialize-LaodiDesktopBridge','Invoke-LaodiDesktopInstaller')) {
    $definition=$ast.Find({param($node)$node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name},$true)
    if($null -eq $definition){throw "Missing installer function: $name"}
    Invoke-Expression $definition.Extent.Text
}
Initialize-LaodiDesktopBridge
$assignment=$ast.Find({param($node)$node -is [Management.Automation.Language.AssignmentStatementAst] -and $node.Left.Extent.Text -eq '$worker'},$true)
Invoke-Expression $assignment.Extent.Text
$root=New-LaodiPrivateTestDirectory
$sid=[Security.Principal.WindowsIdentity]::GetCurrent().User
$safeToClean=$true
$cases=[Collections.Generic.List[object]]::new()
try {
    $payload=Join-Path $root 'payload';New-Item -ItemType Directory -Path $payload|Out-Null
    $source=Join-Path $root 'fixture.go'
    [IO.File]::WriteAllText($source,@'
package main
import("encoding/json";"fmt";"os";"time")
func main(){json.NewEncoder(os.Stdout).Encode(os.Args[1:]);fmt.Fprint(os.Stderr,"synthetic stderr \u4e2d\u6587");for _,arg:=range os.Args[1:]{if arg=="exit37"{os.Exit(37)};if arg=="sleep"{time.Sleep(4*time.Second)}}}
'@,[Text.UTF8Encoding]::new($false))
    & $Go build -o (Join-Path $payload 'laodi.exe') $source
    if($LASTEXITCODE){throw 'Cannot build desktop fixture'}
    $physicalPayload=Split-Path -Parent ([LaodiBootstrap.Desktop]::FinalFile((Join-Path $payload 'laodi.exe')))
    $hash=(Get-FileHash -LiteralPath (Join-Path $payload 'laodi.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $unicode=[string][char]0x4e2d+[char]0x6587
    foreach($name in @('arguments','exit-code','checksum','user')) {
        $bridge=Join-Path $root $name;New-Item -ItemType Directory -Path $bridge|Out-Null
        $arguments=@('space path','',('C:\'+$unicode+'\'), 'quote"tail\', '$() & ; ` % !', 'double\\slash')
        if($name -eq 'exit-code'){$arguments+= 'exit37'}
        $wantHash=if($name -eq 'checksum'){'0'*64}else{$hash}
        $realSID=$sid
        if($name -eq 'user'){$sid=[pscustomobject]@{Value='S-1-0-0'}}
        try {
            if($Desktop) {
                $retained=$false
                $result=Invoke-LaodiDesktopInstaller $physicalPayload $arguments $bridge $wantHash ([ref]$retained)
                if($retained){throw 'Completed handoff retained staging'}
            } else {
                $request=@{executable=(Join-Path $physicalPayload 'laodi.exe');arguments=$arguments;sha256=$wantHash;user_sid=$sid.Value}
                [IO.File]::WriteAllText((Join-Path $bridge 'request.json'),($request|ConvertTo-Json -Depth 4),[Text.UTF8Encoding]::new($false))
                [IO.File]::WriteAllText((Join-Path $bridge 'worker.ps1'),$worker,[Text.UTF8Encoding]::new($false))
                $powershell=Join-Path ([Environment]::GetFolderPath('System')) 'WindowsPowerShell\v1.0\powershell.exe'
                $child=Invoke-LaodiTestProcess -Executable $powershell -Arguments @('-NoProfile','-NonInteractive','-File',(Join-Path $bridge 'worker.ps1'))
                if($child.ExitCode){throw 'Worker failed before reporting a result'}
                $result=Get-Content -LiteralPath (Join-Path $bridge 'result.json') -Raw -Encoding UTF8|ConvertFrom-Json
            }
        } finally {$sid=$realSID}
        $expectedExit=if($name -eq 'exit-code'){37}elseif($name -in @('checksum','user')){1}else{0}
        if($result.exit_code -ne $expectedExit){throw ("Wrong exit code for ${name}: "+$result.stderr)}
        if($name -in @('arguments','exit-code')) {
            $actual=ConvertFrom-Json -InputObject $result.stdout
            if($actual.Count -ne $arguments.Count){throw ('Argument count changed: '+$result.stdout)}
            for($i=0;$i -lt $actual.Count;$i++){if($actual[$i] -cne $arguments[$i]){throw "Argument $i changed"}}
            if($result.stderr -cne ('synthetic stderr '+$unicode)){throw 'UTF-8 stderr changed'}
        } elseif($result.stdout -or $result.stderr -notmatch $(if($name -eq 'checksum'){'checksum changed'}else{'user changed'})){throw 'Worker ran an invalid request'}
        $cases.Add([pscustomobject]@{case=$name;passed=$true;desktop=[bool]$Desktop})
    }
    if($Desktop) {
        $bridge=Join-Path $root 'timeout';New-Item -ItemType Directory -Path $bridge|Out-Null
        $retained=$false;$safeToClean=$false;$timedOut=$false
        try {$null=Invoke-LaodiDesktopInstaller $physicalPayload @('sleep') $bridge $hash ([ref]$retained) 1}
        catch {if($_.Exception.Message -notmatch 'before its deadline'){throw};$timedOut=$true}
        if(-not $timedOut -or -not $retained -or -not (Test-Path -LiteralPath $bridge)){throw 'Uncertain handoff did not retain its files'}
        $deadline=[DateTime]::UtcNow.AddSeconds(15)
        while(-not (Test-Path -LiteralPath (Join-Path $bridge 'result.json'))) {if([DateTime]::UtcNow -ge $deadline){throw 'Synthetic worker did not finish; fixture retained'};Start-Sleep -Milliseconds 200}
        $result=Get-Content -LiteralPath (Join-Path $bridge 'result.json') -Raw -Encoding UTF8|ConvertFrom-Json
        if($result.exit_code -ne 0){throw 'Synthetic timeout worker failed'}
        $safeToClean=$true
        $cases.Add([pscustomobject]@{case='timeout-retention';passed=$true;desktop=$true})
    }
    $cases|ConvertTo-Json
} finally {
    # Only this script's generated private fixture may be recursively removed.
    $resolved=[IO.Path]::GetFullPath($root)
    $parent=[IO.Path]::GetFullPath((Get-LaodiTestTemporaryRoot))
    if($safeToClean -and [IO.Path]::GetDirectoryName($resolved) -eq $parent -and [IO.Path]::GetFileName($resolved) -match '^laodi-windows-synthetic-[a-f0-9]{32}$') {Remove-Item -LiteralPath $resolved -Recurse -Force}
}
