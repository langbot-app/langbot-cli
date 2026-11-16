import json
import os
from pathlib import Path
import pty
import select
import subprocess
import sys
import tempfile
import time

binary = str(Path(sys.argv[1]).resolve())
fixtures = str(Path(__file__).resolve().parent / "testdata")

def run_case(name, path, terminal):
    with tempfile.TemporaryDirectory(prefix="lbctl-wizard-") as home:
        env = dict(os.environ, PATH=path, LANGBOT_HOME=home + "/instances-root",
                   XDG_CONFIG_HOME=home + "/config", LANG="en_US.UTF-8", LBCTL_NO_UPDATE_CHECK="1")
        args = [binary, "install", "--version", "v4.11.8", "--region", "global", "--dry-run", "--output", "json"]
        if not terminal:
            result = subprocess.run(args, env=env, capture_output=True, text=True, timeout=20)
            assert result.returncode == 0, result.stdout + result.stderr
            assert json.loads(result.stdout)["data"]["driver"] == "native"
            print(name + ": native, non-TTY JSON valid")
            return
        master, slave = pty.openpty()
        process = subprocess.Popen(args, env=env, stdin=slave, stdout=slave, stderr=slave)
        os.close(slave)
        output = bytearray()
        deadline = time.monotonic() + 20
        answered = False
        try:
            while time.monotonic() < deadline:
                if select.select([master], [], [], 0.1)[0]:
                    try:
                        chunk = os.read(master, 65536)
                    except OSError:
                        break
                    if not chunk:
                        break
                    output.extend(chunk)
                    if b"Select [1]:" in output and not answered:
                        os.write(master, b"\n")
                        answered = True
                if process.poll() is not None:
                    break
            process.wait(timeout=2)
            text = output.decode()
            assert process.returncode == 0, text
            assert '"driver": "native"' in text, text
            assert answered == (name == "docker-available"), text
            if name == "docker-unavailable":
                assert "using native deployment" in text, text
            print(name + ": PASS\n" + text)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            os.close(master)

run_case("docker-available", fixtures, True)
run_case("docker-unavailable", "/nonexistent", True)
run_case("non-tty", "/nonexistent", False)
