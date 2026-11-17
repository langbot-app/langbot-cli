import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import uuid


parser = argparse.ArgumentParser()
parser.add_argument('--lbctl', required=True)
parser.add_argument('--root', required=True)
args = parser.parse_args()
binary = str(Path(args.lbctl).resolve())
root = Path(args.root).resolve()
root.mkdir(parents=True, exist_ok=False)
environment = dict(os.environ, HOME=str(root / 'home'), LANGBOT_HOME=str(root / 'langbot'),
                   LANGBOT_CONFIG=str(root / 'cli.yaml'), LANGBOT_TELEMETRY='0',
                   LBCTL_NO_UPDATE_CHECK='1', LANG='C')
for name in ('LANGBOT_API_KEY', 'LANGBOT_CONTEXT', 'LANGBOT_ENDPOINT'):
    environment.pop(name, None)
projects = []


def cli(label, *arguments):
    completed = subprocess.run([binary, *arguments], env=environment,
                               capture_output=True, text=True, timeout=600)
    (root / (label + '.stdout')).write_text(completed.stdout)
    (root / (label + '.stderr')).write_text(completed.stderr)
    assert completed.returncode == 0, label + ': ' + completed.stdout + completed.stderr
    print(label + ': exit 0', flush=True)
    return completed.stdout if arguments[-1] == 'json' else completed.stdout + completed.stderr


try:
    for version in ('v4.10.3', 'v4.10.11'):
        directory = root / version
        project = 'lbctl-version-' + uuid.uuid4().hex[:8]
        projects.append((directory / 'compose.yaml', project))
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        installed = cli(version + '-install', 'install', '--driver', 'docker', '--dir', str(directory),
                        '--name', version, '--project', project, '--port', str(port),
                        '--plugin-debug-port', '0', '--adapter-ports', 'none', '--region', 'global',
                        '--image-repository', 'rockchin/langbot', '--version', version, '--yes', '--timeout', '8m')
        if version == 'v4.10.3':
            assert 'Automatic API authentication requires Core v4.10.11 or newer; skipped' in installed
            assert 'CLI connected' not in installed
            assert not (directory / '.api-key').exists()
        else:
            assert 'CLI connected' in installed
            cli(version + '-whoami', 'whoami', '-o', 'json')
            cli(version + '-bots', 'bot', 'list', '-o', 'json')
        for action in ('restart', 'stop', 'start'):
            result = json.loads(cli(version + '-' + action, action, '--dir', str(directory), '--timeout', '5m', '-o', 'json'))
            assert result['ok'] is True
            assert not result['data'].get('local_auth_error')
        status = json.loads(cli(version + '-status', '--endpoint', 'http://127.0.0.1:' + str(port), 'status', '-o', 'json'))
        assert status['data']['runtime']['status'] == 'running', status
        print(version + ': Running; authentication ' + ('skipped' if version == 'v4.10.3' else 'connected'), flush=True)
finally:
    for file, project in reversed(projects):
        if file.exists():
            with (root / 'cleanup.log').open('a') as output:
                subprocess.run(['docker', 'compose', '-f', str(file), '-p', project, 'down', '--remove-orphans'],
                               stdout=output, stderr=output, check=True, timeout=300)
