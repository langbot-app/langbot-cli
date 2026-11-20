import gzip
import hashlib
import http.server
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading

sys.stdout.reconfigure(encoding="utf-8")
sys.stderr.reconfigure(encoding="utf-8")
installer = (Path(__file__).resolve().parent.parent / "install.ps1").read_bytes()
assert installer.isascii(), "installer must survive a response without charset"


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(installer)

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
thread = threading.Thread(target=server.serve_forever)
thread.start()
try:
    for language in ["en", "zh-Hans"]:
        for mode in ["raw", "gzip", "download-failure", "bad-archive-checksum", "bad-binary-checksum"]:
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                payload = b"installer fixture"
                asset = "lbctl_windows_amd64.exe"
                (root / asset).write_bytes(payload)
                digest = "0" * 64 if mode == "bad-binary-checksum" else hashlib.sha256(payload).hexdigest()
                manifest = f"{digest}  {asset}\n"
                download = asset
                if mode in ["gzip", "bad-archive-checksum", "bad-binary-checksum"]:
                    download += ".gz"
                    archive = gzip.compress(payload, mtime=0)
                    (root / download).write_bytes(archive)
                    digest = "0" * 64 if mode == "bad-archive-checksum" else hashlib.sha256(archive).hexdigest()
                    manifest += f"{digest}  {download}\n"
                (root / "checksums.txt").write_text(manifest, encoding="ascii")
                harness = root / "test.ps1"
                harness.write_text(r'''
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = [Console]::OutputEncoding
if ($PSVersionTable.PSVersion.Major -ne 5) { throw 'Windows PowerShell 5.1 required' }
$savedPath = [Environment]::GetEnvironmentVariable('Path', 'User')
function Invoke-WebRequest {
    param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)
    if ($env:FIXTURE_FAIL -eq '1') { throw 'injected download failure' }
    $name = $Uri.Split('/')[-1]
    if ($name -ne 'checksums.txt') {
        $script:requestedAssets += $name
    }
    Copy-Item (Join-Path $env:FIXTURE_ROOT $name) $OutFile
}
$requestedAssets = @()
try {
    irm $env:FIXTURE_URL | iex
    $result = $global:LASTEXITCODE
    if ($env:FIXTURE_FAIL -ne '1' -and ($requestedAssets -join ',') -cne $env:EXPECTED_DOWNLOAD) {
        throw "unexpected assets: $requestedAssets"
    }
    $installed = Join-Path $env:LBCTL_INSTALL_DIR 'lbctl.exe'
    if ($result -eq 0) {
        if ([System.IO.File]::ReadAllText($installed) -cne 'installer fixture') { throw 'wrong executable content' }
    } elseif (Test-Path -LiteralPath $installed) {
        throw 'installed a failed download'
    }
} finally {
    [Environment]::SetEnvironmentVariable('Path', $savedPath, 'User')
}
exit $result
''', encoding="ascii")
                env = {key: value for key, value in os.environ.items() if key.upper() != "PSMODULEPATH"}
                env.update(FIXTURE_ROOT=directory, FIXTURE_FAIL="1" if mode == "download-failure" else "0", EXPECTED_DOWNLOAD=download, FIXTURE_URL=f"http://127.0.0.1:{server.server_port}/install.ps1", LBCTL_LANG=language, LBCTL_INSTALL_DIR=str(root / "installed"), PROCESSOR_ARCHITECTURE="AMD64", PROCESSOR_ARCHITEW6432="AMD64", NO_COLOR="1")
                result = subprocess.run(["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", str(harness)], env=env, capture_output=True, timeout=30)
                text = (result.stdout + result.stderr).decode("utf-8")
                fail = mode not in ["raw", "gzip"]
                assert result.returncode == (1 if fail else 0), text
                expected = ("[失败]" if language == "zh-Hans" else "[FAIL]") if fail else ("已安装 lbctl.exe" if language == "zh-Hans" else "Installed lbctl.exe")
                assert expected in text, text
                assert all(marker not in text for marker in ["ParserError", "ï»¿", "CategoryInfo", "At line:"]), text
                print(f"PowerShell 5.1 / text/plain without charset / {language} / {mode}: PASS")
finally:
    server.shutdown()
    server.server_close()
    thread.join()
