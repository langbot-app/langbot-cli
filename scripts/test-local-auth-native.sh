#!/bin/sh
set -eu

export LANGBOT_TELEMETRY=0 LBCTL_NO_UPDATE_CHECK=1
export LANGBOT_HOME=${LANGBOT_HOME:-/tmp/lbctl-local-auth}
export LANGBOT_CONFIG=${LANGBOT_CONFIG:-$LANGBOT_HOME/cli.yaml}
mkdir -p "$LANGBOT_HOME"
trap 'lbctl stop --name default -o json > "$LANGBOT_HOME/stop.json" 2>/dev/null || true' EXIT
lbctl install --driver native --region global --version v4.11.7 --port 15300 --yes --timeout 15m -o json > "$LANGBOT_HOME/install.json"
for command in 'whoami' 'bot list' 'model list'; do
    lbctl $command -o json > "$LANGBOT_HOME/$(echo "$command" | tr ' ' '-').json"
    printf 'native: %s OK\n' "$command"
done
printf '{}\n' > "$LANGBOT_HOME/bot.json"
lbctl bot create --file "$LANGBOT_HOME/bot.json" --dry-run -o json > "$LANGBOT_HOME/write-preflight.json"
printf 'native: automatic Workspace binding and write preflight OK\n'
lbctl context show -o json > "$LANGBOT_HOME/context.json"
lbctl status -o json > "$LANGBOT_HOME/status.json"
lbctl auth show-key --yes > "$LANGBOT_HOME/revealed-key"
python="$LANGBOT_HOME/instances/default/venvs/v4.11.7/bin/python"
"$python" - <<'PY'
import os, pathlib, yaml
root = pathlib.Path(os.environ['LANGBOT_HOME'])
config = yaml.safe_load((root / 'instances/default/data/config.yaml').read_text())
key = config['api']['global_api_key']
assert len(key) == 47
assert (root / 'revealed-key').read_text().strip() == key
assert key not in pathlib.Path(os.environ['LANGBOT_CONFIG']).read_text()
for path in root.glob('*.json'):
    assert key not in path.read_text(), path.name
assert (root / 'instances/default/.api-key').stat().st_mode & 0o777 == 0o600
print('native: explicit reveal, 0600 storage, config/output secret exclusion OK')
PY
lbctl install --driver native --region global --version v4.11.7 --port 15300 --yes --timeout 15m -o json > "$LANGBOT_HOME/repair.json"
lbctl bot list -o json > "$LANGBOT_HOME/after-repair.json"
printf 'native: repeat install and authenticated resource access OK\n'
lbctl restart -o json > "$LANGBOT_HOME/restart.json"
lbctl bot list -o json > "$LANGBOT_HOME/after-restart.json"
printf 'native: restart and authenticated resource access OK\n'
