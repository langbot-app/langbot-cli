#!/bin/sh
set -eu

export LANGBOT_HOME=${LANGBOT_HOME:-/opt/lang512-rollback}
export LBCTL_NO_UPDATE_CHECK=1
lbctl install --driver native --region global --version v4.11.8 --port 15300 --yes --timeout 15m --output json
instance="$LANGBOT_HOME/instances/default"
uv="$LANGBOT_HOME/tools/uv/0.9.11/uv"
printf 'original\n' > "$instance/data/e2e-marker"
mkdir -p "$instance/venvs/v4.11.9/bin"
cat > "$instance/failing-release.py" <<'PYTHON'
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

Path("data/e2e-marker").write_text("mutated")

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(503)
        self.end_headers()

HTTPServer(("127.0.0.1", 15300), Handler).serve_forever()
PYTHON
printf '#!/bin/sh\nexec "%s" "%s"\n' "$instance/venvs/v4.11.8/bin/python" "$instance/failing-release.py" > "$instance/venvs/v4.11.9/bin/python"
chmod 700 "$instance/venvs/v4.11.9/bin/python"
mv "$uv" "$uv.real"
printf '#!/bin/sh\nexit 0\n' > "$uv"
chmod 700 "$uv"
trap 'mv "$uv.real" "$uv"' EXIT
if lbctl upgrade --version v4.11.9 --yes --timeout 15s --output json; then
    printf 'Expected health-check failure\n' >&2
    exit 1
fi
test "$(cat "$instance/data/e2e-marker")" = original
test "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:15300/)" = 200
lbctl status --output json
printf 'Failed-release rollback: v4.11.8 restored, data restored, WebUI HTTP 200\n'
lbctl logs --follow --tail 3 --timeout 1s --output json || test "$?" = 7
lbctl uninstall --yes --output json
