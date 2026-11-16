#!/bin/sh
set -eu

export LANGBOT_HOME=${LANGBOT_HOME:-/opt/lang512}
export LBCTL_NO_UPDATE_CHECK=1
region=${REGION:-global}
endpoint=http://127.0.0.1:15300

lbctl install --driver native --region "$region" --version v4.11.7 --port 15300 --yes --timeout 15m --output json
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
printf 'WebUI HTTP 200\n'
lbctl status --output json
lbctl logs --tail 5 --output json
lbctl stop --output json
lbctl start --output json
lbctl restart --output json
printf 'data-preserved\n' > "$LANGBOT_HOME/instances/default/data/e2e-marker"
lbctl upgrade --version v4.11.8 --yes --timeout 15m --output json
test "$(cat "$LANGBOT_HOME/instances/default/data/e2e-marker")" = data-preserved
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
printf 'Upgrade v4.11.7 -> v4.11.8: data preserved, WebUI HTTP 200\n'
lbctl doctor --output json
lbctl uninstall --yes --output json
test "$(cat "$LANGBOT_HOME/instances/default/data/e2e-marker")" = data-preserved
printf 'Uninstall: stopped, data preserved\n'
