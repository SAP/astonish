#!/usr/bin/env python3
"""Fresh SQLite/Docker daemon; --verify-chat-first runs the real browser harness.

Required: LOCAL_BOT_BINARY (fresh Linux binary), LOCAL_BOT_DOCKER (Linux CLI),
LOCAL_BOT_LAYERS (host layers), ASTONISH_BOT_BROWSER_PATH (native Chrome).
Resume ONLY this launcher's --state; no production daemon/service-manager calls.
"""
import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import sqlite3
import base64
import uuid
import urllib.request

REPO = Path(__file__).resolve().parents[2]
IMAGE = 'ghcr.io/sap/astonish-sandbox-base:dev'
REQUIRED_EVIDENCE = ('minimal-conversation', 'responsibility-confirmed', 'tool-artifact-reload',
                     'scheduled-tick-return', 'exact-approval-continued', 'paused-approval-blocked',
                     'memory-next-answer', 'details-no-reconnect', 'paused-restart-bytes')


from agent_live_contract import validate_agent_evidence
from skills_live_contract import SKILLS_CONTRACT, SKILLS_REQUIRED, validate_skills_evidence as _validate_skills_evidence
from quality_live_contract import QUALITY_CONTRACT, QUALITY_REQUIRED, strict_json, validate_quality_evidence as _validate_quality_evidence


def validate_skills_evidence(state, actual):
    _validate_skills_evidence(state, actual, validate_quality_evidence)


def validate_quality_evidence(state, actual):
    _validate_quality_evidence(state, actual, validate_employee_evidence)


EMPLOYEE_CONTRACT = 'bot-employee-v1'
EMPLOYEE_REQUIRED = ('title-weather', 'candidate-node', 'schedule-natural', 'offline-restart-tick',
                     'reauth-same-home', 'routine-memory', 'cadence-change', 'pause',
                     'while-paused', 'resume', 'exception-denied-no-repeat',
                     'exception-approved-once', 'hard-delete-preserves-peer', 'terminal-evidence',
                     'existing-bot-read-no-authority', 'existing-bot-adoption',
                     'historical-disabled-schedule')
EMPLOYEE_ADOPTION_REQUIRED = EMPLOYEE_REQUIRED[-3:]
EMPLOYEE_HISTORY_STAGES = ('fixture', 'read', 'schedule', 'restart-active', 'cadence',
                           'pause', 'restart-paused', 'resume', 'restart-resumed')


def validate_employee_evidence(state, actual):
    import re
    if (any(not re.fullmatch(r'[0-9a-f]{64}', str(state.get(k, ''))) for k in ('binary_sha256', 'browser_sha256'))
            or actual.get('unimplemented') or actual.get('unverified')):
        raise RuntimeError('UNVERIFIED employee hashes or outstanding gates')
    if (actual.get('contract') != EMPLOYEE_CONTRACT or actual.get('status') != 'verified'
            or actual.get('candidate') != state.get('binary_sha256')
            or actual.get('browser_candidate') != state.get('browser_sha256')
            or not state.get('browser_sha256') or not state.get('bot_id')):
        raise RuntimeError('UNVERIFIED employee contract/candidate')
    if (set(actual.get('passed', [])) != set(EMPLOYEE_REQUIRED)
            or len(actual.get('passed', [])) != len(EMPLOYEE_REQUIRED)
            or set(actual.get('receipts', {})) != set(EMPLOYEE_REQUIRED)):
        raise RuntimeError('UNVERIFIED employee gates missing or unknown')
    for name in EMPLOYEE_REQUIRED:
        r = actual.get('receipts', {}).get(name, {})
        if (r.get('Candidate') != state['binary_sha256'] or r.get('BotID') != state['bot_id']
                or r.get('Home') != state['url'] + '/#/chat/bots/personal/' + state['bot_id']
                or not r.get('Detail') or not r.get('Source') or r.get('SchemaVersion') != 1):
            raise RuntimeError('UNVERIFIED employee receipt: ' + name)
    for name in EMPLOYEE_ADOPTION_REQUIRED:
        r = actual['receipts'][name]
        fixture = state.get('employee_legacy_fixture', {})
        if (any(not fixture.get(k) for k in ('bot_id', 'home_session_id', 'trigger_id', 'schedule_id'))
                or fixture['bot_id'] == state['bot_id']
                or r.get('LegacyBotID') != fixture['bot_id']
                or r.get('LegacyHome') != state['url'] + '/#/chat/bots/personal/' + fixture['bot_id']
                or r.get('HistoricalTriggerID') != fixture.get('trigger_id')
                or r.get('HistoricalScheduleID') != fixture.get('schedule_id')):
            raise RuntimeError('UNVERIFIED adoption fixture identity: ' + name)
        if name == 'existing-bot-adoption' and any(not r.get(k) for k in
                ('SourceRunID', 'SourceUserEventID', 'MandateID', 'OperationID')):
            raise RuntimeError('UNVERIFIED adoption provenance')
        if name == 'historical-disabled-schedule' and r.get('Stages') != list(EMPLOYEE_HISTORY_STAGES):
            raise RuntimeError('UNVERIFIED historical schedule lifecycle')


CHAT_MODEL_REQUIRED = ('title-thinking', 'title-web-result', 'question-reload-once', 'schedule-independent', 'paused-interactive', 'candidate-node', 'question-restart', 'schedule-consent')
CONFIG_KEYS = {
    'binary': 'LOCAL_BOT_BINARY', 'docker': 'LOCAL_BOT_DOCKER',
    'layers': 'LOCAL_BOT_LAYERS', 'browser': 'ASTONISH_BOT_BROWSER_PATH',
    'embedding_cache': 'LOCAL_BOT_EMBEDDING_CACHE',
    'provider_url': 'LOCAL_BOT_PROVIDER_URL', 'provider_api_key': 'LOCAL_BOT_PROVIDER_API_KEY',
    'model': 'LOCAL_BOT_MODEL',
}


CANDIDATE_CONFIG_VERSION = 1
CANDIDATE_PROVIDER_ENV = 'LOCAL_BOT_PROVIDER_SETTINGS_JSON'
AGENT_TARGET = {'provider': 'New AI Core Provider', 'provider_type': 'sap_ai_core',
                'model': 'anthropic--claude-4.6-sonnet'}
PATH_CONFIG_KEYS = {k: v for k, v in CONFIG_KEYS.items()
                    if k not in ('provider_url', 'provider_api_key', 'model')}


def candidate_provider(config):
    """Validate explicit instance settings only; never resolve credentials or call a model."""
    required = set(PATH_CONFIG_KEYS) | {'schema_version', 'provider_instance', 'provider_config', 'model', 'intended'}
    if (not isinstance(config, dict) or set(config) != required
            or type(config.get('schema_version')) is not int
            or config['schema_version'] != CANDIDATE_CONFIG_VERSION):
        raise ValueError('versioned config requires exactly the documented version 1 fields')
    if any(not isinstance(config[k], str) or not config[k].strip()
           for k in set(PATH_CONFIG_KEYS) | {'provider_instance', 'model'}):
        raise ValueError('candidate paths and identity must be nonempty strings')
    provider = config['provider_config']
    if (not isinstance(provider, dict) or not provider
            or any(not isinstance(k, str) or not isinstance(v, str) or not v.strip()
                   for k, v in provider.items())):
        raise ValueError('provider_config requires nonempty string fields')
    fields = {'litellm': {'type', 'base_url', 'api_key'},
              'sap_ai_core': {'type', 'base_url', 'auth_url', 'client_id', 'client_secret', 'resource_group'}}
    if provider.get('type') not in fields or set(provider) != fields[provider['type']]:
        raise ValueError('unsupported or incomplete isolated provider configuration')
    from urllib.parse import urlparse
    for key in ('base_url', 'auth_url'):
        if key not in provider: continue
        try:
            url = urlparse(provider[key])
            valid = (url.scheme in ('http', 'https') and url.hostname and not url.username
                     and not url.password and not url.query and not url.fragment)
        except ValueError:
            valid = False
        if not valid:
            raise ValueError('invalid provider endpoint; credentials must be separate')
    identity = {'provider': config['provider_instance'], 'provider_type': provider['type'], 'model': config['model']}
    if config['intended'] != identity:
        raise ValueError('intended provider/model parity declaration missing or mismatched')
    return {'providers': {config['provider_instance']: dict(provider)},
            'default_provider': config['provider_instance'], 'default_model': config['model']}


def require_agent_config(config):
    candidate = dict(config) if isinstance(config, dict) else {}
    candidate.pop('bot_routine_policy', None)
    candidate_provider(candidate)
    if candidate['intended'] != AGENT_TARGET:
        raise ValueError('agent acceptance requires the intended AI Core provider and model; aliases are not parity')


def routine_policy_yaml(policy):
    if not isinstance(policy, dict) or set(policy) != {'require_approval'} or policy['require_approval'] not in ([], ['memory_forget']):
        raise ValueError('bot_routine_policy permits only require_approval: [memory_forget]')
    return '\nbot_routine_policy:\n  require_approval: ' + json.dumps(policy['require_approval']) + '\n'


def config_environment(config):
    if not isinstance(config, dict):
        raise ValueError('config requires documented object fields')
    config = dict(config)
    routine = config.pop('bot_routine_policy', {'require_approval': []})
    routine_policy_yaml(routine)
    env = dict(os.environ)
    # An explicit file, not ambient provider state, is authoritative.
    env[CANDIDATE_PROVIDER_ENV] = ''
    if 'schema_version' in config:
        provider = candidate_provider(config)
        env.update({PATH_CONFIG_KEYS[k]: config[k] for k in PATH_CONFIG_KEYS})
        env[CANDIDATE_PROVIDER_ENV] = json.dumps(provider)
        env['LOCAL_BOT_MODEL'] = config['model']
        env['LOCAL_BOT_PROVIDER_URL'] = ''
        env['LOCAL_BOT_PROVIDER_API_KEY'] = ''
    else:
        if set(config) != set(CONFIG_KEYS) or any(not isinstance(v, str) or not v.strip() for v in config.values()):
            raise ValueError('config requires exactly the documented nonempty string fields')
        from urllib.parse import urlparse
        url = urlparse(config['provider_url'])
        if url.scheme not in ('http', 'https') or not url.hostname or url.username or url.password:
            raise ValueError('invalid provider URL; credentials must be separate')
        env.update({CONFIG_KEYS[k]: v for k, v in config.items()})
    env['LOCAL_BOT_ROUTINE_POLICY_JSON'] = json.dumps(routine)
    errors = prerequisites(env)
    if errors: raise ValueError('; '.join(errors))
    source = Path(config['embedding_cache'])
    if not source.is_absolute() or source.is_symlink() or not source.is_dir():
        raise ValueError('invalid embedding cache directory')
    for name, expected in EMBEDDING_HASHES.items():
        path = source / name
        if path.is_symlink() or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise ValueError('embedding cache asset verification failed: ' + name)
    # Reject native macOS binaries accidentally supplied as the Linux candidate/CLI.
    for key in ('binary', 'docker'):
        with open(config[key], 'rb') as f: header = f.read(20)
        if header[:4] != b'\x7fELF' or header[18:20] != b'\xb7\x00':
            raise ValueError(key + ' must be Linux arm64 ELF')
    return env


def validate_chat_model_evidence(state, actual):
    if actual.get('contract') != 'chat-model-v1' or actual.get('status') != 'verified':
        raise RuntimeError('UNVERIFIED title-first contract')
    for name in CHAT_MODEL_REQUIRED:
        receipt = actual.get('receipts', {}).get(name, {})
        if (name not in actual.get('passed', []) or receipt.get('Candidate') != state['binary_sha256']
                or not receipt.get('BotID') or not receipt.get('Detail')
                or receipt.get('Home') != state.get('url', '') + '/#/chat/bots/personal/' + str(receipt.get('BotID', ''))):
            raise RuntimeError('UNVERIFIED title-first receipt: ' + name)


def private_json(path, value):
    path.write_text(json.dumps(value, indent=2)); path.chmod(0o600)


def prerequisites(env):
    missing = []
    try:
        startup_timeout(env)
    except ValueError as error:
        missing.append(str(error))
    for key in ('LOCAL_BOT_BINARY', 'LOCAL_BOT_DOCKER', 'ASTONISH_BOT_BROWSER_PATH'):
        p = Path(env.get(key, ''))
        if not p.is_absolute() or not p.is_file() or not os.access(p, os.X_OK):
            missing.append(key + ' must name an absolute executable file')
    layers = Path(env.get('LOCAL_BOT_LAYERS', ''))
    if not layers.is_absolute() or not (layers / '@base/rootfs').is_dir():
        missing.append('LOCAL_BOT_LAYERS must contain @base/rootfs')
    for tool in ('docker', 'go'):
        if not shutil.which(tool): missing.append(tool + ' unavailable')
    return missing


def wrapper(name, alias, layers, candidate=None, native=False):
    # Bash literal variables avoid word splitting of macOS paths with spaces.
    # The product entrypoint overlays astonish-host onto the chroot binary.
    # Inject ONLY for this launcher's org, without editing an image or layer.
    import shlex
    injection = ''
    if candidate is not None:
        injection = ('candidate=' + shlex.quote(str(candidate)) + '\n'
                     + 'if [ "${1:-}" = container ] && [ "${2:-}" = run ]; then\n'
                     + '  owned=false\n  for arg in "$@"; do\n'
                     + '    if [ "$arg" = ' + shlex.quote('--label=astonish.org=' + name) + ' ]; then owned=true; fi\n'
                     + '  done\n  if [ "$owned" != true ]; then echo "candidate node: refusing unowned session" >&2; exit 64; fi\n'
                     + '  shift 2\n  set -- container run "--mount=type=bind,source=$candidate,target=/usr/local/bin/astonish-host,readonly" "$@"\n'
                     + 'fi\n')
    if native:
        injection = ('if [ "${1:-}" = container ] && [ "${2:-}" = run ]; then\n'
                     + '  owned=false; pinned=false\n  for arg in "$@"; do\n'
                     + '    if [ "$arg" = ' + shlex.quote('--label=astonish.org=' + name) + ' ]; then owned=true; fi\n'
                     + '    if [ "$arg" = "${ASTONISH_SANDBOX_IMAGE:?missing candidate image}" ]; then pinned=true; fi\n'
                     + '  done\n  if [ "$owned" != true ] || [ "$pinned" != true ]; then echo "candidate node: refusing unowned or unpinned session" >&2; exit 64; fi\n'
                     + 'fi\n')
    return ('#!/bin/bash\nset -eu\n' + 'alias=' + shlex.quote(str(alias)) + '\nlayers=' + shlex.quote(str(layers)) + '\n'
            + 'if [ "$1" = ps ]; then shift; exec /opt/docker/docker ps --filter label=astonish.org=' + name + ' "$@"; fi\n'
            + injection
            + 'args=()\nfor arg in "$@"; do args+=("${arg//"$alias"/$layers}"); done\nexec /opt/docker/docker "${args[@]}"\n')


# Pinned non-quantized assets verified against immutable upstream revision
# 1110a243fdf4706b3f48f1d95db1a4f5529b4d41; no config/credentials reuse.
EMBEDDING_MODEL = 'sentence-transformers_all-MiniLM-L6-v2'
EMBEDDING_HASHES = {
    'config.json': '953f9c0d463486b10a6871cc2fd59f223b2c70184f49815e7efbcab5d8908b41',
    'model.onnx': '6fd5d72fe4589f189f8ebc006442dbb529bb7ce38f8082112682524616046452',
    'tokenizer.json': 'be50c3628f2bf5bb5e3a7f17b1f74611b2561a3a27eeab05e5aa30f411572037',
    'tokenizer_config.json': 'acb92769e8195aabd29b7b2137a9e6d6e25c476a4f15aa4355c233426c61576b',
    'special_tokens_map.json': '303df45a03609e4ead04bc3dc1536d0ab19b5358db685b6f3da123d05ec200e3',
    'vocab.txt': '07eced375cec144d27c900241f3e339478dec958f92fddbc551f295c992038a3',
}


def stage_embedding_cache(root, source):
    """Copy only hash-pinned model assets to a fresh owned root before startup.

    LOCAL_BOT_EMBEDDING_CACHE names the complete product model directory.
    Source is read-only; no symlinks, hard links, config trees or credentials copy.
    """
    source = Path(source)
    if not source.is_absolute() or source.is_symlink() or not source.is_dir():
        raise RuntimeError('embedding cache must be an absolute non-symlink directory')
    destination = root / 'config/astonish/models' / EMBEDDING_MODEL
    if destination.exists():
        raise RuntimeError('refusing to overwrite embedding assets')
    destination.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix='.embedding-', dir=destination.parent))
    try:
        for name, expected in EMBEDDING_HASHES.items():
            src = source / name
            if src.is_symlink() or not src.is_file():
                raise RuntimeError('missing or symlink embedding asset: ' + name)
            data = src.read_bytes()
            if hashlib.sha256(data).hexdigest() != expected:
                raise RuntimeError('embedding asset hash mismatch: ' + name)
            target = staging / name
            target.write_bytes(data)
            target.chmod(0o600)
            if hashlib.sha256(target.read_bytes()).hexdigest() != expected:
                raise RuntimeError('staged embedding asset hash mismatch: ' + name)
        staging.rename(destination)
    finally:
        if staging.exists(): shutil.rmtree(staging)
    private_json(root / 'embedding-cache-receipt.json', {
        'source': str(source), 'destination': str(destination),
        'revision': '1110a243fdf4706b3f48f1d95db1a4f5529b4d41',
        'sha256': EMBEDDING_HASHES, 'verified_before_startup': True,
    })


def provider_settings(env):
    """One explicit source for startup YAML and authenticated settings provision."""
    if env.get(CANDIDATE_PROVIDER_ENV):
        try:
            settings = strict_json(env[CANDIDATE_PROVIDER_ENV])
            instance = settings['default_provider']
            provider = settings['providers'][instance]
            config = {k: '/explicit' for k in PATH_CONFIG_KEYS}
            config.update(schema_version=CANDIDATE_CONFIG_VERSION, provider_instance=instance,
                          provider_config=provider, model=settings['default_model'],
                          intended={'provider': instance, 'provider_type': provider['type'], 'model': settings['default_model']})
            validated = candidate_provider(config)
            if settings != validated: raise ValueError('unexpected settings')
            return validated
        except Exception:
            raise ValueError('invalid explicit candidate provider settings') from None
    keys = ('LOCAL_BOT_PROVIDER_URL', 'LOCAL_BOT_MODEL', 'LOCAL_BOT_PROVIDER_API_KEY')
    if any(key in env for key in keys):
        if any(not isinstance(env.get(key), str) or not env[key].strip() for key in keys):
            raise ValueError('explicit provider requires URL, model and API key')
        url, model, key = (env[key] for key in keys)
    else:
        # Preserve the legacy no-config smoke mode only.
        url, model, key = 'http://host.docker.internal:4000', 'claude-4.8-opus', 'not-a-secret'
    return {'providers': {'local-litellm': {'type': 'litellm', 'base_url': url, 'api_key': key}},
            'default_provider': 'local-litellm', 'default_model': model}


def create(env):
    provider = provider_settings(env)
    (REPO / '.tmp').mkdir(exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix='astonish-chat-first-', dir=REPO / '.tmp'))
    root.chmod(0o700)
    for sub in ('home', 'config/astonish/sandbox', 'data', 'logs', 'bin'): (root / sub).mkdir(parents=True, exist_ok=True)
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
    name = 'astonish-chat-first-' + secrets.token_hex(4)
    creds = dict(email='chat-first@example.test', password=secrets.token_urlsafe(32), bootstrap_token=secrets.token_urlsafe(32), url=f'http://127.0.0.1:{port}')
    private_json(root / 'credentials.json', creds)
    (root / 'config/astonish/config.yaml').write_text(f'''storage:
  backend: sqlite
  sqlite:
    data_dir: {root}/data
  auth:
    mode: builtin
    default_org_name: Chat First Smoke
    default_org_slug: {name}
daemon:
  port: 9393
  log_dir: {root}/logs
sandbox:
  enabled: true
  backend: docker
scheduler:
  enabled: true
channels:
  enabled: false
''')
    config_path = root / 'config/astonish/config.yaml'
    with config_path.open('a') as config:
        config.write(routine_policy_yaml(json.loads(env.get('LOCAL_BOT_ROUTINE_POLICY_JSON', '{"require_approval": []}'))))
    # JSON-quoted YAML structures preserve newlines/quotes without interpolation.
    # Stage the same explicit settings for every fresh mode before docker starts.
    config_path.chmod(0o600)
    with config_path.open('a') as config:
        config.write('\ngeneral: ' + json.dumps({k: provider[k] for k in ('default_provider', 'default_model')})
                     + '\nproviders: ' + json.dumps(provider['providers']) + '\n')
    private_json(root / 'provider-startup-receipt.json', {
        'staged_before_startup': True, 'explicit': bool(env.get(CANDIDATE_PROVIDER_ENV) or env.get('LOCAL_BOT_PROVIDER_URL')),
        'config_mode': oct(config_path.stat().st_mode & 0o777),
        'default_provider': provider['default_provider'],
        'provider_type': provider['providers'][provider['default_provider']]['type'],
        'default_model': provider['default_model'],
        'model_configured': bool(provider['default_model']),
        'provider_url_configured': bool(provider['providers'][provider['default_provider']].get('base_url')),
        'provider_api_key_configured': bool(provider['providers'][provider['default_provider']].get('api_key')),
        'runtime_parity_verified': False,
    })
    import native_candidate_staging as native
    shutil.copy2(env['LOCAL_BOT_BINARY'], root / 'astonish')
    shutil.copy2(env['LOCAL_BOT_DOCKER'], root / 'docker-real')
    state = dict(root=str(root), url=creds['url'], port=port, container=name, launcher_pid=os.getpid(), status='staging-image', evidence=[], binary_sha256=hashlib.sha256((root / 'astonish').read_bytes()).hexdigest())
    private_json(root / 'state.json', state)
    print('STATE=' + str(root / 'state.json') + ' LAUNCHER_PID=' + str(os.getpid()), flush=True)
    docker = root / 'bin/docker'
    docker.write_text(wrapper(name, root / 'config/astonish/sandbox/layers', env['LOCAL_BOT_LAYERS'], native=True)); docker.chmod(0o755)
    try:
        if env.get('LOCAL_BOT_EMBEDDING_CACHE'):
            stage_embedding_cache(root, env['LOCAL_BOT_EMBEDDING_CACHE'])
        state.update(native.stage(root, name, IMAGE, docker.read_text()))
    except Exception as error:
        state['status'] = 'blocked'; state['blocker'] = 'native candidate staging: ' + str(error)
        private_json(root / 'state.json', state)
        raise
    state['status'] = 'launching'
    private_json(root / 'state.json', state)
    daemon_env = dict(HOME=str(root / 'home'), XDG_CONFIG_HOME=str(root / 'config'), ASTONISH_DATA_DIR=str(root / 'data'), ASTONISH_MASTER_KEY=secrets.token_hex(32), ASTONISH_BOOTSTRAP_TOKEN=creds['bootstrap_token'], ASTONISH_SANDBOX_IMAGE=state['candidate_image_id'], PATH='/opt/astonish-launcher/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin')
    e = root / 'daemon.env'; e.write_text(''.join(k + '=' + v + '\n' for k, v in daemon_env.items())); e.chmod(0o600)
    args = ['docker', 'run', '-d', '--name', name, '--label', 'astonish.chat-first-smoke=' + name, '-p', f'127.0.0.1:{port}:9393', '--env-file', str(e)]
    for mount in (f'type=bind,source={root},target={root}', f'type=bind,source={env["LOCAL_BOT_LAYERS"]},target={root}/config/astonish/sandbox/layers,readonly', 'type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock'):
        args += ['--mount', mount]
    args += ['--entrypoint', '/opt/astonish', state['candidate_image_id'], 'daemon', 'run']
    state['container_id'] = subprocess.check_output(args, text=True, timeout=60).strip()
    private_json(root / 'state.json', state)
    return state


def inspect_owned(state):
    root = Path(state['root']).resolve()
    if root.parent != (REPO / '.tmp').resolve() or not root.name.startswith('astonish-chat-first-'):
        raise RuntimeError('not an owned smoke root')
    if hashlib.sha256((root / 'astonish').read_bytes()).hexdigest() != state['binary_sha256']:
        raise RuntimeError('owned daemon binary changed')
    if state.get('browser_sha256') and hashlib.sha256((root / 'bin/chat-first-browser').read_bytes()).hexdigest() != state['browser_sha256']:
        raise RuntimeError('owned browser binary changed')
    info = json.loads(subprocess.check_output(['docker', 'inspect', state['container']], text=True))[0]
    if info['Id'] != state['container_id'] or info['Config']['Labels'].get('astonish.chat-first-smoke') != state['container']:
        raise RuntimeError('container ownership mismatch')
    if state.get('candidate_storage'):
        import native_candidate_staging as native
        if state['candidate_storage'] != native.MODE:
            raise RuntimeError('unknown candidate storage mode')
        native.inspect_native(state, info)
    if not info['State']['Running']: raise RuntimeError('owned daemon is not running; inspect owned logs')
    bindings = info['HostConfig']['PortBindings']['9393/tcp']
    if bindings != [{'HostIp': '127.0.0.1', 'HostPort': str(state['port'])}]: raise RuntimeError('not loopback-only')
    return info


def startup_timeout(env=None):
    env = os.environ if env is None else env
    try:
        seconds = int(env.get('LOCAL_BOT_STARTUP_TIMEOUT_SECONDS', '300'))
    except (ValueError, TypeError):
        raise ValueError('LOCAL_BOT_STARTUP_TIMEOUT_SECONDS must be an integer in 1..900')
    if not 1 <= seconds <= 900:
        raise ValueError('LOCAL_BOT_STARTUP_TIMEOUT_SECONDS must be an integer in 1..900')
    return seconds


def startup_phase(state, info):
    # Classify only known markers; never print daemon logs containing secrets.
    path = Path(state['root']) / 'logs/daemon.log'
    # The product writes phases to this file, not Docker stdout. Ignore older
    # startups in the append-only log by anchoring to the newest session store.
    try:
        with path.open('rb') as stream:
            stream.seek(max(0, path.stat().st_size - 65536))
            logs = stream.read().decode('utf-8', errors='replace')
    except FileNotFoundError:
        return 'daemon log not created yet; waiting for HTTP readiness'
    logs = logs.rsplit('Session store:', 1)[-1]
    if 'phase=serve-handoff outcome=complete' in logs:
        return 'serve handoff complete; checking HTTP readiness'
    if 'phase=tool-index-sync' in logs:
        return 'tool-index initialization complete; waiting for serve handoff'
    if 'phase=sandbox' in logs:
        return 'tool-index initialization pending after sandbox wiring (local embeddings may take several minutes)'
    return 'daemon initialization; waiting for HTTP readiness'


def wait_healthy(state, previous_start=None):
    timeout = startup_timeout()
    started = time.monotonic(); deadline = started + timeout; heartbeat = started
    info = inspect_owned(state)
    last_error = 'HTTP readiness not observed'
    while True:
        now = time.monotonic()
        if now >= deadline:
            raise RuntimeError(f'owned daemon health timeout after {timeout}s: {last_error}')
        if now >= heartbeat:
            # Ownership/process errors are fatal, never swallowed as HTTP retries.
            info = inspect_owned(state)
            phase = startup_phase(state, info)
            print(f'READINESS elapsed={now-started:.0f}s deadline={timeout}s phase={phase}', file=sys.stderr, flush=True)
            heartbeat = now + 10
        try:
            # /health is a SPA fallback in this candidate, so HTTP 200 alone
            # is not readiness. Require the unauthenticated typed auth endpoint.
            with urllib.request.urlopen(state['url'] + '/api/auth/setup-status', timeout=min(2, deadline-now)) as response:
                payload = json.load(response)
                valid = isinstance(payload, dict) and type(payload.get('initialized')) is bool and payload.get('auth_mode') == 'builtin'
                if response.status == 200 and valid and (previous_start is None or info['State']['StartedAt'] != previous_start):
                    return inspect_owned(state)
                last_error = f'HTTP {response.status}; typed readiness/restart identity not satisfied'
        except (OSError, urllib.error.URLError, ValueError) as error:
            last_error = type(error).__name__
        time.sleep(min(1, max(0, deadline-time.monotonic())))


def validate_employee_resume(state, path, env):
    root = Path(state['root']).resolve()
    if path.resolve() != root / 'state.json':
        raise RuntimeError('employee resume requires canonical owned state')
    inspect_owned(state)
    if hashlib.sha256(Path(env['LOCAL_BOT_BINARY']).read_bytes()).hexdigest() != state['binary_sha256']:
        raise RuntimeError('employee resume config candidate mismatch')
    if state.get('workflow') not in (None, EMPLOYEE_CONTRACT) or (state.get('bot_id') and state.get('workflow') != EMPLOYEE_CONTRACT):
        raise RuntimeError('cannot resume legacy Bot as employee')
    state['diagnostic_resume'] = True


def employee_legacy_setup(state, call):
    """Owned fixture only: inert legacy creation and disabled trigger, never a turn."""
    inspect_owned(state)
    root = Path(state['root'])
    if state.get('bot_id') or state.get('employee_legacy_attempted'):
        raise RuntimeError('legacy fixture requires fresh setup; ambiguous creation is never replayed')
    state['employee_legacy_attempted'] = True
    private_json(root / 'state.json', state)
    bot = call('/api/studio/bots?scope=personal', dict(
        name='Legacy preservation witness', start_from_title=False,
        idempotency_key='employee-legacy-' + root.name,
        definition=dict(charter='Help with requested reminders.')))
    bid = str(uuid.UUID(bot['id']))
    tid = str(uuid.uuid4())
    bot = call('/api/studio/bots/' + bid + '/triggers/' + tid + '?scope=personal', dict(
        expected_revision=bot['revision'], reason='Owned historical disabled schedule fixture',
        definition=dict(id=tid, kind='cron', enabled=False, cron='0 0 1 6 *',
                        timezone='UTC', instructions='Historical June reminder; leave disabled.')), 'PUT')
    version = call(f'/api/studio/bots/{bid}/versions/{bot["version"]}?scope=personal')
    trigger = next(t for t in version['triggers'] if t['id'] == tid)
    if trigger.get('enabled') is not False:
        raise RuntimeError('legacy fixture is not disabled')
    sid = str(uuid.UUID(trigger['schedule_id']))
    state['employee_legacy_fixture'] = dict(bot_id=bid, trigger_id=tid, schedule_id=sid,
                                           home_session_id=bot['home_session_id'])
    disk = employee_readonly(dict(state, bot_id=bid))
    if not disk['observation_complete'] or disk['runs']:
        raise RuntimeError('legacy fixture readback incomplete or unexpectedly admitted work')
    private_json(root / 'employee-legacy-fixture-disk.json', disk)
    private_json(root / 'employee-legacy-fixture-version.json', version)
    private_json(root / 'state.json', state)


def setup(state):
    root = Path(state['root']); creds = json.loads((root / 'credentials.json').read_text())
    jar = http.cookiejar.CookieJar(); opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    def call(path, body=None, method=None):
        req = urllib.request.Request(state['url'] + path, data=json.dumps(body).encode() if body is not None else None, method=method, headers={'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest'})
        print(f'SETUP phase={path} method={method or ("POST" if body is not None else "GET")}', file=sys.stderr, flush=True)
        try:
            with opener.open(req, timeout=20) as response: return json.load(response)
        except Exception as error:
            raise RuntimeError(f'setup {path} failed: {type(error).__name__}') from error
    wait_healthy(state)
    if not call('/api/auth/setup-status')['initialized']:
        call('/api/auth/register', dict(email=creds['email'], password=creds['password'], bootstrap_token=creds['bootstrap_token']))
    call('/api/auth/login', dict(email=creds['email'], password=creds['password']))
    if not state.get('provider_configured'):
        call('/api/settings/platform/providers', provider_settings(os.environ), 'PUT')
        state['provider_configured'] = True
    if state.get('workflow') == EMPLOYEE_CONTRACT and not state.get('diagnostic_resume'):
        employee_legacy_setup(state, call)
    state['daemon_pid'] = inspect_owned(state)['State']['Pid']
    state['status'] = 'ready'; private_json(root / 'state.json', state)


def validate_evidence(state, actual):
    missing = set(REQUIRED_EVIDENCE) - set(actual.get('passed', []))
    if missing or actual.get('status') != 'verified':
        raise RuntimeError('UNVERIFIED: ' + ', '.join(sorted(missing)))
    if actual.get('candidate') != state['binary_sha256'] or actual.get('bot_id') != state['bot_id']:
        raise RuntimeError('evidence candidate/Bot mismatch')
    for name in REQUIRED_EVIDENCE:
        receipt = actual.get('receipts', {}).get(name, {})
        if (receipt.get('Candidate') != state['binary_sha256'] or receipt.get('BotID') != state['bot_id']
                or not receipt.get('Detail') or receipt.get('Home') != state['url'] + '/#/chat/bots/personal/' + state['bot_id']):
            raise RuntimeError('missing or mismatched assertion receipt: ' + name)


def restart_owned(state):
    # Browser decides WHEN (only after UI pause); launcher alone owns process control.
    inspect_owned(state)
    root = Path(state['root'])
    checkpoint = json.loads((root / 'coherent-checkpoint.json').read_text())
    if checkpoint.get('BotID') != state.get('bot_id') or not checkpoint.get('Receipts', {}).get('paused-approval-blocked'):
        raise RuntimeError('restart lacks owned paused assertion checkpoint')
    if state.get('restart_attempted'):
        raise RuntimeError('restart already attempted; never replay ambiguous callback')
    state['restart_attempted'] = True
    private_json(root / 'state.json', state)
    subprocess.run(['docker', 'restart', state['container_id']], check=True, timeout=90)
    wait_healthy(state)
    state['restart_verified'] = True
    private_json(root / 'state.json', state)


def assert_browser_idle(root):
    processes = subprocess.check_output(['ps', '-ax', '-o', 'command='], text=True)
    for command in processes.splitlines():
        if str(root / 'bin/chat-first-browser') in command or str(root / 'chrome-profile') in command:
            raise RuntimeError('owned browser still running; refresh refused')


def refresh_browser(state):
    """Explicit harness-only hash transition, never a daemon/candidate migration."""
    inspect_owned(state)
    root = Path(state['root'])
    assert_browser_idle(root)
    if not state.get('browser_sha256'): raise RuntimeError('refresh requires a pinned browser')
    original = (root / 'coherent-checkpoint.json').read_bytes()
    cp = json.loads(original)
    if cp.get('Candidate') != state['binary_sha256'] or cp.get('BotID') != state['bot_id']:
        raise RuntimeError('refresh checkpoint identity mismatch')
    browser = root / 'bin/chat-first-browser'
    pending = root / 'bin/chat-first-browser.pending'
    subprocess.run(['go', 'build', '-o', str(pending), './tests/e2eboot/chat_first_browser'], cwd=REPO, check=True, timeout=180)
    inspect_owned(state)
    assert_browser_idle(root)
    if (root / 'coherent-checkpoint.json').read_bytes() != original:
        raise RuntimeError('checkpoint changed during refresh')
    new_hash = hashlib.sha256(pending.read_bytes()).hexdigest()
    receipt = dict(old_hash=state['browser_sha256'], new_hash=new_hash, candidate=state['binary_sha256'], bot_id=state['bot_id'], container_id=state['container_id'], checkpoint_sha256=hashlib.sha256(original).hexdigest(), reason='explicit --refresh-browser; harness only; no consent reset')
    private_json(root / ('browser-refresh-' + str(time.time_ns()) + '.json'), receipt)
    pending.replace(browser)
    state['browser_sha256'] = new_hash
    private_json(root / 'state.json', state)
    print('HARNESS_REFRESH=' + json.dumps(receipt), flush=True)


# Schema-derived allowlists (ent/{personal,team}/schema); never SELECT * or
# share a column union across tables. Home bindings belong only to selected Bots.
EMPLOYEE_EVIDENCE_COLUMNS = {
    'bots': frozenset('id home_session_id home_binding name status version active_run_id lease_expires_at data created_at updated_at'.split()),
    'bot_runs': frozenset('id bot_id session_id version status lease_expires_at data created_at updated_at'.split()),
    'bot_goals': frozenset('id bot_id title status data created_at updated_at'.split()),
    'bot_versions': frozenset('id bot_id version created_at'.split()),
    'bot_approvals': frozenset('id bot_id run_id version status data created_at'.split()),
    'sessions': frozenset('id parent_id bot_id bot_run_id'.split()),
    'session_events': frozenset('id session_id event_data created_at'.split()),
    'scheduled_jobs': frozenset('id name schedule mode payload status last_run_at next_run_at created_at updated_at'.split()),
}
EMPLOYEE_JSON_COLUMNS = frozenset(('data', 'event_data', 'payload', 'home_binding'))
# Public JSON fields of store.BotRun (pkg/store/bots.go). Repository metadata
# such as Instructions and OccurrenceAt is not a second public run document.
EMPLOYEE_RUN_FIELDS = frozenset('work skill_id invocation continuation_reason execution_mode presentation user_event_id history_cursor intent id bot_id version session_id trigger trigger_id idempotency_key status lease created_at started_at finished_at todos checkpoint report usage provider model'.split())


def employee_blob(value):
    """Lossless canonical byte evidence; never guess an encoding for binary data."""
    return {'type': 'sqlite_blob', 'encoding': 'base64',
            'base64': base64.b64encode(value).decode('ascii'), 'size': len(value)}


def employee_evidence_row(row, table, errors, blobs):
    result = {}
    for key, value in dict(row).items():
        if key not in EMPLOYEE_EVIDENCE_COLUMNS.get(table, frozenset()):
            continue
        if isinstance(value, bytes):
            if key in EMPLOYEE_JSON_COLUMNS:
                # Keep exact original bytes separately, even when UTF-8 is valid.
                blobs.append(dict(table=table, row_id=row['id'], column=key,
                                  value=employee_blob(value)))
                try:
                    value = value.decode('utf-8', errors='strict')
                except UnicodeDecodeError:
                    value = employee_blob(value)
            else:
                value = {'type': 'sqlite_blob', 'size': len(value),
                         'sha256': hashlib.sha256(value).hexdigest()}
        result[key] = value
    return result


def employee_json_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate member')
        result[key] = value
    return result


def employee_object(value):
    if not isinstance(value, dict) or value.get('type') == 'sqlite_blob':
        raise ValueError('not an object')
    if len({key.lower() for key in value}) != len(value):
        raise ValueError('ambiguous member')
    return value


def employee_document(value, location, errors):
    try:
        document = employee_object(json.loads(value, object_pairs_hook=employee_json_pairs)
                                   if isinstance(value, str) else value)
        wrappers = [key for key in document if key.lower() == 'bot_document']
        if wrappers:
            if wrappers != ['bot_document'] or len(document) != 1 or not isinstance(document['bot_document'], str):
                raise ValueError('invalid wrapper')
            document = employee_object(json.loads(document['bot_document'], object_pairs_hook=employee_json_pairs))
            if any(key.lower() == 'bot_document' for key in document):
                raise ValueError('recursive wrapper')
        return document
    except (ValueError, TypeError):
        # Do not include raw data (possibly sensitive) in the diagnostic.
        errors.append(location + ': invalid UTF-8 JSON object')
        return {}


def employee_run_session(row, errors):
    """Normalize storage, then corroborate identity; never repair from row fields."""
    count = len(errors)
    document = employee_document(row.get('data'), 'bot_runs.data', errors)
    if len(errors) != count:
        return None
    try:
        fields = {key.lower(): value for key, value in document.items()}
        if 'run' in fields:
            # Private repository metadata is allowed, but not a second public identity.
            # botStoredRun.work is private BotWorkState; only Run.work is the
            # public projection. Do not use the outer work to discover sessions.
            if (EMPLOYEE_RUN_FIELDS - {'work'}).intersection(fields):
                raise ValueError('mixed run')
            document = employee_object(fields['run'])
            fields = {key.lower(): value for key, value in document.items()}
        if 'run' in fields or 'bot_document' in fields:
            raise ValueError('recursive run')
        for key in ('id', 'bot_id'):
            if key in fields and fields[key] != row.get(key):
                raise ValueError('identity mismatch')
        sid = fields.get('session_id')
        if 'session_id' in row and row['session_id'] != sid:
            raise ValueError('session mismatch')
        if sid is not None:
            if not isinstance(sid, str) or not sid or fields.get('id') != row['id'] or fields.get('bot_id') != row['bot_id']:
                raise ValueError('missing session identity')
        return sid
    except (ValueError, TypeError):
        errors.append('bot_runs.data: invalid or conflicting run identity')
        return None


def employee_readonly(state):
    """Acceptance evidence must share the running writer's filesystem namespace.

    No host fallback: Docker/helper/ownership errors fail closed. Only projected
    SQL results cross stdout; no API credentials or DB copies. The helper binary
    is hash-verified in container-local /tmp; it never writes database rows.
    """
    from employee_namespace_reader import Reader
    info = inspect_owned(state)
    root = Path(state['root']).resolve()
    mounts = info.get('Mounts', [])
    if not any(m.get('Type') == 'bind' and m.get('Source') == str(root)
               and m.get('Destination') == str(root) for m in mounts):
        raise RuntimeError('employee writer root mount mismatch')
    data = root / 'data'
    if any(Path(m['Destination']).is_relative_to(data)
           or data.is_relative_to(Path(m['Destination'])) and Path(m['Destination']) != root
           for m in mounts if m.get('Destination')):
        raise RuntimeError('employee writer data shadow mount')
    reader = Reader(REPO, root, info)
    try:
        observed = employee_readonly_local(state, reader=reader)
    finally:
        reader.close()
    after = inspect_owned(state)
    if after['State']['StartedAt'] != info['State']['StartedAt']:
        raise RuntimeError('employee writer restarted during observation')
    observed['observation_source'] = dict(namespace='owned-writer-container',
                                        container_id=info['Id'],
                                        started_at=info['State']['StartedAt'],
                                        transaction='mode=ro; query_only; BEGIN')
    return observed


def employee_readonly_local(state, reader=None):
    """Read only the one owned scoped SQLite database containing this exact Bot.

    No cookie, API call, handler, scheduler mutation, or model is involved.
    SQLite URI mode=ro and query_only make accidental writes impossible.
    """
    root = Path(state['root']).resolve()
    if root.parent != (REPO / '.tmp').resolve() or not root.name.startswith('astonish-chat-first-'):
        raise RuntimeError('not an owned employee root')
    bot = str(uuid.UUID(state['bot_id']))
    pinned = state.get('employee_database')
    paths = [Path(pinned)] if pinned else (reader.paths() if reader else sorted((root / 'data').rglob('*.db')))
    connect = reader.connect if reader else sqlite3.connect
    matches = []
    for path in paths:
        if path.is_symlink() or not path.resolve().is_relative_to(root / 'data'):
            raise RuntimeError('employee database escaped owned root')
        with connect(path.resolve().as_uri() + '?mode=ro', uri=True) as db:
            db.execute('PRAGMA query_only=ON')
            # One consistent ownership/status snapshot across Bot, runs and jobs.
            db.execute('BEGIN')
            tables = {r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
            if 'bots' not in tables: continue
            db.row_factory = sqlite3.Row
            errors, blobs = [], []
            def evidence(row, table):
                return employee_evidence_row(row, table, errors, blobs)
            def projection(table):
                columns = [r[1] for r in db.execute(f'PRAGMA table_info("{table}")')]
                return ', '.join('"' + c + '"' for c in columns if c in EMPLOYEE_EVIDENCE_COLUMNS[table])
            rows = [evidence(r, 'bots') for r in db.execute(f'SELECT {projection("bots")} FROM bots WHERE id=? ORDER BY id', (bot,))]
            if not rows and not pinned: continue
            def selected(table, column, value):
                if table not in tables: return []
                # Identifiers are fixed in this function, never user SQL.
                return [evidence(r, table) for r in db.execute(f'SELECT {projection(table)} FROM "{table}" WHERE "{column}"=? ORDER BY id', (value,))]
            runs = selected('bot_runs', 'bot_id', bot)
            home = rows[0]['home_session_id'] if rows else state.get('employee_home_session')
            related = set(state.get('employee_related_sessions', []))
            # The peer preservation probe changes bot_id on a copied state. Its
            # original Bot's deletion cache must not become the peer's evidence.
            if rows and state.get('employee_home_session') not in (None, home):
                related.clear()
            if home: related.add(home)
            for run in runs:
                sid = employee_run_session(run, errors)
                if sid: related.add(sid)
            # Only identity columns from peers are inspected, never their documents.
            foreign = {r[0] for r in db.execute('SELECT home_session_id FROM bots WHERE id<>?', (bot,))}
            run_columns = {r[1] for r in db.execute('PRAGMA table_info(bot_runs)')}
            foreign_runs = set()
            if 'bot_runs' in tables:
                foreign_runs = {r[0] for r in db.execute('SELECT id FROM bot_runs WHERE bot_id<>?', (bot,))}
                if 'session_id' in run_columns:
                    foreign.update(r[0] for r in db.execute('SELECT session_id FROM bot_runs WHERE bot_id<>?', (bot,)))
            # Follow durable parent links, including descendants that have no BotRun.
            all_sessions = []
            if 'sessions' in tables:
                all_sessions = [dict(r) for r in db.execute(f'SELECT {projection("sessions")} FROM sessions ORDER BY id')]
                foreign.update(r['id'] for r in all_sessions
                               if r.get('bot_id') not in (None, '', bot) or r.get('bot_run_id') in foreign_runs)
            if related & foreign:
                errors.append('sessions: conflicting Bot ownership')
                related.difference_update(foreign)
            while True:
                descendants = {r['id'] for r in all_sessions if r.get('parent_id') in related and r['id'] not in foreign}
                if descendants.issubset(related): break
                related.update(descendants)
            sessions = [r for r in all_sessions if r['id'] in related]
            events = [event for sid in sorted(related) for event in selected('session_events', 'session_id', sid)]
            operations = []
            for event in events:
                value = employee_document(event['event_data'], 'session_events.event_data', errors)
                try:
                    actions = value.get('actions', value.get('Actions'))
                    if actions is None: continue
                    actions = employee_object(actions)
                    delta = actions.get('stateDelta', actions.get('StateDelta'))
                    if delta is None: continue
                    delta = employee_object(delta)
                    op = delta.get('bot_operation')
                    if op is not None: operations.append(employee_object(op))
                except ValueError:
                    errors.append('session_events.event_data: invalid or ambiguous operation envelope')
            jobs = []
            if 'scheduled_jobs' in tables:
                for row in db.execute(f'SELECT {projection("scheduled_jobs")} FROM scheduled_jobs WHERE mode=? ORDER BY id', ('bot_trigger',)):
                    raw = dict(row)
                    value = raw['payload']
                    if isinstance(value, bytes):
                        try: value = value.decode('utf-8', errors='strict')
                        except UnicodeDecodeError: value = None
                    payload = employee_document(value, 'scheduled_jobs.payload (ownership unresolved)', errors)
                    if payload.get('bot_id') == bot: jobs.append(evidence(row, 'scheduled_jobs'))
            matches.append(dict(database=str(path), bots=rows, runs=runs, events=events,
                                operations=operations, jobs=jobs, home_session_id=home,
                                goals=selected('bot_goals', 'bot_id', bot),
                                versions=selected('bot_versions', 'bot_id', bot),
                                approvals=selected('bot_approvals', 'bot_id', bot),
                                sessions=sessions, related_session_ids=sorted(related),
                                blob_evidence=blobs, observation_errors=errors,
                                observation_complete=not errors,
                                blocker=errors[0] if errors else None))
    if len(matches) != 1: raise RuntimeError('employee database identity ambiguous or absent')
    return matches[0]


def employee_command(state, action):
    inspect_owned(state)
    root = Path(state['root'])
    agent = state.get('workflow') == 'bot-agent-live-v1'
    checkpoint_name = 'agent-learning-checkpoint.json' if agent else 'employee-checkpoint.json'
    cp = json.loads((root / checkpoint_name).read_text())
    contract = 'bot-agent-live-v1' if agent else EMPLOYEE_CONTRACT
    if agent and (not state.get('agent_nonce') or cp.get('nonce') != state['agent_nonce']
                  or state.get('agent_learning_bot_id') != state.get('bot_id')
                  or action not in ('read', 'restart-tick')):
        raise RuntimeError('agent learning lifecycle identity/action mismatch')
    if (cp.get('contract') != contract or cp.get('bot_id') != state.get('bot_id')
            or cp.get('candidate') != state['binary_sha256']
            or cp.get('browser_candidate') != state['browser_sha256']):
        raise RuntimeError('employee checkpoint identity mismatch')
    if action.startswith('restart-'):
        required = {'restart-tick': 'schedule-natural', 'restart-paused': 'pause',
                    'restart-denied': 'resume'}
        if action not in required or required[action] not in cp.get('receipts', {}):
            raise RuntimeError('employee restart prerequisite absent')
        if not cp.get('actions', {}).get(action):
            raise RuntimeError('employee restart action not checkpointed')
        # The Go driver stays alive but the native Chrome process must be gone.
        processes = subprocess.check_output(['ps', '-ax', '-o', 'command='], text=True)
        if any(str(root / 'chrome-profile') in line for line in processes.splitlines()):
            raise RuntimeError('native browser must be closed before restart')
        if state.get(action): raise RuntimeError('ambiguous restart is never replayed')
        state[action] = 'attempted'; private_json(root / 'state.json', state)
        before = inspect_owned(state)['State']['StartedAt']
        subprocess.run(['docker', 'restart', state['container_id']], check=True, timeout=90, stdout=subprocess.DEVNULL)
        info = wait_healthy(state, previous_start=before)
        state[action] = 'verified'; private_json(root / 'state.json', state)
        log = subprocess.check_output(['docker', 'logs', '--since', info['State']['StartedAt'], state['container_id']], stderr=subprocess.STDOUT)
        (root / ('employee-' + action + '.log')).write_bytes(log)
        (root / ('employee-' + action + '.log')).chmod(0o600)
    data = employee_readonly(state)
    if not data['observation_complete']:
        private_json(root / 'employee-observer-failure.json', data)
        raise RuntimeError('employee observation incomplete: ' + data['blocker'])
    if action.startswith('restart-'):
        data['restarted_at'] = info['State']['StartedAt']
    state['employee_database'] = data['database']
    state['employee_home_session'] = data['home_session_id']
    state['employee_related_sessions'] = data['related_session_ids']
    private_json(root / 'state.json', state)
    print(json.dumps(data), flush=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify-chat-first', action='store_true')
    parser.add_argument('--state', type=Path, help='resume owned checkpoint without creating another Bot')
    parser.add_argument('--restart-owned', action='store_true', help=argparse.SUPPRESS)
    parser.add_argument('--refresh-browser', action='store_true', help='explicitly rebuild only the idle owned harness, recording old/new hashes without resetting consent')
    parser.add_argument('--config', type=Path, help='explicit JSON paths, provider URL/model/key; validated before startup')
    parser.add_argument('--verify-chat-model', action='store_true', help='title-first real-provider acceptance (not legacy policy acceptance)')
    parser.add_argument('--verify-bot-employee', action='store_true', help='fresh employee workflow; strict versioned evidence, no legacy review scheduling')
    parser.add_argument('--verify-bot-quality', action='store_true', help='fresh isolated real-model quality contract; no diagnostic resume')
    parser.add_argument('--verify-bot-skills', action='store_true', help='fresh isolated real-model skills plus employee and quality proof')
    parser.add_argument('--verify-bot-agent', action='store_true', help='fresh isolated agent candidate journey; incomplete proof remains blocked')
    parser.add_argument('--employee-command', choices=('read', 'restart-tick', 'restart-paused', 'restart-denied'), help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    if args.verify_bot_agent:
        if (not args.config or args.state or args.verify_bot_skills or args.verify_bot_quality
                or args.verify_bot_employee or args.verify_chat_model or args.verify_chat_first
                or args.restart_owned or args.refresh_browser or args.employee_command):
            parser.error('--verify-bot-agent requires only --config and fresh execution')
        try:
            if (not args.config.is_absolute() or args.config.is_symlink() or not args.config.is_file()
                    or args.config.stat().st_mode & 0o077):
                raise ValueError('agent config must be an absolute private non-symlink file (0600)')
            config = strict_json(args.config.read_text())
            require_agent_config(config)
            config_environment(config)
        except Exception:
            # Parser/OS errors may contain supplied values or credential-bearing paths.
            parser.error('agent configuration preflight failed; private version 1 config and exact AI Core identity required')
    if args.verify_bot_skills:
        if (not args.config or args.state or args.verify_bot_quality or args.verify_bot_employee
                or args.verify_chat_model or args.verify_chat_first or args.restart_owned
                or args.refresh_browser or args.employee_command):
            parser.error('--verify-bot-skills requires only --config and fresh execution')
        # Internal composition only; CLI modes remain strictly mutually exclusive.
        args.verify_bot_quality = True
    if args.verify_bot_quality and (not args.config or args.state or args.verify_bot_employee or args.verify_chat_model or args.verify_chat_first or args.restart_owned or args.refresh_browser or args.employee_command):
        parser.error('--verify-bot-quality requires --config and fresh execution; forbids --state and other modes')
    if args.employee_command:
        if not args.state or args.config or args.verify_bot_employee or args.verify_chat_model or args.verify_chat_first or args.restart_owned or args.refresh_browser:
            parser.error('--employee-command requires only --state')
        employee_command(json.loads(args.state.read_text()), args.employee_command)
        return 0
    if args.verify_bot_employee:
        if not args.config or args.verify_chat_model or args.verify_chat_first or args.refresh_browser or args.restart_owned:
            parser.error('--verify-bot-employee requires --config; forbids other modes')
    if args.verify_chat_model:
        if not args.config or args.verify_chat_first or args.refresh_browser or args.restart_owned:
            parser.error('--verify-chat-model requires --config and forbids legacy modes')
    if args.config:
        try:
            config = strict_json(args.config.read_text())
            if 'schema_version' in config and (not args.config.is_absolute() or args.config.is_symlink()
                                               or args.config.stat().st_mode & 0o077):
                raise ValueError('versioned configuration requires a private absolute file')
            os.environ.update(config_environment(config))
        except Exception:
            parser.error('configuration preflight failed: invalid configuration or unavailable prerequisites (values withheld)')
    verify_browser = args.verify_bot_agent or args.verify_chat_first or args.verify_chat_model or args.verify_bot_employee or args.verify_bot_quality
    if args.refresh_browser:
        if not args.state or args.restart_owned: parser.error('--refresh-browser requires --state and forbids --restart-owned')
        refresh_browser(json.loads(args.state.read_text()))
        if not args.verify_chat_first: return 0
    if args.restart_owned:
        if not args.state: parser.error('--restart-owned requires --state')
        restart_owned(json.loads(args.state.read_text()))
        return 0
    errors = prerequisites(os.environ)
    if errors: parser.error('; '.join(errors))
    state = json.loads(args.state.read_text()) if args.state else create(os.environ)
    root = Path(state['root'])
    print('STATE=' + str(root / 'state.json'), flush=True)
    try:
        if args.verify_bot_employee and args.state:
            validate_employee_resume(state, args.state, os.environ)
        if args.verify_bot_employee or args.verify_bot_quality:
            state['workflow'] = EMPLOYEE_CONTRACT
        if args.verify_bot_quality:
            state['quality_nonce'] = secrets.token_hex(32)
        setup(state)
        if verify_browser:
            env = dict(os.environ, CHAT_FIRST_STATE=str(root / 'state.json'))
            # Ambient mode flags must never select a different acceptance journey.
            for key in ('BOT_AGENT_ACCEPTANCE', 'BOT_EMPLOYEE_ACCEPTANCE', 'BOT_QUALITY_ACCEPTANCE', 'BOT_SKILLS_ACCEPTANCE', 'CHAT_MODEL_ACCEPTANCE'):
                env.pop(key, None)
            if args.verify_bot_agent:
                state['workflow'] = 'bot-agent-live-v1'
                state['agent_nonce'] = secrets.token_hex(32)
                state['agent_intended'] = dict(AGENT_TARGET)
                env['BOT_AGENT_ACCEPTANCE'] = '1'
            if args.verify_chat_model:
                if state.get('bot_id') and state.get('workflow') != 'chat-model-v1':
                    raise RuntimeError('cannot reuse legacy Bot for title-first acceptance')
                state['workflow'] = 'chat-model-v1'
                env['CHAT_MODEL_ACCEPTANCE'] = '1'
            if args.verify_bot_employee:
                state['workflow'] = EMPLOYEE_CONTRACT
                env['BOT_EMPLOYEE_ACCEPTANCE'] = '1'
            if args.verify_bot_quality:
                state['workflow'] = QUALITY_CONTRACT
                env['BOT_QUALITY_ACCEPTANCE'] = '1'
            if args.verify_bot_skills:
                state['workflow'] = SKILLS_CONTRACT
                state['skills_nonce'] = secrets.token_hex(32)
                env['BOT_SKILLS_ACCEPTANCE'] = '1'
            browser = root / 'bin/chat-first-browser'
            if not state.get('browser_sha256'):
                subprocess.run(['go', 'build', '-o', str(browser), './tests/e2eboot/chat_first_browser'], cwd=REPO, check=True, timeout=180)
                state['browser_sha256'] = hashlib.sha256(browser.read_bytes()).hexdigest()
            elif hashlib.sha256(browser.read_bytes()).hexdigest() != state['browser_sha256']:
                raise RuntimeError('resume browser candidate changed')
            private_json(root / 'state.json', state)
            try:
                subprocess.run([str(browser)], cwd=REPO, env=env, check=True, timeout=7200 if args.verify_bot_agent else 3600 if args.verify_bot_quality else 1800)
            finally:
                # The browser owns the durable Bot checkpoint even on failure.
                state = json.loads((root / 'state.json').read_text())
            # Partial browser work never counts as whole-workflow acceptance.
            actual = strict_json((root / 'browser-evidence.json').read_text())
            (validate_agent_evidence if args.verify_bot_agent else validate_skills_evidence if args.verify_bot_skills else validate_quality_evidence if args.verify_bot_quality else validate_employee_evidence if args.verify_bot_employee else validate_chat_model_evidence if args.verify_chat_model else validate_evidence)(state, actual)
            inspect_owned(state)
            state['status'] = 'diagnostic-verified' if state.get('diagnostic_resume') else 'verified'
        private_json(root / 'state.json', state)
    except Exception as error:
        state['status'] = 'blocked'; state['blocker'] = str(error)
        evidence_path = root / 'browser-evidence.json'
        if verify_browser and evidence_path.exists():
            evidence = json.loads(evidence_path.read_text())
            state['blocker'] = str(evidence.get('blocker') or error)
            state['blocked_stage'] = evidence.get('stage', 'unknown')
            print('STAGE=' + state['blocked_stage'] + ' EVIDENCE=' + str(evidence_path), file=sys.stderr)
        private_json(root / 'state.json', state)
        print('BLOCKED: ' + state['blocker'], file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__': sys.exit(main())
