#!/bin/sh
set -eu

export LANGBOT_TELEMETRY=0 LBCTL_NO_UPDATE_CHECK=1
export LANGBOT_HOME=${LANGBOT_HOME:-/tmp/lbctl-local-auth-legacy}
export LANGBOT_CONFIG=${LANGBOT_CONFIG:-$LANGBOT_HOME/cli.yaml}
mkdir -p "$LANGBOT_HOME"
trap 'lbctl stop --name default -o json > "$LANGBOT_HOME/stop.json" 2>/dev/null || true' EXIT
lbctl-old install --driver native --region global --version v4.11.7 --port 15300 --yes --timeout 15m -o json > "$LANGBOT_HOME/old-install.json"
python="$LANGBOT_HOME/instances/default/venvs/v4.11.7/bin/python"
"$python" - <<'PY'
import os, pathlib, yaml
root = pathlib.Path(os.environ['LANGBOT_HOME'])
config = yaml.safe_load((root / 'instances/default/data/config.yaml').read_text())
assert not config['api'].get('global_api_key')
print('legacy: v0.5.9 installation has no global API key')
PY
lbctl restart -o json > "$LANGBOT_HOME/restart.json"
lbctl whoami -o json > "$LANGBOT_HOME/whoami.json"
lbctl bot list -o json > "$LANGBOT_HOME/bot-list.json"
printf 'legacy: new lbctl restart backfilled key and bot list succeeded\n'
