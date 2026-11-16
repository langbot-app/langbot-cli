import hashlib
import http.server
import os
from pathlib import Path
import subprocess
import tempfile
import threading

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
        for fail in [False, True]:
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                payload = b"installer fixture"
                (root / "asset").write_bytes(payload)
                (root / "checksums.txt").write_text(hashlib.sha256(payload).hexdigest() + "  lbctl_windows_amd64.exe\n", encoding="ascii")
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
    $name = if ($Uri.EndsWith('checksums.txt')) { 'checksums.txt' } else { 'asset' }
    Copy-Item (Join-Path $env:FIXTURE_ROOT $name) $OutFile
}
try {
    irm $env:FIXTURE_URL | iex
    $result = $global:LASTEXITCODE
    if ($result -eq 0 -and -not (Test-Path (Join-Path $env:LBCTL_INSTALL_DIR 'lbctl.exe'))) { throw 'missing executable' }
} finally {
    [Environment]::SetEnvironmentVariable('Path', $savedPath, 'User')
}
exit $result
''', encoding="ascii")
                env = dict(os.environ, FIXTURE_ROOT=directory, FIXTURE_FAIL="1" if fail else "0", FIXTURE_URL=f"http://127.0.0.1:{server.server_port}/install.ps1", LBCTL_LANG=language, LBCTL_INSTALL_DIR=str(root / "installed"), PROCESSOR_ARCHITECTURE="AMD64", PROCESSOR_ARCHITEW6432="AMD64", NO_COLOR="1")
                result = subprocess.run(["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", str(harness)], env=env, capture_output=True, timeout=30)
                text = (result.stdout + result.stderr).decode("utf-8")
                assert result.returncode == (1 if fail else 0), text
                expected = ("[失败]" if language == "zh-Hans" else "[FAIL]") if fail else ("已安装 lbctl.exe" if language == "zh-Hans" else "Installed lbctl.exe")
                assert expected in text, text
                assert all(marker not in text for marker in ["ParserError", "ï»¿", "CategoryInfo", "At line:"]), text
                print(f"PowerShell 5.1 / text/plain without charset / {language} / failure={fail}: PASS")
finally:
    server.shutdown()
    server.server_close()
    thread.join()
