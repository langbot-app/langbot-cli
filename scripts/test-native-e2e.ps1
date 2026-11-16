$ErrorActionPreference = 'Stop'
$env:LBCTL_NO_UPDATE_CHECK = '1'
$env:LANGBOT_TELEMETRY = '0'
$endpoint = 'http://127.0.0.1:15300'
$instanceDir = Join-Path $env:LANGBOT_HOME 'instances/中文 user'
$registryPath = 'Software\Microsoft\Windows\CurrentVersion\Run'
$registryName = $null
$installed = $false

function Invoke-Lbctl {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    $output = & lbctl @Arguments --dir $instanceDir --output json
    if ($LASTEXITCODE -ne 0) { throw "lbctl $Arguments failed: $output" }
    $envelope = ($output -join "`n") | ConvertFrom-Json
    if (-not $envelope.ok) { throw "lbctl $Arguments returned an error" }
    Write-Host ($output -join "`n")
    return $envelope.data
}

function Assert-RunValue {
    param([bool]$Expected)
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($registryPath)
    try {
        $value = if ($key) { $key.GetValue($registryName, $null) } else { $null }
        if (($null -ne $value) -ne $Expected) { throw "Unexpected Run value: $value" }
        if ($Expected -and $value -ne "`"$((Get-Command lbctl).Source)`" _native-run `"$instanceDir`"") {
            throw "Unexpected startup command: $value"
        }
    } finally {
        if ($key) { $key.Close() }
    }
}

function Assert-Health {
    foreach ($path in @('/healthz', '/')) {
        $response = Invoke-WebRequest "$endpoint$path" -TimeoutSec 10
        if ($response.StatusCode -ne 200) { throw "$path did not return HTTP 200" }
    }
    Write-Host 'Health and WebUI HTTP 200'
}

function Assert-NoConsole {
    param([int]$ProcessId)
    $probe = @'
import ctypes, sys
kernel = ctypes.WinDLL('kernel32', use_last_error=True)
kernel.FreeConsole()
assert not kernel.AttachConsole(int(sys.argv[1])), 'process still has a console'
assert ctypes.get_last_error() == 6, ctypes.get_last_error()
'@
    & (Join-Path $instanceDir 'venvs/v4.11.7/Scripts/python.exe') -c $probe $ProcessId
    if ($LASTEXITCODE -ne 0) { throw "Console probe failed for $ProcessId" }
}

try {
    $installed = $true
    $install = Invoke-Lbctl @('install', '--driver', 'native', '--region', 'global', '--version', 'v4.11.7', '--port', '15300', '--yes', '--timeout', '15m')
    if ($install.service_manager -ne 'windows-run') { throw 'Expected windows-run manager' }
    $registryName = 'langbot-' + $install.record.deployment_id
    Assert-RunValue $true
    Assert-Health
    $status = Invoke-Lbctl @('autostart', 'status')
    if (-not $status.autostart -or $status.mechanism -ne 'HKCU Run') { throw 'Unexpected autostart status' }
    Invoke-Lbctl @('autostart', 'disable') | Out-Null
    Assert-RunValue $false
    Assert-Health
    Invoke-Lbctl @('stop') | Out-Null
    if (Test-Path (Join-Path $instanceDir 'native.pid')) { throw 'Supervisor PID survived stop' }
    Invoke-Lbctl @('start') | Out-Null
    Assert-RunValue $false
    Assert-Health
    $status = Invoke-Lbctl @('autostart', 'status')
    if ($status.autostart) { throw 'Start re-enabled autostart' }
    Invoke-Lbctl @('autostart', 'enable') | Out-Null
    Assert-RunValue $true
    Invoke-Lbctl @('stop') | Out-Null
    Assert-RunValue $false
    Invoke-Lbctl @('start') | Out-Null
    Assert-RunValue $true
    Assert-Health
    Invoke-Lbctl @('stop') | Out-Null
    Invoke-Lbctl @('autostart', 'enable') | Out-Null
    Assert-RunValue $true
    $supervisor = Start-Process -FilePath (Get-Command lbctl).Source -ArgumentList "_native-run `"$instanceDir`"" -PassThru
    $deadline = (Get-Date).AddMinutes(3)
    do {
        if ($supervisor.HasExited) { throw 'Login supervisor exited' }
        try {
            $response = Invoke-WebRequest "$endpoint/healthz" -TimeoutSec 5
            if ($response.StatusCode -eq 200) { break }
        } catch { }
        Start-Sleep -Seconds 1
    } while ((Get-Date) -lt $deadline)
    Assert-Health
    $supervisorId = [int](Get-Content (Join-Path $instanceDir 'native.pid'))
    if ($supervisorId -ne $supervisor.Id) { throw 'Wrong supervisor owns PID file' }
    Assert-NoConsole $supervisorId
    $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId = $supervisorId" | Where-Object Name -eq 'python.exe')
    if ($children.Count -ne 1) { throw 'Expected one Python child' }
    Assert-NoConsole $children[0].ProcessId
    & lbctl _native-run $instanceDir
    if ($LASTEXITCODE -eq 0) { throw 'Duplicate supervisor acquired the lock' }
    if ([int](Get-Content (Join-Path $instanceDir 'native.pid')) -ne $supervisorId) { throw 'Duplicate changed PID ownership' }
    Assert-Health
    Invoke-Lbctl @('uninstall', '--yes') | Out-Null
    $installed = $false
    Assert-RunValue $false
    if (Get-Process -Id $supervisorId -ErrorAction SilentlyContinue) { throw 'Supervisor survived uninstall' }
    foreach ($child in $children) {
        if (Get-Process -Id $child.ProcessId -ErrorAction SilentlyContinue) { throw 'Python survived uninstall' }
    }
    Write-Host 'Windows native lifecycle, login console, PID/lock and registry checks passed'
} finally {
    if ($installed) {
        & lbctl uninstall --dir $instanceDir --yes --output json
    }
}
