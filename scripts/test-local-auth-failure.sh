#!/bin/sh
set -eu

export HOME=${HOME:?Set an isolated HOME for this test}
export LANGBOT_HOME=${LANGBOT_HOME:?Set an isolated LANGBOT_HOME for this test}
export LANGBOT_CONFIG="$LANGBOT_HOME/cli.yaml"
export LANGBOT_TELEMETRY=0 LBCTL_NO_UPDATE_CHECK=1
mkdir -p "$HOME" "$LANGBOT_HOME"
instance="$LANGBOT_HOME/instances/default"
trap 'lbctl stop --dir "$instance" -o json > "$LANGBOT_HOME/stop.json" 2>/dev/null || true' EXIT

lbctl install --driver native --region global --version v4.11.7 --port 15300 --yes --timeout 15m -o json > "$LANGBOT_HOME/install.json"
python="$instance/venvs/v4.11.7/bin/python"
"$python" - <<'PY'
import os, pathlib, yaml
path = pathlib.Path(os.environ['LANGBOT_HOME']) / 'instances/default/data/config.yaml'
config = yaml.safe_load(path.read_text())
config['api']['global_api_key'] = 12345
path.write_text(yaml.safe_dump(config, allow_unicode=True))
PY

LANG=C lbctl restart --dir "$instance" > "$LANGBOT_HOME/restart.txt"
cat "$LANGBOT_HOME/restart.txt"
printf 'restart exit code: 0\n'
test "$(grep -c 'CLI could not connect automatically' "$LANGBOT_HOME/restart.txt")" -eq 1
status_code=0
LANG=C lbctl status > "$LANGBOT_HOME/status.txt" || status_code=$?
test "$status_code" -eq 0 || test "$status_code" -eq 3
cat "$LANGBOT_HOME/status.txt"
grep -q 'Running' "$LANGBOT_HOME/status.txt"
printf 'status: Running; credential-check exit code: %s\n' "$status_code"

lbctl install --driver native --region global --version v4.11.7 --port 15300 --yes --timeout 15m -o json > "$LANGBOT_HOME/repair.json"
lbctl upgrade --dir "$instance" --version v4.11.8 --yes --timeout 15m -o json > "$LANGBOT_HOME/upgrade.json"
status_code=0
LANG=C lbctl status > "$LANGBOT_HOME/upgraded-status.txt" || status_code=$?
test "$status_code" -eq 0 || test "$status_code" -eq 3
grep 'Running' "$LANGBOT_HOME/upgraded-status.txt"

"$python" - <<'PY'
import json, os, pathlib
root = pathlib.Path(os.environ['LANGBOT_HOME'])
key = (root / 'instances/default/.api-key').read_text()
for name in ('repair', 'upgrade'):
    response = json.loads((root / f'{name}.json').read_text())
    assert response['ok'] is True
    assert 'lbctl restart --dir' in response['data']['local_auth_error']
    assert response['data']['local_auth'] is False
    if name == 'upgrade':
        assert response['data']['completed'] is True
    print(f'{name}: exit 0, warning present, deployment running')
for path in list(root.glob('*.json')) + list(root.glob('*.txt')) + [root / 'cli.yaml']:
    assert key not in path.read_text(), path.name
print('credential disclosure check: passed')
PY
