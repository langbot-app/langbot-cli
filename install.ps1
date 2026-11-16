$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$env:LBCTL_INSTALL_SOURCE = "install.ps1"

$language = @($env:LBCTL_LANG, $env:LANGBOT_LANG, $env:LC_ALL, $env:LC_MESSAGES, $env:LANG) |
    Where-Object { -not [string]::IsNullOrWhiteSpace($_) -and $_ -notmatch '^(C|POSIX)([.@]|$)' } | Select-Object -First 1
if (-not $language) { $language = (Get-UICulture).Name }
$isChinese = $language -match '^zh([_-]|$)'
$env:LBCTL_LANG = if ($isChinese) { "zh-Hans" } else { "en" }
function Get-Message([string]$English, [string]$Chinese) {
    if ($isChinese) { $Chinese } else { $English }
}

$heading = Get-Message "LangBot $([char]0x00B7) Production-grade platform for building agentic IM bots $([char]0x00B7) Open source" "LangBot $([char]0x00B7) $([char]0x751F)$([char]0x4EA7)$([char]0x7EA7) AI $([char]0x5373)$([char]0x65F6)$([char]0x901A)$([char]0x4FE1)$([char]0x673A)$([char]0x5668)$([char]0x4EBA)$([char]0x5F00)$([char]0x53D1)$([char]0x5E73)$([char]0x53F0) $([char]0x00B7) $([char]0x5F00)$([char]0x6E90)"
if (-not [Console]::IsOutputRedirected -and $null -eq $env:NO_COLOR -and $env:TERM -ne "dumb") {
    Write-Host $heading -ForegroundColor Cyan
} else { Write-Output $heading }
Write-Output (Get-Message "Connect LLMs, agents, knowledge bases and plugins to QQ, WeChat/WeCom, Feishu, DingTalk, Telegram, Discord, Slack and more" "$([char]0x628A)$([char]0x5927)$([char]0x6A21)$([char]0x578B)$([char]0x3001)Agent$([char]0x3001)$([char]0x77E5)$([char]0x8BC6)$([char]0x5E93)$([char]0x4E0E)$([char]0x63D2)$([char]0x4EF6)$([char]0x63A5)$([char]0x5165) QQ$([char]0x3001)$([char]0x5FAE)$([char]0x4FE1)/$([char]0x4F01)$([char]0x4E1A)$([char]0x5FAE)$([char]0x4FE1)$([char]0x3001)$([char]0x98DE)$([char]0x4E66)$([char]0x3001)$([char]0x9489)$([char]0x9489)$([char]0x3001)Telegram$([char]0x3001)Discord$([char]0x3001)Slack $([char]0x7B49)$([char]0x5E73)$([char]0x53F0)")
Write-Output (Get-Message "Website https://langbot.app $([char]0x00B7) Docs https://docs.langbot.app $([char]0x00B7) Plugins https://space.langbot.app" "$([char]0x5B98)$([char]0x7F51) https://langbot.app $([char]0x00B7) $([char]0x6587)$([char]0x6863) https://docs.langbot.app $([char]0x00B7) $([char]0x63D2)$([char]0x4EF6)$([char]0x5E02)$([char]0x573A) https://space.langbot.app")

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
    throw (Get-Message "Unable to determine the processor architecture" "$([char]0x65E0)$([char]0x6CD5)$([char]0x786E)$([char]0x5B9A)$([char]0x5904)$([char]0x7406)$([char]0x5668)$([char]0x67B6)$([char]0x6784)")
}

switch ($processorArchitecture.ToUpperInvariant()) {
    "AMD64" { $architecture = "amd64" }
    "ARM64" { $architecture = "arm64" }
    default { throw (Get-Message "Unsupported architecture: $processorArchitecture" "$([char]0x4E0D)$([char]0x652F)$([char]0x6301)$([char]0x7684)$([char]0x5904)$([char]0x7406)$([char]0x5668)$([char]0x67B6)$([char]0x6784)$([char]0xFF1A)$processorArchitecture") }
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
    Write-Output (Get-Message "[1/3] Downloading $asset ..." "[1/3] $([char]0x4E0B)$([char]0x8F7D) $asset ...")
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$asset" -OutFile $assetPath
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/checksums.txt" -OutFile $checksumsPath
    Write-Output (Get-Message "[2/3] Verifying download ..." "[2/3] $([char]0x9A8C)$([char]0x8BC1)$([char]0x4E0B)$([char]0x8F7D) ...")

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
        throw (Get-Message "Checksum not found for $asset" "$([char]0x672A)$([char]0x627E)$([char]0x5230) $asset $([char]0x7684)$([char]0x6821)$([char]0x9A8C)$([char]0x548C)")
    }

    $actual = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash
    if ($actual -ine $expected) {
        throw (Get-Message "Checksum verification failed for $asset" "$asset $([char]0x6821)$([char]0x9A8C)$([char]0x5931)$([char]0x8D25)")
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
            Write-Output (Get-Message "Added $resolvedInstallDir to the user PATH." "$([char]0x5DF2)$([char]0x5C06) $resolvedInstallDir $([char]0x52A0)$([char]0x5165)$([char]0x7528)$([char]0x6237) PATH$([char]0x3002)")
        } catch {
            [Console]::Error.WriteLine((Get-Message "[WARN] Installed lbctl; update your user PATH manually: $resolvedInstallDir" "[$([char]0x8B66)$([char]0x544A)] lbctl $([char]0x5DF2)$([char]0x5B89)$([char]0x88C5)$([char]0xFF1B)$([char]0x8BF7)$([char]0x624B)$([char]0x52A8)$([char]0x66F4)$([char]0x65B0)$([char]0x7528)$([char]0x6237) PATH$([char]0xFF1A)$resolvedInstallDir"))
        }
    }

    Write-Output (Get-Message "[3/3] Installed lbctl.exe: $destination" "[3/3] $([char]0x5DF2)$([char]0x5B89)$([char]0x88C5) lbctl.exe$([char]0xFF1A)$destination")
} finally {
    if (Test-Path -LiteralPath $tempDir) {
        Remove-Item -Recurse -Force -LiteralPath $tempDir
    }
}
    $global:LASTEXITCODE = 0
} catch {
    [Console]::Error.WriteLine((Get-Message "[FAIL] Installation failed: $($_.Exception.Message)" "[$([char]0x5931)$([char]0x8D25)] $([char]0x5B89)$([char]0x88C5)$([char]0x5931)$([char]0x8D25)$([char]0xFF1A)$($_.Exception.Message)"))
    $global:LASTEXITCODE = 1
    return
}
