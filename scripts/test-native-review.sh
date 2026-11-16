#!/bin/sh
set -eu

export LANGBOT_HOME=${LANGBOT_HOME:-/opt/lang512-review}
export LBCTL_NO_UPDATE_CHECK=1
endpoint=http://127.0.0.1:15300
instance=$LANGBOT_HOME/instances/default
evidence=${EVIDENCE:-/evidence}
mkdir -p "$evidence"

lbctl install --driver native --region "${REGION:-global}" --version v4.11.7 --port 15300 --yes --timeout 15m > "$evidence/install.txt"
cat "$evidence/install.txt"
grep -F "已安装 LangBot v4.11.7 → $endpoint" "$evidence/install.txt"
grep -F 'lbctl status · lbctl logs · lbctl upgrade --yes' "$evidence/install.txt"
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200

"$instance/venvs/v4.11.7/bin/python" - <<'PY'
import os
import pathlib
import socket
import time
import yaml

for port in (5401, 5410):
    for attempt in range(30):
        try:
            with socket.create_connection(('127.0.0.1', port), timeout=1):
                break
        except OSError:
            time.sleep(1)
    else:
        raise RuntimeError(f'runtime port {port} did not start')
path = pathlib.Path(os.environ['LANGBOT_HOME']) / 'instances/default/data/config.yaml'
config = yaml.safe_load(path.read_text())
config['api']['webhook_prefix'] = 'https://bot.example.com'
path.write_text(yaml.safe_dump(config, allow_unicode=True))
PY

if lbctl install --driver native --name second --port 15313 --region global --version v4.11.8 --yes > "$evidence/conflict.txt" 2>&1; then
    echo 'Second native instance unexpectedly installed'
    exit 1
fi
cat "$evidence/conflict.txt"
grep -F '5401' "$evidence/conflict.txt"
test ! -e "$LANGBOT_HOME/instances/second/deployment.yaml"

lbctl restart > "$evidence/restart.txt"
grep -F '已重启本机部署' "$evidence/restart.txt"
grep -F 'https://bot.example.com' "$instance/data/config.yaml"
lbctl doctor > "$evidence/doctor.txt"
cat "$evidence/doctor.txt"
grep -F '原生部署' "$evidence/doctor.txt"
grep -F 'Box 沙箱' "$evidence/doctor.txt"
grep -F 'stdio MCP' "$evidence/doctor.txt"
grep -F 'ffmpeg' "$evidence/doctor.txt"
lbctl stop > "$evidence/stop.txt"
grep -F '已停止本机部署' "$evidence/stop.txt"
lbctl start > "$evidence/start.txt"
grep -F '已启动本机部署' "$evidence/start.txt"
lbctl upgrade --version v4.11.8 --yes --timeout 15m > "$evidence/upgrade.txt"
cat "$evidence/upgrade.txt"
grep -F '已升级 LangBot v4.11.7 → v4.11.8' "$evidence/upgrade.txt"
grep -F 'https://bot.example.com' "$instance/data/config.yaml"
test "$(curl -s -o /dev/null -w '%{http_code}' "$endpoint/")" = 200
lbctl logs --tail 30 > "$evidence/logs.txt"
lbctl uninstall --yes > "$evidence/uninstall.txt"
grep -F '已卸载本机受管部署；数据和部署记录已保留' "$evidence/uninstall.txt"
printf 'Native review regression passed\n'
