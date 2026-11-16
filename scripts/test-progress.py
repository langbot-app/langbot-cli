import http.server
import json
import os
from pathlib import Path
import pty
import select
import subprocess
import sys
import tempfile
import threading
import time

binary = str(Path(sys.argv[1]).resolve())


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        time.sleep(0.7)
        body = json.dumps({"code": 0, "data": {"version": "v4.11.8", "edition": "community"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
thread = threading.Thread(target=server.serve_forever)
thread.start()
try:
    for name, terminal, overrides, machine in [
        ("tty", True, {}, False),
        ("no-color", True, {"NO_COLOR": "1"}, False),
        ("dumb", True, {"TERM": "dumb"}, False),
        ("json", True, {}, True),
        ("pipe", False, {}, False),
    ]:
        with tempfile.TemporaryDirectory() as home:
            env = dict(os.environ, HOME=home, TERM="xterm", LANGBOT_TELEMETRY="0", LBCTL_NO_UPDATE_CHECK="1")
            env.pop("NO_COLOR", None)
            env.update(overrides)
            args = [binary, "version", "--server", "--endpoint", f"http://127.0.0.1:{server.server_port}"]
            if machine:
                args += ["-o", "json"]
            if terminal:
                master, slave = pty.openpty()
                process = subprocess.Popen(args, env=env, stdout=slave, stderr=slave)
                os.close(slave)
                output = bytearray()
                deadline = time.monotonic() + 15
                try:
                    while time.monotonic() < deadline:
                        if select.select([master], [], [], 0.1)[0]:
                            try:
                                data = os.read(master, 65536)
                            except OSError:
                                break
                            if not data:
                                break
                            output.extend(data)
                    process.wait(timeout=2)
                    assert process.returncode == 0, output.decode()
                    text = output.decode()
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait()
                    os.close(master)
            else:
                result = subprocess.run(args, env=env, capture_output=True, text=True, timeout=15)
                assert result.returncode == 0, result.stdout + result.stderr
                text = result.stdout + result.stderr
            assert ("\x1b[2K" in text) == (name == "tty"), (name, text)
            print(name + ": PASS\n" + text)
finally:
    server.shutdown()
    server.server_close()
    thread.join()
