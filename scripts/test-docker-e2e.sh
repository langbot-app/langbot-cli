#!/bin/sh
set -eu

test "$(id -u)" -ne 0
export LANGBOT_HOME=${LANGBOT_HOME:?set an isolated LANGBOT_HOME}
export XDG_CONFIG_HOME="$LANGBOT_HOME/config"
export LANGBOT_TELEMETRY=0 LBCTL_NO_UPDATE_CHECK=1
name=lang546-e2e
port=${LBCTL_DOCKER_TEST_PORT:-25346}
repository=${LBCTL_DOCKER_TEST_REPOSITORY:-rockchin/langbot}
mkdir -p "$LANGBOT_HOME"
dir="$LANGBOT_HOME/instances/$name"
cleanup() {
    if test -f "$dir/compose.yaml"; then
        lbctl uninstall --name "$name" --yes --output json || true
    fi
}
trap cleanup EXIT
id
docker version --format '{{.Server.Version}}'
lbctl install --driver docker --name "$name" --region global --version v4.11.7 \
    --image-repository "$repository" --port "$port" --plugin-debug-port 0 --adapter-ports none \
    --yes --timeout 15m --output json
image=$(docker compose -f "$dir/compose.yaml" -p "langbot-$name" images -q langbot)
docker run --rm --user 0:0 --mount "type=bind,src=$dir/data,dst=/data" --entrypoint python "$image" -c \
    "import os; os.makedirs('/data/root-only',exist_ok=True); open('/data/root-only/marker','w').write('preserved'); os.chmod('/data/root-only',0o700)"
test ! -r "$dir/data/root-only/marker"
lbctl upgrade --name "$name" --version v4.11.8 --dry-run --output json
lbctl upgrade --dir "$dir" --version v4.11.8 --yes --timeout 15m --output json
docker run --rm --user 0:0 --mount "type=bind,src=$dir/data,dst=/data,readonly" --entrypoint python "$image" -c \
    "assert open('/data/root-only/marker').read() == 'preserved'"
test "$(cat "$dir"/backups/upgrade-*/data/root-only/marker)" = preserved
test "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/")" = 200
printf 'Non-root Docker install + upgrade: root-owned data and readable backup preserved; WebUI HTTP 200\n'
