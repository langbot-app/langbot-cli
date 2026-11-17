import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.request
import uuid

import yaml


parser = argparse.ArgumentParser()
parser.add_argument('--lbctl', required=True)
parser.add_argument('--official-compose', required=True)
parser.add_argument('--root', required=True)
args = parser.parse_args()
binary = str(Path(args.lbctl).resolve())
root = Path(args.root).resolve()
root.mkdir(parents=True, exist_ok=True)
environment = dict(os.environ, LANGBOT_HOME=str(root / 'home'),
                   LANGBOT_CONFIG=str(root / 'cli.yaml'), LANGBOT_TELEMETRY='0',
                   LBCTL_NO_UPDATE_CHECK='1', LANG='C')
for name in ('LANGBOT_API_KEY', 'LANGBOT_CONTEXT', 'LANGBOT_ENDPOINT'):
    environment.pop(name, None)
projects = []
prefix = 'lbctl-auth-' + uuid.uuid4().hex[:8]


def cli(label, *arguments):
    completed = subprocess.run([binary, *arguments, '-o', 'json'], env=environment,
                               capture_output=True, text=True, timeout=600)
    (root / (label + '.json')).write_text(completed.stdout)
    (root / (label + '.log')).write_text(completed.stderr)
    if completed.returncode:
        raise RuntimeError(label + ' failed: ' + completed.stdout)
    return json.loads(completed.stdout)['data']


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


def compose(file, project, *arguments):
    with (root / 'compose.log').open('a') as output:
        subprocess.run(['docker', 'compose', '-f', str(file), '-p', project, *arguments],
                       stdout=output, stderr=output, check=True, timeout=300)


try:
    installed = []
    for name in ('first', 'second'):
        directory = root / name
        project = prefix + '-' + name
        projects.append((directory / 'compose.yaml', project))
        result = cli(name + '-install', 'install', '--driver', 'docker', '--dir', str(directory),
                     '--name', name, '--project', project, '--port', str(free_port()),
                     '--plugin-debug-port', '0', '--adapter-ports', 'none', '--region', 'global',
                     '--image-repository', 'rockchin/langbot', '--version', 'v4.11.7', '--yes', '--timeout', '8m')
        record = result['record']
        context = record['deployment_id']
        installed.append(record)
        for command in (['whoami'], ['bot', 'list'], ['model', 'list']):
            cli(name + '-' + '-'.join(command), '--context', context, *command)
        body = root / 'bot.json'
        body.write_text('{}')
        cli(name + '-write-preflight', '--context', context, 'bot', 'create', '--file', str(body), '--dry-run')
        print(name + ': fresh Docker install, whoami, bot list, model list OK', flush=True)
    first_key = (root / 'first/.api-key').read_text()
    second_key = (root / 'second/.api-key').read_text()
    assert first_key != second_key
    first_identity = json.loads((root / 'first-whoami.json').read_text())['data']
    second_identity = json.loads((root / 'second-whoami.json').read_text())['data']
    assert first_identity['instance_uuid'] != second_identity['instance_uuid']
    print('multi-instance: distinct keys and identities OK', flush=True)

    official = yaml.safe_load(Path(args.official_compose).read_text())
    port = free_port()
    for name, service in official['services'].items():
        service.pop('container_name', None)
        service['image'] = 'rockchin/langbot:v4.11.7'
        service['ports'] = [f'127.0.0.1:{port}:5300'] if name == 'langbot' else []
    source = root / 'official'
    source.mkdir()
    file = source / 'compose.yaml'
    file.write_text(yaml.safe_dump(official, sort_keys=False))
    original = file.read_bytes()
    project = prefix + '-adopt'
    projects.append((file, project))
    compose(file, project, 'up', '-d')
    endpoint = f'http://127.0.0.1:{port}'
    deadline = time.monotonic() + 300
    while True:
        try:
            with urllib.request.urlopen(endpoint + '/healthz', timeout=3) as response:
                if response.status == 200:
                    break
        except (OSError, TimeoutError):
            pass
        if time.monotonic() > deadline:
            raise RuntimeError('official Compose startup timed out')
        time.sleep(2)
    adopted = cli('adopt', 'adopt', '--dir', str(root / 'adopted'), '--file', str(file),
                  '--project', project, '--endpoint', endpoint)
    assert adopted['restart_required'] is True
    assert file.read_bytes() == original
    context = adopted['record']['deployment_id']
    cli('adopt-restart', '--context', context, 'restart', '--timeout', '5m')
    cli('adopt-bots', '--context', context, 'bot', 'list')
    cli('adopt-write-preflight', '--context', context, 'bot', 'create', '--file', str(root / 'bot.json'), '--dry-run')
    reused = cli('readopt', 'adopt', '--dir', str(root / 'adopted'), '--file', str(file),
                 '--project', project, '--endpoint', endpoint)
    assert reused['restart_required'] is False
    assert file.read_bytes() == original
    print('adopt: root-owned config, activation hint, restart, bot list, key reuse, unchanged Compose OK', flush=True)

    keys = [first_key, second_key, (root / 'adopted/.api-key').read_text()]
    for path in [root / 'cli.yaml', *root.glob('*.json'), *root.glob('*.log')]:
        text = path.read_text()
        assert not any(key in text for key in keys), path.name
    print('all instances: CLI config, JSON and diagnostics exclude keys OK', flush=True)
finally:
    for file, project in reversed(projects):
        if file.exists():
            compose(file, project, 'down', '--remove-orphans')
