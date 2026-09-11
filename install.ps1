$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

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
    throw "Unable to determine the processor architecture"
}

switch ($processorArchitecture.ToUpperInvariant()) {
    "AMD64" { $architecture = "amd64" }
    "ARM64" { $architecture = "arm64" }
    default { throw "Unsupported architecture: $processorArchitecture" }
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
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$asset" -OutFile $assetPath
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/checksums.txt" -OutFile $checksumsPath

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
        throw "Checksum not found for $asset"
    }

    $actual = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash
    if ($actual -ine $expected) {
        throw "Checksum verification failed for $asset"
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
            Write-Output "Added $resolvedInstallDir to the user PATH."
        } catch {
            Write-Warning "Installed lbctl, but could not update the user PATH: $($_.Exception.Message)"
        }
    }

    Write-Output "Installed lbctl.exe to $destination"
} finally {
    if (Test-Path -LiteralPath $tempDir) {
        Remove-Item -Recurse -Force -LiteralPath $tempDir
    }
}
