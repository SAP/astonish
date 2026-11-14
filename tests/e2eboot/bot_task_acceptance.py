#!/usr/bin/env python3
"""Strict isolated Bot Task acceptance stages (not final provider certification).

Exact current-invocation evidence covers production HTTP journeys, physical Docker
Homes, actual owned daemon SIGKILL/restart, and disposable loopback PostgreSQL.
No normal daemon, paid provider keys, user database or shared resource cleanup.
"""
import argparse
import contextlib
import json
import os
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path

from bots_acceptance_driver import clean_env, go_command, run
from skills_acceptance_driver import ExactGoEvidence

CONTROLLED_STAGES = ('compatibility', 'workspace', 'search', 'execution', 'continuity')
STAGES = (*CONTROLLED_STAGES, 'all')
COMPATIBILITY = {
    'pkg/api': {
        'TestBotAgentWorkHTTPAcceptance',
        *(f'TestBotAgentWorkHTTPAcceptance/{scope}/{mode}'
          for scope in ('personal', 'team') for mode in ('interactive', 'autonomous')),
    },
    'pkg/daemon': {
        'TestBotAuthorizedScheduleHTTPAcceptance',
        *(f'TestBotAuthorizedScheduleHTTPAcceptance/{scope}' for scope in ('personal', 'team')),
        'TestBotEmployeeOperationsHTTPAcceptance',
        *(f'TestBotEmployeeOperationsHTTPAcceptance/{scope}/{case}'
          for scope in ('personal', 'team')
          for case in ('goal', 'paused_goal', 'task_once', 'priority_pause', 'deny',
                       'memory_deny', 'approve', 'expiry')),
    },
}


WORKSPACE = {
    'pkg/sandbox/docker': {'TestBotHomeWorkspace', 'TestBotHomeWorkspaceRejectInputs'},
    'pkg/store/entstore': {'TestBotTaskStorage', 'TestBotTaskStorage/personal', 'TestBotTaskStorage/team',
                          'TestBotTaskPublicationRecovery', 'TestBotTaskPublicationRecovery/personal', 'TestBotTaskPublicationRecovery/team', 'TestBotTaskPage'},
    'pkg/api': {'TestBotTaskWorkspaceHTTPAcceptance', 'TestBotTaskWorkspaceHTTPAcceptance/personal', 'TestBotTaskWorkspaceHTTPAcceptance/team',
                *(f'TestBotTaskWorkspaceHTTPAcceptance/{scope}/{case}' for scope in ('personal', 'team') for case in ('held', 'aborted', 'cancelled-write'))},
    'pkg/launcher': {'TestBotTaskWorkspace', 'TestBotTaskWorkspaceDockerIntegration',
                     *(f'TestBotTaskWorkspaceDockerIntegration/{scope}' for scope in ('personal', 'team')),
                     *(f'TestBotTaskWorkspaceDockerIntegration/{scope}/{case}' for scope in ('personal', 'team') for case in ('held', 'aborted', 'cancelled-write'))},
}


SEARCH = {
    'pkg/execution': {'TestBotPublicResearchScope',
                      *(f'TestBotPublicResearchScope/{case}' for case in
                        ('facet', 'provider', 'credential', 'subject', 'source', 'quota'))},
    'pkg/launcher': {'TestBotSearchFirst', 'TestBotSearchFirstLogicalTarget',
                     'TestBotSearchFirstRestart',
                     *(f'TestBotSearchFirstRestart/{case}' for case in
                       ('reopen-no-unlock', 'child-cancel-no-mutation', 'stale-cancel-no-mutation', 'cancel-no-late-unlock')),
                     'TestBotConfiguredSearch', 'TestBotConfiguredSearchPhysicalMCP',
                     *(f'TestBotConfiguredSearch/{case}' for case in
                       ('success', 'empty', '429', 'credential-change', 'revoked', 'spoofed'))},
    'pkg/store/entstore': {'TestBotPublicResearchRestartQuota',
                          *(f'TestBotPublicResearchRestartQuota/{scope}' for scope in ('personal', 'team')),
                          *(f'TestBotPublicResearchRestartQuota/{scope}/{case}' for scope in ('personal', 'team')
                            for case in ('reopen-ceiling', 'reopen-provenance', 'released-no-replay'))},
    'pkg/daemon': {'TestBotConfiguredSearchHTTPAcceptance',
                   'TestBotConfiguredSearchHTTPAcceptance/personal',
                   'TestBotConfiguredSearchHTTPAcceptance/team',
                   *(f'TestBotConfiguredSearchHTTPAcceptance/{scope}/restart-receipts' for scope in ('personal', 'team'))},
}


EXECUTION = {
    'pkg/daemon': {'TestBotTaskScheduleHTTPAcceptance',
                   *(f'TestBotTaskScheduleHTTPAcceptance/{scope}' for scope in ('personal', 'team')),
                   *(f'TestBotTaskScheduleHTTPAcceptance/{scope}/{case}' for scope in ('personal', 'team')
                     for case in ('greeting', 'automatic-capture', 'owner-correction-pin', 'manual', 'schedule-three-ticks', 'delete-manual'))},
}

CONTINUITY = {
    'pkg/daemon': {'TestBotTaskContinuityHTTPAcceptance',
                   *(f'TestBotTaskContinuityHTTPAcceptance/{scope}' for scope in ('personal', 'team')),
                   *(f'TestBotTaskContinuityHTTPAcceptance/{scope}/{case}' for scope in ('personal', 'team')
                     for case in ('before-restart', 'process-restart-files-state'))},
    'pkg/store/entstore': {'TestBotTaskPostgresContinuity',
                          *(f'TestBotTaskPostgresContinuity/{scope}' for scope in ('personal', 'team')),
                          *(f'TestBotTaskPostgresContinuity/{scope}/{case}' for scope in ('personal', 'team')
                            for case in ('committed-pin', 'uncertain-effect', 'cancel-approval-revoke', 'reopen-no-replay')),
                          'TestBotTaskWorkspaceArchivePostgresDocker',
                          *(f'TestBotTaskWorkspaceArchivePostgresDocker/{scope}' for scope in ('personal', 'team')),
                          'TestBotTaskRestoreValidation',
                          *(f'TestBotTaskRestoreValidation/{field}' for field in ('digest', 'task_id', 'bot_id', 'authority_constraint_ref', 'publication_state')),
                          'TestBotTaskRestoreMappingKeepsHistoricalBytes',
                          'TestBotTaskPurge', *(f'TestBotTaskPurge/{scope}' for scope in ('personal', 'team')),
                          'TestBotTaskWorkspaceCommitFence', *(f'TestBotTaskWorkspaceCommitFence/{scope}' for scope in ('personal', 'team'))},
    'pkg/launcher': {'TestBotTaskWorkspaceArchiveDocker', *(f'TestBotTaskWorkspaceArchiveDocker/{scope}' for scope in ('personal', 'team'))},
}


@contextlib.contextmanager
def continuity_postgres(env):
    # Never consume an inherited test DSN. Own one uniquely named, labelled,
    # loopback-only container; no volume, host paths, pruning or user databases.
    name = 'astonish-task-continuity-' + uuid.uuid4().hex
    label = 'astonish.task-continuity-owner=' + name
    password = uuid.uuid4().hex
    started = False
    try:
        subprocess.run(['docker', 'run', '--detach', '--name', name, '--label', label,
                        '--publish', '127.0.0.1::5432', '--env', 'POSTGRES_PASSWORD=' + password,
                        '--env', 'POSTGRES_USER=task_continuity', '--env', 'POSTGRES_DB=postgres',
                        'pgvector/pgvector:pg16'], env=env, check=True, capture_output=True, text=True, timeout=120)
        started = True
        result = subprocess.run(['docker', 'port', name, '5432/tcp'], env=env,
                                check=True, capture_output=True, text=True, timeout=30)
        host, port = result.stdout.strip().split(':')
        if host != '127.0.0.1' or not port.isdigit() or not 0 < int(port) < 65536:
            raise ValueError('owned PostgreSQL must bind exclusively to loopback')
        deadline = time.monotonic() + 45
        while True:
            ready = subprocess.run(['docker', 'exec', name, 'pg_isready', '-U', 'task_continuity', '-d', 'postgres'],
                                   env=env, capture_output=True, timeout=10)
            if ready.returncode == 0:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError('owned PostgreSQL did not become ready')
            time.sleep(0.2)
        yield dict(env, ASTONISH_TEST_DSN=f'postgres://task_continuity:{password}@127.0.0.1:{port}/postgres?sslmode=disable',
                   ASTONISH_BOTS_TEST_DB_ISOLATED='1', ASTONISH_TEST_TASK_POSTGRES_OWNED='1')
    finally:
        if started:
            ownership = subprocess.run(['docker', 'inspect', '--format',
                                        '{{index .Config.Labels "astonish.task-continuity-owner"}}', name],
                                       env=env, check=True, capture_output=True, text=True, timeout=30)
            if ownership.stdout.strip() != name:
                raise RuntimeError('refusing cleanup: PostgreSQL ownership label mismatch')
            subprocess.run(['docker', 'rm', '--force', name], env=env, check=True,
                           capture_output=True, text=True, timeout=30)


@contextlib.contextmanager
def search_candidate(env):
    # Build this checkout, for the inspected local image architecture. Never use
    # a normal daemon/node binary, inherited credentials, or a mutable user path.
    with tempfile.TemporaryDirectory(prefix='astonish-search-owned-') as directory:
        image = 'ghcr.io/sap/astonish-sandbox-base:latest'
        result = subprocess.run(['docker', 'image', 'inspect', image, '--format', '{{.Architecture}}'],
                                env=env, text=True, capture_output=True, check=True, timeout=30)
        arch = result.stdout.strip()
        if arch not in ('arm64', 'amd64'):
            raise ValueError('unsupported owned sandbox image architecture: ' + arch)
        binary = str(Path(directory) / 'astonish-search-node')
        build_env = dict(env, CGO_ENABLED='0', GOOS='linux', GOARCH=arch)
        if not run(['go', 'build', '-o', binary, '.'], build_env):
            raise RuntimeError('fresh owned search candidate build failed')
        yield dict(env, ASTONISH_TEST_CONFIGURED_SEARCH='1',
                   ASTONISH_TEST_SEARCH_NODE=binary, ASTONISH_SANDBOX_IMAGE=image)


def compatibility_env(source):
    # No implicit production/provider/cluster/config environment propagation.
    return {key: value for key, value in clean_env(source).items()
            if not key.startswith('ASTONISH_') and not key.endswith('API_KEY')
            and key not in ('KUBECONFIG', 'DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_CONFIG')}


def run_suites(stage, suites, env):
    for package, names in suites.items():
        evidence = ExactGoEvidence(package, names)
        roots = sorted(name for name in names if '/' not in name)
        passed = run(go_command('./' + package, roots, tags='integration' if stage == 'continuity' and package == 'pkg/store/entstore' else None), env, evidence)
        errors = evidence.errors()
        if not passed or errors:
            print(f'BLOCKED: {stage} failed: ' + '; '.join(errors), file=sys.stderr)
            return False
    return True


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stage', required=True, choices=STAGES)
    parser.add_argument('--config', help='Explicit absolute private JSON for a fresh final Task candidate')
    args = parser.parse_args(argv)
    stage = args.stage
    if stage != 'all':
        if args.config:
            parser.error('--config is only used by the final all stage')
        return controlled_stage(stage)
    # Fail before builds, Docker or paid calls, without disclosing config contents
    # or credential-bearing paths. The launcher owns full provider/search parsing.
    try:
        import stat
        from quality_live_contract import strict_json
        config = Path(args.config) if args.config else None
        if (config is None or not config.is_absolute() or config.is_symlink()
                or not config.is_file() or stat.S_IMODE(config.stat().st_mode) != 0o600
                or not isinstance(strict_json(config.read_bytes()), dict)):
            raise ValueError('private config unavailable')
    except (OSError, ValueError, TypeError):
        print('BLOCKED: all requires explicit absolute non-symlink private JSON config (0600); no work started', file=sys.stderr)
        return 2
    for stage in CONTROLLED_STAGES:
        if controlled_stage(stage) != 0:
            print('BLOCKED: all stopped at controlled stage ' + stage, file=sys.stderr)
            return 1
    env = compatibility_env(os.environ)
    env['LOCAL_BOT_TASK_CONFIG'] = str(config)
    command = [sys.executable, '-B', str(Path(__file__).with_name('local_bot_smoke_launcher.py')),
               '--verify-bot-tasks', '--config', str(config)]
    if not run(command, env):
        print('BLOCKED: fresh Task candidate has unmet required evidence', file=sys.stderr)
        return 1
    print(json.dumps({'stage': 'all', 'status': 'passed', 'task_certification': True,
                      'controlled_stages': list(CONTROLLED_STAGES),
                      'unverified': ['kubernetes-physical-home', 'openshell-certification']}))
    return 0


def controlled_stage(stage):
    env = compatibility_env(os.environ)
    if stage == 'continuity':
        env.update(ASTONISH_TEST_TASK_CONTINUITY='1', ASTONISH_TEST_TASK_WORKSPACE='1', ASTONISH_SANDBOX_IMAGE='ghcr.io/sap/astonish-sandbox-base:latest')
        try:
            with continuity_postgres(env) as owned_env:
                if not run_suites(stage, CONTINUITY, owned_env):
                    return 1
                # Reuse the existing three-real-occurrence production scheduler
                # journey rather than replacing it with simulated ticks.
                if not run_suites(stage, EXECUTION, dict(owned_env, ASTONISH_TEST_TASK_EXECUTION='1')):
                    return 1
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            print('BLOCKED: owned continuity prerequisite failed: ' + type(error).__name__, file=sys.stderr)
            return 1
        print(json.dumps({'stage': 'continuity', 'status': 'passed', 'task_certification': False,
                          'journeys': ['owned-subprocess-sigkill-restart-same-home-helper-sqlite',
                                       'disposable-postgres-pins-cancel-revoke-uncertain-no-replay',
                                       'three-real-scheduler-occurrences-delete-manual',
                                        'fenced-full-home-export-explicit-restore-held-publication',
                                        'postgres-full-home-export-restore'],
                          'unverified': ['kubernetes-physical-home']}))
        return 0
    if stage == 'execution':
        env.update(ASTONISH_TEST_TASK_EXECUTION='1', ASTONISH_SANDBOX_IMAGE='ghcr.io/sap/astonish-sandbox-base:latest')
        if not run_suites(stage, EXECUTION, env):
            return 1
        print(json.dumps({'stage': 'execution', 'status': 'passed', 'task_certification': False,
                          'journeys': ['physical-home-automatic-capture-no-optional-ritual', 'owner-correction-immutable-pin', 'manual-three-scheduler-occurrences-delete-manual']}))
        return 0
    if stage == 'workspace':
        env['ASTONISH_TEST_TASK_WORKSPACE'] = '1'
        env['ASTONISH_SANDBOX_IMAGE'] = 'ghcr.io/sap/astonish-sandbox-base:latest'
        suites = WORKSPACE
    else:
        suites = COMPATIBILITY
    if stage == 'search':
        try:
            with search_candidate(env) as search_env:
                if not run_suites(stage, SEARCH, search_env):
                    return 1
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            print('BLOCKED: search candidate unavailable: ' + str(error), file=sys.stderr)
            return 1
        print(json.dumps({'stage': 'search', 'status': 'passed', 'task_certification': False,
                          'journeys': ['owner-http-configured-perplexity-before-detail',
                                       'physical-candidate-node-tavily-mcp',
                                       'public-subject-scope-and-fail-closed-search']}))
        return 0
    if not run_suites(stage, suites, env):
        return 1
    print(json.dumps({'stage': stage, 'status': 'passed',
                      'task_certification': False,
                      'journeys': (['physical-docker-task-helper-sqlite-continuity', 'scoped-task-cas-trusted-publication', 'lease-and-owner-bound-workspace'] if stage == 'workspace' else
                                   ['toolless-answer-and-exact-once-http-publication', 'legacy-instruction-scheduling', 'approval-denial-expiry'])}))
    return 0


if __name__ == '__main__':
    sys.exit(main())
