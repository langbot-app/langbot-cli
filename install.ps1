$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$env:LBCTL_INSTALL_SOURCE = "install.ps1"

$language = @($env:LBCTL_LANG, $env:LANGBOT_LANG, $env:LC_ALL, $env:LC_MESSAGES, $env:LANG) |
    Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -First 1
if (-not $language) { $language = (Get-UICulture).Name }
$isChinese = $language -match '^zh([_-]|$)'
$env:LBCTL_LANG = if ($isChinese) { "zh-Hans" } else { "en" }
function Get-Message([string]$English, [string]$Chinese) {
    if ($isChinese) { $Chinese } else { $English }
}

try {

$repository = "langbot-app/langbot-cli"
$version = if ($env:LBCTL_VERSION) { $env:LBCTL_VERSION } else { "latest" }

if ($env:LBCTL_INSTALL_DIR) {
    $installDir = $env:LBCTL_INSTALL_DIR
} elseif ($env:LOCALAPPDATA) {
    $installDir = Join-Path $env:LOCALAPPDATA "Programs\lbctl"
} else {
    $installDir = Join-Path $HOME ".local\bin"
}

$processorArchitecture = if ($env:PROCESSOR_ARCHITEW6432) {
    $env:PROCESSOR_ARCHITEW6432
} else {
    $env:PROCESSOR_ARCHITECTURE
}
if ([string]::IsNullOrWhiteSpace($processorArchitecture)) {
    throw (Get-Message "Unable to determine the processor architecture" "无法确定处理器架构")
}

switch ($processorArchitecture.ToUpperInvariant()) {
    "AMD64" { $architecture = "amd64" }
    "ARM64" { $architecture = "arm64" }
    default { throw (Get-Message "Unsupported architecture: $processorArchitecture" "不支持的处理器架构：$processorArchitecture") }
}

$asset = "lbctl_windows_$architecture.exe"
$downloadBase = if ($version -eq "latest") {
    "https://github.com/$repository/releases/latest/download"
} else {
    "https://github.com/$repository/releases/download/$version"
}

$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("lbctl-" + [guid]::NewGuid().ToString("N"))
$assetPath = Join-Path $tempDir $asset
$checksumsPath = Join-Path $tempDir "checksums.txt"

try {
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    [Net.ServicePointManager]::SecurityProtocol = `
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Write-Output (Get-Message "[1/3] Downloading $asset ..." "[1/3] 下载 $asset ...")
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$asset" -OutFile $assetPath
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/checksums.txt" -OutFile $checksumsPath
    Write-Output (Get-Message "[2/3] Verifying download ..." "[2/3] 验证下载 ...")

    $escapedAsset = [regex]::Escape($asset)
    $checksumPattern = "^(?<hash>[0-9a-fA-F]{64})\s+\*?$escapedAsset$"
    $expected = $null
    foreach ($line in Get-Content -LiteralPath $checksumsPath) {
        $match = [regex]::Match($line.Trim(), $checksumPattern)
        if ($match.Success) {
            $expected = $match.Groups["hash"].Value
            break
        }
    }
    if (-not $expected) {
        throw (Get-Message "Checksum not found for $asset" "未找到 $asset 的校验和")
    }

    $actual = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash
    if ($actual -ine $expected) {
        throw (Get-Message "Checksum verification failed for $asset" "$asset 校验失败")
    }

    $resolvedInstallDir = [System.IO.Path]::GetFullPath($installDir)
    New-Item -ItemType Directory -Force -Path $resolvedInstallDir | Out-Null
    $destination = Join-Path $resolvedInstallDir "lbctl.exe"
    Copy-Item -Force -LiteralPath $assetPath -Destination $destination

    $pathTrimCharacters = [char[]]"\/"
    $normalizedInstallDir = $resolvedInstallDir.TrimEnd($pathTrimCharacters)
    $currentEntries = @($env:Path -split ";" | Where-Object { $_ })
    if (-not ($currentEntries | Where-Object { $_.TrimEnd($pathTrimCharacters) -ieq $normalizedInstallDir })) {
        $env:Path = "$resolvedInstallDir;$env:Path"
    }

    $userPath = [Environment]::GetEnvironmentVariable("Path", [System.EnvironmentVariableTarget]::User)
    $userEntries = @($userPath -split ";" | Where-Object { $_ })
    if (-not ($userEntries | Where-Object { $_.TrimEnd($pathTrimCharacters) -ieq $normalizedInstallDir })) {
        $newUserPath = if ([string]::IsNullOrWhiteSpace($userPath)) {
            $resolvedInstallDir
        } else {
            "$userPath;$resolvedInstallDir"
        }
        try {
            [Environment]::SetEnvironmentVariable("Path", $newUserPath, [System.EnvironmentVariableTarget]::User)
            Write-Output (Get-Message "Added $resolvedInstallDir to the user PATH." "已将 $resolvedInstallDir 加入用户 PATH。")
        } catch {
            [Console]::Error.WriteLine((Get-Message "[WARN] Installed lbctl; update your user PATH manually: $resolvedInstallDir" "[警告] lbctl 已安装；请手动更新用户 PATH：$resolvedInstallDir"))
        }
    }

    Write-Output (Get-Message "[3/3] Installed lbctl.exe: $destination" "[3/3] 已安装 lbctl.exe：$destination")
} finally {
    if (Test-Path -LiteralPath $tempDir) {
        Remove-Item -Recurse -Force -LiteralPath $tempDir
    }
}
    $global:LASTEXITCODE = 0
} catch {
    [Console]::Error.WriteLine((Get-Message "[FAIL] Installation failed: $($_.Exception.Message)" "[失败] 安装失败：$($_.Exception.Message)"))
    $global:LASTEXITCODE = 1
    return
}
