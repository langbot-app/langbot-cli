#!/bin/sh
set -eu

export LANGBOT_HOME=${LANGBOT_HOME:-/opt/lang512}
export LBCTL_NO_UPDATE_CHECK=1
region=${REGION:-global}
endpoint=http://127.0.0.1:15300

mkdir -p "$LANGBOT_HOME"
printf 'blocked runtime directory' > "$LANGBOT_HOME/tools"
if lbctl install --driver native --region "$region" --version v4.11.7 --port 15300 --yes --output json > "$LANGBOT_HOME/failed-install.json"; then
    printf 'Expected runtime preparation failure\n' >&2
    exit 1
fi
grep '"step": "download_uv"' "$LANGBOT_HOME/failed-install.json"
record="$LANGBOT_HOME/instances/default/deployment.yaml"
identity=$(grep '^deployment_id:' "$record")
mkdir -p "$LANGBOT_HOME/instances/default/data"
printf 'resume-preserved' > "$LANGBOT_HOME/instances/default/data/resume-marker"
rm "$LANGBOT_HOME/tools"
lbctl install --driver native --region "$region" --port 15300 --yes --timeout 15m --output json
test "$(grep '^deployment_id:' "$record")" = "$identity"
test "$(cat "$LANGBOT_HOME/instances/default/data/resume-marker")" = resume-preserved
test -f "$LANGBOT_HOME/instances/default/venvs/v4.11.7/bin/python"
printf 'Failed installation resumed: original identity, version and data preserved\n'
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
printf 'WebUI HTTP 200\n'
autostart=$(lbctl autostart status --output json)
printf '%s\n' "$autostart"
lbctl autostart disable --output json
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
printf 'Autostart disabled without stopping WebUI\n'
lbctl status --output json
lbctl logs --tail 5 --output json
lbctl stop --name default --output json
lbctl start --name default --output json
lbctl restart --name default --output json
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
lbctl uninstall --name default --yes --output json
test "$(cat "$LANGBOT_HOME/instances/default/data/e2e-marker")" = data-preserved
printf 'Uninstall: stopped, data preserved\n'
lbctl install --name replacement --driver native --region "$region" --version v4.11.7 --port 15300 --yes --timeout 15m --output json
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
test "$(cat "$LANGBOT_HOME/instances/default/data/e2e-marker")" = data-preserved
lbctl uninstall --name replacement --yes --output json
printf 'Uninstall then reinstall under a new name: same port, WebUI HTTP 200, old data preserved\n'
