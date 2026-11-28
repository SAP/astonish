"""Disposable pgvector lifecycle for strict acceptance; never adopts an external DB.

The caller supplies a sanitized environment. Credentials are generated per invocation,
passed through environment (not command arguments), and redacted by the shared runner.
Only the uniquely labeled container created here may be started or removed.
"""
import json
import re
import secrets
import subprocess
import sys
import time

from bots_acceptance_driver import run

DSN = 'ASTONISH_TEST_DSN'
ISOLATED = 'ASTONISH_BOTS_TEST_DB_ISOLATED'
IMAGE = 'pgvector/pgvector:pg17'
OWNER_LABEL = 'astonish.acceptance.owner'


def postgres_mode(source):
    """Absence permits owned provisioning; present-but-empty is invalid, not absence."""
    if DSN not in source and ISOLATED not in source:
        return 'owned'
    if source.get(DSN, '').strip() and source.get(ISOLATED) == '1':
        return 'external'
    raise ValueError('explicit disposable ASTONISH_TEST_DSN and '
                     'ASTONISH_BOTS_TEST_DB_ISOLATED=1 required when either is set')


def run_owned_postgres(command, env):
    """Run the strict child against a fresh owned DB, removing only that DB in finally."""
    token = secrets.token_hex(16)
    name = 'astonish-agent-pg-' + token
    password = secrets.token_hex(32)
    docker_env = dict(env, POSTGRES_PASSWORD=password)
    container_id = None
    attempted_create = False
    result = 2

    def docker(*args, check=True):
        # Capture all diagnostics: Docker failures can include credential-bearing config.
        return subprocess.run(['docker', *args], env=docker_env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              timeout=120, check=check)

    def owned(identifier):
        info = json.loads(docker('inspect', identifier).stdout)
        if not isinstance(info, list) or len(info) != 1:
            raise ValueError('invalid container inspection')
        info = info[0]
        cid = info.get('Id', '')
        if (not re.fullmatch(r'[0-9a-f]{64}', cid)
                or info.get('Name') != '/' + name
                or info.get('Config', {}).get('Labels', {}).get(OWNER_LABEL) != token
                or info.get('Config', {}).get('Image') != IMAGE
                or (container_id is not None and cid != container_id)):
            raise ValueError('container ownership mismatch')
        return info

    try:
        attempted_create = True
        created = docker('create', '--name', name, '--label', OWNER_LABEL + '=' + token,
                         '-e', 'POSTGRES_PASSWORD', '-p', '127.0.0.1::5432', IMAGE)
        cid = created.stdout.strip()
        if not re.fullmatch(r'[0-9a-f]{64}', cid):
            raise ValueError('invalid created container ID')
        container_id = cid
        owned(container_id)
        docker('start', container_id)
        info = owned(container_id)
        bindings = info.get('NetworkSettings', {}).get('Ports', {}).get('5432/tcp')
        if (not isinstance(bindings, list) or len(bindings) != 1
                or bindings[0].get('HostIp') != '127.0.0.1'):
            raise ValueError('database must bind only loopback')
        port = bindings[0].get('HostPort', '')
        if not port.isdigit() or not 1 <= int(port) <= 65535:
            raise ValueError('invalid database port')
        for _ in range(60):
            if docker('exec', container_id, 'pg_isready', '-h', '127.0.0.1',
                      '-U', 'postgres', check=False).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('database readiness timeout')
        child_env = dict(env, ASTONISH_TEST_DSN=(
            f'postgres://postgres:{password}@127.0.0.1:{port}/postgres?sslmode=disable'),
            ASTONISH_BOTS_TEST_DB_ISOLATED='1', ASTONISH_REQUIRE_SKILL_POSTGRES='1')
        print('Running strict PostgreSQL acceptance against an owned disposable loopback database',
              flush=True)
        result = 0 if run(command, child_env) else 1
    except (OSError, ValueError, KeyError, TypeError, RuntimeError, subprocess.SubprocessError):
        print('BLOCKED: owned PostgreSQL setup or execution failed (private diagnostics suppressed)',
              file=sys.stderr)
    finally:
        if attempted_create:
            try:
                # Also recover a create that succeeded in Docker but whose response was lost.
                # Never remove by an unverified name, nor touch a foreign colliding container.
                info = owned(container_id or name)
                docker('rm', '-f', '-v', info['Id'])
            except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
                print('BLOCKED: owned PostgreSQL cleanup could not be verified; '
                      'no unverified container was removed', file=sys.stderr)
                result = 2
    return result
