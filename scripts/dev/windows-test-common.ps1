# Development-only helpers; product installation does not require PowerShell scripts.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# A sampler launched from PowerShell 7 can inherit its module search path.
# Load the host's own modules explicitly before using Windows PowerShell 5.1.
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Utility\Microsoft.PowerShell.Utility.psd1') -ErrorAction Stop
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Management\Microsoft.PowerShell.Management.psd1') -ErrorAction Stop
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Security\Microsoft.PowerShell.Security.psd1') -ErrorAction Stop

function Get-LaodiTestTemporaryRoot {
    # Hosted Windows runners may expose TEMP as C:\Users\RUNNER~1\... .
    # Keep production rejection of short aliases; put fixtures on a long path.
    $root = $env:RUNNER_TEMP
    if (-not $root) {
        $root = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Temp'
    }
    if ($root -notmatch '^[A-Za-z]:[\\/]' -or $root.Contains('~')) {
        throw 'Native tests require a local long-path temporary directory. Set RUNNER_TEMP to one.'
    }
    return [IO.Path]::GetFullPath($root)
}

function New-LaodiPrivateTestDirectory {
    $path = Join-Path (Get-LaodiTestTemporaryRoot) ('laodi-windows-synthetic-' + [Guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($path)
    $owner = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $system = New-Object Security.Principal.SecurityIdentifier('S-1-5-18')
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetOwner($owner)
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit'
    foreach ($sid in @($owner, $system)) {
        $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', $inherit, 'None', 'Allow')
        [void]$acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $path -AclObject $acl
    return $path
}

function ConvertTo-LaodiProcessArgument([string]$Value) {
    # Windows C-runtime quoting. ProcessStartInfo calls the executable directly;
    # neither cmd.exe nor a text pipeline evaluates these arguments.
    $quoted = [Text.StringBuilder]::new('"')
    $slashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq '\') { $slashes++; continue }
        if ($character -eq '"') {
            [void]$quoted.Append(('\' * ($slashes * 2 + 1)))
            [void]$quoted.Append('"')
        } else {
            [void]$quoted.Append(('\' * $slashes))
            [void]$quoted.Append($character)
        }
        $slashes = 0
    }
    [void]$quoted.Append(('\' * ($slashes * 2)))
    [void]$quoted.Append('"')
    return $quoted.ToString()
}

function Start-LaodiTestProcess {
    param([string]$Executable, [string[]]$Arguments, [string]$Directory, [hashtable]$Environment = @{}, [byte[]]$InputBytes = @(), [switch]$KeepInputOpen)
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $Executable
    $info.Arguments = (($Arguments | ForEach-Object { ConvertTo-LaodiProcessArgument $_ }) -join ' ')
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    if ($Directory) { $info.WorkingDirectory = $Directory }
    foreach ($key in $Environment.Keys) { $info.EnvironmentVariables[$key] = [string]$Environment[$key] }
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $info
    if (-not $process.Start()) { throw 'Synthetic child process failed to start.' }
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if ($InputBytes.Length -gt 0) { $process.StandardInput.BaseStream.Write($InputBytes, 0, $InputBytes.Length) }
    if (-not $KeepInputOpen) { $process.StandardInput.Close() }
    return [pscustomobject]@{ Process = $process; Stdout = $stdout; Stderr = $stderr }
}

function Get-LaodiChildResources($Children) {
    $working = 0L; $private = 0L; $handles = 0; $count = 0
    foreach ($child in $Children) {
        if ($child.Process.HasExited) { continue }
        try {
            $child.Process.Refresh()
            $working += $child.Process.WorkingSet64
            $private += $child.Process.PrivateMemorySize64
            $handles += $child.Process.HandleCount
            $count++
        } catch { if (-not $child.Process.HasExited) { throw } }
    }
    return [pscustomobject]@{ process_count = $count; working_set_bytes_sum = $working; private_bytes_sum = $private; handles_sum = $handles }
}

function Complete-LaodiTestProcess {
    param($Child, [int]$TimeoutMilliseconds = 10000)
    if (-not $Child.Process.WaitForExit($TimeoutMilliseconds)) {
        # Only this exact process handle was created by this test harness.
        $Child.Process.Kill()
        $Child.Process.WaitForExit()
        throw 'Synthetic child process exceeded its bounded deadline.'
    }
    return [pscustomobject]@{ ExitCode = $Child.Process.ExitCode; Stdout = $Child.Stdout.Result; Stderr = $Child.Stderr.Result }
}

function Invoke-LaodiTestProcess {
    param([string]$Executable, [string[]]$Arguments, [string]$Directory, [hashtable]$Environment = @{}, [byte[]]$InputBytes = @(), [int]$TimeoutMilliseconds = 10000)
    $child = Start-LaodiTestProcess -Executable $Executable -Arguments $Arguments -Directory $Directory -Environment $Environment -InputBytes $InputBytes
    try { return Complete-LaodiTestProcess -Child $child -TimeoutMilliseconds $TimeoutMilliseconds }
    finally { $child.Process.Dispose() }
}

function Write-LaodiTestJSON([string]$Path, $Value) {
    [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path))
    $text = ConvertTo-Json -InputObject $Value -Depth 30 -Compress
    $temporary = $Path + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
    [IO.File]::WriteAllText($temporary, $text, [Text.UTF8Encoding]::new($false))
    if ([IO.File]::Exists($Path)) { [IO.File]::Replace($temporary, $Path, [System.Management.Automation.Language.NullString]::Value) }
    else { [IO.File]::Move($temporary, $Path) }
}

function Get-LaodiTestCounts($State) {
    $counts = @{}
    foreach ($event in @($State.events)) {
        if ($null -eq $event) { continue }
        if (-not $counts.ContainsKey($event.kind)) { $counts[$event.kind] = 0 }
        $counts[$event.kind]++
    }
    return $counts
}

function Wait-LaodiTestEvents {
    param($Child, [string]$StatePath, [hashtable]$Expected, [int]$ExpectedTotal = -1)
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    do {
        if ($Child.Process.HasExited) { throw 'Synthetic monitor stopped before expected events were recorded.' }
        if (Test-Path -LiteralPath $StatePath) {
            $state = [IO.File]::ReadAllText($StatePath) | ConvertFrom-Json
            if ($state.initialized) {
                $counts = Get-LaodiTestCounts $state
                $matches = $true
                foreach ($key in $Expected.Keys) {
                    $actual = if ($counts.ContainsKey($key)) { $counts[$key] } else { 0 }
                    if ($actual -ne $Expected[$key]) { $matches = $false }
                }
                $eventCount = @($state.events | Where-Object { $null -ne $_ }).Count
                if ($ExpectedTotal -ge 0 -and $eventCount -ne $ExpectedTotal) { $matches = $false }
                if ($matches) { return $state }
            }
        }
        Start-Sleep -Milliseconds 50
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Synthetic event persistence did not meet the expected counts before deadline.'
}
