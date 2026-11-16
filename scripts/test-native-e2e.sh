#!/bin/sh
set -eu

export LANGBOT_HOME=${LANGBOT_HOME:-/opt/lang512}
export LBCTL_NO_UPDATE_CHECK=1
region=${REGION:-global}
endpoint=http://127.0.0.1:15300

lbctl install --driver native --region "$region" --version v4.11.7 --port 15300 --yes --timeout 15m --output json
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
printf 'WebUI HTTP 200\n'
autostart=$(lbctl autostart status --output json)
printf '%s\n' "$autostart"
lbctl autostart disable --output json
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
printf 'Autostart disabled without stopping WebUI\n'
lbctl status --output json
lbctl logs --tail 5 --output json
lbctl stop --output json
lbctl start --output json
lbctl restart --output json
lbctl autostart status --output json | grep '"autostart": false'
printf 'data-preserved\n' > "$LANGBOT_HOME/instances/default/data/e2e-marker"
lbctl upgrade --version v4.11.8 --yes --timeout 15m --output json
test "$(cat "$LANGBOT_HOME/instances/default/data/e2e-marker")" = data-preserved
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
printf 'Upgrade v4.11.7 -> v4.11.8: data preserved, WebUI HTTP 200\n'
lbctl autostart status --output json | grep '"autostart": false'
if printf '%s\n' "$autostart" | grep -q '"supported": true'; then
    lbctl autostart enable --output json
    lbctl autostart status --output json | grep '"autostart": true'
fi
lbctl doctor --output json
lbctl uninstall --yes --output json
test "$(cat "$LANGBOT_HOME/instances/default/data/e2e-marker")" = data-preserved
printf 'Uninstall: stopped, data preserved\n'
