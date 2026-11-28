"""Implementation for bots_acceptance.sh; stdlib only, safe to import in tests."""
import argparse
import io
import json
import os
from pathlib import Path
import queue
import re
import shlex
import shutil
import signal
import hashlib
import uuid
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "ghcr.io/sap/astonish-sandbox-base:dev"
BROWSER = ["TestBotTitleChatBrowserAcceptance", "TestBotAmbiguousTitleQuestionBrowserAcceptance",
           "TestBotThinkingRestoreBrowserAcceptance", "TestBotWorkspaceBrowserAcceptance", "TestBotScheduledWorkspaceBrowserAcceptance",
           "TestBotChatFirstWorkspaceBrowserAcceptance", "TestBotChatFirstOrderedConversationBrowserAcceptance",
           "TestBotChatFirstConversationHTTPAcceptance", "TestBotProposalRoutesStrictHTTP",
           "TestBotSourceIntakeProposalHTTPAcceptance", "TestBotSourceIntakeAdaptiveHTTP",
           "TestBotSourceIntakeTemplateHTTP",
           "TestChatChronologyHTTPAcceptance_Browser", "TestMemoryHTTPAcceptance_Browser"]
CLI = ["TestBotTitleChatPTYAcceptance", "TestBotCLIAndPTYAcceptance", "TestBotParityPTYAcceptance", "TestBotBareCodePTYAcceptance",
       "TestBotCLIExplicitIntentSubprocessAcceptance", "TestBotCLIIntentAmbiguousNameSubprocessAcceptance",
       "TestBotCLIIntentLocalGatingSubprocessAcceptance", "TestBotPTYTerminalQueries",
       "TestBotsCLIExplicitIntents", "TestBotsCLIIntentStateAndServerErrors",
       "TestBotsCLIUnknownCommandDoesNotResolve", "TestBotsStopRun", "TestBotChatFlagBoundaries"]
DAEMON_HTTP = ["TestBotAuthorizedScheduleHTTPAcceptance", "TestBotEmployeeOperationsHTTPAcceptance"]
PG = ["TestIntegrationPostgresBotEmployeeOperations", "TestIntegrationPostgresBotChatModel", "TestIntegrationPostgresBotTitleStartState",
      "TestIntegrationBotPostgresBackupRecovery", "TestIntegrationPostgresBotServicesSpendRace",
      "TestIntegrationPostgresBotServicesSpendSettlementAndCaps", "TestIntegrationPostgresBotServicesCorrection",
      "TestIntegrationPostgresBotServicesCleanup"]
GATES = ["make-test", "frontend-typecheck", "frontend-build", "frontend-lint", "make-lint",
         "browser", "daemon-http", "cli-pty", "sqlite", "postgres", "docker-home", "kubernetes-home", "spec-coverage"]
LIVE_BOTS = ["TestBotsLiveLifecycle", "TestBotsLiveKubernetesRetainedHome"]
RELEASE_SCENARIOS = {f"BOTS-{number:03d}" for number in range(1, 11)}


TITLE_CONFIRMATION_BROWSER = ["TestBotTitleChatBrowserAcceptance", "TestBotAmbiguousTitleQuestionBrowserAcceptance",
                              "TestBotThinkingRestoreBrowserAcceptance", "TestBotTitleConfirmationDockerBrowserAcceptance",
                              "TestBotTitleTeamOptionBrowserAcceptance", "TestBotTitleTeamCustomBrowserAcceptance"]
TITLE_CONFIRMATION_CASES = {
    './pkg/launcher': ['TestBotTitleConfirmationFactoryJourney'],
    './pkg/api': ['TestBotTitleConfirmationHTTPAcceptance', 'TestBotTitleConfirmationStateHTTPAcceptance', 'TestBotTitleConfirmedLaterQuestionContinuation', 'TestBotTitleStartHTTPAcceptance', 'TestBotRunStateHTTPAcceptance'],
    './pkg/daemon': ['TestBotTitleConfiguredSearchHTTP', 'TestBotInteractiveAutoHomeHTTP', 'TestBotWorkerRecordedZeroExcludesOwnerWait', 'TestBotWorkerMeteredResumeReturnsCumulativeCommitUsage'],
    './pkg/store/entstore': ['TestBotTitleConfirmationProcessRestart', 'TestBotTitlePublicResearchAdmission', 'TestInteractiveContinuationExpiredQuestion'],
}


DURABLE_PROGRESS_CASES = {
    './pkg/api': ['TestBotProgressSixHourClockAcceptance', 'TestBotProgressEmployeeHTTP',
                  'TestBotProgressRecoveryHTTP', 'TestBotProgressLifecycleHTTP',
                  'TestBotProgressRestartHTTP', 'TestBotProgressDeleteHTTP', 'TestBotProgressStateSSE',
                  'TestBotProgressSynthesisReceiptHTTP', 'TestBotProgressProcessRestartAcceptance',
                  'TestBotProgressBrowserAcceptance', 'TestBotTitleConfirmationDockerBrowserAcceptance',
                  'TestBotTitleConfirmedLaterQuestionContinuation', 'TestBotProgressReadJournalRoundTrip'],
    './pkg/daemon': ['TestBotProgressScheduledAdmissionHTTP', 'TestBotPermanentDeleteHTTP'],
    './pkg/store/entstore': ['TestBotProgressQuotaSuspension', 'TestBotProgressRollbackConcurrentAndCommit',
                            'TestBotProgressActiveTimeAcrossRecovery', 'TestBotProgressFencesAndAccounting',
                            'TestBotProgressSettlementAfterWorkAdvance', 'TestBotProgressQueuedAuthorityLoss',
                            'TestBotProgressScheduledContinuation', 'TestBotProgressControllerDurableContextRestart',
                            'TestBotProgressDurableDispatch', 'TestBotProgressSettledSuspendContinue'],
    './pkg/bot': ['TestBotProgressControllerDecisions', 'TestBotProgressControllerCorrectionAndUnsafeBoundary',
                  'TestBotProgressControllerPersistenceFailure', 'TestBotProgressBudgetCumulativeDurationAndAuthority'],
    './pkg/provider': ['TestBotProgressBudgetModelOperationTimeout'],
    './pkg/launcher': ['TestBotProgressDispatchToolWaitsForPersistedBoundary', 'TestBotProgressDispatchInteractiveNative',
                      'TestBotProgressDispatchTrustedClassification', 'TestBotProgressDispatchCancellationDrainsChild',
                      'TestBotProgressDispatchProviderFailClosed'],
    './cmd/astonish': ['TestBotProgressPTYAcceptance'],
}


DURABLE_PROGRESS_SUBTESTS = {
    './pkg/api': ['TestBotProgressProcessRestartAcceptance/settled',
                  'TestBotProgressProcessRestartAcceptance/settled-write',
                  'TestBotProgressProcessRestartAcceptance/in-flight',
                  'TestBotProgressSixHourClockAcceptance/personal',
                  'TestBotProgressSixHourClockAcceptance/team'],
}


# Mandatory tiers: no partial mode and no historical receipts.
FILE_FIRST_GATES = {
    'http': ('./pkg/api', ['TestBotFileContentPersistenceHTTP', 'TestBotFileDeliveryHTTP', 'TestBotFileFirstPublicationSSE']),
    'worker': ('./pkg/daemon', ['TestBotFileFirstWorkerHTTP']),
    'browser': ('./pkg/api', ['TestBotFileWorkspaceBrowserAcceptance']),
    'cli-pty': ('./cmd/astonish', ['TestBotFileWorkspacePTYAcceptance']),
    'docker-worker': ('./pkg/daemon', ['TestProductionBotWorkspaceDockerLive']),
    'docker-home': ('./pkg/sandbox/docker', ['TestBotHomeDockerPersistenceLive']),
    'continuity': ('./pkg/daemon', ['TestBotTaskContinuityHTTPAcceptance']),
    # Required even while public CLI backup acceptance is not implemented.
    'backup': ('./cmd/astonish', ['TestBotFileFirstBackupCLI']),
    'postgres': ('./pkg/store/entstore', ['TestBotAgentPostgresContinuityCASProjection',
                                        'TestBotAgentPostgresContinuityRestoreQuiescence',
                                        'TestBotTaskWorkspaceArchivePostgresDocker']),
    'kubernetes-home': ('./tests/e2e/bots', ['TestBotsLiveKubernetesRetainedHome']),
}
FILE_FIRST_SCOPED = {'TestBotFileContentPersistenceHTTP', 'TestBotFileFirstWorkerHTTP',
                     'TestBotFileWorkspaceBrowserAcceptance', 'TestBotFileWorkspacePTYAcceptance',
                     'TestBotAgentPostgresContinuityCASProjection', 'TestBotAgentPostgresContinuityRestoreQuiescence',
                     'TestBotTaskWorkspaceArchivePostgresDocker'}
FILE_FIRST_PG_ENV = {'ASTONISH_TEST_DSN', 'ASTONISH_BOTS_TEST_DB_ISOLATED'}
FILE_FIRST_K8S_ENV = {'KUBECONFIG', 'ASTONISH_BOTS_K8S_CONTEXT', 'ASTONISH_BOTS_K8S_ISOLATED',
                      'ASTONISH_E2E_DEDICATED', 'ASTONISH_E2E_SANDBOX_NAMESPACE',
                      'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE', 'ASTONISH_E2E_HOMES_PVC'}


DEFERRED_INFRA_TIERS = frozenset({'postgres', 'kubernetes-home', 'controller-kubernetes-home'})
DEFERRED_INFRA_REASON = 'user-requested: live Kubernetes/PostgreSQL validation deferred for later user testing; NOT PASS'


def deferred_infra_record(**fields):
    return dict(fields, status='DEFERRED', reason=DEFERRED_INFRA_REASON,
                blocking=False, evidence_status='NOT PASS', errors=[], passed=[],
                fresh=False, prerequisites_verified=False)


def acceptance_scope(args):
    deferred = getattr(args, 'defer_kubernetes_postgresql', False)
    return {'profile': 'local-deferred-kubernetes-postgresql' if deferred else 'strict',
            'deferred_by': 'user-requested' if deferred else None,
            'excluded_physical_tiers': sorted(DEFERRED_INFRA_TIERS) if deferred else [],
            'full_infrastructure_acceptance': not deferred}


def file_first_env(source, tier='local'):
    allowed = {'PATH', 'HOME', 'TMPDIR', 'USER', 'LOGNAME', 'SHELL', 'LANG', 'LC_ALL', 'TERM',
               'GOROOT', 'GOPATH', 'GOCACHE', 'GOMODCACHE', 'GOPROXY', 'GONOSUMDB', 'GOPRIVATE',
               'CGO_ENABLED', 'CC', 'CXX', 'PKG_CONFIG_PATH', 'DOCKER_HOST', 'DOCKER_CONTEXT',
               'DOCKER_CONFIG', 'ASTONISH_BOT_BROWSER_PATH', 'ASTONISH_SANDBOX_IMAGE',
               'ASTONISH_BOT_HOME_TEST_LAYERS', 'ASTONISH_MEMORY_BROWSER_ACCEPTANCE',
               'ASTONISH_TEST_DOCKER', 'ASTONISH_TEST_TASK_CONTINUITY'}
    if tier in ('postgres', 'kubernetes-home'):
        allowed |= FILE_FIRST_PG_ENV
    if tier == 'kubernetes-home':
        allowed |= FILE_FIRST_K8S_ENV
    return {key: value for key, value in source.items() if key in allowed}


def file_first_prerequisites(env, defer_kubernetes_postgresql=False):
    """Check all required infrastructure before launching any subprocess."""
    errors = {'local': [], 'postgres': [], 'kubernetes-home': []}
    local, pg, k8s = errors.values()
    for key in ('ASTONISH_MEMORY_BROWSER_ACCEPTANCE', 'ASTONISH_TEST_DOCKER', 'ASTONISH_TEST_TASK_CONTINUITY'):
        if env.get(key) != '1':
            local.append(key + '=1 required')
    browser = env.get('ASTONISH_BOT_BROWSER_PATH', '')
    if not browser or not Path(browser).is_absolute() or not Path(browser).is_file() or not os.access(browser, os.X_OK):
        local.append('ASTONISH_BOT_BROWSER_PATH must name an absolute executable browser')
    if not env.get('ASTONISH_SANDBOX_IMAGE', '').strip():
        local.append('ASTONISH_SANDBOX_IMAGE required: existing local image, no pull')
    layers = Path(env.get('ASTONISH_BOT_HOME_TEST_LAYERS', ''))
    if not layers.is_absolute() or not (layers / '@base/rootfs/bin/sh').is_file():
        local.append('ASTONISH_BOT_HOME_TEST_LAYERS must be absolute and contain @base/rootfs/bin/sh')
    if not (ROOT / 'web/node_modules').is_dir():
        local.append('web/node_modules missing; install dependencies explicitly')
    for tool in ('go', 'npm', 'docker'):
        if not shutil.which(tool, path=env.get('PATH', '')):
            local.append(tool + ' required on PATH')
    if defer_kubernetes_postgresql:
        return {'local': local} if local else {}
    if not env.get('ASTONISH_TEST_DSN', '').strip() or env.get('ASTONISH_BOTS_TEST_DB_ISOLATED') != '1':
        pg.append('ASTONISH_TEST_DSN and ASTONISH_BOTS_TEST_DB_ISOLATED=1 required: dedicated disposable PostgreSQL')
    for key in ('ASTONISH_BOTS_K8S_ISOLATED', 'ASTONISH_E2E_DEDICATED'):
        if env.get(key) != '1':
            k8s.append(key + '=1 required')
    if not shutil.which('kubectl', path=env.get('PATH', '')):
        k8s.append('kubectl required on PATH')
    if not env.get('ASTONISH_BOTS_K8S_CONTEXT'):
        k8s.append('ASTONISH_BOTS_K8S_CONTEXT required; no implicit context')
    config = Path(env.get('KUBECONFIG', ''))
    if not config.is_absolute() or not config.is_file():
        k8s.append('KUBECONFIG must name an absolute dedicated cluster config file')
    for key in ('ASTONISH_E2E_SANDBOX_NAMESPACE', 'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE'):
        ns = env.get(key, '')
        if not (key.endswith('CONTROL_PLANE_NAMESPACE') and ns == 'astonishe2e') and not re.fullmatch(r'(astonishe2e|astonish-e2e)-[a-z0-9]([-a-z0-9]*[a-z0-9])?', ns):
            k8s.append(key + ' must name an explicit dedicated acceptance namespace')
    if env.get('ASTONISH_E2E_SANDBOX_NAMESPACE') == env.get('ASTONISH_E2E_CONTROL_PLANE_NAMESPACE'):
        k8s.append('sandbox and control-plane namespaces must differ')
    if env.get('ASTONISH_E2E_HOMES_PVC', '') in ('', 'astonish-layers', 'astonish-uppers'):
        k8s.append('ASTONISH_E2E_HOMES_PVC must name an independent pre-provisioned Bound RWX claim')
    return {tier: issues for tier, issues in errors.items() if issues}


def file_first_kubernetes_probe(env):
    """Read-only probes; never switch context, provision claims or sweep resources."""
    def capture(args):
        result = subprocess.run(['kubectl'] + args, env=env, cwd=ROOT, capture_output=True, text=True, timeout=30)
        if result.returncode:
            raise OSError('dedicated Kubernetes read-only probe failed')
        return result.stdout.strip()
    context = env['ASTONISH_BOTS_K8S_CONTEXT']
    if capture(['config', 'current-context']) != context:
        return ['kubectl current-context must match ASTONISH_BOTS_K8S_CONTEXT; never switched']
    prefix = ['--context', context, '--request-timeout=15s']
    for key in ('ASTONISH_E2E_SANDBOX_NAMESPACE', 'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE'):
        capture(prefix + ['get', 'namespace', env[key], '-o', 'name'])
    for claim in ('astonish-layers', 'astonish-uppers', env['ASTONISH_E2E_HOMES_PVC']):
        value = json.loads(capture(prefix + ['get', 'pvc', '-n', env['ASTONISH_E2E_SANDBOX_NAMESPACE'], claim, '-o', 'json']))
        if value.get('status', {}).get('phase') != 'Bound' or 'ReadWriteMany' not in value.get('spec', {}).get('accessModes', []):
            return ['dedicated Kubernetes PVCs must all be Bound RWX']
    return []


def file_first(args):
    source = dict(os.environ)
    env = file_first_env(source)
    report = {'schema_version': 1, 'stage': 'file-first', 'status': 'BLOCKED',
              'gates': [{'tier': tier, 'package': package, 'required': names +
                         [name + '/' + scope for name in names if name in FILE_FIRST_SCOPED for scope in ('personal', 'team')],
                         'status': 'NOT RUN', 'passed': [], 'errors': []}
                        for tier, (package, names) in FILE_FIRST_GATES.items()],
              'unverified': ['real-provider quality (controlled fixtures only)',
                             'full file-first PostgreSQL sentinel coverage beyond named integration cases',
                             'full deployed file-first workflow beyond retained-home Kubernetes case']}
    report['acceptance_scope'] = acceptance_scope(args)
    deferred = getattr(args, 'defer_kubernetes_postgresql', False)
    for gate in report['gates']:
        if deferred and gate['tier'] in DEFERRED_INFRA_TIERS:
            gate.update(deferred_infra_record())
    path = args.report
    if path is None:
        fd, name = tempfile.mkstemp(prefix='astonish-file-first-', suffix='.json')
        os.close(fd)
        path = Path(name)
    started = time.monotonic()
    try:
        errors = (file_first_prerequisites(env, defer_kubernetes_postgresql=True) if deferred
                  else file_first_prerequisites(file_first_env(source, 'kubernetes-home')))
        if errors:
            report['errors'] = errors
            for gate in report['gates']:
                if gate['status'] == 'DEFERRED':
                    continue
                issues = errors.get('local', []) + errors.get(gate['tier'], [])
                if gate['tier'] == 'kubernetes-home':
                    issues += errors.get('postgres', [])
                if issues:
                    gate.update(status='BLOCKED', errors=issues)
            return 2
        try:
            for probe in (['docker', 'info', '--format', '{{.ServerVersion}}'],
                          ['docker', 'image', 'inspect', '--format', '{{.Id}}', env['ASTONISH_SANDBOX_IMAGE']]):
                if not run(probe, env, timeout=30):
                    report['errors'] = ['physical Docker prerequisite failed']
                    return 2
            issues = [] if deferred else file_first_kubernetes_probe(file_first_env(source, 'kubernetes-home'))
            if issues:
                report['errors'] = issues
                return 2
        except (OSError, TimeoutError, subprocess.TimeoutExpired, ValueError) as error:
            report['errors'] = [redact(str(error), file_first_env(source, 'kubernetes-home'))]
            return 2
        if not run(['npm', '--prefix', 'web', 'run', 'build'], env):
            report.update(status='FAIL', errors=['Studio build failed'])
            return 1
        for gate in report['gates']:
            if gate['status'] == 'DEFERRED':
                continue
            tier, package = gate['tier'], gate['package']
            child_env = file_first_env(source, tier)
            if tier == 'postgres':
                child_env['ASTONISH_TEST_TASK_WORKSPACE'] = '1'
            if tier == 'kubernetes-home':
                child_env['ASTONISH_E2E_SANDBOX_BACKEND'] = 'k8s'
            command = go_command(package, FILE_FIRST_GATES[tier][1], {'postgres': 'integration', 'kubernetes-home': 'e2e'}.get(tier))
            if tier == 'kubernetes-home':
                command[command.index('-timeout=15m')] = '-timeout=20m'
            log_path = Path(str(path) + '.' + tier + '.jsonl')
            gate.update(command=command, log=str(log_path), fresh=True, prerequisites_verified=True)
            log = log_path.open('w')
            evidence = ConsolidationEvidence(gate['required'], log, child_env)
            try:
                ok = run(command, child_env, evidence, timeout=1500)
                package_id = 'github.com/SAP/astonish/' + package.removeprefix('./')
                required = {(package_id, name) for name in gate['required']}
                issues = evidence.errors() + ['missing exact package/test run-pass: ' + '/'.join(key)
                                              for key in sorted(required - evidence.qualified_passes)]
                issues += ['executed package/test did not finish: ' + '/'.join(key)
                           for key in sorted(evidence.qualified_runs - evidence.qualified_passes)]
                if package_id not in evidence.packages_passed:
                    issues.append('package did not finish with pass: ' + package_id)
                gate.update(status='PASS' if ok and not issues else 'FAIL', errors=issues,
                            passed=[name for pkg, name in sorted(evidence.qualified_passes) if (pkg, name) in required])
                if not ok:
                    gate['errors'].append('test subprocess or evidence validation failed')
                if tier == 'backup' and not required.intersection(evidence.qualified_runs):
                    gate.update(status='BLOCKED')
                    gate['errors'].append('TestBotFileFirstBackupCLI absent or not executable; no substitute accepted')
            except (OSError, TimeoutError, subprocess.TimeoutExpired) as error:
                gate.update(status='FAIL', errors=[redact(str(error), child_env)])
            finally:
                log.close()
                gate['log_sha256'] = hashlib.sha256(log_path.read_bytes()).hexdigest()
        statuses = {gate['status'] for gate in report['gates'] if gate['status'] != 'DEFERRED'}
        report['status'] = 'FAIL' if 'FAIL' in statuses else ('BLOCKED' if statuses != {'PASS'} else 'PASS')
        return {'PASS': 0, 'FAIL': 1, 'BLOCKED': 2}[report['status']]
    except (OSError, TimeoutError, subprocess.TimeoutExpired, KeyboardInterrupt) as error:
        report.update(status='FAIL', errors=[redact(str(error), file_first_env(source, 'kubernetes-home'))])
        return 1
    finally:
        report['elapsed_seconds'] = round(time.monotonic() - started, 3)
        report['unverified'] += [gate['tier'] for gate in report['gates'] if gate['status'] != 'PASS']
        path.write_text(json.dumps(report, indent=2) + '\n')
        print('FILE_FIRST_REPORT ' + str(path), flush=True)
        if report.get('errors'):
            print(report['status'] + ': ' + json.dumps(report['errors']), flush=True)
        for gate in report['gates']:
            print(gate['tier'] + ': ' + gate['status'], flush=True)
            for error in gate['errors']:
                print('  ' + error, flush=True)
        print('file-first: ' + report['status'] + '; named gates only, not full release certification', flush=True)


def durable_progress(args):
    """Strict fresh evidence. Never load live settings or accept historical receipts."""
    allowed = {'PATH', 'HOME', 'TMPDIR', 'USER', 'LOGNAME', 'SHELL', 'LANG', 'LC_ALL', 'TERM',
               'GOROOT', 'GOPATH', 'GOCACHE', 'GOMODCACHE', 'GOPROXY', 'GONOSUMDB', 'GOPRIVATE',
               'CGO_ENABLED', 'CC', 'CXX', 'PKG_CONFIG_PATH', 'DOCKER_HOST', 'DOCKER_CONTEXT',
               'DOCKER_CONFIG', 'ASTONISH_BOT_BROWSER_PATH', 'ASTONISH_SANDBOX_IMAGE'}
    env = {k: v for k, v in os.environ.items() if k in allowed}
    report = {'schema_version': 1, 'stage': 'durable-progress', 'status': 'BLOCKED', 'gates': [],
              'clock_boundary': 'employee controller only; real lease/auth/operation clocks',
              'unverified': ['two-hour wall-clock soak', 'real-provider quality', 'PostgreSQL concurrency',
                             'Kubernetes', 'full tool-node overlay']}
    path = args.report
    if path is None:
        fd, name = tempfile.mkstemp(prefix='astonish-durable-progress-', suffix='.json')
        os.close(fd)
        path = Path(name)
    errors = []
    for key in ('ASTONISH_MEMORY_BROWSER_ACCEPTANCE', 'ASTONISH_TEST_DOCKER'):
        if os.environ.get(key) != '1':
            errors.append(key + '=1 required')
    browser = env.get('ASTONISH_BOT_BROWSER_PATH', '')
    if not browser or not Path(browser).is_absolute() or not os.access(browser, os.X_OK):
        errors.append('ASTONISH_BOT_BROWSER_PATH must name an absolute executable browser')
    if not env.get('ASTONISH_SANDBOX_IMAGE'):
        errors.append('ASTONISH_SANDBOX_IMAGE must name an existing local image (no pull)')
    for tool in ('go', 'npm', 'docker'):
        if not shutil.which(tool, path=env.get('PATH')):
            errors.append(tool + ' required on PATH')
    if args.postgres:
        if not os.environ.get('ASTONISH_TEST_DSN') or os.environ.get('ASTONISH_BOTS_TEST_DB_ISOLATED') != '1':
            errors.append('PostgreSQL requires explicit ASTONISH_TEST_DSN and ASTONISH_BOTS_TEST_DB_ISOLATED=1')
        errors.append('durable-progress PostgreSQL concurrency tier not yet implemented')
    if args.soak or args.real_provider:
        if args.config is None:
            errors.append('optional soak/provider tier requires --config with isolated settings and finite spend limits')
        errors.append('durable-progress wall-clock/provider tier not yet implemented; no provider launched')
    started = time.monotonic()
    try:
        if errors:
            report['errors'] = errors
            for error in errors:
                print('BLOCKED: ' + error)
            return 2
        env.update(ASTONISH_MEMORY_BROWSER_ACCEPTANCE='1', ASTONISH_TEST_DOCKER='1')
        for command in (['docker', 'info', '--format', '{{.ServerVersion}}'],
                        ['docker', 'image', 'inspect', '--format', '{{.Id}}', env['ASTONISH_SANDBOX_IMAGE']]):
            if not run(command, env, timeout=30):
                report['errors'] = ['physical Docker prerequisite failed']
                return 2
        if not run(['npm', '--prefix', 'web', 'run', 'build'], env):
            report['status'] = 'FAIL'
            report['errors'] = ['Studio build failed']
            return 1
        all_passed = True
        for package, names in DURABLE_PROGRESS_CASES.items():
            required = names + DURABLE_PROGRESS_SUBTESTS.get(package, [])
            evidence = GoEvidence(required)
            passed = run(go_command(package, names), env, evidence)
            package_id = 'github.com/SAP/astonish/' + package.removeprefix('./')
            qualified = {(package_id, name) for name in required}
            missing = qualified - evidence.qualified_passes
            issues = evidence.errors() + ['missing exact package/test: ' + '/'.join(n) for n in sorted(missing)]
            if package_id not in evidence.packages_passed:
                issues.append('package did not finish with pass: ' + package_id)
            passed = passed and not issues
            all_passed = all_passed and passed
            report['gates'].append({'package': package, 'status': 'PASS' if passed else 'FAIL',
                                    'required': required, 'passed': [n for p, n in sorted(evidence.qualified_passes) if (p, n) in qualified],
                                    'errors': issues})
        report['status'] = 'PASS' if all_passed else 'FAIL'
        return 0 if all_passed else 1
    except (OSError, TimeoutError, KeyboardInterrupt) as error:
        report['status'] = 'FAIL'
        report['errors'] = [redact(str(error), env)]
        return 1
    finally:
        report['elapsed_seconds'] = round(time.monotonic() - started, 3)
        path.write_text(json.dumps(report, indent=2) + '\n')
        print('DURABLE_PROGRESS_REPORT ' + str(path), flush=True)
        print('durable-progress: ' + report['status'] + '; optional tiers remain unverified', flush=True)


def title_confirmation():
    """Fresh built clients and owned Docker Home, never production configuration."""
    # Unlike release clean_env, controlled gates must not receive provider keys.
    env = {k: v for k, v in clean_env(os.environ).items()
           if k in {'PATH', 'HOME', 'TMPDIR', 'USER', 'LOGNAME', 'SHELL', 'LANG', 'LC_ALL', 'TERM',
                    'GOROOT', 'GOPATH', 'GOCACHE', 'GOMODCACHE', 'GOPROXY', 'GONOSUMDB', 'GOPRIVATE',
                    'CGO_ENABLED', 'CC', 'CXX', 'PKG_CONFIG_PATH', 'DOCKER_HOST', 'DOCKER_CONTEXT',
                    'DOCKER_CONFIG', 'ASTONISH_BOT_BROWSER_PATH', 'ASTONISH_SANDBOX_IMAGE'}}
    errors = []
    if os.environ.get('ASTONISH_MEMORY_BROWSER_ACCEPTANCE') != '1':
        errors.append('ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 required')
    browser = env.get('ASTONISH_BOT_BROWSER_PATH', '')
    if not browser or not Path(browser).is_absolute() or not os.access(browser, os.X_OK):
        errors.append('ASTONISH_BOT_BROWSER_PATH must name an absolute executable browser')
    if not env.get('ASTONISH_SANDBOX_IMAGE'):
        errors.append('ASTONISH_SANDBOX_IMAGE must name an existing local image (no pull)')
    for tool in ('go', 'npm', 'docker'):
        if not shutil.which(tool, path=env.get('PATH')):
            errors.append(tool + ' required on PATH')
    if errors:
        for error in errors:
            print('BLOCKED: ' + error)
        return 2
    env.update(ASTONISH_MEMORY_BROWSER_ACCEPTANCE='1', ASTONISH_TEST_DOCKER='1')
    try:
        for probe in (['docker', 'info', '--format', '{{.ServerVersion}}'],
                      ['docker', 'image', 'inspect', '--format', '{{.Id}}', env['ASTONISH_SANDBOX_IMAGE']]):
            if not run(probe, env, timeout=30):
                return 2
        if not run(['npm', '--prefix', 'web', 'run', 'build'], env):
            return 1
        cases = list(TITLE_CONFIRMATION_CASES.items()) + [('./pkg/api', TITLE_CONFIRMATION_BROWSER),
                                                         ('./cmd/astonish', ['TestBotTitleChatPTYAcceptance'])]
        for package, names in cases:
            evidence = GoEvidence(names)
            if not run(go_command(package, names), env, evidence):
                return 1
            qualified = {('github.com/SAP/astonish/' + package.removeprefix('./'), name) for name in names}
            if not qualified.issubset(evidence.qualified_passes):
                print('EVIDENCE FAILURE: missing exact package/test run-pass evidence')
                return 1
    except (OSError, TimeoutError) as error:
        print('BLOCKED: ' + redact(str(error), env))
        return 2
    print('title-confirmation: personal/team built-browser answers, PTY, configured-search HTTP and controlled lifecycle passed; real Docker Home absence/provisioning/mount receipt passed. Team title admission uses production HTTP because NewBotDialog is personal-only. Two-process SQLite/authorization restart passed. Full tool-node overlay, Task files and real-provider suggestion quality/latency remain separate acceptance obligations.')
    return 0


def title_confirmation_contract():
    """No-skip, isolated loopback HTTP and compatibility evidence, not release proof."""
    cases = {
        './pkg/launcher': ['TestBotTitleConfirmationFactoryJourney'],
        './pkg/api': ['TestBotTitleConfirmationHTTPAcceptance'],
        './pkg/daemon': ['TestBotTitleConfiguredSearchHTTP', 'TestBotInteractiveAutoHomeHTTP', 'TestBotWorkerRecordedZeroExcludesOwnerWait', 'TestBotWorkerMeteredResumeReturnsCumulativeCommitUsage'],
        './pkg/store/entstore': ['TestBotTitleConfirmationProcessRestart', 'TestBotTitlePublicResearchAdmission', 'TestInteractiveContinuationExpiredQuestion'],
    }
    for package, names in cases.items():
        result = subprocess.run(['go', 'test', '-json', '-count=1', package, '-run', '^(' + '|'.join(names) + ')$'], cwd=ROOT, env=clean_env(os.environ), text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        passed = set()
        skipped = False
        for line in result.stdout.splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                print(line)
                continue
            if event.get('Action') == 'skip':
                skipped = True
            if event.get('Action') == 'pass':
                passed.add(event.get('Test'))
            if event.get('Output'):
                print(event['Output'], end='')
        if result.returncode or skipped or not set(names).issubset(passed):
            return 1
    print('title-confirmation-contract: controlled production factory, HTTP and ordinary-question compatibility passed; real provider quality and physical sandbox not exercised')
    return 0


def redact(text, env):
    secrets = []
    for key, value in env.items():
        if value and any(token in key for token in ("DSN", "PASSWORD", "TOKEN", "API_KEY", "MASTER_KEY", "SECRET")):
            secrets.append(value)
            if "DSN" in key:
                try:
                    parsed = urlsplit(value)
                    secrets.extend([parsed.password or "", unquote(parsed.password or "")])
                except ValueError:
                    pass
                for quoted, plain in re.findall(r"password\s*=\s*'([^']*)'|password\s*=\s*([^\s]+)", value):
                    secrets.append(quoted or plain)
    for secret in sorted((s for s in secrets if isinstance(s, str) and s), key=len, reverse=True):
        text = text.replace(secret, "[REDACTED]")
    text = re.sub(r"(?i)(postgres(?:ql)?://)[^\s\"']+", r"\1[REDACTED]", text)
    return text


def clean_env(source):
    # Explicit allowlist: no inherited production ASTONISH_DSN/config or MAKEFLAGS/GOFLAGS.
    names = {"PATH", "HOME", "TMPDIR", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TERM",
             "GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOPROXY", "GONOSUMDB", "GOPRIVATE",
             "CGO_ENABLED", "CC", "CXX", "PKG_CONFIG_PATH", "DOCKER_HOST", "DOCKER_CONTEXT",
             "DOCKER_CONFIG", "KUBECONFIG", "ASTONISH_TEST_DSN", "ASTONISH_SANDBOX_IMAGE",
             "ASTONISH_BOT_HOME_TEST_LAYERS", "ASTONISH_E2E_SANDBOX_NAMESPACE",
             "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE", "ASTONISH_E2E_DEDICATED", "ASTONISH_E2E_HOMES_PVC",
             "ASTONISH_BOT_BROWSER_PATH", "ASTONISH_MEMORY_BROWSER_PATH", "ASTONISH_STREAM_BROWSER_PATH",
             "BIFROST_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY",
             "ASTONISH_BOTS_K8S_CONTEXT", "ASTONISH_BOTS_K8S_ISOLATED",
             "ASTONISH_BOTS_TEST_DB_ISOLATED", "ASTONISH_PG_ACTIVATION_DISPOSABLE"}
    return {k: v for k, v in source.items() if k in names or k.startswith("ASTONISH_BOTS_LIVE_")}


def prerequisites(env, local=False):
    errors = []
    for tool in ("go", "node", "npm", "make", "golangci-lint") + (() if local else ("docker", "kubectl")):
        if not shutil.which(tool, path=env.get("PATH")):
            errors.append(tool + " must be installed on PATH")
    browser = env.get("ASTONISH_BOT_BROWSER_PATH", "")
    if not browser or not Path(browser).is_absolute() or not os.access(browser, os.X_OK):
        errors.append("ASTONISH_BOT_BROWSER_PATH must name an absolute executable browser")
    if not (ROOT / "web/node_modules").is_dir():
        errors.append("web/node_modules missing; install frontend dependencies explicitly")
    if local:
        return errors
    if env.get("ASTONISH_PG_ACTIVATION_DISPOSABLE") != "1":
        errors.append("ASTONISH_PG_ACTIVATION_DISPOSABLE=1 required for dedicated Chat-model PostgreSQL transactions")
    if env.get("ASTONISH_BOTS_TEST_DB_ISOLATED") != "1":
        errors.append("ASTONISH_BOTS_TEST_DB_ISOLATED=1 required: dedicated disposable PostgreSQL instance, never production")
    dsn = env.get("ASTONISH_TEST_DSN", "")
    if not dsn.strip():
        errors.append("ASTONISH_TEST_DSN required (isolated admin-capable PostgreSQL test DSN)")
    if env.get("ASTONISH_SANDBOX_IMAGE") != IMAGE:
        errors.append("ASTONISH_SANDBOX_IMAGE must explicitly equal " + IMAGE)
    layers = Path(env.get("ASTONISH_BOT_HOME_TEST_LAYERS", ""))
    if not layers.is_absolute() or not (layers / "@base/rootfs/bin/sh").is_file():
        errors.append("ASTONISH_BOT_HOME_TEST_LAYERS must be absolute and contain @base/rootfs/bin/sh")
    if env.get("ASTONISH_BOTS_K8S_ISOLATED") != "1":
        errors.append("ASTONISH_BOTS_K8S_ISOLATED=1 required for the pre-provisioned acceptance namespace")
    if not env.get("ASTONISH_BOTS_K8S_CONTEXT"):
        errors.append("ASTONISH_BOTS_K8S_CONTEXT required; no implicit current-context selection")
    if not env.get("ASTONISH_E2E_SANDBOX_NAMESPACE"):
        errors.append("ASTONISH_E2E_SANDBOX_NAMESPACE required; no default/shared namespace")
    if env.get("ASTONISH_E2E_DEDICATED") != "1":
        errors.append("ASTONISH_E2E_DEDICATED=1 required by the shared safety harness")
    for key in ("ASTONISH_E2E_SANDBOX_NAMESPACE", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"):
        ns = env.get(key, "")
        if not (key.endswith("CONTROL_PLANE_NAMESPACE") and ns == "astonishe2e") and not re.fullmatch(r"(astonishe2e|astonish-e2e)-[a-z0-9]([-a-z0-9]*[a-z0-9])?", ns):
            errors.append(key + " must name an explicit dedicated acceptance namespace")
    if env.get("ASTONISH_E2E_SANDBOX_NAMESPACE") == env.get("ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"):
        errors.append("sandbox and control-plane namespaces must differ")
    if not env.get("KUBECONFIG") or not Path(env["KUBECONFIG"]).is_file():
        errors.append("KUBECONFIG must name an explicit dedicated cluster config file")
    if env.get("ASTONISH_E2E_HOMES_PVC", "") in ("", "astonish-layers", "astonish-uppers"):
        errors.append("ASTONISH_E2E_HOMES_PVC must name an independent pre-provisioned Bound RWX claim")
    if not any(env.get(key) for key in ("BIFROST_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY")):
        errors.append("a supported provider API key is required by the live Kubernetes bootstrap")
    return errors


class GoEvidence:
    def __init__(self, required):
        self.required = set(required)
        self.passed = set()
        self.failed = []
        self.ran = set()
        self.qualified_runs = set()
        self.qualified_passes = set()
        self.packages_passed = set()

    def feed(self, line):
        try:
            event = json.loads(line)
        except (ValueError, TypeError):
            return
        if not isinstance(event, dict):
            return
        name, action = event.get("Test"), event.get("Action")
        package = event.get("Package")
        if isinstance(package, str) and package:
            key = (package, name)
            if name and action == "run":
                self.qualified_runs.add(key)
            if name and action == "pass" and key in self.qualified_runs:
                self.qualified_passes.add(key)
            if not name and action == "pass":
                self.packages_passed.add(package)
        if name and action == "run":
            self.ran.add(name)
        if name and action == "pass":
            self.passed.add(name)
        if action in ("skip", "fail"):
            self.failed.append((name or event.get("Package", "package"), action))

    def errors(self):
        errors = ["requested test did not pass: " + n for n in sorted(self.required - (self.passed & self.ran))]
        errors += ["executed test did not finish: " + n for n in sorted(self.ran - self.passed)]
        errors += [n + ": " + state for n, state in self.failed]
        if not self.ran:
            errors.append("no matching tests executed")
        return errors


def run(command, env, evidence=None, timeout=1200, cwd=ROOT):
    print("COMMAND " + redact(shlex.join([str(x) for x in command]), env), flush=True)
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, text=True, errors="replace", start_new_session=True)
    lines = queue.Queue()
    def consume():
        for line in process.stdout:
            lines.put(line)
        lines.put(None)
    threading.Thread(target=consume, daemon=True).start()
    start = time.monotonic()
    try:
        while True:
            if time.monotonic() - start > timeout:
                raise TimeoutError("gate exceeded timeout")
            try:
                line = lines.get(timeout=min(30, timeout))
            except queue.Empty:
                print("RUNNING (waiting for command output)", flush=True)
                continue
            if line is None:
                break
            if evidence:
                evidence.feed(line)
            print(redact(line.rstrip(), env), flush=True)
        code = process.wait(timeout=10)
    finally:
        if process.poll() is None:
            # Only this driver's newly created process group; never global cleanup.
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        process.stdout.close()
    if evidence is not None:
        evidence.exit_code = code
    errors = evidence.errors() if evidence else []
    for error in errors:
        print("EVIDENCE FAILURE " + redact(error, env), flush=True)
    return code == 0 and not errors


def go_command(package, names, tags=None, pattern=None):
    if not names or any(not name for name in names):
        raise ValueError("a gate must require named tests; empty selectors are not acceptance")
    command = ["go", "test", "-json", "-count=1", "-p", "1", "-timeout=15m"]
    if tags:
        command += ["-tags=" + tags]
    return command + [package, "-run", pattern or "^(" + "|".join(names) + ")$"]


def sanitized_makefile(source):
    # Preserve the actual repository targets, omitting ONLY production auto-loads.
    return "\n".join(line for line in source.splitlines()
                     if line.strip() not in ("-include .env", "-include .env.local")) + "\n"


def scenario_coverage_errors(evidences, root=ROOT):
    """Fail closed on catalog gaps and require this invocation's exact Go evidence.

    report.mjs has no exported parser/JSON interface and --strict only checks
    drift, not missing scenarios. Mirror its flat catalog format here, but do
    not trust its optional historical .last-run.json or COVERS comments as proof.
    """
    try:
        catalog = (root / "tests/scenarios/bots.yaml").read_text()
        module = re.search(r"^module\s+(\S+)", (root / "go.mod").read_text(), re.M).group(1)
    except (OSError, AttributeError) as error:
        return ["cannot read Bot scenario catalog/module: " + str(error)]
    scenarios = []
    current = None
    reading_refs = False
    for line in catalog.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        item = re.fullmatch(r'  - id: (BOTS-\d+)', line)
        if item:
            current = {"id": item[1], "status": None, "refs": []}
            scenarios.append(current)
            reading_refs = False
        elif current is not None:
            status = re.fullmatch(r'    status: (\w+)', line)
            if status:
                current["status"] = status[1]
                reading_refs = False
            elif line.startswith("    test_refs:"):
                reading_refs = line == "    test_refs:"
                if line not in ("    test_refs:", "    test_refs: []"):
                    current["refs"].append("invalid test_refs format")
            elif reading_refs:
                ref = re.fullmatch(r'      - "([^"\n]+)"', line)
                if ref:
                    current["refs"].append(ref[1])
                else:
                    current["refs"].append("invalid test_refs format")
    errors = []
    # Count independently so malformed/removed entries cannot shrink the gate.
    ids = [scenario["id"] for scenario in scenarios]
    for missing in sorted(RELEASE_SCENARIOS - set(ids)):
        errors.append(missing + ": required scenario absent")
    if len(ids) != len(set(ids)) or len(re.findall(r"^\s+-\s+id:", catalog, re.M)) != len(ids):
        errors.append("duplicate or malformed scenario IDs")
    for scenario in scenarios:
        label = scenario["id"] + ": "
        if scenario["status"] != "covered":
            errors.append(label + "status is not covered (" + str(scenario["status"]) + ")")
        if not scenario["refs"]:
            errors.append(label + "no deployed test_refs")
        for ref in scenario["refs"]:
            match = re.fullmatch(r"(tests/e2e/[\w/-]+_test\.go)::(Test\w+(?:/[\w/-]+)?)", ref)
            if not match:
                errors.append(label + "invalid deployed test_ref: " + ref)
                continue
            file, test = match.groups()
            try:
                source = (root / file).read_text()
            except OSError:
                source = ""
            if not re.search(r"^func\s+" + re.escape(test.split('/')[0]) + r"\s*\(\s*\w+\s+\*testing\.T\s*\)", source, re.M):
                errors.append(label + "test_ref has no real Go test: " + ref)
                continue
            package = module + "/" + str(Path(file).parent)
            key = (package, test)
            if not any(key in evidence.qualified_passes and package in evidence.packages_passed
                       and not evidence.errors() for evidence in evidences):
                errors.append(label + "no current non-skipped executed evidence: " + ref)
    return errors


CONSOLIDATION_PHASES = ('baseline', 'runtime', 'storage', 'controller', 'events', 'clients', 'all')
CONSOLIDATION_BASELINE = {
    './pkg/launcher': ['TestBotEnhancedChatBaseline'],
    './pkg/api': ['TestBotEnhancedChatBaseline'],
    './pkg/daemon': ['TestBotEnhancedChatBaseline'],
}


# Progress manifests are deliberately smaller than the full acceptance matrix.
# Do not derive selectors from source files: deleting a test must fail evidence.
CONSOLIDATION_RUNTIME = {
    'runtime-captured-drill-tools': ('./pkg/tools', ['TestCapturedDrillStaticSnapshot']),
    'runtime-trusted-snapshot': ('./pkg/launcher', ['TestBotArtifactReviewTrustedSnapshot']),
    'runtime-kubernetes-bootstrap': ('./tests/e2eboot', [
        'TestRetainedHomeNoModelPreflight', 'TestRetainedHomeOwnership',
        'TestRetainedHomeBootstrapNoProviderOrSweep', 'TestRetainedHomeDedicatedFakeClient',
        'TestRetainedHomeDedicatedContextBeforeNetwork', 'TestRetainedHomeGenericProviderGateUnchanged',
        'TestRetainedHomeKubernetesInputs',
        'TestRetainedHomePublicRestoreCallsPlanAndRestore',
        'TestRetainedHomeRestoreOwnershipCleanupOrderSourcePreserved',
        'TestRetainedHomeRestoreCannotAdoptSource',
        'TestRetainedHomePausedSchedulesRejectsFabricatedEvidence',
        'TestRetainedHomeSQLReceiptRejectsRecreatedDatabase',
        'TestRetainedHomePublicRestoreFailureCleanupOnlyTarget',
        'TestRetainedHomeColdRestoreHasNoPlanningStub']),
    'runtime-template-wiring': ('./pkg/daemon', [
        'TestProductionBotTemplateAssemblySource', 'TestProductionBotTemplateBootstrapCapture',
        'TestProductionBotTemplateMissingBundleFailsClosed', 'TestProductionBotTemplateWiringScopedDispatch']),
    'runtime-delegate-receipts': ('./pkg/tools', [
        'TestDelegateJoinOutcomeBoundToServerChildren', 'TestDelegateTasksRealManagerRecordsJoin',
        'TestDelegateTasksRealManagerRetryRecordsJoin', 'TestDelegateRetryJoinRejectsAlteredIdentity',
        'TestDelegateJoinRequiresWholeBatchSuccess']),
    'runtime-prepared-plan': ('./pkg/launcher', [
        'TestBotPreparedPlanAnnouncementResume', 'TestBotPreparedPlanVerifierFailureResume']),
    'runtime-prepared-review': ('./pkg/api', [
        'TestBotPreparedReviewMissingEngineNeverDispatches',
        'TestBotPreparedBoundaryUnsettledRecoveryPreservesUncertainOutcome',
        'TestBotChatFailurePreservesWrappedUncertainOutcome']),
    'runtime-prepared-engine': ('./pkg/agent', [
        'TestPlanPreparedTrustedSettlementReconcilesIntent', 'TestPlanPreparedLegacyVerificationAssignsIdentity',
        'TestPlanPreparedAnnouncementRestore', 'TestPlanPreparedPublishRestartFailureEvidenceAndDuplicate',
        'TestPlanPreparedStaleBase', 'TestPlanPreparedSuspendBeforeVerifyAndUncertainRestart',
        'TestPlanPreparedSettledSaveDenialRetainsLocalEvidence', 'TestPlanPreparedConcurrentWaves',
        'TestPlanPreparedCallbackIsolation', 'TestPlanPreparedAnnouncementStableAfterCommitFailure',
        'TestPlanPreparedVerificationOnceAfterCommitFailure']),
    'runtime-prepared-store': ('./pkg/store/entstore', [
        'TestBotPreparedPlanExactApprovalPublication', 'TestBotPreparedPlanSQLiteReopen',
        'TestBotPreparedPlanApprovalRollbackAndIntegrity', 'TestBotPreparedPlanAuthorityAndUncertain']),
    'runtime-http': ('./pkg/api', ['TestStudioChatHTTPScopedRuntimeFactory',
                                  'TestStudioChatHTTPScopedAuthorityAdmission',
                                  'TestStudioChatHTTPScopedServiceCallbacks',
                                  'TestStudioChatHTTPDistillReviewCurrentAttempt',
                                  'TestStudioChatHTTPDistillManagerResetClosesState']),
    'runtime-live-authority': ('./pkg/chatruntime', [
        'TestLiveAuthorityBuildAndBindFreshMembership',
        'TestLiveAuthorityPersonalOnlyDoesNotRequireTeamMembership',
        'TestLiveAuthorityPromotionDoesNotBroadenCeilingOrBypassTeam',
        'TestLiveAuthorityMissingDependenciesFailClosed', 'TestLiveAuthoritySnapshotCannotBeRetargeted',
        'TestLiveAuthorityContextModelRechecksAtIteration',
        'TestLiveAuthorityModelPreservesBudgetCancellationAndLocal',
        'TestLiveAuthorityModelPreflightDenialClosesReads', 'TestLiveAuthorityModelPreflightReadLifetime',
        'TestLiveAuthorityDirectoryFailureAndCancellation']),
    'runtime-child-alias-leases': ('./pkg/sandbox', [
        'TestChildAliasLeasePreservesBorrowedClients', 'TestChildAliasLeaseLeavesReplacementUntouched']),
    'runtime-construction': ('./pkg/launcher', [
        'TestBotWorkflowQuestionProgressWithoutAutonomousClassifier',
        'TestChatRuntimeActualDelegateReadFreshAuthority', 'TestChatRuntimePersistedLeaseTransitions',
        'TestChatRuntimeCumulativeBudgetContinuity',
        'TestChatRuntimeDelegateAliasCleanupConcurrent', 'TestChatRuntimeDelegateCleanupJoinsBeforeAliasRemoval',
        'TestChatRuntimeLiveMembershipFreshAuthority', 'TestChatRuntimeLiveMembershipLazyMCP',
        'TestChatRuntimeLiveMembershipModelBoundary', 'TestChatRuntimeMCPExactQualifiedIdentity',
        'TestChatRuntimeContextFixture', 'TestChatRuntimeContextSynchronousCancellation',
        'TestChatRuntimeContextDerivedCancellation', 'TestChatRuntimeContextRelease',
        'TestChatRuntimeContextDeadlineAndValues', 'TestChatRuntimeContextNotification',
        'TestStudioChatRuntimeCapabilitiesHTTP', 'TestStudioChatRuntimeTemplatesHTTP',
        'TestStudioChatRuntimeRemoteMCPHTTP', 'TestStudioChatRuntimeA2AHTTP',
        'TestStudioChatRuntimeFactoryFlowContinuationHTTP',
        'TestStudioChatRuntimeFactoryFlowContinuationAcrossAttempts',
        'TestChatRuntimeScopedBrowserArtifactsAndHandoff',
        'TestStudioChatRuntimeFactoryScopedEmailTransport',
        'TestChatRuntimeScopedSchedulerAuthority', 'TestChatRuntimeScopedMemoryAuthority',
        'TestChatRuntimeScopedCredentialInjection', 'TestChatRuntimeScopedBotInjectionExactApproval',
        'TestChatRuntimeScopedDrillReportPersistence',
        'TestStudioChatRuntimeFactoryUsesSharedScopedConstructor',
        'TestChatRuntimeLazyDiscoveryScopeAndIdentity', 'TestChatRuntimeLazyAndEagerExecutableIdentity',
        'TestChatRuntimeLazyRejectsInvalidDiscovery', 'TestChatRuntimeLazyNameCollisions',
        'TestChatRuntimeBoundContextLifetime', 'TestChatRuntimeHookCannotExtendInvocationLifetime',
        'TestChatRuntimeLifetimeIntersection', 'TestChatRuntimeLifetimeLinkCleanup',
        'TestChatRuntimeConfiguredProcessAndFileExecution', 'TestChatRuntimeBrowserArtifactModeParity',
        'TestChatRuntimeConfiguredSearchProviderParity', 'TestChatRuntimeCapabilityParityConfiguredProtocols',
        'TestChatRuntimeCapabilityParityConfiguredServices', 'TestChatRuntimeCapabilityParityConfiguredPlan',
        'TestChatRuntimeRestrictedChildAuthority', 'TestChatRuntimeNestedDrillFences',
        'TestChatRuntimeNestedFlowFences', 'TestChatRuntimeFlowPreservesNarrowedChildPolicy',
        'TestChatRuntimeChildCancellationJoinedReturn',
        'TestChatRuntimeAutonomousPlanRequiresMirrorGrant',
        # Gap/denial and verifier sub-dispatch evidence, not a successful autonomous plan lifecycle.
        'TestChatRuntimeAutonomousPlanExactMirrorPregrantGap',
        'TestChatRuntimeAutonomousPlanExactVerifierGrant', 'TestChatRuntimeAutonomousPlanVerifierAuthority',
        'TestChatRuntimeCapabilityParity', 'TestChatRuntimeCapabilityParityConfiguredHTTPAndFlow', 'TestChatRuntimeResolvedComposition',
        'TestChatRuntimeCodeAuthorizationAndDelegation', 'TestChatRuntimeFlowRunnerBinding',
        'TestChatRuntimeUnscopedFlowPreservesRequestCatalog', 'TestChatRuntimeCleanupAndAdditiveHooks',
        'TestChatRuntimeRejectsUnwrappableCapability', 'TestChatRuntimeCancelledInvocationDoesNotDispatch',
        'TestChatRuntimeScopedEnginesExecuteAndDeny', 'TestChatRuntimeScopedFlowResume',
        'TestChatRuntimeScopedFlowCancellationCleanup',
        'TestChatRuntimeDirectToolsetDistillation',
        'TestChatRuntimeSharedPlanLifecycle', 'TestStudioChatRuntimeSharedPlanHTTP',
        'TestBuildChatFlowDistillerNoFallback', 'TestBuildChatFlowDistillerValidation',
        'TestBuildChatFlowDistillerDryRun',
        'TestChatRuntimeFlowDrillMissingDependenciesShadowAmbient',
        'TestChatRuntimeFlowDrillExplicitExecutionIsolation',
        'TestChatRuntimeFlowDrillMissingInputsDependencies', 'TestChatRuntimeFlowDrillUnscopedPreservesContext']),
}
CONSOLIDATION_EVENTS = {
    'events-http': ('./pkg/api', [
        'TestBotEventsHTTPAcceptance', 'TestBotHistoryStreamHTTPAcceptance',
        'TestBotCanonicalPresentationHTTPFields', 'TestBotFileFirstPublicationSSE',
        'TestBotFileContentPersistenceHTTP', 'TestBotTitleMandateAcknowledgementHomeHTTP',
        'TestBotPresentationMatrixHTTPAcceptance', 'TestBotHomePresentationMatrixHTTPAcceptance',
        'TestBotLinkedSessionRestoreProjectionHTTPAcceptance',
        'TestBotChatFirstConversationHTTPAcceptance']),
    'events-projection': ('./pkg/api', [
        'TestBotCanonicalPlanUsesSharedProgressContract', 'TestBotCanonicalSharedPresentationFields',
        'TestBotCanonicalFramesAtomicCursor', 'TestBotCanonicalFramesHiddenAndInvalidRows',
        'TestBotCanonicalSourceTextOwnership', 'TestSessionEventProjectorOrderedParity',
        'TestSessionEventProjectorReviewDoesNotTrustEmbeddedQuestion',
        'TestSessionEventProjectorHomeRunSpeechBinding', 'TestSessionEventProjectorRedactsCanonicalPayload',
        'TestSessionEventProjectorHomeSpeechRetainsProvenance',
        'TestSessionEventProjectorHomeOrdinaryADKActivity', 'TestSessionEventProjectorHomeOrdinaryADKDelegation',
        'TestBotHomeNarrativeHistoricalTelemetry']),
    'events-production-capture': ('./pkg/launcher', ['TestBotRuntimeProductionCapture']),
}
CONSOLIDATION_CLIENTS = {
    'clients-browser': ('./pkg/api', ['TestBotFileWorkspaceBrowserAcceptance',
        'TestBotConversationPresentationBrowserAcceptance', 'TestBotClientStatesSharedVisualBrowserAcceptance',
        'TestBotChatFirstOrderedConversationBrowserAcceptance', 'TestBotScheduledWorkspaceBrowserAcceptance']),
    'clients-pty': ('./cmd/astonish', [
        'TestBotParityPTYAcceptance', 'TestBotFileWorkspacePTYAcceptance',
        'TestBotProgressPTYAcceptance', 'TestBotComposerRevisionPTYAcceptance', 'TestBotCLIAndPTYAcceptance',
        'TestBotConversationPresentationPTYAcceptance']),
    'clients-terminal': ('./pkg/launcher', [
        'TestBotCanonicalWatchCursorIdentity', 'TestBotScheduledReportsMapperLiveAndReplay',
        'TestBotTranscriptSharedRendering', 'TestBotCanonicalReviewIsObservation',
        'TestBotOwnerAnswerHumanReadableOnce']),
    'clients-transport': ('./pkg/client', ['TestBotsWatchReconnectCursorAndDetach', 'TestBotsWatchForbiddenNoRetry']),
}
CONSOLIDATION_STORAGE = {
    'storage-docker': ('./pkg/sandbox/docker', [
        'TestCompiledHomeFilesNoPythonDocker', 'TestBotHomeStreamArchiveDockerLive',
        'TestBotHomeArchivePrivateWriterContentionDockerLive', 'TestBotHomeCompiledArchiveDockerLive']),
    'storage-docker-backup-cli': ('./cmd/astonish', ['TestBotFileFirstBackupCLI', 'TestBotFileFirstStoppedBackupCLI']),
    'storage-compiled-linux': ('./pkg/sandbox', [
        'TestCompiledHomeFilesProtocol', 'TestCompiledHomeFilesScopedRoots',
        'TestCompiledHomeFilesFenceAndStreamErrors', 'TestCompiledHomeFilesResidualStages',
        'TestCompiledHomeFilesCancellationProcess',
        'TestCompiledHomeFilesCrashRecoveryPreservesIndependentWriter',
        'TestCompiledHomeFilesRecoveryRejectsSpecialStages', 'TestCompiledHomeFilesPrivateRecoveryBeforeQuota',
        'TestCompiledWorkspaceStageCancellationAndRead',
        'TestCompiledWorkspaceCrashRecovery', 'TestCompiledWorkspaceRecoveryPreservesRevisionsAndReads',
        'TestCompiledWorkspaceRecoveryRejectsSpecialStages', 'TestCompiledWorkspaceRecoveryBeforeQuota',
        'TestCompiledWorkspaceRecoveryNestedWriter', 'TestCompiledWorkspaceRecoveryPreservesActiveWriter']),
    'storage-kubernetes-adapter-fixture': ('./pkg/sandbox/k8s', [
        'TestBotHomeFilesFilesystem', 'TestBotHomeFilesConfinement',
        'TestCompiledWorkspaceUnicodeReceipt', 'TestCompiledWorkspaceJoinsRequestWriter',
        'TestCompiledWorkspaceWarmNoHelper']),
}
CONSOLIDATION_CONTROLLER = {
    'controller-api': ('./pkg/api', [
        'TestBotAuthenticationEvidenceHTTPAcceptance', 'TestBotFileDeliveryHTTPNativeAuthorization',
        'TestBotContinueInteractiveRouteRequiresAuthentication', 'TestBotPlanningOwnerHTTPControls',
        'TestCapturedDrillOccurrenceExactAndNoReplay', 'TestCapturedDrillRepeatedOccurrenceConcurrentNoReplay',
        'TestBotEnhancedChatBaseline', 'TestBotTitleConfirmedLaterQuestionContinuation',
        'TestBotProgressRecoveryHTTP', 'TestBotProgressLifecycleHTTP', 'TestBotProgressRestartHTTP',
        'TestBotTitleAnswerAuthorityHTTPRegression', 'TestBotHistoryStreamHTTPAcceptance',
        'TestBotResumeNoReplayAfterEffect', 'TestBotResumeUnusedTargetDenialRestoresExactAction',
        'TestBotResumeAmbiguousConsumptionNeverRetryable', 'TestBotChatExactApprovalContinuation',
        'TestBotResumeModelPreservesExactNumbers', 'TestBotMCPApprovalCheckpointTargetBinding']),
    'controller-personal-mailbox': ('./pkg/store/entstore', ['TestPersonalSettingsBotEmailRoundTripScopeAndClear']),
    'controller-daemon': ('./pkg/daemon', [
        'TestBotConfiguredProcessAdmissionHTTP',
        'TestBotConfiguredBrowserAdmissionHTTP', 'TestBotPreparedPlanAdmissionHTTP', 'TestBotOwnerPlanConsentHTTP',
        'TestBotConfiguredServiceAdmissionHTTP', 'TestCapturedScheduledDrillDriftHTTP',
        'TestScheduledDrillApprovalHasNoCapturedChildAuthority',
        'TestAllModeDockerFixtureContract', 'TestAllModeDockerModelScript',
        'TestBotEnhancedChatBaseline', 'TestBotFileFirstWorkerHTTP',
        'TestBotWorkerPollRecoversCrashAfterDatabaseReopen', 'TestBotWorkerPublicationIdlePollAndUncertainReplay',
        'TestProductionBotWorkerBorrowsChatResources', 'TestBotHomeSessionRecoveryPreflight',
        'TestProductionBotTemplateAssemblySource', 'TestProductionBotTemplateBootstrapCapture',
        'TestProductionBotTemplateMissingBundleFailsClosed', 'TestProductionBotTemplateWiringScopedDispatch',
        'TestBotWorkerSharedLiveAuthority', 'TestBotUncertainEffectRestartHTTPNoReplay',
        'TestBotUncertainEffectCrashHTTPNoReplay',
        'TestBotIntentHTTPWorkerDraftApprovalExactOnce', 'TestBotApproveContinueHTTPWorkerExactOnce',
        'TestBotChatConfiguredAdmissionParityHTTP', 'TestBotChatConfiguredContinuousAdmissionParityHTTP',
        'TestBotConfiguredProtocolAdmissionHTTP', 'TestBotConfiguredSearchOwnerResumeHTTP',
        'TestBotConfigurationProductionPolicyHTTP']),
    'controller-store': ('./pkg/store/entstore', [
        'TestBotSQLiteSQLStorageLockOrderNoSessionReentry', 'TestBackupFileFirstSQLiteConcurrentRestoreArbitration',
        'TestBotPlanningDurableOwnerMode', 'TestBotRuntimeSupervisorLeaseLossCancels', 'TestBotHomeFileCommitFence',
        'TestBotApprovalResumeFenced', 'TestBotApprovalRequeueCurrentAuthority',
        'TestBotApprovalConcurrentConsumption', 'TestBotApprovalCrashRecoveryEvidence',
        'TestBotApprovalResumePreDispatchDenial', 'TestBotApproveContinueAtomic',
        'TestBotContentApprovalCodec', 'TestBotContentApprovalCorruptMissing',
        'TestBotContentApprovalSQLiteReopen',
        'TestBotProgressQueuedAuthorityLoss',
        'TestBotConfigurationPolicyTransaction', 'TestBotPolicyTeamMCPTransaction',
        'TestBotReviewedConfigurationRevokesOldGrants', 'TestBotAdaptiveConversionRollback',
        'TestBotPausedPolicyRejectsUnreviewedPolicyReplacement',
        'TestReviewedConfigurationRejectsStaleScheduleBinding',
        'TestReviewedConfigurationResumeKeepsSchedulesDisabled', 'TestBotPausedStoppedConfiguration',
        'TestBotResumeDisabledScheduleApproval', 'TestBotResumeDisabledScheduleRejectsCorruption',
        'TestBotIndependentScheduleConsent', 'TestBotScheduleTargetOnlyEnablement', 'TestBotResumeScheduleRejectsCorruption',
        'TestBotResumeScheduleRollback', 'TestBotResumeScheduleLegacyReadinessCAS',
        'TestBotAuthorizedScheduleQueuedReconciliation', 'TestBackupFileFirstRestoreGuards',
        'TestBackupFileFirstRestoreFilesBeforeSQL', 'TestBackupFileFirstRestore', 'TestBackupFileFirstMappingRunAuthorityAndOpaqueContent',
        'TestBackupFileFirstMappingIndependentOwnership', 'TestBackupFileFirstReconciliationPreservesQuiescence',
        'TestBackupFileFirstRestoreBackendNeutral',
        'TestPostgresFileFirstReceiptBinding', 'TestPostgresFileFirstReceiptReservedBeforeCreate',
        'TestPostgresFileFirstNeverAdoptsExistingOrAmbiguousCreate', 'TestPostgresFileFirstCleanupExactReceiptAndRetry',
        'TestPostgresFileFirstTenantBeforePublication', 'TestPostgresFileFirstPublicationRollbackAndAmbiguousCommit',
        'TestPostgresFileFirstTenantTransactionAndFailure', 'TestPostgresFileFirstExclusionAndGrantFailures',
        'TestPostgresFileFirstPublicCleanupReceiptRetry', 'TestPostgresFileFirstCapabilityPreflightReadOnly',
        'TestTenantGenerationRoute', 'TestTenantGenerationRouteTeamSchema', 'TestStagedPostgresOrgReceiptPreflight']),
    'controller-docker': ('./pkg/daemon', [
        'TestBotEnhancedChatNoPythonDocker', 'TestBotNewMessageDockerBetweenCallReproduction',
        'TestBotEnhancedChatTeamNoPythonDocker']),
    # Give the 692-second both-scope journey its own unchanged 15m process budget.
    'controller-docker-allmode': ('./pkg/daemon', ['TestBotAllModeRetainedSessionDocker']),
    'controller-kubernetes-home': ('./tests/e2e/bots', [
        'TestBotsLiveKubernetesRetainedHome',
        'TestBotsLiveKubernetesAllModeRetainedLifecycle',
        'TestBotsLiveKubernetesNoPythonScheduledContinuationDelegate',
        'TestBotsLiveKubernetesTeamLifecycleExactDownloads',
        'TestBotsLiveKubernetesStoppedColdBrowseBackupRestore']),
    # Keep delegated pending-question recovery separate; its own unchanged 15m budget.
    'controller-docker-delegate': ('./pkg/daemon', ['TestBotDelegatedRetainedSessionDocker']),
}
# Baseline names occur in multiple packages. Keep their child obligations qualified
# by tier rather than applying one package's children to another package.
CONSOLIDATION_CONTROLLER_SUBTESTS = {
    'controller-kubernetes-home': {
        'TestBotsLiveKubernetesAllModeRetainedLifecycle': ['personal', 'team'],
        'TestBotsLiveKubernetesNoPythonScheduledContinuationDelegate': ['personal', 'team'],
    },
    'controller-docker-delegate': {
        'TestBotDelegatedRetainedSessionDocker': ['personal', 'team'],
    },
    'controller-docker-allmode': {
        'TestBotAllModeRetainedSessionDocker': ['personal', 'team'],
    },
    'controller-api': {
        'TestBotPlanningOwnerHTTPControls': ['personal', 'team'],
        'TestBotAuthenticationEvidenceHTTPAcceptance': ['oauth_capability_deny', 'local_evidence'],
        'TestBotEnhancedChatBaseline': ['title_question_answer_once', 'budget_stops_without_replay'] +
            ['title_question_answer_once/' + scope + '/' + answer
             for scope in ('personal', 'team') for answer in ('option', 'custom')],
        'TestBotProgressRecoveryHTTP': ['personal', 'team'],
        'TestBotProgressLifecycleHTTP': ['personal', 'team', 'personal/lost-authority', 'team/lost-authority'],
        'TestBotProgressRestartHTTP': ['personal', 'team'],
        'TestBotTitleAnswerAuthorityHTTPRegression': [scope + '/durable=' + durable + '/' + decision
            for scope in ('personal', 'team') for durable in ('false', 'true')
            for decision in ('owner', 'foreign', 'revoked', 'mandate-revoked', 'policy-unavailable')
            if durable == 'true' or decision not in ('mandate-revoked', 'policy-unavailable')],
        'TestBotResumeNoReplayAfterEffect': ['model', 'extra-tool', 'tool-error'],
        # Empty regular intent is named #00 by Go's testing package.
        'TestBotChatExactApprovalContinuation': ['#00', 'draft_test'],
        'TestBotMCPApprovalCheckpointTargetBinding': ['same-target-once', 'changed-target-blocked'],
    },
    'controller-daemon': {
        # Each slash-delimited name is one actual t.Run; no synthetic scope/mode parents.
        'TestBotConfiguredProcessAdmissionHTTP': [scope + '/' + mode + '/' + leaf
            for scope, mode in (('personal', 'studio'), ('personal', 'interactive'), ('team', 'interactive'))
            for leaf in ('lifecycle', 'foreign-process_read', 'foreign-process_write', 'foreign-process_kill')]
            + [scope + '/scheduled-exact/' + leaf for scope in ('personal', 'team')
               for leaf in ('shell_command', 'process_read', 'process_write', 'process_kill',
                            'foreign-process_read', 'foreign-process_write', 'foreign-process_kill', 'membership-loss')],
        'TestBotConfiguredBrowserAdmissionHTTP': ['personal/studio', 'personal/interactive',
            'personal/scheduled-exact-resume', 'personal/scheduled-membership-drift',
            'team/interactive', 'team/scheduled-exact-resume', 'team/scheduled-membership-drift'],
        'TestBotPreparedPlanAdmissionHTTP': ['personal', 'team'],
        'TestBotOwnerPlanConsentHTTP': ['personal', 'team'],
        'TestBotConfiguredServiceAdmissionHTTP': [service + '/' + scope + '/' + mode
            for service in ('a2a', 'email', 'drill') for scope in ('personal', 'team')
            for mode in ('studio', 'interactive', 'scheduled-exact-resume')
            if scope == 'personal' or mode != 'studio'],
        'TestCapturedScheduledDrillDriftHTTP': [scope + '/' + variant
            for scope in ('personal', 'team') for variant in ('suite', 'tool', 'arguments', 'restricted-shell')],
        'TestBotEnhancedChatBaseline': ['title_configured_search', 'schedule_confirmation_then_execution',
            'owner_priority_and_control', 'paused_recovery', 'continuation_preserves_budget'] +
            [child + '/' + scope for child in ('schedule_confirmation_then_execution', 'paused_recovery')
             for scope in ('personal', 'team')] +
            ['title_configured_search/' + scope + '/missing=' + missing
             for scope in ('personal', 'team') for missing in ('false', 'true')] +
            ['owner_priority_and_control/' + scope + '/' + scenario for scope in ('personal', 'team')
             for scenario in ('goal', 'paused_goal', 'task_once', 'priority_pause', 'deny', 'memory_deny', 'approve', 'expiry')],
        'TestBotFileFirstWorkerHTTP': ['personal', 'team'],
        'TestBotConfigurationProductionPolicyHTTP': ['personal', 'team'],
        # Real subprocess crash/reopen and loopback HTTP evidence, not physical Docker.
        # The crash worker helper is launched internally, never an acceptance selector.
        'TestBotUncertainEffectCrashHTTPNoReplay': ['personal', 'team'],
        'TestBotIntentHTTPWorkerDraftApprovalExactOnce': ['personal', 'team'],
        'TestBotApproveContinueHTTPWorkerExactOnce': ['personal', 'team'],
        # Seven admitted rows; autonomous exact resume is one leaf per separate run,
        # and routine production mandates deny shell/flow. Not continuous parity.
        'TestBotChatConfiguredAdmissionParityHTTP': ['personal', 'team', 'personal/studio'] +
            [scope + '/' + mode for scope in ('personal', 'team')
             for mode in ('interactive', 'interactive-mandate', 'autonomous')],
        'TestBotChatConfiguredContinuousAdmissionParityHTTP': ['personal', 'team', 'personal/studio'] +
            [scope + '/' + mode for scope in ('personal', 'team')
             for mode in ('interactive', 'autonomous')],
        # Personal-only Studio plus personal/team interactive, pending search,
        # exact MCP approval/resume and stale MCP configuration rejection.
        'TestBotConfiguredSearchOwnerResumeHTTP': ['personal', 'team'] +
            [scope + '/' + drift + '-drift' for scope in ('personal', 'team')
             for drift in ('model', 'provider', 'credential', 'dispatch-credential', 'extra-tool')],
        'TestBotConfiguredProtocolAdmissionHTTP': ['personal/studio'] +
            [scope + '/' + mode for scope in ('personal', 'team')
             for mode in ('interactive', 'scheduled-exact-boundary',
                          'scheduled-mcp-exact-boundary', 'scheduled-mcp-stale-config')],
        'TestBotWorkerPollRecoversCrashAfterDatabaseReopen': ['personal', 'team'],
        'TestBotWorkerPublicationIdlePollAndUncertainReplay': ['none', 'before-publication', 'transport', 'after-send'],
    },
    'controller-store': {
        'TestBotSQLiteSQLStorageLockOrderNoSessionReentry': ['personal', 'team'],
        'TestBackupFileFirstSQLiteConcurrentRestoreArbitration': ['personal', 'team'] + [
            scope + '/' + outcome for scope in ('personal', 'team')
            for outcome in ('commit', 'interrupt-sql-cleanup-retry')],
        'TestBackupFileFirstRestoreBackendNeutral': ['exact', 'interrupted-cleanup-retry', 'wrong-return', 'wrong-kind'],
        'TestPostgresFileFirstNeverAdoptsExistingOrAmbiguousCreate': ['already_exists', 'connection_lost_after_CREATE'],
        'TestPostgresFileFirstTenantBeforePublication': ['success', 'tenant_rollback'],
        'TestPostgresFileFirstPublicationRollbackAndAmbiguousCommit': ['success', 'insert-error', 'commit-error', 'target-populated'],
        'TestPostgresFileFirstTenantTransactionAndFailure': ['success', 'second-scope-error', 'commit-error', 'missing-entry'],
        'TestPostgresFileFirstExclusionAndGrantFailures': ['success', 'lock-error', 'populated', 'grant-error'],
        'TestTenantGenerationRoute': ['legacy', 'absent_canonical_row', 'published_generation', 'empty_canonical_route',
            'foreign_org', 'foreign_instance_legacy', 'foreign_generation_instance', 'unreceipted_generation',
            'wrong_generation', 'invalid_archive_digest', 'unsafe_route', 'truncated_route'],
        'TestStagedPostgresOrgReceiptPreflight': ['valid', 'nil_journal', 'foreign_instance', 'not_created', 'cleaned',
            'ready', 'zero_oid', 'unrecorded', 'wrong_oid', 'wrong_comment', 'published', 'wrong_phase'],
        'TestBotPlanningDurableOwnerMode': ['personal', 'team'],
        'TestBotResumeDisabledScheduleApproval': ['personal', 'team'],
        'TestBotResumeDisabledScheduleRejectsCorruption': [scope + '/' + scenario
            for scope in ('personal', 'team') for scenario in ('owner', 'version', 'bot', 'trigger',
                'cron', 'timezone', 'payload', 'next-run', 'missing', 'active')],
        'TestBotIndependentScheduleConsent': ['personal', 'team'],
        'TestBotScheduleTargetOnlyEnablement': ['personal', 'team'],
        'TestBotResumeScheduleRejectsCorruption': [scope + '/' + scenario
            for scope in ('personal', 'team') for scenario in ('digest', 'owner', 'bot', 'trigger', 'version',
                'cron', 'timezone', 'payload', 'missing', 'enabled', 'enabled-next', 'next-run',
                'wrong-scope', 'readiness', 'transaction-binding')],
        'TestBotResumeScheduleRollback': ['personal', 'team'],
        'TestBotResumeScheduleLegacyReadinessCAS': [scope + '/' + mode
            for scope in ('personal', 'team') for mode in ('missing-readiness', 'readiness-failed',
                'binding-changed', 'revision-changed')],
        'TestBotConfigurationPolicyTransaction': ['personal', 'team'],
        'TestBotReviewedConfigurationRevokesOldGrants': ['personal', 'team'],
        'TestBotAdaptiveConversionRollback': ['personal', 'team'],
        'TestBotPausedPolicyRejectsUnreviewedPolicyReplacement': ['personal', 'team'],
        'TestReviewedConfigurationRejectsStaleScheduleBinding': [scope + '/' + mutation
            for scope in ('personal', 'team') for mutation in ('missing', 'mode', 'version',
                'owner', 'bot', 'trigger', 'cron', 'timezone', 'flow', 'instructions', 'params')],
        'TestReviewedConfigurationResumeKeepsSchedulesDisabled': ['personal', 'team'],
        'TestBotPausedStoppedConfiguration': ['personal', 'team'] + [scope + '/' + status
            for scope in ('personal', 'team') for status in ('paused', 'stopped')],
        'TestBotRuntimeSupervisorLeaseLossCancels': ['personal', 'team'],
        'TestBotHomeFileCommitFence': [scope + '/' + mode for scope in ('personal', 'team')
            for mode in ('allowed', 'stale-lease', 'wrong-home', 'cancelled', 'callback-error')],
        'TestBotApprovalResumePreDispatchDenial': ['personal', 'team'],
        'TestBotApproveContinueAtomic': [scope + '/' + scenario for scope in ('personal', 'team')
            for scenario in ('queued', 'paused', 'expired', 'wrong-digest', 'revoked', 'rollback',
                'requeue-failure', 'stopped', 'deleted', 'changed-version', 'stale-checkpoint',
                'wrong-actor', 'changed-action', 'denied')],
        'TestBotContentApprovalCodec': ['bot', 'approval', 'run', 'version', 'digest',
            'tool', 'effect', 'target', 'spend', 'size', 'bodydigest'],
        'TestBotContentApprovalSQLiteReopen': ['personal', 'team'],
        'TestBotApprovalResumeFenced': ['personal', 'team'],
        'TestBotApprovalConcurrentConsumption': ['personal', 'team'],
        'TestBotApprovalCrashRecoveryEvidence': [scope + '/' + state
            for scope in ('personal', 'team')
            for state in ('approved-not-dispatched', 'consumed-outcome-unknown')],
        'TestBotApprovalRequeueCurrentAuthority': [scope + '/' + mode for scope in ('personal', 'team')
            for mode in ('paused', 'expired', 'revoked-membership', 'denied', 'wrong-approval', 'changed-version')],
        'TestBotProgressQueuedAuthorityLoss': [scope + '/' + mode for scope in ('personal', 'team')
            for mode in ('settled', 'pending', 'uncertain', 'wrong-binding', 'committed')],
        'TestBotAuthorizedScheduleQueuedReconciliation': [scope + '/' + operation
            for scope in ('personal', 'team') for operation in ('schedule_edit', 'schedule_pause',
                'schedule_remove', 'schedule_create', 'schedule_create/disabled-unrelated')],
        'TestBackupFileFirstRestoreGuards': ['mapping', 'reset', 'missing-backend', 'populated', 'leftover-tenant'],
        'TestBackupFileFirstRestoreFilesBeforeSQL': [kind + '/' + mode
            for kind in ('personal', 'team')
            for mode in ('exact', 'corrupt', 'truncated', 'trailing', 'close-error', 'interrupt-sql')],
        'TestBackupFileFirstRestore': ['personal', 'team'],
    },
}
CONSOLIDATION_SUBTESTS = {
    'TestCompiledHomeFilesCancellationProcess': ['abrupt=false', 'abrupt=true'],
    'TestCompiledHomeFilesRecoveryRejectsSpecialStages': ['symlink', 'hardlink', 'fifo', 'directory'],
    'TestCompiledWorkspaceCrashRecovery': ['write', 'publish'],
    'TestCompiledWorkspaceRecoveryRejectsSpecialStages': ['symlink', 'hardlink', 'fifo', 'directory', 'revision-symlink'],
    'TestBotArtifactReviewTrustedSnapshot': ['foreign-run', 'foreign-owner', 'different-path', 'corrupt'],
    'TestCapturedDrillStaticSnapshot': ['static', 'dynamic', 'retry', 'browser', 'flow', 'malformed', 'missing-classifier'],
    'TestCapturedDrillOccurrenceExactAndNoReplay': ['exact', 'arguments', 'tool', 'owner', 'revoked',
        'uncaptured', 'shell', 'shell-grant', 'cancelled', 'missing-classifier', 'late-owner', 'late-fence',
        'late-revoked', 'rebound-policy'],
    'TestRetainedHomeNoModelPreflight': ['ASTONISH_E2E_DEDICATED', 'ASTONISH_E2E_BACKEND',
        'ASTONISH_E2E_SANDBOX_BACKEND', 'ASTONISH_E2E_SANDBOX_NAMESPACE',
        'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE', 'KUBECONFIG', 'ASTONISH_E2E_KUBE_CONTEXT',
        'ASTONISH_E2E_RESOURCE_OWNER', 'ASTONISH_SANDBOX_IMAGE', 'ASTONISH_E2E_HOMES_PVC',
        'ASTONISH_TEST_DSN', 'ASTONISH_E2E_POSTGRES_INSTANCE'],
    'TestRetainedHomeKubernetesInputs': ['missing-' + key for key in (
        'ASTONISH_E2E_DEDICATED', 'ASTONISH_E2E_SANDBOX_NAMESPACE',
        'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE', 'KUBECONFIG', 'ASTONISH_E2E_KUBE_CONTEXT',
        'ASTONISH_SANDBOX_IMAGE', 'ASTONISH_E2E_HOMES_PVC')] + ['#00', '#01'],
    'TestProductionBotTemplateWiringScopedDispatch': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary', 'interactive', 'autonomous')],
    'TestDelegateJoinOutcomeBoundToServerChildren': ['joined', 'wrong-child', 'wrong-batch', 'failed', 'missing', 'no-identity'],
    'TestDelegateRetryJoinRejectsAlteredIdentity': ['joined', 'forged', 'json-copy', 'child', 'batch', 'task',
        'parent', 'initial-child', 'attempt-count', 'attempt-number', 'attempt-child', 'attempt-status',
        'attempt-error', 'reordered', 'missing-attempt', 'failed', 'error', 'missing-result', 'wrong-call', 'cancelled'],
    'TestDelegateJoinRequiresWholeBatchSuccess': ['false', 'true'],
    'TestBotPreparedPlanAnnouncementResume': ['personal', 'team'],
    'TestBotPreparedPlanVerifierFailureResume': ['personal', 'team'],
    'TestBotPreparedReviewMissingEngineNeverDispatches': ['awaiting_approval', 'published'],
    'TestBotPreparedBoundaryUnsettledRecoveryPreservesUncertainOutcome': ['dispatch_started', 'uncertain'],
    'TestBotChatFailurePreservesWrappedUncertainOutcome': [
        'bot_revision_or_lease_conflict', 'context_canceled', 'context_deadline_exceeded'],
    'TestPlanPreparedPublishRestartFailureEvidenceAndDuplicate': ['0', '7'],
    'TestPlanPreparedConcurrentWaves': ['false', 'true'],
    'TestBotPreparedPlanExactApprovalPublication': ['personal', 'team'],
    'TestBotPreparedPlanSQLiteReopen': ['personal', 'team'],
    'TestBotPreparedPlanApprovalRollbackAndIntegrity': ['personal', 'team'],
    'TestBotPreparedPlanAuthorityAndUncertain': ['personal', 'team'],
    'TestChatRuntimeActualDelegateReadFreshAuthority': [scope + '/' + mode + '/' + fence
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
        for fence in ('none', 'membership-before-model', 'membership-before-tool', 'lease-before-model', 'lease-before-tool')
        if mode != 'ordinary-chat' or not fence.startswith('lease-')],
    # Bot leases only: ordinary Chat has no Bot run lease.
    'TestChatRuntimePersistedLeaseTransitions': [scope + '/' + mode + '/' + transition
        for scope in ('personal', 'team') for mode in ('interactive-bot', 'autonomous-bot')
        for transition in ('renewed', 'expired', 'replaced')],
    'TestChildAliasLeasePreservesBorrowedClients': ['backend', 'node-adapter'],
    'TestStudioChatHTTPScopedAuthorityAdmission': ['missing-team', 'revoked-team', 'revoked-org',
        'missing-platform', 'missing-router', 'suspended-user', 'bind-directory-unavailable'],
    'TestLiveAuthorityBuildAndBindFreshMembership': [scope + '/' + change
        for scope in ('personal', 'team')
        for change in ('user-disabled', 'org-disabled', 'org-removed', 'team-removed')],
    'TestLiveAuthorityMissingDependenciesFailClosed': [missing + '/' + fallback
        for missing in ('platform', 'router', 'both') for fallback in ('0', '1')],
    'TestLiveAuthorityContextModelRechecksAtIteration': [scope + '/' + change
        for scope in ('personal', 'team')
        for change in ('org-demoted', 'owner-demoted', 'team-demoted', 'platform-demoted', 'team-removed')],
    'TestLiveAuthorityModelPreservesBudgetCancellationAndLocal': ['false', 'true'],
    'TestChatRuntimeLiveMembershipFreshAuthority': [scope + '/' + mode + '/' + stage
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
        for stage in ('post-construction', 'post-discovery')],
    'TestChatRuntimeLiveMembershipLazyMCP': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeLiveMembershipModelBoundary': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    # Exact MCP grant/configuration tests currently cover autonomous Bot only.
    'TestChatRuntimeMCPExactQualifiedIdentity': [scope + '/' + scenario
        for scope in ('personal', 'team') for scenario in ('exact', 'bridge', 'exact-capacity',
            'arguments', 'command', 'server-args', 'url', 'disabled', 'env', 'server-name',
            'tool-name', 'bare-grant', 'cancelled', 'startup-config', 'startup-lease')],
    'TestChatRuntimeContextSynchronousCancellation': [source + '/' + check
        for source in ('parent', 'bound') for check in ('Err', 'Done')],
    'TestChatRuntimeContextDerivedCancellation': ['independent-lifetimes', 'same-lifetime'],
    'TestChatRuntimeContextDeadlineAndValues': ['parent', 'bound', 'expired-parent', 'expired-bound'],
    'TestChatRuntimeContextNotification': ['parent', 'bound'],
    'TestBotWorkerSharedLiveAuthority': [scope + '/' + authority
        for scope in ('personal', 'team') for authority in ('organization', 'team')],
    # Each scope includes terminal-persistence restart, rejected write replay and
    # an independently authorized receipt read; it is not a mid-effect crash test.
    'TestBotUncertainEffectRestartHTTPNoReplay': ['personal', 'team'],
    'TestChatRuntimeRestrictedChildAuthority': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    # Real shared meter and file-ledger continuation; not SQL/controller resume.
    'TestChatRuntimeCumulativeBudgetContinuity': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeChildCancellationJoinedReturn': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeNestedDrillFences': [scope + '/' + mode + '/' + fence
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
        for fence in ('none', 'cancel', 'lease-revoked', 'budget')
        if mode != 'ordinary-chat' or fence in ('none', 'cancel')],
    'TestChatRuntimeNestedFlowFences': [scope + '/' + mode + '/' + fence
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
        for fence in ('none', 'cancel', 'lease-revoked', 'budget')
        if mode != 'ordinary-chat' or fence in ('none', 'cancel')],
    'TestChatRuntimeFlowPreservesNarrowedChildPolicy': ['personal', 'team'],
    'TestChatRuntimeAutonomousPlanExactMirrorPregrantGap': ['personal', 'team'],
    'TestChatRuntimeAutonomousPlanExactVerifierGrant': ['personal', 'team'],
    'TestChatRuntimeAutonomousPlanVerifierAuthority': ['personal', 'team'],
    'TestBotFileFirstBackupCLI': ['personal', 'team'],
    'TestBotFileFirstStoppedBackupCLI': ['personal', 'team'],
    'TestBotPresentationMatrixHTTPAcceptance': ['personal', 'team'] + [scope + '/' + scenario
        for scope in ('personal', 'team') for scenario in ('live', 'history', 'reconnect', 'revoked')],
    'TestBotHomePresentationMatrixHTTPAcceptance': ['personal', 'team'] + [scope + '/' + scenario
        for scope in ('personal', 'team') for scenario in ('live', 'history', 'reconnect', 'revoked')],
    'TestBotLinkedSessionRestoreProjectionHTTPAcceptance': ['personal', 'team'] + [scope + '/' + link
        for scope in ('personal', 'team') for link in ('linkedHome', 'linkedRun')] + [scope + '/' + link + '/' + scenario
        for scope in ('personal', 'team') for link in ('linkedHome', 'linkedRun')
        for scenario in ('empty', 'populated', 'revoked', 'uncommittedPublication', 'unavailable', 'wrongApp')],
    'TestSessionEventProjectorHomeOrdinaryADKActivity': ['write_file', 'edit_file', 'load_file'] + [
        name + '/' + result for name in ('write_file', 'edit_file', 'load_file')
        for result in ('success', 'failure')],
    'TestBotClientStatesSharedVisualBrowserAcceptance': ['personal', 'team'] + [scope + '/' + state
        for scope in ('personal', 'team') for state in (
            'loading_read_failure_retry_empty', 'disconnect_cursor_draft_busy_catchup',
            'shared_public_visual_comparison', 'Escape_existing_details',
            'scope_cancellation_no_stale_transcript', 'populated_paused_stopped_deleted')] + [
        scope + '/shared_public_visual_comparison/' + mode for scope in ('personal', 'team')
        for mode in ('ordinary_comparison', 'Bot_live_reconnect', 'Bot_restore')] + [
        'team/populated_paused_stopped_deleted/team_viewer_paused_readonly'],
    'TestBotConversationPresentationBrowserAcceptance': ['personal', 'team'],
    'TestBotConversationPresentationPTYAcceptance': ['draft', 'active', 'paused'],
    'TestBotFileWorkspaceBrowserAcceptance': ['personal', 'team'],
    'TestBotFileWorkspacePTYAcceptance': ['personal', 'team'],
    'TestBotParityPTYAcceptance': ['draft', 'active', 'paused'],
    'TestBotProgressPTYAcceptance': ['personal', 'team'],
    'TestChatRuntimeCapabilityParity': ['configured-three-mode-subset/' + scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeCapabilityParityConfiguredServices': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeCapabilityParityConfiguredPlan': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot')],
    'TestChatRuntimeConfiguredProcessAndFileExecution': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeBrowserArtifactModeParity': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeConfiguredSearchProviderParity': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('chat', 'interactive-bot', 'autonomous-bot')],
    'TestChatRuntimeCapabilityParityConfiguredProtocols': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot', 'autonomous-approved')],
    'TestChatRuntimeAutonomousPlanRequiresMirrorGrant': ['personal', 'team'],
    'TestChatRuntimeCapabilityParityConfiguredHTTPAndFlow': [scope + '/' + mode
        for scope in ('personal', 'team') for mode in ('chat', 'interactive-bot', 'autonomous-bot')],
    'TestStudioChatHTTPScopedRuntimeFactory': ['success', 'construction-failure', 'missing-agent', 'missing-result', 'missing-plan'],
    'TestChatRuntimeLazyNameCollisions': ['ordinary', 'extension', 'toolset', 'direct'],
    'TestChatRuntimeHookCannotExtendInvocationLifetime': ['run', 'declaration', 'discovery'],
    'TestChatRuntimeLifetimeIntersection': [shorter + '/cancel-' + cancelled
                                           for shorter in ('caller', 'hook') for cancelled in ('caller', 'hook')],
    'TestChatRuntimeLifetimeLinkCleanup': ['run', 'declaration', 'discovery', 'flow', 'denied', 'discovery-error'],
    'TestChatRuntimeScopedEnginesExecuteAndDeny': ['run_flow', 'run_drill'],
    'TestChatRuntimeFlowDrillExplicitExecutionIsolation': ['inputs=false', 'inputs=true'],
    'TestCompiledHomeFilesFenceAndStreamErrors': ['parent', 'stage', 'target'],
    'TestCompiledHomeFilesScopedRoots': ['valid', 'k8s-owner', 'unknown-owner-prefix', 'wrong-owner',
                                       'owner-link', 'owner-hardlink', 'owner-fifo', 'data-link',
                                       'private-link', 'missing-private', 'create-private'],
}
CONSOLIDATION_BOUNDARIES = {
    'controller-personal-mailbox': 'required fail-first personal mailbox persistence prerequisite: TestPersonalSettingsBotEmailRoundTripScopeAndClear; optional bot_email JSON configuration/scoped credential reference only, no secrets or mailbox content; schema/Ent expansion needs separate authorization, no production migration/deployment; failure is FAIL, never a skipped or dropped full service row',
    'runtime-captured-drill-tools': 'detached static captured suite/fingerprint and nonstatic rejection through actual shared drill handler; not live browser or full capability parity',
    'runtime-trusted-snapshot': 'production artifact reader trusted content snapshot, foreign run/owner/path and corrupt bytes rejection; not physical storage or a nonexistent launcher captured-drill test',
    'runtime-template-wiring': 'ordinary NewWiredChatAgent bootstrap capture and production helper handoff of the same template/session registries, backend and borrowed pool; shared selected-template dispatch in personal/team ordinary/interactive/autonomous modes with exact-grant, changed-argument, foreign-scope and revoked-turn denial; controlled transport/authorization, not physical isolation, scheduler lifecycle or full configured capability parity',
    'runtime-delegate-receipts': 'actual in-memory server manager joins and two-attempt retry identity integrity; manager-private non-JSON provenance attests only that admitted server tasks joined successfully, never child prose, claimed effects or artifacts; no broader grants, physical backend or full authority-matrix proof',
    'runtime-prepared-plan': 'successful autonomous prepared announce/running/verifier/mirror/Results and failed-verifier recovery in personal/team scopes through the production factory and API adapter, real file/shell dispatcher and SQLite; controlled model/guard/node transport, not HTTP routes, scheduler or physical backend acceptance',
    'runtime-prepared-review': 'missing private engine rejects awaiting-approval and published recovery before saved-leaf effects; dispatch_started/uncertain recovery and wrapped cancellation/conflict retain typed uncertainty, checkpoint and no-replay; API adapter fixture, not HTTP',
    'runtime-prepared-engine': 'shared plan engine stable candidates, settled success/failure evidence, concurrency, stale/uncertain recovery and no verifier replay; in-process engine fixtures',
    'runtime-prepared-store': 'personal/team SQLite exact approval and dispatch-intent transaction, content integrity/SQL-body exclusion, uncertain no-replay, publication idempotency and reopen; not physical retained storage or PostgreSQL',
    'clients-browser': 'built Studio isolated HTTP: mixed reviewed publication/native exact bytes, title/question/typed answer, scheduled execution journey and direct ordinary Chat/Bot live/reconnect/restore visual/state comparison; no paid model or physical retained-backend claim',
    'clients-pty': 'native PTY subprocesses over controlled HTTP, personal/team files and separate lifecycle controls',
    'clients-terminal': 'shared terminal projection and canonical reconnect fixtures',
    'clients-transport': 'authenticated HTTP watcher cursor/retry and detach checks',
    'events-http': 'authorized finite/live/reconnected HTTP projection, file publication and title-answer ordering; controlled local stores',
    'events-projection': 'canonical payload/identity/privacy fixtures; not a complete UI parity matrix',
    'events-production-capture': 'actual ChatRunner ADK/runtime journal projected through terminal mapping; controlled model',
    'controller-api': 'TestBotPlanningOwnerHTTPControls parent/personal/team require durable owner HTTP planMode, explicit false and missing/imported/unknown/stale consent rejection; source-defined platform JWT/local/OAuth evidence checks (oauth_capability_deny/local_evidence), native ticket GET/HEAD/range exact bytes with fresh membership and expiry denial, anonymous interactive-continuation rejection; isolated HTTP admission/answers/progress/history plus no-replay adapter fixture; controlled models, not physical compute',
    'controller-daemon': 'TestBotConfiguredProcessAdmissionHTTP requires its actual parent and all 28 slash-delimited rows (no synthetic scope/mode parents): personal Studio and personal/team interactive full shell/read/write/read/kill/final-read lifecycle plus foreign read/write/kill namespace denial; personal/team scheduled-exact shell/read/write/kill, foreign read/write/kill and membership-loss prove individual exact-action HTTP approval/resume, one dispatch, duplicate no-replay, fresh membership denial without consumption, and balanced borrowed cleanup; separate test-owned helper processes execute actual shared PTY tools, not continuous scheduled lifecycle or physical browser/Docker/Kubernetes; TestBotConfiguredBrowserAdmissionHTTP requires seven exact scope/mode rows: personal Studio and personal/team interactive, scheduled-exact-resume and scheduled-membership-drift; production browser_pdf and HTTP exact downloadable binary bytes through controlled CDP/process transport, exact owner resume/revocation/restoration, no replay/no host fallback and borrowed cleanup, not physical browser or independently verified effects; TestBotPreparedPlanAdmissionHTTP requires personal/team HTTP admission/decision/approve-and-continue with new fence, exact PLAN.md mirror once, denial without consumption and duplicate no-replay, not HTTP plan-selection consent or complete announcement/verifier/Results lifecycle; TestBotOwnerPlanConsentHTTP separately requires its actual parent and personal/team leaves: durable HTTP planMode admission, originating definition wait and exact approvedPlanExecution consent through continue-interactive, same run/session, fresh fence, real verifier once and persisted Results without SetApproved shortcuts; changed plan/revision/sequence and stale/completed consent 409, foreign session/missing/conflicting controls 400 and foreign owner 403 cause no verifier effect; controlled authenticated middleware/model/retained transport, not built client or physical compute; TestBotConfiguredServiceAdmissionHTTP requires all 15 actual a2a/email/drill rows: personal Studio plus personal/team interactive and scheduled-exact-resume; completed A2A five/team email two/drill five subsets do not waive the three failing personal email rows or full parent; separate controller-personal-mailbox persistence prerequisite required; TestCapturedScheduledDrillDriftHTTP requires both scopes suite/tool/arguments/restricted-shell denial; isolated HTTP worker/file-first and schedule flow plus lifecycle/shared-resource fixtures; TestBotConfiguredProtocolAdmissionHTTP executes configured loopback MCP/search/template adapters in personal Studio and personal/team interactive rows; scheduled-exact-boundary proves pending-only exact search approval with zero MCP/search dispatch, while scheduled-mcp-exact-boundary proves qualified config/argument-bound HTTP approval/resume, fresh owner revocation/restoration, exactly one actual MCP HTTP execution and duplicate no-replay; scheduled-mcp-stale-config rejects changed executable configuration without node dispatch or approval consumption; TestBotConfiguredSearchOwnerResumeHTTP separately proves search owner restoration at stable Bot version after model/provider/credential and dispatch-time credential drift: unchanged exact arguments/checkpoint, zero dispatch/consumption during denial, fresh lease retry and one configured provider HTTP execution; extra-tool drift denies post-resume actions with no replay; changed Bot version rejection remains mandatory in controller-store; not continuous scheduled MCP/search execution; not physical compute',
    'controller-store': 'TestBotPlanningDurableOwnerMode parent/personal/team require durable private owner mode, new-turn noninheritance, same-run explicit false, mode-bound idempotency and foreign/missing/stale/imported consent denial; temporary SQLite lease/approval/queued-authority and new-format restore guards; not physical restore ordering',
    'controller-docker': 'opt-in physical personal and team default-layer no-Python success, exact retained-byte recovery after eviction, stopped-compute read, 24 total/8 per-key hydration-open limits and 15s controlled overhead limit; no all-mode or live Kubernetes claim',
    'controller-docker-allmode': 'opt-in physical personal/team unchanged default-template no-Python title/answer, shared read_file, scheduled execution, compute eviction and original-session same-run continuation, real delegate child transcripts with exact 165-byte content and cold HTTP browse/download/run inspection without GET-triggered work; controlled model, not live Kubernetes or full capability/authority parity',
    'runtime-kubernetes-bootstrap': 'pure no-model input and ownership rejection, fake-client read-only preflight and mismatched-context checks; generic provider gate unchanged; not live Kubernetes or database evidence',
    'controller-kubernetes-home': 'dedicated no-model physical retained-home plus source-defined all-mode personal/team lifecycle, no-Python scheduled/continuation/delegate, team exact HTTP downloads and stopped cold browse/backup-restore; five actual parents and four scope leaves, never synthetic team/cold leaves; requires twelve explicit inputs, positively owned namespaces/independent Bound RWX claims and fresh owned exact logs; no provider configuration or sweeps; compilation/fake clients are not physical evidence; backend-neutral SQLite and fully wired PostgreSQL staged file-first importer have mandatory controller-store unit dependencies, not physical certification; ColdBackupRestore now calls retainedHomePublicRestore (PlanPlatformRestore then RestorePlatformBackup) on newRetainedHomeRestoreSQL with retainedHomeRestoreOwner; independent PostgreSQL target and exact OID/comment/generation cleanup, reopened routing, paused schedules, fresh Home ownership and exact stopped HTTP bytes are source present; seven mandatory runtime-kubernetes-bootstrap restore safety helpers are NONPHYSICAL and never replace the live scenario; physical inputs remain absent',
    'controller-docker-delegate': 'opt-in physical personal/team unchanged default-template no-Python question-only lazy compute, original retained-session recovery, authenticated restricted delegate exact 165-byte child transcript and genuine final pending ask_user card after progress checkpoint; controlled model, not live Kubernetes or complete three-mode authority/capability parity',
    'runtime-http': 'real isolated POST /api/studio/chat and SSE; controlled model, not deployed UI',
    'runtime-live-authority': 'temporary SQLite live membership and role-ceiling checks at input binding/model iteration, forwarded model-preflight support and read lifetime closure on denial/cancellation/provider dispatch; controlled provider, no physical compute',
    'runtime-child-alias-leases': 'optional alias-only lease release in controlled backend/node-adapter pools; borrowed owner/sibling and replacement preservation, not physical backend lifecycle',
    'runtime-construction': 'shared scoped constructor/catalog/dispatch, configured process/file execution through a separate helper process and browser artifact mode parity through controlled CDP, actual delegated read and request-owned joined alias cleanup, persisted SQLite lease renewal/expiry/replacement, cumulative shared model/search/delegate/tool accounting with file-ledger restore and exhausted-budget denial, live membership, exact MCP identity and cancellation fixtures; controlled model/node transport, not physical backend parity or SQL/controller budget resume; successful prepared autonomous lifecycle is required separately by runtime-prepared-plan',
    'storage-docker-backup-cli': 'public CLI personal/team SQLite backup/verify/restore with detached and stopped Chat compute, failure cleanup/retry and exact reopened HTTP bytes; not cross-backend backup',
    'storage-docker': 'physical Docker retained files and compiled archive transports; not full storage acceptance',
    'storage-compiled-linux': 'compiled Linux root protocol fixtures; not physical Kubernetes',
    'storage-kubernetes-adapter-fixture': 'Kubernetes adapter with fake API and external compiled node binary; NOT live Kubernetes',
}
# Required acceptance descriptions. These are obligations, not unconditional
# blockers: fresh exact scenario evidence can discharge a wired requirement.
CONSOLIDATION_REMAINING = {
    'clients': {
        'clients-all-mode-conversation': 'built Studio and native PTY full title-answer-tools-schedule-delivery journey across modes, paused/viewer/error/disconnect states; workspace-only fixtures are not complete conversation acceptance',
        'clients-shared-visual-parity': 'prove one ordered Chat/Bot visual source for tools/delegates/plans/artifacts/errors in live and restored modes without blanket Home tool suppression',
    },
    'events': {
        'events-full-presentation-matrix': 'complete shared Chat/Bot live/history/reconnect matrix including partial streaming, concurrent tools, interleaved delegation, plans and reviewed delivery; direct ADK Home file/delegation and historical telemetry regressions plus a mixed Home HTTP live/history/reconnect scenario are required',
        'events-linked-session-restore': 'prove Bot-linked ordinary session restore uses the same ordered public projection and authority across all modes',
    },
    'controller': {
        'controller-all-mode-no-python': 'combined scheduled/autonomous/continuation/delegate execution with actual shared tools and retained session reuse/recovery without overlay Python across all modes on Docker AND live Kubernetes',
        'controller-team-physical': 'physical team-scoped controller/Home lifecycle and identical downloadable bytes on Docker AND live Kubernetes; the named Docker team no-Python test is only the Docker half',
        'controller-cold-cross-backend': 'stopped-compute browse/download and cold backup/restore on Docker AND live Kubernetes without GET provisioning or run startup, paused schedules and fresh ownership',
        'controller-authority-matrix': 'complete three-mode personal/team fresh membership/lease/budget/exact approval/child-authority and uncertain-effect no-replay matrix; named denial fixtures are only a subset',
    },
    'runtime': {
        'three-mode-capability-parity': 'ordinary Chat, interactive Bot and scheduler/autonomous Bot: identical configured file/shell/process/search/browser/MCP/A2A/flow/drill/email/plan/template execution',
        'runtime-authority-matrix': 'personal/team scopes, fresh membership/lease checks, exact approvals, budgets, child authority, denial and uncertain-effect no-replay across all three modes',
        'runtime-lazy-session-lifecycle': 'question without compute; first tool creates one session; user/scheduled/continuation/delegate reuse, eviction and retained recovery on Docker AND live Kubernetes',
        'runtime-production-wiring': 'complete production composition, prompt/accounting/cleanup hooks and no host fallback with actual configured external capability fixtures',
    },
    'storage': {
        'storage-live-kubernetes': 'dedicated live Kubernetes: scoped UID/mount binding, one session, zero warm helper creations, cancellation/reaping, idle recreate and byte recovery; adapter fixtures are not cluster evidence',
        'storage-offline-backup-restore': 'stopped-compute browse/download and cold backup/restore using owned attachments, no GET provisioning or Bot run; files-before-SQL, paused schedules, exact new-format bytes on both backends',
        'storage-ordinary-chat-tool-parity': 'actual shared Chat filesystem tools in all Bot modes without overlay Python; unchanged schemas, no internal storage tools exposed to model',
        'storage-fences-and-transport-matrix': 'complete cross-backend no-follow/no-cross-mount links/FIFO/bind-mount, regular single-link, digest/size/quota/range, staged/fsynced CAS/create-only/no-replace, stale target/UID/purge/admission/commit denial and ambiguous no-replay matrix',
        'storage-cancel-stream-lifecycle': 'complete remote termination/reaping and joined streams, interrupted read/write/archive, nested commit reads without deadlock, target resolution outside SQL and no session repository reentry',
        'storage-durable-data-matrix': 'file-first refs, SQL-body exclusion, compact disk memory, revisions, independent templates, missing/corrupt files, authorization and test-owned cleanup on Docker AND live Kubernetes',
        'storage-complete-compiled-implementation': 'all remaining workspace/archive consumers and offline paths exercised through compiled implementation, exact new-format bytes across restore, unchanged untrusted scratch execution',
    },
}


class ConsolidationEvidence(GoEvidence):
    """Exact package evidence plus bounded diagnostic markers and an owned log."""
    def __init__(self, required, log, env):
        super().__init__(required)
        self.log, self.env = log, env
        self.diagnosed = False
        self.finished_failure = False
        self.malformed = False
        self.qualified_skips = set()
        self.qualified_failures = set()
        self.no_test_packages = set()

    def errors(self):
        return super().errors() + (['malformed Go JSON evidence'] if self.malformed else [])

    def feed(self, line):
        self.log.write(redact(line, self.env))
        self.log.flush()
        try:
            event = json.loads(line)
        except (ValueError, TypeError):
            self.malformed = True
            return
        if (not isinstance(event, dict) or not isinstance(event.get('Package'), str)
                or not event['Package'] or event.get('Action') not in
                    ('start', 'run', 'pause', 'cont', 'pass', 'bench', 'fail', 'output', 'skip',
                     'build-output', 'build-fail')
                or ('Test' in event and not isinstance(event['Test'], str))
                or ('Output' in event and not isinstance(event['Output'], str))):
            self.malformed = True
            return
        super().feed(line)
        key = (event['Package'], event.get('Test'))
        if event['Action'] == 'skip' and key[1]:
            self.qualified_skips.add(key)
        if event['Action'] in ('fail', 'build-fail'):
            self.qualified_failures.add(key)
        if event['Action'] == 'output' and not key[1] and '[no test files]' in event.get('Output', ''):
            self.no_test_packages.add(key[0])
        if event.get('Package') == 'github.com/SAP/astonish/pkg/daemon' and event.get('Test') == 'TestBotEnhancedChatNoPythonDocker':
            if 'BOT_NO_PYTHON_REGRESSION:' in event.get('Output', ''):
                self.diagnosed = True
            if event.get('Action') == 'fail':
                self.finished_failure = True


def consolidation_exact_errors(evidence, package, required):
    package_id = 'github.com/SAP/astonish/' + package.removeprefix('./')
    issues = evidence.errors()
    issues += ['missing exact package/test run-pass: ' + package_id + '/' + name
               for name in required if (package_id, name) not in evidence.qualified_passes]
    issues += ['executed package/test did not finish: ' + '/'.join(key)
               for key in sorted(evidence.qualified_runs - evidence.qualified_passes)]
    if package_id not in evidence.packages_passed:
        issues.append('package did not finish with pass: ' + package_id)
    return issues


def consolidation_required_tests(tier, names):
    children = CONSOLIDATION_SUBTESTS | CONSOLIDATION_CONTROLLER_SUBTESTS.get(tier, {})
    return names + [name + '/' + child for name in names for child in children.get(name, [])]


# Approved acceptance dependencies. Exact-grant continuation of the same logical
# run is valid scheduled execution; no uninterrupted consequential run is required.
# Each dependency still requires the ENTIRE declared tier/package/subcase manifest.
CONSOLIDATION_REQUIREMENT_TIERS = {
    'three-mode-capability-parity': ('runtime-construction', 'controller-daemon', 'controller-personal-mailbox', 'runtime-captured-drill-tools', 'runtime-prepared-plan'),
    'runtime-authority-matrix': ('runtime-live-authority', 'runtime-construction', 'controller-store', 'controller-daemon', 'controller-api', 'runtime-prepared-plan'),
    'runtime-lazy-session-lifecycle': ('controller-docker-allmode', 'controller-docker-delegate', 'controller-kubernetes-home'),
    'runtime-production-wiring': ('runtime-http', 'runtime-construction', 'runtime-template-wiring', 'controller-daemon', 'controller-personal-mailbox', 'runtime-trusted-snapshot', 'runtime-prepared-plan'),
    'controller-all-mode-no-python': ('controller-docker-allmode', 'controller-docker-delegate', 'controller-kubernetes-home'),
    'controller-team-physical': ('controller-docker', 'controller-docker-allmode', 'controller-kubernetes-home'),
    'controller-cold-cross-backend': ('runtime-kubernetes-bootstrap', 'controller-store', 'storage-docker-backup-cli', 'controller-docker-allmode', 'controller-kubernetes-home'),
    'controller-authority-matrix': ('runtime-live-authority', 'runtime-construction', 'controller-store', 'controller-daemon', 'controller-api', 'runtime-prepared-plan'),
    'storage-live-kubernetes': ('controller-kubernetes-home',),
    'storage-offline-backup-restore': ('runtime-kubernetes-bootstrap', 'controller-store', 'storage-docker-backup-cli', 'controller-kubernetes-home'),
    'storage-ordinary-chat-tool-parity': ('controller-docker-allmode', 'controller-daemon', 'controller-kubernetes-home'),
    'storage-fences-and-transport-matrix': ('storage-compiled-linux', 'storage-kubernetes-adapter-fixture', 'controller-kubernetes-home'),
    'storage-cancel-stream-lifecycle': ('storage-compiled-linux', 'storage-docker', 'storage-kubernetes-adapter-fixture', 'controller-kubernetes-home'),
    'storage-durable-data-matrix': ('controller-store', 'storage-docker', 'controller-kubernetes-home'),
    'storage-complete-compiled-implementation': ('storage-docker', 'storage-docker-backup-cli', 'controller-kubernetes-home'),
    'events-full-presentation-matrix': ('events-http', 'events-projection', 'events-production-capture'),
    'events-linked-session-restore': ('events-http', 'events-projection', 'events-production-capture'),
    'clients-all-mode-conversation': ('clients-browser', 'clients-pty', 'events-http', 'events-projection'),
    'clients-shared-visual-parity': ('clients-browser', 'clients-pty', 'clients-terminal', 'events-http', 'events-projection'),
}


# Source-defined Kubernetes scenarios and public cold restore are wired below.
# Mandatory safety helpers are NONPHYSICAL; dedicated inputs and fresh live
# exact receipts remain necessary to discharge the physical scenarios.
CONSOLIDATION_REQUIREMENT_UNRESOLVED = {}
# Scenario -> (tier, actual parent). Children are taken from the exact existing
# manifests, never invented scope/mode records. Full tier completion remains
# mandatory in addition to these explicit approved-scenario associations.
CONSOLIDATION_REQUIREMENT_SCENARIOS = {
    'events-full-presentation-matrix': {
        'mixed-live-history-reconnect': ('events-http', 'TestBotPresentationMatrixHTTPAcceptance'),
        'mixed-home-live-history-reconnect': ('events-http', 'TestBotHomePresentationMatrixHTTPAcceptance'),
        'canonical-ordered-projection': ('events-projection', 'TestSessionEventProjectorOrderedParity'),
        'ordinary-adk-home-file-activity': ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKActivity'),
        'ordinary-adk-home-delegation': ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKDelegation'),
        'home-telemetry-classification': ('events-projection', 'TestBotHomeNarrativeHistoricalTelemetry'),
        'production-runtime-capture': ('events-production-capture', 'TestBotRuntimeProductionCapture'),
    },
    'events-linked-session-restore': {
        'linked-home-and-run-public-restore': ('events-http', 'TestBotLinkedSessionRestoreProjectionHTTPAcceptance'),
        'ordinary-adk-home-file-activity': ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKActivity'),
        'ordinary-adk-home-delegation': ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKDelegation'),
    },
    'clients-all-mode-conversation': {
        'title-question-typed-answer-ordered': ('clients-browser', 'TestBotChatFirstOrderedConversationBrowserAcceptance'),
        'scheduled-run-announcement-delivery': ('clients-browser', 'TestBotScheduledWorkspaceBrowserAcceptance'),
        'reviewed-preview-exact-download': ('clients-browser', 'TestBotConversationPresentationBrowserAcceptance'),
        'ordinary-source-conversation-http': ('events-http', 'TestBotChatFirstConversationHTTPAcceptance'),
        'direct-client-state-matrix': ('clients-browser', 'TestBotClientStatesSharedVisualBrowserAcceptance'),
        'mixed-built-studio': ('clients-browser', 'TestBotConversationPresentationBrowserAcceptance'),
        'mixed-native-pty': ('clients-pty', 'TestBotConversationPresentationPTYAcceptance'),
    },
    'clients-shared-visual-parity': {
        'direct-ordinary-bot-live-restore-comparison': ('clients-browser', 'TestBotClientStatesSharedVisualBrowserAcceptance'),
        'ordinary-adk-home-file-activity': ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKActivity'),
        'ordinary-adk-home-delegation': ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKDelegation'),
        'linked-ordinary-session-projection': ('events-http', 'TestBotLinkedSessionRestoreProjectionHTTPAcceptance'),
        'mixed-native-pty': ('clients-pty', 'TestBotConversationPresentationPTYAcceptance'),
        'mixed-built-studio': ('clients-browser', 'TestBotConversationPresentationBrowserAcceptance'),
        'canonical-terminal': ('clients-terminal', 'TestBotTranscriptSharedRendering'),
        'canonical-projection': ('events-projection', 'TestSessionEventProjectorOrderedParity'),
    },
    'three-mode-capability-parity': {
        'configured-three-mode-tools': ('runtime-construction', 'TestChatRuntimeCapabilityParityConfiguredServices'),
        'configured-protocols': ('runtime-construction', 'TestChatRuntimeCapabilityParityConfiguredProtocols'),
        'configured-process-file': ('runtime-construction', 'TestChatRuntimeConfiguredProcessAndFileExecution'),
        'configured-browser': ('runtime-construction', 'TestChatRuntimeBrowserArtifactModeParity'),
        'scheduled-exact-service-resume': ('controller-daemon', 'TestBotConfiguredServiceAdmissionHTTP'),
        'scheduled-exact-protocol-resume': ('controller-daemon', 'TestBotConfiguredProtocolAdmissionHTTP'),
        'personal-mailbox-persistence': ('controller-personal-mailbox', 'TestPersonalSettingsBotEmailRoundTripScopeAndClear'),
        'autonomous-plan-verifier-results': ('runtime-prepared-plan', 'TestBotPreparedPlanAnnouncementResume'),
    },
    'runtime-authority-matrix': {
        'fresh-discovery-authority': ('runtime-live-authority', 'TestLiveAuthorityBuildAndBindFreshMembership'),
        'lease-transitions': ('runtime-construction', 'TestChatRuntimePersistedLeaseTransitions'),
        'cumulative-budget': ('runtime-construction', 'TestChatRuntimeCumulativeBudgetContinuity'),
        'restricted-child': ('runtime-construction', 'TestChatRuntimeRestrictedChildAuthority'),
        'cancel-join': ('runtime-construction', 'TestChatRuntimeChildCancellationJoinedReturn'),
        'exact-owner-plan-consent': ('controller-daemon', 'TestBotOwnerPlanConsentHTTP'),
        'crash-no-replay': ('controller-daemon', 'TestBotUncertainEffectCrashHTTPNoReplay'),
        'independent-reconciliation': ('controller-store', 'TestBotApprovalCrashRecoveryEvidence'),
        'current-exact-resume': ('controller-api', 'TestBotChatExactApprovalContinuation'),
    },
    'runtime-lazy-session-lifecycle': {
        'docker-all-mode-retained': ('controller-docker-allmode', 'TestBotAllModeRetainedSessionDocker'),
        'docker-delegate-retained': ('controller-docker-delegate', 'TestBotDelegatedRetainedSessionDocker'),
        'kubernetes-all-mode-retained': ('controller-kubernetes-home', 'TestBotsLiveKubernetesAllModeRetainedLifecycle'),
        'kubernetes-delegate-retained': ('controller-kubernetes-home', 'TestBotsLiveKubernetesNoPythonScheduledContinuationDelegate'),
    },
    'runtime-production-wiring': {
        'ordinary-http-shared-runtime': ('runtime-http', 'TestStudioChatHTTPScopedRuntimeFactory'),
        'production-template-dispatch': ('runtime-template-wiring', 'TestProductionBotTemplateWiringScopedDispatch'),
        'configured-http-services': ('controller-daemon', 'TestBotConfiguredServiceAdmissionHTTP'),
        'borrowed-resource-cleanup': ('controller-daemon', 'TestProductionBotWorkerBorrowsChatResources'),
        'owner-consent-verifier-results': ('controller-daemon', 'TestBotOwnerPlanConsentHTTP'),
        'personal-mailbox-persistence': ('controller-personal-mailbox', 'TestPersonalSettingsBotEmailRoundTripScopeAndClear'),
    },
    'controller-all-mode-no-python': {
        'docker-all-mode-no-python': ('controller-docker-allmode', 'TestBotAllModeRetainedSessionDocker'),
        'docker-delegate-no-python': ('controller-docker-delegate', 'TestBotDelegatedRetainedSessionDocker'),
        'kubernetes-all-mode-no-python': ('controller-kubernetes-home', 'TestBotsLiveKubernetesAllModeRetainedLifecycle'),
        'kubernetes-delegate-no-python': ('controller-kubernetes-home', 'TestBotsLiveKubernetesNoPythonScheduledContinuationDelegate'),
    },
    'controller-team-physical': {
        'docker-team-home': ('controller-docker', 'TestBotEnhancedChatTeamNoPythonDocker'),
        'docker-team-download': ('controller-docker-allmode', 'TestBotAllModeRetainedSessionDocker'),
        'kubernetes-team-download': ('controller-kubernetes-home', 'TestBotsLiveKubernetesTeamLifecycleExactDownloads'),
    },
    'controller-cold-cross-backend': {
        'docker-cold-backup-restore': ('storage-docker-backup-cli', 'TestBotFileFirstStoppedBackupCLI'),
        'docker-cold-http': ('controller-docker-allmode', 'TestBotAllModeRetainedSessionDocker'),
        'kubernetes-cold-backup-restore': ('controller-kubernetes-home', 'TestBotsLiveKubernetesStoppedColdBrowseBackupRestore'),
    },
}
CONSOLIDATION_REQUIREMENT_SCENARIOS.update({
    'storage-offline-backup-restore': {
        'no-model-bootstrap': ('runtime-kubernetes-bootstrap', 'TestRetainedHomeNoModelPreflight'),
        'files-before-sql': ('controller-store', 'TestBackupFileFirstRestoreFilesBeforeSQL'),
        'sqlite-concurrent-restore-arbitration': ('controller-store', 'TestBackupFileFirstSQLiteConcurrentRestoreArbitration'),
        'paused-independent-ownership': ('controller-store', 'TestBackupFileFirstMappingIndependentOwnership'),
        'stopped-public-cli': ('storage-docker-backup-cli', 'TestBotFileFirstStoppedBackupCLI'),
    },
    'storage-ordinary-chat-tool-parity': {
        'all-mode-retained-shared-tools': ('controller-docker-allmode', 'TestBotAllModeRetainedSessionDocker'),
        'shared-file-worker': ('controller-daemon', 'TestBotFileFirstWorkerHTTP'),
    },
    'storage-fences-and-transport-matrix': {
        'compiled-protocol': ('storage-compiled-linux', 'TestCompiledHomeFilesProtocol'),
        'scoped-descriptor-roots': ('storage-compiled-linux', 'TestCompiledHomeFilesScopedRoots'),
        'commit-stream-fences': ('storage-compiled-linux', 'TestCompiledHomeFilesFenceAndStreamErrors'),
        'file-crash-independent-writer': ('storage-compiled-linux', 'TestCompiledHomeFilesCrashRecoveryPreservesIndependentWriter'),
        'file-special-stage-denial': ('storage-compiled-linux', 'TestCompiledHomeFilesRecoveryRejectsSpecialStages'),
        'private-recovery-before-quota': ('storage-compiled-linux', 'TestCompiledHomeFilesPrivateRecoveryBeforeQuota'),
        'workspace-crash-no-replay': ('storage-compiled-linux', 'TestCompiledWorkspaceCrashRecovery'),
        'workspace-revision-read-preservation': ('storage-compiled-linux', 'TestCompiledWorkspaceRecoveryPreservesRevisionsAndReads'),
        'workspace-special-stage-denial': ('storage-compiled-linux', 'TestCompiledWorkspaceRecoveryRejectsSpecialStages'),
        'workspace-recovery-before-quota': ('storage-compiled-linux', 'TestCompiledWorkspaceRecoveryBeforeQuota'),
        'workspace-nested-writer': ('storage-compiled-linux', 'TestCompiledWorkspaceRecoveryNestedWriter'),
        'workspace-active-writer': ('storage-compiled-linux', 'TestCompiledWorkspaceRecoveryPreservesActiveWriter'),
        'adapter-filesystem': ('storage-kubernetes-adapter-fixture', 'TestBotHomeFilesFilesystem'),
        'adapter-confinement': ('storage-kubernetes-adapter-fixture', 'TestBotHomeFilesConfinement'),
    },
    'storage-cancel-stream-lifecycle': {
        'file-residual-stage-confinement': ('storage-compiled-linux', 'TestCompiledHomeFilesResidualStages'),
        'file-cancellation-process': ('storage-compiled-linux', 'TestCompiledHomeFilesCancellationProcess'),
        'workspace-stage-cancellation': ('storage-compiled-linux', 'TestCompiledWorkspaceStageCancellationAndRead'),
        'joined-request-writer': ('storage-kubernetes-adapter-fixture', 'TestCompiledWorkspaceJoinsRequestWriter'),
        'warm-no-helper': ('storage-kubernetes-adapter-fixture', 'TestCompiledWorkspaceWarmNoHelper'),
        'docker-stream-archive': ('storage-docker', 'TestBotHomeStreamArchiveDockerLive'),
        'docker-writer-contention': ('storage-docker', 'TestBotHomeArchivePrivateWriterContentionDockerLive'),
    },
    'storage-durable-data-matrix': {
        'restore-guards': ('controller-store', 'TestBackupFileFirstRestoreGuards'),
        'sqlite-storage-lock-order': ('controller-store', 'TestBotSQLiteSQLStorageLockOrderNoSessionReentry'),
        'sqlite-concurrent-restore-arbitration': ('controller-store', 'TestBackupFileFirstSQLiteConcurrentRestoreArbitration'),
        'restored-bytes': ('controller-store', 'TestBackupFileFirstRestore'),
        'opaque-content-authority': ('controller-store', 'TestBackupFileFirstMappingRunAuthorityAndOpaqueContent'),
        'compiled-docker-no-python': ('storage-docker', 'TestCompiledHomeFilesNoPythonDocker'),
    },
    'storage-complete-compiled-implementation': {
        'compiled-docker-no-python': ('storage-docker', 'TestCompiledHomeFilesNoPythonDocker'),
        'compiled-archive': ('storage-docker', 'TestBotHomeCompiledArchiveDockerLive'),
        'cold-public-cli': ('storage-docker-backup-cli', 'TestBotFileFirstStoppedBackupCLI'),
    },
})
# Live cross-backend scenarios remain mandatory in strict mode for every storage
# matrix. The explicit local profile records each as DEFERRED/NOT PASS instead.
for key in CONSOLIDATION_REMAINING['storage']:
    if key in CONSOLIDATION_REQUIREMENT_SCENARIOS:
        CONSOLIDATION_REQUIREMENT_SCENARIOS[key]['live-retained-kubernetes'] = (
            'controller-kubernetes-home', 'TestBotsLiveKubernetesRetainedHome')
CONSOLIDATION_REQUIREMENT_SCENARIOS['controller-authority-matrix'] = dict(
    CONSOLIDATION_REQUIREMENT_SCENARIOS['runtime-authority-matrix'])


def consolidation_manifests():
    return {'runtime': CONSOLIDATION_RUNTIME, 'storage': CONSOLIDATION_STORAGE,
            'controller': CONSOLIDATION_CONTROLLER, 'events': CONSOLIDATION_EVENTS,
            'clients': CONSOLIDATION_CLIENTS}


def consolidation_requirement_evidence(phases, gates, requirements=None, defer_kubernetes_postgresql=False):
    """Account only fresh exact required tiers; never accept report labels alone.

    requirements is an internal test seam, not a CLI/imported receipt override.
    Unwired scenarios stay explicit; wired requirements are evidence-derived.
    """
    manifests = {tier: value for manifest in consolidation_manifests().values()
                 for tier, value in manifest.items()}
    if requirements is None:
        requirements = {phase: {key: {'tiers': CONSOLIDATION_REQUIREMENT_TIERS.get(key, ()),
                                      'unresolved': CONSOLIDATION_REQUIREMENT_UNRESOLVED.get(
                                          key, [] if key in CONSOLIDATION_REQUIREMENT_SCENARIOS else [reason])}
                                for key, reason in CONSOLIDATION_REMAINING[phase].items()}
                        for phase in phases}
        if defer_kubernetes_postgresql and 'storage' in requirements:
            requirements['storage']['storage-live-kubernetes']['unresolved'] = []
    results = []
    for phase in phases:
        declared = requirements.get(phase, {})
        # Omitting an ID from a map must not erase its obligation.
        for key in sorted(set(CONSOLIDATION_REMAINING.get(phase, {})) | set(declared)):
            spec = declared.get(key, {})
            errors = list(spec.get('unresolved', []))
            tiers = spec.get('tiers', ())
            if key not in CONSOLIDATION_REMAINING.get(phase, {}):
                errors.append('unknown requirement ID')
            if not tiers:
                errors.append('no complete required-tier mapping')
            dependencies = []
            tier_evidence = {}
            for tier in tiers:
                if defer_kubernetes_postgresql and tier in DEFERRED_INFRA_TIERS:
                    dependencies.append(deferred_infra_record(tier=tier))
                    continue
                matches = [gate for gate in gates if gate.get('tier') == tier]
                issues = []
                if tier not in manifests:
                    issues.append('unknown required tier')
                elif len(matches) != 1:
                    issues.append('required tier missing or duplicated')
                else:
                    package, names = manifests[tier]
                    required = consolidation_required_tests(tier, names)
                    gate = matches[0]
                    if gate.get('package') != package or gate.get('required') != required:
                        issues.append('gate does not match full declared package/subcase manifest')
                    if (gate.get('status') != 'PASS' or gate.get('fresh') is not True
                            or gate.get('prerequisites_verified') is not True or gate.get('errors')
                            or not gate.get('log') or not gate.get('command')):
                        issues.append('fresh successful gate and verified prerequisites required')
                    issues.extend('observed gate: ' + str(issue) for issue in gate.get('errors', []))
                    command = consolidation_command(tier, package, names)
                    if gate.get('command') != command or not Path(gate.get('log') or '').is_file():
                        issues.append('exact harness command and owned evidence log required')
                    else:
                        try:
                            with Path(gate['log']).open() as log:
                                evidence = ConsolidationEvidence(required, io.StringIO(), {})
                                for line in log:
                                    evidence.feed(line)
                            issues.extend(consolidation_exact_errors(evidence, package, required))
                            tier_evidence[tier] = evidence
                        except (OSError, UnicodeError):
                            issues.append('owned evidence log unreadable')
                    runner = gate.get('runner')
                    if runner is not None:
                        try:
                            if (tier not in LINUX_FIXTURE_TIERS or runner.get('kind') != 'disposable-linux-container'
                                    or not re.fullmatch(r'astonish-e2e-files-[0-9a-f]{32}', runner.get('container', ''))
                                    or not re.fullmatch(r'sha256:[0-9a-f]{64}', runner.get('image', ''))
                                    or runner.get('architecture') not in ('amd64', 'arm64')
                                    or runner.get('execution_command') != consolidation_linux_fixture_command(package, names, runner)
                                    or runner.get('create_command') != consolidation_linux_fixture_create_command(names, runner)
                                    or runner.get('copy_commands') != ([['docker', 'cp', runner['test_binary'], runner['container'] + ':/fixtures/tests']]
                                        + ([['docker', 'cp', runner['node_binary'], runner['container'] + ':/fixtures/node']] if runner.get('node_binary') else []))
                                    or runner.get('build_command') != ['go', 'test', '-c', '-p', '1', '-o', runner['test_binary'], package]
                                    or Path(runner['test_binary']).parent != Path(gate['log']).parent
                                    or hashlib.sha256(Path(runner['test_binary']).read_bytes()).hexdigest() != runner.get('test_sha256')):
                                issues.append('exact owned Linux fixture build/transport evidence required')
                            if tier == 'storage-kubernetes-adapter-fixture' and (
                                    not Path(runner.get('node_binary') or '').is_absolute()
                                    or hashlib.sha256(Path(runner['node_binary']).read_bytes()).hexdigest() != runner.get('node_sha256')):
                                issues.append('exact fresh Linux node binary evidence required')
                        except (OSError, KeyError, TypeError, ValueError):
                            issues.append('owned Linux fixture transport evidence unreadable')
                    if not set(required).issubset(gate.get('passed', [])):
                        issues.append('missing exact required subcase evidence')
                dependencies.append({'tier': tier, 'status': 'BLOCKED' if issues else 'PASS', 'errors': issues})
                errors.extend(tier + ': ' + issue for issue in issues)
            scenarios = []
            for scenario, (tier, parent) in CONSOLIDATION_REQUIREMENT_SCENARIOS.get(key, {}).items():
                if defer_kubernetes_postgresql and tier in DEFERRED_INFRA_TIERS:
                    scenarios.append(deferred_infra_record(scenario=scenario, tier=tier,
                        required=consolidation_required_tests(tier, [parent]), exact_evidence_status='NOT PASS'))
                    continue
                dependency = next((d for d in dependencies if d['tier'] == tier), None)
                package, names = manifests.get(tier, ('', []))
                required = consolidation_required_tests(tier, [parent])
                issues = []
                if parent not in names or dependency is None:
                    issues.append('approved scenario missing from full required-tier mapping: ' + tier + '/' + parent)
                elif dependency['errors']:
                    issues.extend(dependency['errors'])
                evidence = tier_evidence.get(tier)
                scenario_errors = (consolidation_exact_errors(evidence, package, required)
                                   if evidence is not None else ['exact scenario log unavailable'])
                scenarios.append({'scenario': scenario, 'tier': tier, 'required': required,
                                  'status': 'BLOCKED' if issues else 'PASS', 'errors': issues,
                                  'exact_evidence_status': 'FAIL' if scenario_errors else 'PASS',
                                  'exact_evidence_errors': scenario_errors})
                errors.extend(scenario + ': ' + issue for issue in issues)
            only_deferred = (defer_kubernetes_postgresql and key == 'storage-live-kubernetes'
                             and not errors and tiers and all(tier in DEFERRED_INFRA_TIERS for tier in tiers))
            if only_deferred:
                results.append(deferred_infra_record(phase=phase, id=key,
                    required_tiers=dependencies, scenarios=scenarios, unresolved=[]))
                continue
            results.append({'phase': phase, 'id': key, 'status': 'BLOCKED' if errors else 'PASS',
                            'acceptance_profile': 'local-deferred-kubernetes-postgresql' if defer_kubernetes_postgresql else 'strict',
                            'physical_validation': 'DEFERRED' if any(d['status'] == 'DEFERRED' for d in dependencies) else 'required',
                            'backend_scope': 'Docker-only; Kubernetes/PostgreSQL DEFERRED, NOT PASS' if defer_kubernetes_postgresql and key == 'controller-cold-cross-backend' else 'declared tiers',
                            'reason': '; '.join(errors), 'required_tiers': dependencies,
                            'scenarios': scenarios,
                            'unresolved': list(spec.get('unresolved', []))})
    return results


RETAINED_HOME_K8S_ENV = {
    'KUBECONFIG', 'ASTONISH_E2E_DEDICATED', 'ASTONISH_E2E_BACKEND',
    'ASTONISH_E2E_SANDBOX_BACKEND', 'ASTONISH_E2E_SANDBOX_NAMESPACE',
    'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE', 'ASTONISH_E2E_KUBE_CONTEXT',
    'ASTONISH_E2E_RESOURCE_OWNER', 'ASTONISH_SANDBOX_IMAGE', 'ASTONISH_E2E_HOMES_PVC',
    'ASTONISH_TEST_DSN', 'ASTONISH_E2E_POSTGRES_INSTANCE',
}


def consolidation_command(tier, package, names):
    command = go_command(package, names, 'e2e' if tier in
                         ('controller-kubernetes-home', 'runtime-kubernetes-bootstrap') else None)
    if tier == 'controller-kubernetes-home':
        command[command.index('-timeout=15m')] = '-timeout=20m'
    return command


LINUX_FIXTURE_TIERS = frozenset({'storage-compiled-linux', 'storage-kubernetes-adapter-fixture'})


def consolidation_linux_fixture_prerequisites(source, tier, env):
    """An explicit local Linux image, never an ambient sandbox or a cluster."""
    issues = []
    if source.get('ASTONISH_TEST_DOCKER') != '1':
        issues.append('ASTONISH_TEST_DOCKER=1 required for disposable Linux fixture runner')
    image = source.get('ASTONISH_BOT_FILES_TEST_IMAGE', '')
    if not image.strip():
        issues.append('ASTONISH_BOT_FILES_TEST_IMAGE required: existing test-owned Linux image; no pull/build')
    if tier == 'storage-compiled-linux' and source.get('ASTONISH_TEST_COMPILED_HOME_FILES') != '1':
        issues.append('ASTONISH_TEST_COMPILED_HOME_FILES=1 required for opt-in root protocol fixtures')
    binary = source.get('ASTONISH_BOT_FILES_TEST_BINARY', '')
    if tier == 'storage-kubernetes-adapter-fixture':
        if (not Path(binary).is_absolute() or not Path(binary).is_file()
                or not os.access(binary, os.X_OK) or Path(binary).is_symlink()):
            issues.append('absolute executable ASTONISH_BOT_FILES_TEST_BINARY required: freshly built Linux node binary')
    for tool in ('go', 'docker'):
        if not shutil.which(tool, path=env.get('PATH', '')):
            issues.append(tool + ' required on PATH for Linux fixture runner')
    if issues:
        return None, issues
    try:
        result = subprocess.run(['docker', 'image', 'inspect', image], cwd=ROOT,
                                env=env, capture_output=True, text=True, timeout=30)
        rows = json.loads(result.stdout) if result.returncode == 0 else []
        if len(rows) != 1 or rows[0].get('Os') != 'linux' or rows[0].get('Architecture') not in ('amd64', 'arm64'):
            return None, ['existing Linux amd64/arm64 fixture image required; inspect failed (no pull)']
        image_id = rows[0].get('Id', '')
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', image_id):
            return None, ['immutable local fixture image ID required']
        if tier == 'storage-kubernetes-adapter-fixture':
            with Path(binary).open('rb') as node:
                header = node.read(20)
            machine = int.from_bytes(header[18:20], 'little')
            if (header[:6] != b'\x7fELF\x02\x01'
                    or machine != {'amd64': 62, 'arm64': 183}[rows[0]['Architecture']]):
                return None, ['node binary must be a Linux ELF matching the fixture image architecture']
        return {'kind': 'disposable-linux-container', 'image': image_id,
                'architecture': rows[0]['Architecture'], 'node_binary': binary if tier == 'storage-kubernetes-adapter-fixture' else None}, []
    except (OSError, ValueError, TypeError, KeyError, subprocess.TimeoutExpired):
        return None, ['local Linux fixture image/node prerequisite probe failed']


def consolidation_linux_fixture_command(package, names, runner):
    """test2json qualifies the unmodified Go test executable with its actual package."""
    return ['go', 'tool', 'test2json', '-t', '-p',
            'github.com/SAP/astonish/' + package.removeprefix('./'),
            'docker', 'start', '--attach', runner['container']]


def consolidation_linux_fixture_create_command(names, runner):
    command = ['docker', 'create', '--pull=never', '--name', runner['container'],
               '--network=none', '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges',
               '--pids-limit=512', '--volume', '/fixtures', '--tmpfs', '/tmp:rw,nosuid,nodev,size=1g']
    if runner.get('node_binary'):
        command += ['--env', 'ASTONISH_BOT_FILES_TEST_BINARY=/fixtures/node']
        if 'TestK8sArchiveCompiledCLI' in names:
            command += ['--env', 'ASTONISH_BOT_ARCHIVE_TEST_BINARY=/fixtures/node']
    return command + ['--env', 'ASTONISH_TEST_COMPILED_HOME_FILES=1', '--entrypoint', '/fixtures/tests',
                      runner['image'], '-test.v=test2json', '-test.timeout=15m',
                      '-test.run=^(' + '|'.join(re.escape(name) for name in names) + ')$']


def consolidation_linux_fixture_run(tier, package, names, runner, env, directory, evidence):
    # Cross-compile on the host using its resolved module/toolchain cache. No host
    # source, credentials, Docker socket or retained layers are mounted in the child.
    runner.update(container='astonish-e2e-files-' + uuid.uuid4().hex,
                  test_binary=str(directory / (tier + '.test')))
    build_env = dict(env, GOOS='linux', GOARCH=runner['architecture'], CGO_ENABLED='0')
    runner['build_command'] = ['go', 'test', '-c', '-p', '1', '-o', runner['test_binary'], package]
    with (directory / (tier + '.build.log')).open('w') as build_log:
        class BuildLog:
            def errors(self):
                return []

            def feed(self, line):
                build_log.write(redact(line, build_env))
        if not run(runner['build_command'], build_env, BuildLog()):
            return False
    runner['test_sha256'] = hashlib.sha256(Path(runner['test_binary']).read_bytes()).hexdigest()
    if runner.get('node_binary'):
        # Retain the exact bytes streamed by Docker cp alongside the receipt.
        # A source-tree build can be replaced while other gates are running;
        # hashing it now but re-reading it later is not immutable evidence.
        node_snapshot = directory / (tier + '.node')
        with node_snapshot.open('xb') as snapshot:
            snapshot.write(Path(runner['node_binary']).read_bytes())
        node_snapshot.chmod(0o555)
        runner['node_binary'] = str(node_snapshot)
        runner['node_sha256'] = hashlib.sha256(node_snapshot.read_bytes()).hexdigest()
    runner['execution_command'] = consolidation_linux_fixture_command(package, names, runner)
    runner['create_command'] = consolidation_linux_fixture_create_command(names, runner)
    runner['copy_commands'] = [['docker', 'cp', runner['test_binary'], runner['container'] + ':/fixtures/tests']]
    if runner.get('node_binary'):
        runner['copy_commands'].append(['docker', 'cp', runner['node_binary'], runner['container'] + ':/fixtures/node'])
    try:
        # Docker cp streams over the selected transport and also works when the
        # daemon cannot bind the macOS host's private /var/folders temporary path.
        for setup in [runner['create_command']] + runner['copy_commands']:
            result = subprocess.run(setup, cwd=ROOT, env=env, capture_output=True, text=True, timeout=120)
            if result.returncode:
                raise OSError('test-owned Linux fixture create/copy failed: ' + redact(result.stderr[:2000], env))
        return run(runner['execution_command'], env, evidence)
    finally:
        # A killed Docker CLI does not imply remote termination. Remove ONLY this
        # invocation's unpredictable owned name; never enumerate or sweep containers.
        result = subprocess.run(['docker', 'rm', '-f', '-v', runner['container']], cwd=ROOT, env=env,
                                capture_output=True, text=True, timeout=30)
        if result.returncode and 'No such container' not in result.stderr:
            raise OSError('test-owned Linux fixture container cleanup failed')


def consolidation_kubernetes_prerequisites(source):
    # Only explicit dedicated fixture inputs; no providers, inspector attach, PG
    # routing variables, ambient kube context, GOFLAGS, or ordinary app settings.
    env = {k: v for k, v in file_first_env(source).items()
           if not k.startswith('ASTONISH_') and not k.startswith('DOCKER_')}
    env.update({k: v for k, v in source.items() if k in RETAINED_HOME_K8S_ENV})
    issues = []
    for key, expected in (('ASTONISH_E2E_DEDICATED', '1'), ('ASTONISH_E2E_BACKEND', 'postgres'),
                          ('ASTONISH_E2E_SANDBOX_BACKEND', 'k8s')):
        if env.get(key) != expected:
            issues.append(key + '=' + expected + ' required')
    if source.get('ASTONISH_E2E_KEEP_ALIVE'):
        issues.append('ASTONISH_E2E_KEEP_ALIVE forbidden; no inspector attachment')
    dedicated = r'(astonishe2e|astonish-e2e)-[a-z0-9]([-a-z0-9]*[a-z0-9])?'
    for key in ('ASTONISH_E2E_SANDBOX_NAMESPACE', 'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE',
                'ASTONISH_E2E_RESOURCE_OWNER'):
        value = env.get(key, '')
        if len(value) > 63 or not re.fullmatch(dedicated, value):
            issues.append(key + ' must name an explicit dedicated fixture')
    if env.get('ASTONISH_E2E_SANDBOX_NAMESPACE') == env.get('ASTONISH_E2E_CONTROL_PLANE_NAMESPACE'):
        issues.append('sandbox and control-plane namespaces must differ')
    config = env.get('KUBECONFIG', '')
    if (not Path(config).is_absolute() or not Path(config).is_file()
            or os.pathsep in config or str(Path(config)) != config or '..' in Path(config).parts):
        issues.append('KUBECONFIG must name one explicit absolute dedicated config file')
    if not env.get('ASTONISH_E2E_KUBE_CONTEXT', '').strip():
        issues.append('ASTONISH_E2E_KUBE_CONTEXT required; no implicit context')
    if not env.get('ASTONISH_SANDBOX_IMAGE', '').strip():
        issues.append('ASTONISH_SANDBOX_IMAGE required: test-owned source-built image; no pull/build')
    homes = env.get('ASTONISH_E2E_HOMES_PVC', '')
    if (len(homes) > 63 or not re.fullmatch(r'[a-z0-9]([-a-z0-9]*[a-z0-9])?', homes)
            or homes in ('astonish-layers', 'astonish-uppers')):
        issues.append('ASTONISH_E2E_HOMES_PVC must name an independent RWX claim')
    try:
        from urllib.parse import parse_qs
        dsn = urlsplit(env.get('ASTONISH_TEST_DSN', ''))
        query = parse_qs(dsn.query, keep_blank_values=True, strict_parsing=True)
        valid = (dsn.scheme in ('postgres', 'postgresql') and dsn.username and dsn.password
                 and dsn.hostname and dsn.port and dsn.path == '/postgres' and not dsn.fragment
                 and not any(c in dsn.hostname for c in ',/\\')
                 and query in ({'sslmode': [mode]} for mode in ('disable', 'require', 'verify-ca', 'verify-full')))
        endpoint = ('[' + dsn.hostname + ']' if ':' in (dsn.hostname or '') else dsn.hostname or '') + ':' + str(dsn.port or '')
    except ValueError:
        valid, endpoint = False, ''
    if not valid:
        issues.append('ASTONISH_TEST_DSN requires explicit PostgreSQL user/password/host/port, /postgres and sole non-fallback sslmode; no ambient routing')
    if not env.get('ASTONISH_E2E_POSTGRES_INSTANCE') or env.get('ASTONISH_E2E_POSTGRES_INSTANCE') != endpoint:
        issues.append('ASTONISH_E2E_POSTGRES_INSTANCE must match approved dedicated DSN host:port')
    for tool in ('go', 'kubectl'):
        if not shutil.which(tool, path=env.get('PATH', '')):
            issues.append(tool + ' required on PATH')
    return env, issues


def consolidation_kubernetes_probe(env):
    """Read only exact named resources. Never switch context or enumerate/sweep."""
    def capture(args):
        result = subprocess.run(['kubectl'] + args, env=env, cwd=ROOT,
                                capture_output=True, text=True, timeout=30)
        if result.returncode:
            raise OSError('dedicated Kubernetes read-only probe failed')
        return result.stdout.strip()
    context = env['ASTONISH_E2E_KUBE_CONTEXT']
    if capture(['config', 'current-context']) != context:
        return ['current-context must match ASTONISH_E2E_KUBE_CONTEXT; never switched']
    prefix = ['--context', context, '--request-timeout=15s']
    owner = env['ASTONISH_E2E_RESOURCE_OWNER']
    label = 'astonish.sap.com/e2e-owner'
    for key in ('ASTONISH_E2E_SANDBOX_NAMESPACE', 'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE'):
        ns = json.loads(capture(prefix + ['get', 'namespace', env[key], '-o', 'json']))
        metadata = ns.get('metadata', {})
        if (metadata.get('name') != env[key] or metadata.get('labels', {}).get(label) != owner
                or metadata.get('deletionTimestamp')):
            return ['dedicated namespace must be positively fixture-owned and not deleting']
    volumes = set()
    for claim in ('astonish-layers', 'astonish-uppers', env['ASTONISH_E2E_HOMES_PVC']):
        pvc = json.loads(capture(prefix + ['get', 'pvc', '-n', env['ASTONISH_E2E_SANDBOX_NAMESPACE'], claim, '-o', 'json']))
        metadata, spec = pvc.get('metadata', {}), pvc.get('spec', {})
        volume = spec.get('volumeName')
        if (metadata.get('name') != claim or metadata.get('namespace') != env['ASTONISH_E2E_SANDBOX_NAMESPACE']
                or metadata.get('labels', {}).get(label) != owner or metadata.get('deletionTimestamp')
                or pvc.get('status', {}).get('phase') != 'Bound' or 'ReadWriteMany' not in spec.get('accessModes', [])
                or not volume or volume in volumes):
            return ['dedicated claims must be positively fixture-owned, independently backed, Bound RWX and not deleting']
        volumes.add(volume)
    return []


def consolidation_progress_gate(tier, package, names, source, directory, defer_kubernetes_postgresql=False):
    required = consolidation_required_tests(tier, names)
    if defer_kubernetes_postgresql and tier in DEFERRED_INFRA_TIERS:
        return deferred_infra_record(tier=tier, package=package, required=required,
            boundary=CONSOLIDATION_BOUNDARIES[tier], command=consolidation_command(tier, package, names), log=None)
    env = file_first_env(source)
    docker_tier = tier in ('storage-docker', 'storage-docker-backup-cli', 'controller-docker', 'controller-docker-allmode', 'controller-docker-delegate')
    if not docker_tier:
        env = {k: v for k, v in env.items() if not k.startswith('ASTONISH_')}
    issues = []
    if tier == 'clients-browser':
        browser = source.get('ASTONISH_BOT_BROWSER_PATH', '')
        if source.get('ASTONISH_MEMORY_BROWSER_ACCEPTANCE') != '1':
            issues.append('ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 required')
        if not Path(browser).is_absolute() or not Path(browser).is_file() or not os.access(browser, os.X_OK):
            issues.append('absolute executable ASTONISH_BOT_BROWSER_PATH required')
        env['ASTONISH_MEMORY_BROWSER_ACCEPTANCE'] = source.get('ASTONISH_MEMORY_BROWSER_ACCEPTANCE', '')
        env['ASTONISH_BOT_BROWSER_PATH'] = browser
        try:
            with (ROOT / 'web/package.json').open() as package_file:
                version = json.load(package_file)['version']
            if not isinstance(version, str) or not version or Path(version).name != version:
                raise ValueError('invalid Studio build version')
            if not (ROOT / 'web/dist' / version / 'index.html').is_file():
                issues.append('built Studio web/dist/<package version>/index.html required; build Studio explicitly first')
        except (OSError, ValueError, KeyError, TypeError):
            issues.append('readable Studio package version and built index required')
    if docker_tier:
        if env.get('ASTONISH_TEST_DOCKER') != '1':
            issues.append('ASTONISH_TEST_DOCKER=1 required; no automatic Docker execution')
        if not env.get('ASTONISH_SANDBOX_IMAGE', '').strip():
            issues.append('ASTONISH_SANDBOX_IMAGE required: explicit test-owned source-built image; no pull/build')
        layers = Path(env.get('ASTONISH_BOT_HOME_TEST_LAYERS', ''))
        if not layers.is_absolute() or not (layers / '@base/rootfs/bin/sh').is_file():
            issues.append('ASTONISH_BOT_HOME_TEST_LAYERS must be absolute existing default layers')
    linux_runner = None
    if tier in LINUX_FIXTURE_TIERS and not sys.platform.startswith('linux'):
        linux_runner, runner_issues = consolidation_linux_fixture_prerequisites(source, tier, env)
        issues.extend(runner_issues)
    if tier == 'storage-compiled-linux' and sys.platform.startswith('linux'):
        if source.get('ASTONISH_TEST_COMPILED_HOME_FILES') != '1':
            issues.append('Linux and ASTONISH_TEST_COMPILED_HOME_FILES=1 required for opt-in root protocol fixtures')
        else:
            env['ASTONISH_TEST_COMPILED_HOME_FILES'] = '1'
    if tier == 'storage-kubernetes-adapter-fixture' and sys.platform.startswith('linux'):
        binary = source.get('ASTONISH_BOT_FILES_TEST_BINARY', '')
        if (not Path(binary).is_absolute()
                or not Path(binary).is_file() or not os.access(binary, os.X_OK)):
            issues.append('Linux and absolute executable ASTONISH_BOT_FILES_TEST_BINARY required; explicit freshly built node binary, no implicit build')
        else:
            env['ASTONISH_BOT_FILES_TEST_BINARY'] = binary
    kubernetes_tier = tier == 'controller-kubernetes-home'
    if kubernetes_tier:
        env, issues = consolidation_kubernetes_prerequisites(source)
        env['ASTONISH_E2E_SANDBOX_BACKEND'] = 'k8s'
    command = consolidation_command(tier, package, names)
    log_path = directory / (tier + '.jsonl')
    gate = {'tier': tier, 'package': package, 'required': required,
            'boundary': CONSOLIDATION_BOUNDARIES[tier], 'status': 'BLOCKED',
            'passed': [], 'errors': issues, 'command': command, 'log': None,
            'evidence_scope': 'implemented subset only', 'fresh': False,
            'prerequisites_verified': not issues}
    if issues:
        return gate
    if kubernetes_tier:
        helper_package, helper_names = CONSOLIDATION_RUNTIME['runtime-kubernetes-bootstrap']
        helper = consolidation_progress_gate('runtime-kubernetes-bootstrap', helper_package,
                                              helper_names, source, directory)
        gate['bootstrap_evidence'] = helper
        if helper['status'] != 'PASS':
            gate.update(errors=['mandatory exact no-model bootstrap tests did not pass'],
                        prerequisites_verified=False)
            return gate
        try:
            issues += consolidation_kubernetes_probe(env)
        except (OSError, TimeoutError, subprocess.TimeoutExpired, ValueError, TypeError, AttributeError):
            issues.append('dedicated Kubernetes read-only ownership probe failed')
        if issues:
            gate.update(errors=issues, prerequisites_verified=False)
            return gate
    gate['log'] = str(log_path)
    started = time.monotonic()
    with log_path.open('w') as log:
        evidence = ConsolidationEvidence(required, log, env)
        try:
            if linux_runner is not None:
                gate['runner'] = linux_runner
                ok = consolidation_linux_fixture_run(tier, package, names, linux_runner, env, directory, evidence)
            else:
                ok = run(command, env, evidence)
        except (OSError, TimeoutError, subprocess.TimeoutExpired) as error:
            ok = False
            issues.append(redact(str(error), env))
    issues += consolidation_exact_errors(evidence, package, required)
    if not ok:
        issues.append('test subprocess or evidence validation failed')
    package_id = 'github.com/SAP/astonish/' + package.removeprefix('./')
    gate.update(status='FAIL' if issues else 'PASS', errors=issues, fresh=True,
                passed=sorted(name for pkg, name in evidence.qualified_passes if pkg == package_id),
                elapsed_seconds=round(time.monotonic() - started, 3))
    return gate


def consolidation_progress(args, source, directory, report):
    if args.phase == 'all':
        # Fresh original baseline plus physical regression, never historical receipts.
        consolidation_baseline(source, directory, report)
        for gate in report['gates']:
            gate['phase'] = 'baseline'
            gate['tier'] = 'baseline-' + gate['package'].rsplit('/', 1)[-1]
        regression = report['regression']
        report['gates'].append(dict(regression, phase='baseline', tier='baseline-docker-regression',
            status=('PASS' if regression['status'] == 'FIXED' else
                    'BLOCKED' if regression['status'] == 'BLOCKED' else 'FAIL')))
    phases = ('runtime', 'storage', 'controller', 'events', 'clients') if args.phase == 'all' else (args.phase,)
    report['evidence_policy'] = 'fresh exact manifest evidence; unresolved full-journey requirements block acceptance'
    manifests = consolidation_manifests()
    owners = {tier: phase for phase, manifest in manifests.items() for tier in manifest}
    scheduled = set()
    for phase in phases:
        manifest = dict(manifests[phase])
        # Execute cross-phase dependencies freshly once, rather than requiring an
        # imported receipt or duplicating their execution in --phase all.
        for key in (CONSOLIDATION_REMAINING[phase] if phase in ('clients', 'storage') else ()):
            for tier in CONSOLIDATION_REQUIREMENT_TIERS.get(key, ()):
                owner = owners.get(tier)
                if owner is not None:
                    manifest.setdefault(tier, manifests[owner][tier])
        for tier, (package, names) in manifest.items():
            if tier in scheduled:
                continue
            scheduled.add(tier)
            gate = (consolidation_progress_gate(tier, package, names, source, directory, True)
                    if getattr(args, 'defer_kubernetes_postgresql', False)
                    else consolidation_progress_gate(tier, package, names, source, directory))
            gate['phase'] = owners[tier] if args.phase == 'all' else phase
            report['gates'].append(gate)
    requirements = (consolidation_requirement_evidence(phases, report['gates'], defer_kubernetes_postgresql=True)
                    if getattr(args, 'defer_kubernetes_postgresql', False)
                    else consolidation_requirement_evidence(phases, report['gates']))
    report['requirement_evidence'] = requirements
    report['deferred_requirements'] = [item for item in requirements if item['status'] == 'DEFERRED']
    report['remaining_requirements'] = [item for item in requirements if item['status'] not in ('PASS', 'DEFERRED')]
    report['phase_acceptance'] = {}
    for phase in (CONSOLIDATION_PHASES[:-1] if args.phase == 'all' else phases):
        phase_gates = [gate for gate in report['gates'] if gate.get('phase') == phase and gate['status'] != 'DEFERRED']
        blocked = any(item['phase'] == phase for item in report['remaining_requirements'])
        report['phase_acceptance'][phase] = ('FAIL' if any(gate['status'] == 'FAIL' for gate in phase_gates)
            else 'BLOCKED' if blocked or not phase_gates or any(gate['status'] != 'PASS' for gate in phase_gates)
            else 'PASS')
    report['unverified'] += [item['id'] + ': ' + item['reason'] for item in report['remaining_requirements']]
    # Dependency PASS comes from re-reading fresh exact logs and checking the full
    # declared command/manifest/prerequisites, not merely a gate's status label.
    verified_tiers = {dependency['tier'] for item in requirements
                      for dependency in item['required_tiers'] if dependency['status'] == 'PASS'}
    verified_clients = {label for label, tier in (('built browser', 'clients-browser'),
                                                ('native PTY', 'clients-pty')) if tier in verified_tiers}
    report['unverified'] = [item for item in report['unverified'] if item not in verified_clients]
    report['status'] = ('FAIL' if any(gate['status'] == 'FAIL' for gate in report['gates'])
        else 'PASS' if report['phase_acceptance'] and all(status == 'PASS' for status in report['phase_acceptance'].values())
        else 'BLOCKED')
    return {'PASS': 0, 'FAIL': 1, 'BLOCKED': 2}[report['status']]


def consolidation_docker_regression(source, directory):
    env = file_first_env(source)
    errors = []
    if env.get('ASTONISH_TEST_DOCKER') != '1':
        errors.append('ASTONISH_TEST_DOCKER=1 required for fresh physical regression')
    if not env.get('ASTONISH_SANDBOX_IMAGE'):
        errors.append('ASTONISH_SANDBOX_IMAGE required; no automatic pull')
    layers = Path(env.get('ASTONISH_BOT_HOME_TEST_LAYERS', ''))
    if not layers.is_absolute() or not (layers / '@base/rootfs/bin/sh').is_file():
        errors.append('absolute existing default layers required')
    if errors:
        return {'status': 'BLOCKED', 'errors': errors}
    name = 'TestBotEnhancedChatNoPythonDocker'
    log_path = directory / 'docker-regression.jsonl'
    with log_path.open('w') as log:
        evidence = ConsolidationEvidence([name], log, env)
        ok = run(go_command('./pkg/daemon', [name]), env, evidence)
    skipped = any(action == 'skip' for _, action in evidence.failed)
    reproduced = not ok and evidence.diagnosed and evidence.finished_failure and not skipped
    issues = consolidation_exact_errors(evidence, './pkg/daemon', [name])
    fixed = ok and not issues
    return {'status': 'REPRODUCED' if reproduced else ('FIXED' if fixed else 'FAIL'),
            'test': name, 'log': str(log_path),
            'errors': [] if fixed else issues,
            'meaning': 'REPRODUCED is historical failure evidence only; revised acceptance requires FIXED'}


def consolidation_baseline(source, directory, report):
    """Execute the original baseline freshly; never import historical receipts."""
    http_env = {k: v for k, v in file_first_env(source).items() if not k.startswith('ASTONISH_')}
    for package, names in CONSOLIDATION_BASELINE.items():
        log_path = directory / (package.rsplit('/', 1)[-1] + '.jsonl')
        command = go_command(package, names)
        with log_path.open('w') as log:
            evidence = ConsolidationEvidence(names, log, http_env)
            ok = run(command, http_env, evidence)
        package_id = 'github.com/SAP/astonish/' + package.removeprefix('./')
        issues = consolidation_exact_errors(evidence, package, names)
        if not ok:
            issues.append('test subprocess or evidence validation failed')
        report['gates'].append({'package': package, 'required': names,
            'boundary': 'constructor' if package.endswith('launcher') else 'isolated HTTP worker',
            'status': 'PASS' if not issues else 'FAIL', 'fresh': True, 'command': command,
            'passed': sorted(name for pkg, name in evidence.qualified_passes if pkg == package_id),
            'errors': issues, 'log': str(log_path)})
    report['regression'] = consolidation_docker_regression(source, directory)
    if any(gate['status'] == 'FAIL' for gate in report['gates']) or report['regression']['status'] in ('FAIL', 'REPRODUCED'):
        report['status'] = 'FAIL'
        return 1
    if report['regression']['status'] != 'FIXED':
        report['status'] = 'BLOCKED'
        return 2
    report['status'] = 'PASS'
    return 0


def chat_consolidation(args):
    source = dict(os.environ)
    env = file_first_env(source)
    # Controlled HTTP fixtures receive neither infrastructure flags nor provider secrets.
    http_env = {k: v for k, v in env.items() if not k.startswith('ASTONISH_')}
    directory = Path(tempfile.mkdtemp(prefix='astonish-chat-consolidation-'))
    path = args.report or directory / 'report.json'
    report = {'schema_version': 1, 'stage': 'chat-consolidation', 'phase': args.phase,
              'status': 'BLOCKED', 'gates': [], 'artifact_directory': str(directory),
              'unverified': ['paid-provider workflow', 'Kubernetes', 'PostgreSQL', 'built browser', 'native PTY']}
    report['acceptance_scope'] = acceptance_scope(args)
    if getattr(args, 'defer_kubernetes_postgresql', False):
        report['deferred_validation'] = [deferred_infra_record(capability=name) for name in ('Kubernetes', 'PostgreSQL')]
    started = time.monotonic()
    try:
        if args.phase in ('runtime', 'storage', 'controller', 'events', 'clients', 'all'):
            return consolidation_progress(args, source, directory, report)
        if args.phase != 'baseline':
            report['errors'] = ['phase acceptance not implemented yet: ' + args.phase]
            return 2
        return consolidation_baseline(source, directory, report)
    except (OSError, TimeoutError, subprocess.TimeoutExpired, KeyboardInterrupt) as error:
        report.update(status='FAIL', errors=[redact(str(error), env)])
        return 1
    finally:
        report['elapsed_seconds'] = round(time.monotonic() - started, 3)
        path.write_text(json.dumps(report, indent=2) + '\n')
        print('CHAT_CONSOLIDATION_REPORT ' + str(path), flush=True)
        if getattr(args, 'defer_kubernetes_postgresql', False):
            print('Kubernetes/PostgreSQL: DEFERRED (user-requested; NOT PASS; local profile only)', flush=True)
        print('chat-consolidation/' + args.phase + ': ' + report['status'], flush=True)


def completed_source_hashes():
    """Bind receipts to current source/config bytes, including untracked tests."""
    result = subprocess.run(['git', 'ls-files', '-co', '--exclude-standard', '-z'],
                            cwd=ROOT, check=True, stdout=subprocess.PIPE)
    paths = sorted(set(result.stdout.decode().split('\0')) - {''})
    suffixes = {'.go', '.py', '.ts', '.tsx', '.js', '.jsx', '.json', '.mod', '.sum', '.yaml', '.yml', '.md', '.html', '.css'}
    return {name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest() for name in paths
            if (ROOT / name).is_file() and 'web/dist/' not in name
            and (Path(name).suffix in suffixes or Path(name).name.startswith('Dockerfile'))}


def completed_storage_manifest():
    manifests = consolidation_manifests()
    result = dict(manifests['storage'])
    all_tiers = {tier: spec for manifest in manifests.values() for tier, spec in manifest.items()}
    for key in CONSOLIDATION_REMAINING['storage']:
        for tier in CONSOLIDATION_REQUIREMENT_TIERS.get(key, ()):
            if tier in all_tiers:
                result.setdefault(tier, all_tiers[tier])
    return result


def completed_filefirst_logs(report, transcript=None):
    """Old reports require the owned full stdout transcript AND zero parent exit.

    COMMAND boundaries must exactly match the current driver; labels are ignored.
    New reports use their direct owned JSONL instead.
    """
    sections = {}
    if transcript:
        command, lines, package_finished = None, [], False
        for line in Path(transcript).read_text().splitlines(keepends=True):
            if line.startswith('COMMAND '):
                if command is not None:
                    sections.setdefault(command, []).append(lines)
                command, lines, package_finished = tuple(shlex.split(line[len('COMMAND '):])), [], False
            elif command is not None:
                # Ignore only the driver's explicit wait marker. Keep every other
                # line until package completion, so malformed transport output
                # cannot disappear while extracting a Go command from stdout.
                if (line.lstrip().startswith(('{', '['))
                        or (not package_finished and not line.startswith('RUNNING (waiting for command output)'))):
                    lines.append(line)
                    try:
                        event = json.loads(line)
                        if isinstance(event, dict) and event.get('Action') == 'pass' and not event.get('Test'):
                            package_finished = True
                    except ValueError:
                        pass
        if command is not None:
            sections.setdefault(command, []).append(lines)
    logs = {}
    for gate in report['gates']:
        tier = gate['tier']
        if tier in DEFERRED_INFRA_TIERS:
            continue
        package, names = FILE_FIRST_GATES[tier]
        command = go_command(package, names)
        if gate.get('log'):
            if gate.get('command') != command:
                raise ValueError('file-first command mismatch: ' + tier)
            logs[tier] = Path(gate['log']).read_text().splitlines(keepends=True)
        else:
            matches = sections.get(tuple(command), [])
            if len(matches) != 1:
                raise ValueError('exact owned file-first transcript command missing/duplicated: ' + tier)
            logs[tier] = matches[0]
    return logs


def completed_phase6_check(storage_path, filefirst_path, transcript=None):
    storage = json.loads(Path(storage_path).read_text())
    filefirst = json.loads(Path(filefirst_path).read_text())
    for report, stage in ((storage, 'chat-consolidation'), (filefirst, 'file-first')):
        if report.get('stage') != stage or report.get('errors'):
            raise ValueError('wrong stage or report error: ' + stage)
        if report.get('acceptance_scope') != acceptance_scope(type('Args', (), {'defer_kubernetes_postgresql': True})()):
            raise ValueError('exact explicit deferred-local profile required')
    if storage.get('phase') != 'storage':
        raise ValueError('storage phase required')
    manifest = completed_storage_manifest()
    if sorted(g['tier'] for g in storage['gates']) != sorted(manifest):
        raise ValueError('full current storage manifest missing/duplicated')
    artifacts = {str(Path(storage_path).resolve()), str(Path(filefirst_path).resolve())}
    if transcript:
        artifacts.add(str(Path(transcript).resolve()))
    for gate in storage['gates']:
        tier = gate['tier']
        if tier in DEFERRED_INFRA_TIERS:
            if gate.get('status') != 'DEFERRED' or gate.get('reason') != DEFERRED_INFRA_REASON:
                raise ValueError('live tier must remain explicit NOT PASS')
            continue
        package, names = manifest[tier]
        required = consolidation_required_tests(tier, names)
        if (gate.get('package') != package or gate.get('required') != required
                or gate.get('command') != consolidation_command(tier, package, names)
                or gate.get('fresh') is not True or gate.get('prerequisites_verified') is not True
                or gate.get('errors') or not gate.get('log')):
            raise ValueError('invalid storage gate: ' + tier)
        artifacts.add(str(Path(gate['log']).resolve()))
        evidence = ConsolidationEvidence(required, io.StringIO(), {})
        for line in Path(gate['log']).read_text().splitlines(keepends=True):
            evidence.feed(line)
        issues = consolidation_exact_errors(evidence, package, required)
        if issues:
            raise ValueError(tier + ': ' + '; '.join(issues))
        runner = gate.get('runner')
        if runner:
            artifacts.update(str(Path(runner[key]).resolve()) for key in ('test_binary', 'node_binary') if runner.get(key))
    # Existing requirement validator also checks exact Linux transport/build,
    # retained binary hashes, full scenario children and prerequisite receipts.
    requirements = consolidation_requirement_evidence(('storage',), storage['gates'],
                                                       defer_kubernetes_postgresql=True)
    if not requirements or any(row['status'] not in ('PASS', 'DEFERRED') for row in requirements):
        raise ValueError('current storage requirement validation failed: ' + json.dumps(requirements))
    if sorted(g['tier'] for g in filefirst['gates']) != sorted(FILE_FIRST_GATES):
        raise ValueError('full current file-first manifest missing/duplicated')
    logs = completed_filefirst_logs(filefirst, transcript)
    for gate in filefirst['gates']:
        tier = gate['tier']
        if tier in DEFERRED_INFRA_TIERS:
            if gate.get('status') != 'DEFERRED' or gate.get('reason') != DEFERRED_INFRA_REASON:
                raise ValueError('file-first live tier must remain explicit NOT PASS')
            continue
        package, names = FILE_FIRST_GATES[tier]
        required = names + [name + '/' + scope for name in names if name in FILE_FIRST_SCOPED for scope in ('personal', 'team')]
        if gate.get('package') != package or gate.get('required') != required or gate.get('errors'):
            raise ValueError('invalid full file-first gate: ' + tier)
        if gate.get('log'):
            artifacts.add(str(Path(gate['log']).resolve()))
            if gate.get('log_sha256') != hashlib.sha256(Path(gate['log']).read_bytes()).hexdigest():
                raise ValueError('file-first JSONL hash mismatch')
        evidence = ConsolidationEvidence(required, io.StringIO(), {})
        for line in logs[tier]:
            evidence.feed(line)
        issues = consolidation_exact_errors(evidence, package, required)
        if issues:
            raise ValueError(tier + ': ' + '; '.join(issues))
    return {path: hashlib.sha256(Path(path).read_bytes()).hexdigest() for path in sorted(artifacts)}


def completed_final_quality_commands():
    packages = ['./pkg/chatruntime/...', './pkg/launcher/...', './pkg/api/...', './pkg/bot/...',
                './pkg/sandbox/...', './pkg/store/...', './pkg/tui/...', './pkg/client/...',
                './pkg/daemon/...', './pkg/tools/...', './pkg/browser/...', './cmd/astonish/...']
    return {
        'python': ['python3', '-B', '-m', 'unittest', 'discover', '-s', 'tests/e2eboot', '-p', '*acceptance*test.py'],
        'typecheck': ['npm', '--prefix', 'web', 'run', 'typecheck'],
        'build': ['npm', '--prefix', 'web', 'run', 'build'],
        'frontend': ['npm', '--prefix', 'web', 'test'],
        'go': ['go', 'test', '-json', '-p', '2', '-timeout=15m'] + packages,
        'lint': ['golangci-lint', 'run'] + packages,
        'diff': ['git', 'diff', '--check'],
    }


def completed_exact_log(path, package, required):
    evidence = ConsolidationEvidence(required, io.StringIO(), {})
    with Path(path).open() as log:
        for line in log:
            evidence.feed(line)
    issues = consolidation_exact_errors(evidence, package, required)
    if issues:
        raise ValueError(str(path) + ': ' + '; '.join(issues))
    return evidence


def completed_final_check(baseline_path, all_path, quality_path, coverage_reports=()):
    """Recheck actual complete runs; labels alone never discharge a gate."""
    artifacts = {str(Path(path).resolve()) for path in (baseline_path, all_path, quality_path)}
    scope = acceptance_scope(type('Args', (), {'defer_kubernetes_postgresql': True})())
    def regression(gate):
        if gate.get('test') != 'TestBotEnhancedChatNoPythonDocker' or gate.get('errors') or not gate.get('log'):
            raise ValueError('exact physical baseline regression missing')
        completed_exact_log(gate['log'], './pkg/daemon', [gate['test']])
        artifacts.add(str(Path(gate['log']).resolve()))
    def baseline_gate(gate, package, names):
        if (gate.get('package') != package or gate.get('required') != names
                or gate.get('command') != go_command(package, names) or gate.get('fresh') is not True
                or gate.get('errors') or not gate.get('log')):
            raise ValueError('exact current baseline gate required')
        completed_exact_log(gate['log'], package, names)
        artifacts.add(str(Path(gate['log']).resolve()))
    baseline = json.loads(Path(baseline_path).read_text())
    full = json.loads(Path(all_path).read_text())
    for report, phase in ((baseline, 'baseline'), (full, 'all')):
        if (report.get('stage') != 'chat-consolidation' or report.get('phase') != phase
                or report.get('acceptance_scope') != scope or report.get('errors')):
            raise ValueError('exact current ' + phase + ' report/profile required')
    if sorted(g.get('package', '') for g in baseline['gates']) != sorted(CONSOLIDATION_BASELINE):
        raise ValueError('full baseline manifest missing/duplicated')
    for gate in baseline['gates']:
        baseline_gate(gate, gate['package'], CONSOLIDATION_BASELINE[gate['package']])
    regression(baseline['regression'])
    manifests = consolidation_manifests()
    manifest = {tier: spec for entries in manifests.values() for tier, spec in entries.items()}
    baseline_tiers = {'baseline-' + package.rsplit('/', 1)[-1]: (package, names)
                      for package, names in CONSOLIDATION_BASELINE.items()}
    expected = set(manifest) | set(baseline_tiers) | {'baseline-docker-regression'}
    if sorted(g['tier'] for g in full['gates']) != sorted(expected):
        raise ValueError('full current all manifest missing/duplicated')
    for gate in full['gates']:
        tier = gate['tier']
        if tier in baseline_tiers:
            baseline_gate(gate, *baseline_tiers[tier])
            continue
        if tier == 'baseline-docker-regression':
            regression(gate)
            continue
        if tier in DEFERRED_INFRA_TIERS:
            if gate.get('status') != 'DEFERRED' or gate.get('reason') != DEFERRED_INFRA_REASON:
                raise ValueError('live tier must remain explicit DEFERRED NOT PASS')
            continue
        package, names = manifest[tier]
        required = consolidation_required_tests(tier, names)
        if (gate.get('package') != package or gate.get('required') != required
                or gate.get('command') != consolidation_command(tier, package, names)
                or gate.get('fresh') is not True or gate.get('prerequisites_verified') is not True
                or gate.get('errors') or not gate.get('log')):
            raise ValueError('invalid full all gate: ' + tier)
        completed_exact_log(gate['log'], package, required)
        artifacts.add(str(Path(gate['log']).resolve()))
        runner = gate.get('runner')
        if runner:
            artifacts.update(str(Path(runner[key]).resolve()) for key in ('test_binary', 'node_binary') if runner.get(key))
    phases = ('runtime', 'storage', 'controller', 'events', 'clients')
    requirements = consolidation_requirement_evidence(phases, full['gates'], defer_kubernetes_postgresql=True)
    if (not requirements or any(row['status'] not in ('PASS', 'DEFERRED') for row in requirements)
            or full.get('requirement_evidence') != requirements or full.get('remaining_requirements') != []):
        raise ValueError('current complete all requirement/scenario validation failed')
    artifacts.update(completed_quality_check(quality_path, (all_path,) + tuple(coverage_reports)))
    return {path: hashlib.sha256(Path(path).read_bytes()).hexdigest() for path in sorted(artifacts)}


# Exact optional diagnostics outside this consolidation's controlled fixtures.
# Never infer exemptions from a package, a name prefix, or a skip message.
QUALITY_OPTIONAL_DIAGNOSTICS = {
    ('github.com/SAP/astonish/pkg/api', name): reason
    for reason, names in (
        ('paid-provider replay prohibited by user', (
            'TestBotCapturedConfiguredReviewerLive', 'TestBotRealProgressBrowserAcceptance',
            'TestBotRealSidecarBrowserAcceptance', 'TestBotRealStockHTTPAcceptance',
            'TestBotRealStockTitleLegacyLiveHTTPDiagnostic', 'TestBotRealStockTitleLiveHTTPDiagnostic',
            'TestBotWorkCitationModelOnlyReproduction', 'TestBotWorkFormatRecoveryLive',
            'TestBotWorkHomeModelOnlyReproduction', 'TestBotWorkModelOnlyReproduction',
            'TestDiagnosticRealStockReview')),
        ('private historical capture diagnostic; controlled capturedProxy/exactworkflow remain mandatory', (
            'TestBotCapturedArtifactReviewCoverage', 'TestBotCapturedCompletionEligibility',
            'TestBotCapturedCompletionReviewEntry', 'TestBotContentCitationDiagnosticRecordedFailure',
            'TestBotRealProgressBrowserEvidence', 'TestBotRealProgressBrowserPreflight',
            'TestBotRealProgressCapturedExecution', 'TestBotRealSidecarBrowserPreflight',
            'TestBotRealSidecarCapturedAssessment', 'TestBotRealStockTitleLiveHTTPEvidence',
            'TestLiveProgressInspectRetainedBrowser')),
    ) for name in names
}
QUALITY_OPTIONAL_DIAGNOSTICS.update({
    ('github.com/SAP/astonish/pkg/api', 'TestBotGenerationHealthHTTP130Seconds'):
        'optional 130-second diagnostic; canonical bounded generation/interruption/reconnect gates remain mandatory',
    ('github.com/SAP/astonish/pkg/daemon', 'TestBotExactRealProviderDockerReproduction'):
        'paid real-provider replay prohibited; controlled exactworkflow is mandatory',
    ('github.com/SAP/astonish/pkg/daemon', 'TestBotGenuineCorrectedDockerPublication'):
        'paid reviewer prohibited; controlled publication/exact bytes are mandatory',
    ('github.com/SAP/astonish/pkg/daemon', 'TestBotInitialAnswerDockerContinuationReproduction'):
        'private real-provider diagnostic; controlled startup/continuation remain mandatory',
    ('github.com/SAP/astonish/pkg/daemon', 'TestBotCapturedCompletionDockerRecovery'):
        'private historical captured event diagnostic; controlled capturedProxy remains mandatory',
    ('github.com/SAP/astonish/pkg/launcher', 'TestResumeSession_RealPoisonedSession'):
        'unavailable private historical Code session diagnostic; ordinary Code tests remain mandatory',
    ('github.com/SAP/astonish/pkg/sandbox/docker', 'TestProductionBotIODiagnostic'):
        'optional private production IO diagnostic; owned persistence/performance gates remain mandatory',
    ('github.com/SAP/astonish/pkg/browser', 'TestResolveCDPURL_DirectFallback'):
        'unchanged diagnostic intentionally times out against random non-9222 test port; not live browser evidence',
    ('github.com/SAP/astonish/pkg/tools', 'TestRunFlow_ExecutionError'):
        'unchanged unconditional placeholder diagnostic; configured actual flow execution gate remains mandatory',
})
# Packages without source tests are structural, not skipped-test acceptance.
QUALITY_NO_TEST_PACKAGES = frozenset({
    'github.com/SAP/astonish/pkg/sandbox/openshell/gen/openshellv1',
    'github.com/SAP/astonish/pkg/sandbox/tmplmeta',
    'github.com/SAP/astonish/pkg/store/pgutil',
    'github.com/SAP/astonish/pkg/tui/backend',
})


def completed_quality_skip_errors(evidence, coverage):
    """Reconcile exact supplemental RUN/PASS; never mutate/promote broad results."""
    if evidence.malformed or evidence.qualified_failures or not evidence.qualified_runs:
        raise ValueError('broad Go quality malformed/failed/empty')
    if not {pkg for pkg, _ in evidence.qualified_runs}.issubset(evidence.packages_passed):
        raise ValueError('broad Go quality package termination missing')
    package_skips = {name for name, action in evidence.failed if action == 'skip'} - {
        name for _, name in evidence.qualified_skips}
    if not package_skips.issubset(evidence.no_test_packages & QUALITY_NO_TEST_PACKAGES):
        raise ValueError('unknown skipped package: ' + repr(sorted(package_skips)))
    incomplete = evidence.qualified_runs - evidence.qualified_passes - evidence.qualified_skips
    missing = incomplete | {key for key in evidence.qualified_skips
                            if key not in coverage and key not in QUALITY_OPTIONAL_DIAGNOSTICS}
    if missing:
        raise ValueError('broad Go quality raw JSONL exact mandatory skip coverage missing: ' + '; '.join(
            pkg + '::' + name for pkg, name in sorted(missing)))
    return {pkg + '::' + name: ('exact supplemental RUN/PASS: ' + coverage[(pkg, name)]
            if (pkg, name) in coverage else 'NOT PASS: ' + QUALITY_OPTIONAL_DIAGNOSTICS[(pkg, name)])
            for pkg, name in sorted(evidence.qualified_skips)}


def completed_quality_coverage(reports, artifacts):
    """Only fresh exact command/raw-log gates may discharge broad skips."""
    coverage = {}
    manifests = {tier: spec for entries in consolidation_manifests().values() for tier, spec in entries.items()}
    scopes = dict(FILE_FIRST_GATES)
    scopes.update(manifests)
    scopes.update({'baseline-' + p.rsplit('/', 1)[-1]: (p, names) for p, names in CONSOLIDATION_BASELINE.items()})
    for report_path in reports:
        report = json.loads(Path(report_path).read_text())
        artifacts.add(str(Path(report_path).resolve()))
        gates = list(report.get('gates', []))
        if report.get('stage') == 'chat-consolidation' and report.get('phase') == 'baseline':
            gates = [dict(g, tier='baseline-' + g['package'].rsplit('/', 1)[-1]) for g in gates]
            if report.get('regression'):
                gates.append(dict(report['regression'], tier='baseline-docker-regression'))
        for gate in gates:
            if gate.get('status') == 'DEFERRED':
                if gate.get('tier') not in DEFERRED_INFRA_TIERS or gate.get('reason') != DEFERRED_INFRA_REASON:
                    raise ValueError('unknown deferred supplemental tier')
                continue
            # The physical baseline regression uses its own proven exact log.
            if gate.get('tier') == 'baseline-docker-regression':
                package, required = './pkg/daemon', [gate['test']]
                if gate.get('test') != 'TestBotEnhancedChatNoPythonDocker' or gate.get('errors'):
                    raise ValueError('failed/unknown supplemental baseline regression')
            else:
                package, required = gate.get('package'), gate.get('required')
                parents = list(dict.fromkeys(n.split('/')[0] for n in (required or [])))
                expected = (consolidation_command(gate['tier'], package, parents)
                            if gate.get('tier') in {tier for entries in consolidation_manifests().values() for tier in entries}
                            else go_command(package, parents))
                if gate.get('tier') in scopes:
                    scoped_package, scoped_names = scopes[gate['tier']]
                    if package != scoped_package or set(parents) != set(scoped_names):
                        raise ValueError('supplemental tier package/test scope mismatch')
                elif (report.get('schema_version') != 'skip-coverage-v1'
                        or not set(parents).issubset(QUALITY_LOCAL_EXTRA_TESTS.get(package, ()))):
                    raise ValueError('unknown supplemental scope/test')
                if (gate.get('status') != 'PASS' or gate.get('fresh') is not True
                        or gate.get('errors') or not required or gate.get('command') != expected):
                    raise ValueError('fresh exact supplemental command required: ' + str(gate.get('tier')))
            runner = gate.get('runner')
            if report.get('schema_version') == 'skip-coverage-v1':
                if gate.get('prerequisites_verified') is not True:
                    raise ValueError('extra prerequisites must be verified')
                if package == './pkg/sandbox/k8s' and set(required) != {'TestBotHomePurgeFilesystem'} and not runner:
                    raise ValueError('local Kubernetes filesystem fixtures require owned Linux runner')
            if gate.get('log_sha256') and hashlib.sha256(Path(gate['log']).read_bytes()).hexdigest() != gate['log_sha256']:
                raise ValueError('supplemental raw log digest mismatch')
            if runner:
                if (runner.get('kind') != 'disposable-linux-container'
                        or not re.fullmatch(r'astonish-e2e-files-[0-9a-f]{32}', runner.get('container', ''))
                        or not re.fullmatch(r'sha256:[0-9a-f]{64}', runner.get('image', ''))
                        or runner.get('architecture') not in ('amd64', 'arm64')
                        or runner.get('execution_command') != consolidation_linux_fixture_command(package, parents, runner)
                        or runner.get('create_command') != consolidation_linux_fixture_create_command(parents, runner)
                        or runner.get('build_command') != ['go', 'test', '-c', '-p', '1', '-o', runner['test_binary'], package]
                        or hashlib.sha256(Path(runner['test_binary']).read_bytes()).hexdigest() != runner.get('test_sha256')
                        or (runner.get('node_binary') and hashlib.sha256(Path(runner['node_binary']).read_bytes()).hexdigest() != runner.get('node_sha256'))):
                    raise ValueError('exact immutable owned Linux supplemental transport required')
                artifacts.update(str(Path(runner[k]).resolve()) for k in ('test_binary', 'node_binary') if runner.get(k))
            evidence = completed_exact_log(gate['log'], package, required)
            artifacts.add(str(Path(gate['log']).resolve()))
            coverage.update({key: str(Path(gate['log']).resolve()) for key in evidence.qualified_passes})
    return coverage


def completed_skip_inventory(quality_path, reports):
    artifacts = set()
    coverage = completed_quality_coverage(reports, artifacts)
    quality = json.loads(Path(quality_path).read_text())
    if quality.get('schema_version') == 'quality-delta-v1':
        quality = json.loads(Path(quality['base_quality']).read_text())
    gate = next(g for g in quality['gates'] if g['id'] == 'go')
    if gate.get('exit_code') != 0 or gate.get('log_sha256') != hashlib.sha256(Path(gate['log']).read_bytes()).hexdigest():
        raise ValueError('successful immutable broad quality log required')
    evidence = ConsolidationEvidence([], io.StringIO(), {})
    for line in Path(gate['log']).read_text().splitlines():
        evidence.feed(line + '\n')
    if evidence.malformed or evidence.qualified_failures:
        raise ValueError('failed/malformed broad quality cannot be reconciled')
    missing = evidence.qualified_skips - set(coverage) - set(QUALITY_OPTIONAL_DIAGNOSTICS)
    return evidence, coverage, sorted(missing)


# Exact executable extra-fixture allowlist; unknown skips block, never execute
# an arbitrary newly added/private test just because it occurred in broad quality.
QUALITY_LOCAL_EXTRA_TESTS = {
    './pkg/api': frozenset('''TestBostonQuestionCleanBrowserAcceptance
TestBotAgentWorkBrowserAcceptance TestBotAmbiguousTitleQuestionBrowserAcceptance
TestBotBlockedExplanationBrowserAcceptance TestBotBudgetStopBrowserAcceptance
TestBotChatFirstWorkspaceBrowserAcceptance TestBotCheckpointFailureBrowserAcceptance
TestBotComposerRevisionBrowserAcceptance TestBotDetailsWithoutSkillsBrowserAcceptance
TestBotEmployeeBrowserAcceptance TestBotEmployeeEscalationBrowserAcceptance
TestBotGenerationHealthBrowser TestBotGenericRuntimeFailureBrowserAcceptance
TestBotPermanentDeleteBrowserAcceptance TestBotProgressBrowserAcceptance
TestBotResponsibilityConfirmedOnlyBrowserAcceptance TestBotScheduleConfirmedOnlyBrowserAcceptance
TestBotScheduledNewRunProcessDockerAcceptance TestBotSkillsBrowserAcceptance
TestBotTaskWorkspaceHTTPAcceptance TestBotThinkingRestoreBrowserAcceptance
TestBotTitleChatBrowserAcceptance TestBotTitleConfirmationDockerBrowserAcceptance
TestBotTitleTeamCustomBrowserAcceptance TestBotTitleTeamOptionBrowserAcceptance
TestBotWorkQualityBrowserAcceptance TestBotWorkspaceBrowserAcceptance
TestChatChronologyHTTPAcceptance_ActionableBoundary TestChatChronologyHTTPAcceptance_Baseline
TestChatChronologyHTTPAcceptance_Browser TestChatChronologyHTTPAcceptance_EdgeCases
TestChatDelegationRegressionBrowser TestChatGenerationProgressBrowser
TestDelegationHandoffBrowserAcceptance TestFileToolAPILiveDeliveryRestore
TestLiveProgressBrowserTimeoutTransition TestLiveProgressRenderedMermaidComparison
TestMemoryHTTPAcceptance_Browser TestPlanRevisionBrowserAcceptance'''.split()),
    './pkg/browser': frozenset({'TestLocalBrowserReuseAcceptance'}),
    './pkg/daemon': frozenset({'TestBotKnowledgeDockerEveryTurn',
        'TestBotPermanentDeleteAttachedDockerAcceptance', 'TestBotTaskScheduleHTTPAcceptance'}),
    './pkg/launcher': frozenset({'TestBotConfiguredSearchPhysicalMCP',
        'TestBotTaskWorkspaceArchiveDocker', 'TestBotTaskWorkspaceDockerIntegration', 'TestCodeBrowserLoopbackAcceptance'}),
    './pkg/sandbox/docker': frozenset('''TestBotHomeAdmissionDockerLive TestBotHomeFilesDockerLive
TestBotHomeFilesMissingDockerLive TestBotHomeHelperInspectionDockerLive TestBotHomeWorkspace
TestBotHomeWorkspaceScratchPinnedInput TestBotKnowledgeDockerPhysical TestBotSessionFilesDockerLive
TestCompiledHomeFilesStaleAttachmentDockerLive TestCompiledKnowledgeNodeDockerNoPython
TestCompiledScriptNodeDockerNoPython TestCompiledWorkspaceNodeDocker
TestCompiledWorkspaceNodeDockerNestedCommitReads TestDetachedCompiledHomeFilesDockerLive
TestDockerPersistenceAcceptance TestHealth_LiveDocker TestHomeFileTransactionHelperDockerLive'''.split()),
    './pkg/sandbox/k8s': frozenset('''TestBotHomeContentReadbackAdapter TestBotHomeFilesCancelBlockedUpload
TestBotHomeFilesMissingAndUnsafe TestBotHomeFilesPrivateProspectiveQuota TestBotHomeFilesPrivateSharedLock
TestBotHomeFilesRejectForgedContentMetadata TestBotHomeFilesRootIdentity TestBotHomeOfflineReadFilesystem
TestBotKnowledgeCompiledFilesystem TestK8sArchiveCompiledCLI TestBotHomePurgeFilesystem'''.split()),
}


def completed_collect_skip_coverage(quality_path, reports, output_path, selected=None):
    """Bounded owned extra gates only; no providers, daemon or existing Bot replay."""
    evidence, coverage, missing = completed_skip_inventory(quality_path, reports)
    grouped = {}
    for package, name in missing:
        parent = name.split('/')[0]
        identifier = package.removeprefix('github.com/SAP/astonish/').replace('/', '-')
        if package.endswith('/k8s'):
            identifier += '-purge' if parent == 'TestBotHomePurgeFilesystem' else '-linux'
        grouped.setdefault(identifier, ( './' + package.removeprefix('github.com/SAP/astonish/'), set()))[1].add(parent)
    print('MISSING_EXACT_COVERAGE', json.dumps([p + '::' + n for p, n in missing]), flush=True)
    print('EXTRA_GATES', json.dumps({key: go_command(package, sorted(names)) for key, (package, names) in grouped.items()}), flush=True)
    if selected is None:
        return
    if selected not in grouped:
        raise ValueError('unknown or already covered extra gate: ' + selected)
    output_path = Path(output_path).resolve()
    if output_path.exists():
        raise ValueError('extra output must be new')
    directory = Path(tempfile.mkdtemp(prefix='astonish-skip-coverage-'))
    package, parents = grouped[selected]
    parents = sorted(parents)
    if not set(parents).issubset(QUALITY_LOCAL_EXTRA_TESTS.get(package, ())):
        raise ValueError('unknown extra tests require explicit scope review, never automatic execution')
    required = sorted({n for pkg, n in missing if './' + pkg.removeprefix('github.com/SAP/astonish/') == package
                       and n.split('/')[0] in parents} | set(parents))
    source = dict(os.environ)
    env = file_first_env(source)
    # No ambient paid/provider routing. The explicit local gates use controlled fixtures.
    env = {k: v for k, v in env.items() if not k.startswith('ASTONISH_')}
    env.update({k: source[k] for k in ('ASTONISH_TEST_DOCKER', 'ASTONISH_SANDBOX_IMAGE',
        'ASTONISH_BOT_HOME_TEST_LAYERS', 'ASTONISH_BOT_HOME_TEST_TEMPLATE',
        'ASTONISH_BOT_FILES_TEST_BINARY', 'ASTONISH_TEST_COMPILED_HOME_FILES',
        'ASTONISH_TEST_TASK_CONTINUITY', 'ASTONISH_BOT_BROWSER_PATH') if k in source})
    env['ASTONISH_TEST_TASK_WORKSPACE'] = '1'
    env['ASTONISH_TEST_TASK_EXECUTION'] = '1'
    env['ASTONISH_TEST_CONFIGURED_SEARCH'] = '1'
    env['ASTONISH_TEST_SEARCH_NODE'] = source.get('ASTONISH_BOT_FILES_TEST_BINARY', '')
    env['ASTONISH_CODE_BROWSER_PATH'] = source.get('ASTONISH_BOT_BROWSER_PATH', '')
    env['ASTONISH_TEST_WORKSPACE_BINARY'] = source.get('ASTONISH_BOT_FILES_TEST_BINARY', '')
    env['ASTONISH_BOT_ARCHIVE_TEST_BINARY'] = source.get('ASTONISH_BOT_FILES_TEST_BINARY', '')
    for key in ('ASTONISH_MEMORY_BROWSER_ACCEPTANCE', 'ASTONISH_STREAM_BROWSER_ACCEPTANCE',
                'ASTONISH_BOT_DELETE_BROWSER_ACCEPTANCE', 'ASTONISH_LOCAL_BROWSER_ACCEPTANCE',
                'ASTONISH_CODE_BROWSER_ACCEPTANCE', 'ASTONISH_FILE_TOOL_API_LIVE',
                'ASTONISH_BOT_BROWSER_ACCEPTANCE', 'ASTONISH_BROWSER_CONTEXT_TEST'):
        env[key] = '1'
    if source.get('ASTONISH_TEST_DOCKER') != '1' or not source.get('ASTONISH_SANDBOX_IMAGE'):
        raise ValueError('explicit owned Docker/image inputs required')
    browser = Path(source.get('ASTONISH_BOT_BROWSER_PATH', ''))
    if not browser.is_absolute() or not browser.is_file() or not os.access(browser, os.X_OK):
        raise ValueError('explicit local browser executable required')
    command = go_command(package, parents)
    runner = None
    if selected.endswith('-linux'):
        runner, errors = consolidation_linux_fixture_prerequisites(source, 'storage-kubernetes-adapter-fixture', env)
        if errors:
            raise ValueError('; '.join(errors))
    if selected.endswith('-purge'):
        # Test-owned empty disposable containers only, no retained or host mounts.
        env['ASTONISH_BOT_HOME_PURGE_TEST_IMAGE'] = source['ASTONISH_SANDBOX_IMAGE']
        env['ASTONISH_BOT_HOME_PURGE_MOUNT_TEST'] = '1'
    log_path = directory / (selected + '.jsonl')
    gate = dict(tier=selected, package=package, required=required, command=command,
                fresh=True, prerequisites_verified=True, log=str(log_path), errors=[], status='FAIL')
    with log_path.open('w') as log:
        observed = ConsolidationEvidence(required, log, env)
        if runner:
            gate['runner'] = runner
            ok = consolidation_linux_fixture_run('storage-kubernetes-adapter-fixture', package, parents,
                                                 runner, env, directory, observed)
        else:
            ok = run(command, env, observed)
    gate['errors'] = consolidation_exact_errors(observed, package, required)
    gate['passed'] = sorted(n for pkg, n in observed.qualified_passes)
    gate['status'] = 'PASS' if ok and not gate['errors'] else 'FAIL'
    output_path.write_text(json.dumps(dict(schema_version='skip-coverage-v1', gates=[gate]), indent=2) + '\n')
    if gate['status'] != 'PASS':
        raise ValueError('extra gate did not pass (including every leaf): ' + str(output_path))


def completed_quality_base_check(quality, artifacts, coverage=None):
    commands = completed_final_quality_commands()
    for path in (ROOT / 'web/dist').rglob('*'):
        if path.is_file():
            artifacts.add(str(path.resolve()))
    if sorted(g['id'] for g in quality['gates']) != sorted(commands):
        raise ValueError('full quality manifest missing/duplicated')
    for gate in quality['gates']:
        if (gate.get('command') != commands[gate['id']] or gate.get('exit_code') != 0
                or gate.get('cwd') != str(ROOT) or not gate.get('log')
                or gate.get('log_sha256') != hashlib.sha256(Path(gate['log']).read_bytes()).hexdigest()):
            raise ValueError('actual exact successful quality command/hash required: ' + gate['id'])
        artifacts.add(str(Path(gate['log']).resolve()))
        if gate['id'] == 'go':
            evidence = ConsolidationEvidence([], io.StringIO(), {})
            with Path(gate['log']).open() as log:
                for line in log:
                    evidence.feed(line)
            completed_quality_skip_errors(evidence, coverage or {})


# Narrow reconciliation policy, not a snapshot exclusion. These files still
# participate in every hash; additions/deletions and all other drift are blocked.
QUALITY_DELTA_FILES = {
    'cmd/astonish/code_theme_catalog_pty_test.go',
    'docs/architecture/terminal-app.md',
    'tests/e2eboot/bots_acceptance_driver.py',
    'tests/e2eboot/bots_acceptance_driver_test.py',
}


def completed_quality_delta_changes(before, after):
    changed = sorted(name for name in set(before) | set(after) if before.get(name) != after.get(name))
    if (not changed or not set(changed).issubset(QUALITY_DELTA_FILES)
            or any(name not in before or name not in after for name in changed)):
        raise ValueError('quality snapshot delta contains unknown source/addition/deletion or is empty: ' + repr(changed))
    return [dict(path=name, before=before[name], after=after[name]) for name in changed]


def completed_quality_delta_commands(changes):
    paths = {row['path'] for row in changes}
    commands = {}
    if 'cmd/astonish/code_theme_catalog_pty_test.go' in paths:
        commands['go-cmd'] = ['go', 'test', '-json', '-count=1', '-p', '2', '-timeout=15m', './cmd/astonish/...']
        commands['lint-cmd'] = ['golangci-lint', 'run', './cmd/astonish/...']
    if any(path.startswith('tests/e2eboot/') for path in paths):
        commands['python'] = completed_final_quality_commands()['python']
    # Markdown has no executable consumers; whitespace validation is its only
    # applicable original gate. All executable changes have their full replay.
    commands['diff'] = completed_final_quality_commands()['diff']
    return commands


def completed_quality_go_evidence(path):
    evidence = ConsolidationEvidence([], io.StringIO(), {})
    with Path(path).open() as log:
        for line in log:
            evidence.feed(line)
    if (evidence.errors() or not evidence.qualified_runs
            or evidence.qualified_runs != evidence.qualified_passes
            or not {pkg for pkg, _ in evidence.qualified_runs}.issubset(evidence.packages_passed)):
        raise ValueError('full affected package replay incomplete/failed/skipped')
    return evidence


def completed_quality_check(quality_path, coverage_reports=()):
    quality_path = Path(quality_path).resolve()
    quality = json.loads(quality_path.read_text())
    artifacts = {str(quality_path)}
    current = completed_source_hashes()
    coverage = completed_quality_coverage(coverage_reports, artifacts)
    if quality.get('schema_version') != 'quality-delta-v1':
        if quality.get('source_hashes') != current:
            raise ValueError('quality source snapshot missing/stale')
        completed_quality_base_check(quality, artifacts, coverage)
    else:
        base_path = Path(quality['base_quality']).resolve()
        if quality.get('base_sha256') != hashlib.sha256(base_path.read_bytes()).hexdigest():
            raise ValueError('original quality receipt digest mismatch')
        base = json.loads(base_path.read_text())
        if base.get('schema_version') == 'quality-delta-v1':
            raise ValueError('nested quality delta forbidden')
        completed_quality_base_check(base, artifacts, coverage)
        artifacts.add(str(base_path))
        if quality.get('source_hashes') != current or quality.get('base_source_hashes') != base.get('source_hashes'):
            raise ValueError('quality delta source snapshot missing/stale')
        changes = completed_quality_delta_changes(base['source_hashes'], current)
        if quality.get('changes') != changes:
            raise ValueError('quality delta exact changed file/hash list mismatch')
        commands = completed_quality_delta_commands(changes)
        if sorted(g['id'] for g in quality['gates']) != sorted(commands):
            raise ValueError('full affected package replay manifest missing/duplicated')
        for gate in quality['gates']:
            if (gate.get('command') != commands[gate['id']] or gate.get('exit_code') != 0
                    or gate.get('cwd') != str(ROOT) or not gate.get('log')
                    or gate.get('log_sha256') != hashlib.sha256(Path(gate['log']).read_bytes()).hexdigest()):
                raise ValueError('actual successful delta replay command/hash required: ' + gate['id'])
            artifacts.add(str(Path(gate['log']).resolve()))
            if gate['id'] == 'go-cmd':
                replay = completed_quality_go_evidence(gate['log'])
                original = completed_quality_go_evidence(next(g['log'] for g in base['gates'] if g['id'] == 'go'))
                package = 'github.com/SAP/astonish/cmd/astonish'
                prior = {key for key in original.qualified_runs if key[0] == package or key[0].startswith(package + '/')}
                if (not prior or not prior.issubset(replay.qualified_passes)
                        or package not in replay.packages_passed
                        or any(pkg != package and not pkg.startswith(package + '/') for pkg in replay.packages_passed)):
                    raise ValueError('affected package replay lost original test coverage or wrong package')
    # Catch writes during validation as well as during collection.
    if completed_source_hashes() != current:
        raise ValueError('source changed during quality validation')
    return artifacts


def completed_quality_delta_collect(base_path, output_path):
    """Run actual scoped gates, retaining original receipt and every raw log."""
    base_path, output_path = Path(base_path).resolve(), Path(output_path).resolve()
    base_digest = hashlib.sha256(base_path.read_bytes()).hexdigest()
    base = json.loads(base_path.read_text())
    if base.get('schema_version') == 'quality-delta-v1':
        raise ValueError('nested quality delta forbidden')
    completed_quality_base_check(base, set())
    current = completed_source_hashes()
    changes = completed_quality_delta_changes(base['source_hashes'], current)
    gates = []
    if output_path.exists():
        raise ValueError('delta output must be new')
    for identifier, command in completed_quality_delta_commands(changes).items():
        log_path = output_path.with_name(output_path.name + '.' + identifier + '.log')
        print('QUALITY_DELTA_COMMAND', json.dumps(command), flush=True)
        with log_path.open('xb') as log:
            result = subprocess.run(command, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
        gates.append(dict(id=identifier, command=command, cwd=str(ROOT), exit_code=result.returncode,
                          log=str(log_path), log_sha256=hashlib.sha256(log_path.read_bytes()).hexdigest()))
        if result.returncode:
            raise ValueError('delta gate failed: ' + identifier + '; raw log: ' + str(log_path))
        if completed_source_hashes() != current:
            raise ValueError('source changed during delta replay; recollect from unchanged original receipt')
    if hashlib.sha256(base_path.read_bytes()).hexdigest() != base_digest:
        raise ValueError('original receipt changed during delta replay')
    delta = dict(schema_version='quality-delta-v1', base_quality=str(base_path), base_sha256=base_digest,
                 base_source_hashes=base['source_hashes'], source_hashes=current, changes=changes, gates=gates)
    with output_path.open('x') as output:
        output.write(json.dumps(delta, sort_keys=True, indent=2) + '\n')
    completed_quality_check(output_path)
    output_path.chmod(0o400)
    print('QUALITY_DELTA_SHA256', hashlib.sha256(output_path.read_bytes()).hexdigest())


def completed_plan_revision(plan_path, verify_command, reason, revision_step='infrastructure-acceptance'):
    """Definition payload only. Never write PLAN or progress/registry state."""
    text = Path(plan_path).read_text()
    metadata = json.loads(re.search(r'## Plan Metadata\n\n```json\n(.*?)\n```', text, re.S)[1])
    def section(title):
        match = re.search(r'^## ' + re.escape(title) + r'\n\n(.*?)(?=^## |\Z)', text, re.M | re.S)
        if not match:
            raise ValueError('missing persisted section: ' + title)
        return match[1].strip()
    steps, group = [], ''
    current = None
    details = []
    for line in section('Phases').splitlines():
        if line.startswith('### ⟳ Parallel group: '):
            group = line.split(': ', 1)[1]
        elif re.match(r'^- \[[ x~!]\] \*\*', line):
            if current:
                current['details'] = '\n'.join(details).strip()
                steps.append(current)
            match = re.fullmatch(r'- \[[ x~!]\] \*\*([^*]+)\*\* — (.+)', line)
            if not match:
                raise ValueError('unrecognized phase header')
            current = dict(name=match[1], description=match[2], parallel_group=group, files=[])
            details = []
        elif line.startswith('  ') and current:
            value = line[2:]
            field = next((key for key in ('Summary', 'Outcome', 'Verify-Kind', 'Verify') if value.startswith(key + ': ')), None)
            if field:
                current[{'Summary': 'summary', 'Outcome': 'outcome', 'Verify': 'verify', 'Verify-Kind': 'verify_kind'}[field]] = value[len(field) + 2:]
            elif value.startswith('- File ('):
                match = re.fullmatch(r'- File \((new|modify|delete)\): (.+)', value)
                if not match:
                    raise ValueError('unrecognized file declaration')
                current['files'].append(dict(kind=match[1], path=match[2]))
            elif not value.startswith('Evidence: '):
                details.append(value)
    if current:
        current['details'] = '\n'.join(details).strip()
        steps.append(current)
    if not steps or len({s['name'] for s in steps}) != len(steps):
        raise ValueError('missing or duplicate persisted phases')
    for step in steps:
        if set(step) != {'name', 'description', 'parallel_group', 'files', 'summary', 'outcome', 'verify', 'verify_kind', 'details'} or not step['details']:
            raise ValueError('incomplete persisted phase')
    target = next(s for s in steps if s['name'] == revision_step)
    target['verify'] = verify_command
    target['details'] += '\nFull owned acceptance executes outside the fixed 10-minute plan recorder cap. The short verifier re-reads every current exact manifest/command/JSONL/scenario and retained binary hash, binds zero parent exits and current source/artifact bytes to an immutable receipt digest, and refuses stale or incomplete evidence; no local gate is removed. Live Kubernetes/PostgreSQL remain explicitly deferred NOT PASS.'
    return dict(action='revise', revision=dict(plan_id=metadata['plan_id'], base_revision=metadata['revision'],
        goal=re.search(r'^\*\*Goal:\*\* (.+)$', text, re.M)[1], reason=reason, requires_approval=False,
        document=dict(context=re.sub(r'^### ', '## ', section('Context'), flags=re.M),
                      verification=section('Verification'), what_not_to_do=section('What Not To Change')), steps=steps))


def completed_evidence_main(argv):
    parser = argparse.ArgumentParser(description='Seal/revalidate completed phase6 evidence; never execute acceptance or edit PLAN')
    parser.add_argument('operation', choices=('seal', 'verify', 'revision', 'collect-quality-delta', 'skip-inventory', 'collect-skip-coverage'))
    parser.add_argument('--coverage-report', type=Path, action='append', default=[])
    parser.add_argument('--extra-gate')
    parser.add_argument('--base-quality', type=Path)
    parser.add_argument('--evidence-kind', choices=('phase6', 'final-integrated', 'employee'), default='phase6')
    parser.add_argument('--employee-report', type=Path)
    parser.add_argument('--employee-exit', type=int)
    parser.add_argument('--baseline', type=Path)
    parser.add_argument('--all', dest='all_report', type=Path)
    parser.add_argument('--quality', type=Path)
    parser.add_argument('--baseline-exit', type=int)
    parser.add_argument('--all-exit', type=int)
    parser.add_argument('--revision-step', default='infrastructure-acceptance')
    parser.add_argument('--receipt', type=Path)
    parser.add_argument('--sha256')
    parser.add_argument('--storage', type=Path)
    parser.add_argument('--file-first', type=Path)
    parser.add_argument('--file-first-transcript', type=Path)
    parser.add_argument('--storage-exit', type=int)
    parser.add_argument('--file-first-exit', type=int)
    parser.add_argument('--plan', type=Path)
    parser.add_argument('--verify-command')
    parser.add_argument('--reason')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args(argv)
    try:
        if args.operation in ('skip-inventory', 'collect-skip-coverage'):
            if not args.quality or (args.operation == 'collect-skip-coverage' and (not args.extra_gate or not args.output)):
                raise ValueError('quality and explicit extra gate/new output required')
            completed_collect_skip_coverage(args.quality, args.coverage_report, args.output,
                args.extra_gate if args.operation == 'collect-skip-coverage' else None)
            return 0
        if args.operation == 'collect-quality-delta':
            if not args.base_quality or not args.output:
                raise ValueError('quality delta collection requires --base-quality and new --output')
            completed_quality_delta_collect(args.base_quality, args.output)
            return 0
        if args.operation == 'revision':
            if not args.plan or not args.verify_command or not args.reason or not args.output or args.output.resolve() == args.plan.resolve():
                raise ValueError('revision requires plan, command, reason and distinct artifact output')
            payload = completed_plan_revision(args.plan, args.verify_command, args.reason, args.revision_step)
            args.output.write_text(json.dumps(payload, indent=2) + '\n')
            print('REVISION_PAYLOAD', args.output)
            return 0
        if not args.receipt:
            raise ValueError('receipt required')
        if args.operation == 'seal':
            if args.evidence_kind == 'employee':
                if args.employee_exit != 0 or not args.employee_report:
                    raise ValueError('direct parent-observed employee all zero exit and report required')
                artifacts = completed_employee_check(args.employee_report, args.quality, args.coverage_report)
                receipt = dict(schema_version=3, evidence_kind='employee',
                    employee_report=str(args.employee_report.resolve()),
                    quality=str(args.quality.resolve()) if args.quality else None,
                    coverage_reports=[str(p.resolve()) for p in args.coverage_report],
                    parent_observed_exits={'employee': 0}, source_hashes=completed_source_hashes(), artifacts=artifacts)
            elif args.evidence_kind == 'final-integrated':
                if (args.baseline_exit != 0 or args.all_exit != 0 or not args.baseline
                        or not args.all_report or not args.quality):
                    raise ValueError('direct parent-observed baseline/all zero exits and quality required')
                artifacts = completed_final_check(args.baseline, args.all_report, args.quality, args.coverage_report)
                receipt = dict(schema_version=2, evidence_kind='final-integrated',
                    baseline=str(args.baseline.resolve()), all_report=str(args.all_report.resolve()),
                    quality=str(args.quality.resolve()), coverage_reports=[str(p.resolve()) for p in args.coverage_report],
                    parent_observed_exits={'baseline': 0, 'all': 0},
                    source_hashes=completed_source_hashes(), artifacts=artifacts)
            else:
                if args.storage_exit != 0 or args.file_first_exit != 0 or not args.storage or not args.file_first:
                    raise ValueError('direct parent-observed full run zero exits and reports required')
                artifacts = completed_phase6_check(args.storage, args.file_first, args.file_first_transcript)
                receipt = dict(schema_version=1, storage=str(args.storage.resolve()), file_first=str(args.file_first.resolve()),
                    transcript=str(args.file_first_transcript.resolve()) if args.file_first_transcript else None,
                    parent_observed_exits={'storage': 0, 'file-first': 0}, source_hashes=completed_source_hashes(), artifacts=artifacts)
            with args.receipt.open('x') as output:
                output.write(json.dumps(receipt, sort_keys=True, indent=2) + '\n')
            args.receipt.chmod(0o400)
            print('COMPLETED_RECEIPT_SHA256', hashlib.sha256(args.receipt.read_bytes()).hexdigest())
        else:
            if not args.sha256 or hashlib.sha256(args.receipt.read_bytes()).hexdigest() != args.sha256:
                raise ValueError('immutable receipt digest mismatch')
            receipt = json.loads(args.receipt.read_text())
            final = args.evidence_kind == 'final-integrated'
            employee_kind = args.evidence_kind == 'employee'
            if (receipt.get('schema_version') != (3 if employee_kind else 2 if final else 1)
                    or receipt.get('parent_observed_exits') != ({'employee': 0} if employee_kind else {'baseline': 0, 'all': 0} if final else {'storage': 0, 'file-first': 0})
                    or ((final or employee_kind) and receipt.get('evidence_kind') != args.evidence_kind)):
                raise ValueError('direct successful parent run attestation missing')
            if receipt['source_hashes'] != completed_source_hashes():
                raise ValueError('current source changed since completed acceptance')
            for path, digest in receipt['artifacts'].items():
                if hashlib.sha256(Path(path).read_bytes()).hexdigest() != digest:
                    raise ValueError('completed artifact hash mismatch: ' + path)
            artifacts = (completed_employee_check(receipt['employee_report'], receipt.get('quality'), receipt.get('coverage_reports', ())) if employee_kind
                         else completed_final_check(receipt['baseline'], receipt['all_report'], receipt['quality'], receipt.get('coverage_reports', ())) if final
                         else completed_phase6_check(receipt['storage'], receipt['file_first'], receipt['transcript']))
            if artifacts != receipt['artifacts']:
                raise ValueError('current manifest artifact set mismatch')
            print(('COMPLETED_EMPLOYEE' if employee_kind else 'COMPLETED_FINAL_INTEGRATED' if final else 'COMPLETED_PHASE6')
                  + '_LOCAL_EVIDENCE_VERIFIED; live Kubernetes/PostgreSQL and paid providers UNVERIFIED NOT PASS')
        return 0
    except (OSError, ValueError, KeyError, TypeError, StopIteration, subprocess.SubprocessError) as error:
        print('COMPLETED_EVIDENCE_BLOCKED:', str(error), file=sys.stderr)
        return 2



# Employee acceptance is a controlled-local tier, not release/deployment proof.
EMPLOYEE_PHASES = ('contracts', 'dispatch', 'delivery', 'latency', 'presentation', 'all')
EMPLOYEE_GATES = {
    'contracts': {
        './pkg/launcher': ['TestBotSharedResearchAdvertisedSchema', 'TestBotSharedResearchToolErrorCorrection',
                           'TestChatRuntimeConfiguredSearchProviderParity', 'TestBotScopedChatConfigStrict',
                           'TestBotSharedSearchFailsClosed'],
        './pkg/api': ['TestBotAgentNonexecutionOutcomeBinding', 'TestBotAgentBridgeNonexecutionConcreteIdentity'],
    },
    'dispatch': {
        './pkg/daemon': ['TestBotPromptDispatchMaintenanceBlocked',
                         'TestBotWorkerDispatchCapacitySameBotAndJoinedStop',
                         'TestBotWorkerDispatchMailboxCoalescingOverflowAndStop',
                         'TestBotWorkerDispatchOverflowRediscoverySinglePagePriority',
                         'TestBotWorkerDispatchDrainJoinsAndRejectsLateAdmission',
                         'TestBotWorkerDispatchNotifierTrustedRoutingOnly',
                         'TestBotWorkerDispatchConcurrentNotifyStop',
                         'TestBotWorkerPollRecoversCrashAfterDatabaseReopen',
                         'TestBotWorkerPauseRecoveryAfterDatabaseReopen',
                         'TestBotWorkerResumedBudgetExhausted'],
    },
    'delivery': {
        './pkg/api': ['TestBotEmployeeReadOnlyPublishesWithoutReview', 'TestBotEmployeeAdaptiveAdapterInquiry',
                      'TestBotEmployeeAdaptiveResultSufficiency',
                      'TestBotEmployeeReviewBypassRequiresPositiveReadOnlyResult',
                      'TestBotFileDeliveryHTTP', 'TestBotFileContentPersistenceHTTP',
                      'TestBotFileFirstPublicationSSE', 'TestBotTitleConfirmedLaterQuestionContinuation',
                      'TestBotEmployeeObjectiveInquiryHTTP', 'TestBotEmployeeExplicitFileHTTP',
                      'TestBotInteractiveAuthorityHTTPAcceptance'],
        './pkg/daemon': ['TestBotTitleConfiguredSearchHTTP', 'TestBotTaskScheduleHTTPAcceptance',
                         'TestBotUncertainEffectCrashHTTPNoReplay', 'TestBotConfiguredSearchOwnerResumeHTTP'],
        './pkg/launcher': ['TestBotAutonomousFetchFinalHomePublication'],
    },
    'latency': {
        './pkg/daemon': ['TestBotPromptDispatchDockerMaintenanceBlocked',
                         'TestBotNewMessageDockerBetweenCallReproduction',
                         'TestBotEnhancedChatNoPythonDocker', 'TestBotEnhancedChatTeamNoPythonDocker',
                         'TestBotAllModeRetainedSessionDocker', 'TestBotDelegatedRetainedSessionDocker'],
    },
    'presentation': {
        './pkg/api': ['TestBotEmployeeObjectiveBrowser', 'TestBotClientStatesSharedVisualBrowserAcceptance',
                      'TestBotConversationPresentationBrowserAcceptance',
                      'TestBotEventsHTTPAcceptance', 'TestBotHistoryStreamHTTPAcceptance'],
        './cmd/astonish': ['TestBotEmployeeObjectivePTY', 'TestBotConversationPresentationPTYAcceptance'],
    },
}
EMPLOYEE_SUBTESTS = {
    'TestBotEmployeeExplicitFileHTTP': ['personal', 'team'],
    'TestBotEmployeeObjectiveInquiryHTTP': [scope + '/adequate-first=' + adequate
        for scope in ('personal', 'team') for adequate in ('false', 'true')],
    'TestBotSharedResearchAdvertisedSchema': ['complete', 'omitted', 'null', 'live-eight', 'live-six'],
    'TestBotSharedSearchFailsClosed': ['missing-factory', 'ambient-secret', 'nil-model', 'factory-error'],
    'TestBotEmployeeAdaptiveResultSufficiency': ['adequate-first', 'non-news', 'read-error', 'no-results',
                                               'useful-partial', 'scheduled', 'latest-owner'],
    'TestBotTitleConfiguredSearchHTTP': [scope + '/missing=' + missing
        for scope in ('personal', 'team') for missing in ('false', 'true')],
    'TestBotPromptDispatchMaintenanceBlocked': ['personal', 'team'],
    'TestBotEmployeeObjectiveBrowser': ['personal', 'team'],
    'TestBotUncertainEffectCrashHTTPNoReplay': ['personal', 'team'],
    'TestBotTaskScheduleHTTPAcceptance': [scope + '/' + scenario for scope in ('personal', 'team')
        for scenario in ('greeting', 'automatic-capture', 'owner-correction-pin', 'manual',
                         'schedule-three-ticks', 'delete-manual')],
    'TestBotConversationPresentationPTYAcceptance': ['draft', 'active', 'paused'],
}
# Discharged gaps remain mandatory exact test + diagnostic requirements, not
# an unconditional waiver based on a loosely related package pass.
EMPLOYEE_COVERAGE_GAPS = {}
EMPLOYEE_MARKERS = {
    ('./pkg/api', 'TestBotEmployeeObjectiveInquiryHTTP/' + scope + '/adequate-first=' + adequate):
        ('ordinary-chat-compared=true', 'shared-configured-provider=true',
         'final-source-body-equal=true', 'exact-observations=' + ('1' if adequate == 'true' else '3'),
         'Chat-model=' + ('2' if adequate == 'true' else '4'),
         'Bot-model=' + ('2' if adequate == 'true' else '4'))
    for scope in ('personal', 'team') for adequate in ('false', 'true')
}
EMPLOYEE_MARKERS.update({
    ('./pkg/api', 'TestBotEmployeeExplicitFileHTTP/' + scope):
        ('employee-explicit-file=true', 'owner-objective=true', 'shared-chat-loop=true',
         'authorized-write=true', 'trusted-publication=true', 'committed-message=true',
         'exact-immutable-download=true', 'no-replay=true')
    for scope in ('personal', 'team')
})
EMPLOYEE_MARKERS.update({
    ('./pkg/launcher', 'TestBotSharedResearchAdvertisedSchema/live-' + count):
        ('exact-historical-envelope=true', 'original-digest-preserved=true',
         'bridge=configured_search/perplexity_web_search', 'provenance=495.../52daa...',
         'SQL=36263/36268', 'requested=' + requested, 'effective=5')
    for count, requested in (('six', '6'), ('eight', '8'))
})


class EmployeeEvidence(ConsolidationEvidence):
    """Bind diagnostics to exact running package/subcase, never package prose."""
    def __init__(self, required, log, env):
        super().__init__(required, log, env)
        self.observed_markers = {}
        self.active_tests = set()

    def feed(self, line):
        super().feed(line)
        try:
            event = json.loads(line)
        except (ValueError, TypeError):
            return
        if not isinstance(event, dict):
            return
        key = (event.get('Package'), event.get('Test'))
        if event.get('Action') == 'run':
            self.active_tests.add(key)
        if event.get('Action') in ('pass', 'fail', 'skip'):
            self.active_tests.discard(key)
        if event.get('Action') != 'output' or key not in self.active_tests:
            return
        output = event.get('Output', '')
        if not isinstance(output, str):
            return
        for (package, test), markers in EMPLOYEE_MARKERS.items():
            if key != ('github.com/SAP/astonish/' + package.removeprefix('./'), test):
                continue
            for marker in markers:
                if re.search(r'(?<!\S)' + re.escape(marker) + r'(?!\S)', output):
                    self.observed_markers.setdefault(key, set()).add(marker)


def employee_exact_errors(evidence, package, required):
    issues = consolidation_exact_errors(evidence, package, required)
    for (marker_package, test), markers in EMPLOYEE_MARKERS.items():
        if marker_package != package or test not in required:
            continue
        key = ('github.com/SAP/astonish/' + package.removeprefix('./'), test)
        observed = evidence.observed_markers.get(key, set())
        issues += ['missing exact employee diagnostic: ' + key[0] + '/' + test + ': ' + marker
                   for marker in markers if marker not in observed]
    return issues


def employee_required(package, names):
    children = dict(CONSOLIDATION_SUBTESTS)
    for manifest in CONSOLIDATION_CONTROLLER_SUBTESTS.values():
        children.update(manifest)
    children.update(EMPLOYEE_SUBTESTS)
    return names + [name + '/' + child for name in names for child in children.get(name, [])]


def employee_invocation():
    return ['python3', '-B', 'tests/e2eboot/bots_acceptance_driver.py',
            '--stage', 'employee', '--employee-phase', 'all']


def employee_batches(phases):
    """Each mandatory physical parent owns its unchanged 15m Go timeout."""
    for phase in phases:
        for package, names in EMPLOYEE_GATES[phase].items():
            for batch in ([ [name] for name in names ] if phase == 'latency' else [names]):
                yield phase, package, batch


def employee_studio_artifacts():
    version = json.loads((ROOT / 'web/package.json').read_text())['version']
    return sorted(p for p in (ROOT / 'web/dist' / version).rglob('*') if p.is_file())


def completed_employee_check(report_path, quality_path=None, coverage_reports=()):
    report_path = Path(report_path)
    report = json.loads(report_path.read_text())
    if (report.get('schema_version') != 'employee-v1' or report.get('stage') != 'employee'
            or report.get('phase') != 'all' or report.get('status') != 'PASS'
            or report.get('evidence_tier') != 'controlled-local' or report.get('errors')
            or report.get('coverage_gaps') or EMPLOYEE_COVERAGE_GAPS
            or report.get('command') != employee_invocation() or report.get('exit_code') != 0):
        raise ValueError('completed full employee command/context/zero exit required')
    if report.get('source_hashes') != completed_source_hashes():
        raise ValueError('current source changed since employee acceptance')
    if report.get('unverified') != ['live Kubernetes/PostgreSQL', 'paid-provider quality and latency',
                                   'deployment and existing user Bots']:
        raise ValueError('employee live/paid/deployment boundaries must remain unverified')
    artifacts = {str(report_path.resolve()): employee_digest(report_path)}
    def bind(path, digest):
        path = Path(path)
        if employee_digest(path) != digest:
            raise ValueError('employee artifact hash mismatch: ' + str(path))
        artifacts[str(path.resolve())] = digest
    builds = report.get('builds', [])
    if [b.get('id') for b in builds] != ['ui', 'candidate', 'node']:
        raise ValueError('full current employee builds missing/duplicated')
    binaries = {}
    for build in builds:
        name = build['id']; command = build.get('command', [])
        expected = (['npm', '--prefix', 'web', 'run', 'build'] if name == 'ui' else
                    ['go', 'build', '-o', command[3] if len(command) > 3 else '', '.'])
        if build.get('status') != 'PASS' or build.get('exit_code') != 0 or command != expected:
            raise ValueError('employee build command/zero exit missing: ' + name)
        if name != 'ui':
            binary = Path(command[3])
            if binary.name != ('astonish' if name == 'candidate' else 'astonish-node') or not binary.is_absolute():
                raise ValueError('employee exact built binary missing')
            binaries[str(binary)] = name
        bind(build['log'], build['log_sha256'])
    built = report.get('build_artifacts', {})
    studio = employee_studio_artifacts()
    if not studio or set(built) != set(binaries) | {str(p) for p in studio}:
        raise ValueError('exact current employee binary and Studio artifact set required')
    for path, digest in built.items():
        bind(path, digest)
    batches = list(employee_batches(EMPLOYEE_PHASES[:-1]))
    gates = report.get('gates', [])
    if len(gates) != len(batches):
        raise ValueError('full current employee manifest missing/duplicated')
    for gate, (phase, package, names) in zip(gates, batches):
        required = employee_required(package, names)
        if (gate.get('phase') != phase or gate.get('package') != package
                or gate.get('required') != required or gate.get('command') != go_command(package, names)
                or gate.get('status') != 'PASS' or gate.get('exit_code') != 0
                or gate.get('fresh') is not True or gate.get('prerequisites_verified') is not True
                or gate.get('errors')):
            raise ValueError('invalid employee exact gate/zero exit: ' + phase)
        bind(gate['log'], gate['log_sha256'])
        evidence = EmployeeEvidence(required, io.StringIO(), {})
        with Path(gate['log']).open() as raw:
            for line in raw:
                evidence.feed(line)
        issues = employee_exact_errors(evidence, package, required)
        passed = sorted(test for pkg, test in evidence.qualified_passes
                        if pkg == 'github.com/SAP/astonish/' + package.removeprefix('./'))
        if issues or gate.get('passed') != passed:
            raise ValueError('employee raw exact evidence failed: ' + '; '.join(issues or ['passed inventory mismatch']))
    if quality_path:
        quality_artifacts = completed_quality_check(quality_path, coverage_reports)
        for path in quality_artifacts:
            artifacts[path] = employee_digest(path)
    if completed_source_hashes() != report['source_hashes']:
        raise ValueError('source changed during employee validation')
    return artifacts


def employee(args):
    phases = EMPLOYEE_PHASES[:-1] if args.employee_phase == 'all' else (args.employee_phase,)
    env = file_first_env(dict(os.environ))
    report_dir = Path(tempfile.mkdtemp(prefix='astonish-employee-acceptance-'))
    report_path = args.report or report_dir / 'report.json'
    report = dict(schema_version='employee-v1', stage='employee', phase=args.employee_phase,
                  status='BLOCKED', evidence_tier='controlled-local', gates=[],
                  command=employee_invocation() if args.employee_phase == 'all' else
                          employee_invocation()[:-1] + [args.employee_phase], exit_code=None,
                  unverified=['live Kubernetes/PostgreSQL', 'paid-provider quality and latency',
                              'deployment and existing user Bots'], source_hashes=completed_source_hashes())
    started = time.monotonic()
    try:
        # Even a scoped invocation cannot silently waive its physical prerequisites.
        env['ASTONISH_TEST_TASK_CONTINUITY'] = '1'
        issues = file_first_prerequisites(env, defer_kubernetes_postgresql=True)
        if issues:
            report['errors'] = issues
            return 2
        for command in (['docker', 'info', '--format', '{{.ServerVersion}}'],
                        ['docker', 'image', 'inspect', '--format', '{{.Id}}', env['ASTONISH_SANDBOX_IMAGE']]):
            if not run(command, env, timeout=30):
                report['errors'] = ['required physical Docker prerequisite probe failed']
                return 2
        # Isolated config/data roots; inherited credentials/deployment flags were
        # excluded by the allowlist. Test fixtures create their own scoped stores.
        home = report_dir / 'home'
        home.mkdir()
        env.update(XDG_CONFIG_HOME=str(home / 'config'), XDG_DATA_HOME=str(home / 'data'),
                   ASTONISH_TEST_TASK_EXECUTION='1',
                   ASTONISH_BOT_BROWSER_ACCEPTANCE='1', ASTONISH_MEMORY_BROWSER_ACCEPTANCE='1',
                   ASTONISH_MEMORY_BROWSER_PATH=env['ASTONISH_BOT_BROWSER_PATH'])
        builds = [('ui', ['npm', '--prefix', 'web', 'run', 'build']),
                  ('candidate', ['go', 'build', '-o', str(report_dir / 'astonish'), '.']),
                  ('node', ['go', 'build', '-o', str(report_dir / 'astonish-node'), '.'])]
        report['builds'] = []
        for name, command in builds:
            log_path = report_dir / (name + '.log')
            with log_path.open('w') as log:
                capture = EmployeeBuildLog(log)
                ok = run(command, env, capture)
            report['builds'].append(dict(id=name, command=command, status='PASS' if ok else 'FAIL',
                                         exit_code=getattr(capture, 'exit_code', None),
                                         log=str(log_path), log_sha256=employee_digest(log_path)))
            if not ok:
                report['status'] = 'FAIL'
                return 1
        artifacts = [report_dir / 'astonish', report_dir / 'astonish-node']
        artifacts += employee_studio_artifacts()
        if len(artifacts) < 3:
            raise ValueError('current built Studio artifacts missing')
        report['build_artifacts'] = {str(p): employee_digest(p) for p in artifacts}
        for batch_index, (phase, package, names) in enumerate(employee_batches(phases)):
            required = employee_required(package, names)
            command = go_command(package, names)
            log_path = report_dir / (str(batch_index) + '-' + phase + '-' + package.removeprefix('./').replace('/', '-') + '.jsonl')
            gate = dict(phase=phase, package=package, required=required, command=command,
                        status='RUNNING', fresh=True, prerequisites_verified=True, log=str(log_path))
            report['gates'].append(gate)
            with log_path.open('w') as log:
                evidence = EmployeeEvidence(required, log, env)
                ok = run(command, env, evidence)
            errors = employee_exact_errors(evidence, package, required)
            gate.update(status='PASS' if ok and not errors else 'FAIL', errors=errors,
                        exit_code=getattr(evidence, 'exit_code', None),
                        passed=sorted(name for pkg, name in evidence.qualified_passes
                                      if pkg == 'github.com/SAP/astonish/' + package.removeprefix('./')),
                        log_sha256=employee_digest(log_path))
            if not ok or errors:
                report['status'] = 'FAIL'
                return 1
        if completed_source_hashes() != report['source_hashes']:
            report['errors'] = ['source changed during acceptance; recollect current builds and exact evidence']
            return 2
        if args.employee_phase == 'all' and EMPLOYEE_COVERAGE_GAPS:
            report['coverage_gaps'] = dict(EMPLOYEE_COVERAGE_GAPS)
            return 2
        report['status'] = 'PASS'
        report['exit_code'] = 0
        return 0
    except (OSError, ValueError, TimeoutError, subprocess.SubprocessError) as error:
        report['errors'] = [str(error)]
        return 2
    finally:
        if report['exit_code'] is None:
            report['exit_code'] = 1 if report['status'] == 'FAIL' else 2
        report['elapsed_seconds'] = round(time.monotonic() - started, 3)
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
        print('EMPLOYEE_REPORT', report_path, report['status'], flush=True)


class EmployeeBuildLog:
    def __init__(self, log):
        self.log = log

    def feed(self, line):
        self.log.write(line)
        self.log.flush()

    def errors(self):
        return []


def employee_digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


ACTIVE_BOT_GATES = {
    'creation': ('./pkg/store/entstore', ['TestBotActiveCreationContract']),
    'http': ('./pkg/api', ['TestBotActiveCreateHTTPAcceptance', 'TestBotChatFirstConversationHTTPAcceptance',
                           'TestBotProposalRoutesStrictHTTP', 'TestBotSourceIntakeProposalHTTPAcceptance',
                           'TestBotSourceIntakeAdaptiveHTTP', 'TestBotSourceIntakeTemplateHTTP']),
    'schedules': ('./pkg/daemon', ['TestBotAuthorizedScheduleHTTPAcceptance']),
    'resume': ('./pkg/store/entstore', ['TestReviewedConfigurationResumeKeepsSchedulesDisabled',
                                      'TestRoutineResumeAfterPausedResponsibilityUpdate']),
    'cli-pty': ('./cmd/astonish', ['TestBotCLIExplicitIntentSubprocessAcceptance',
                                 'TestBotCLIAndPTYAcceptance', 'TestBotTitleChatPTYAcceptance']),
    'browser': ('./pkg/api', ['TestBotActiveCreationBrowserAcceptance',
                             'TestBotChatFirstOrderedConversationBrowserAcceptance']),
}
ACTIVE_BOT_SCOPED = {'TestBotActiveCreationContract', 'TestBotActiveCreateHTTPAcceptance',
                     'TestBotAuthorizedScheduleHTTPAcceptance', 'TestBotActiveCreationBrowserAcceptance'}


def active_bot_required(names):
    return names + [name + '/' + scope for name in names if name in ACTIVE_BOT_SCOPED
                    for scope in ('personal', 'team')]


def active_bot_status(args):
    """Fresh local contract/client evidence; no upgrade tier or paid provider."""
    if args.checks_only:
        suite = ['python3', '-B', '-m', 'unittest', 'discover', '-s', 'tests/e2eboot',
                 '-p', 'bots_acceptance_driver_test.py', '-k', 'ActiveBotAcceptanceTests']
        ok = run(suite, file_first_env(os.environ))
        print('CHECKS ONLY; no Active Bot behavior or browser journey executed')
        return 0 if ok else 1
    env = file_first_env(os.environ)
    browser = env.get('ASTONISH_BOT_BROWSER_PATH', '')
    report = dict(schema_version=1, stage='active-bot-status', status='BLOCKED', gates=[],
                  unverified=['deployed sandbox and real-provider quality', 'PostgreSQL fresh schema'])
    path = args.report
    if path is None:
        fd, name = tempfile.mkstemp(prefix='astonish-active-bot-', suffix='.json')
        os.close(fd)
        path = Path(name)
    try:
        if not browser or not Path(browser).is_absolute() or not Path(browser).is_file() or not os.access(browser, os.X_OK):
            report['errors'] = ['ASTONISH_BOT_BROWSER_PATH must name an actual absolute executable Chrome path']
            return 2
        with tempfile.TemporaryDirectory(prefix='astonish-active-bot-home-') as home:
            env.update(XDG_CONFIG_HOME=home + '/config', XDG_DATA_HOME=home + '/data',
                       ASTONISH_BOT_BROWSER_ACCEPTANCE='1', ASTONISH_MEMORY_BROWSER_ACCEPTANCE='1',
                       ASTONISH_MEMORY_BROWSER_PATH=browser)
            if not run(['npm', '--prefix', 'web', 'run', 'build'], env):
                report['status'] = 'FAIL'
                report['errors'] = ['current Studio build failed']
                return 1
            for tier, (package, names) in ACTIVE_BOT_GATES.items():
                required = active_bot_required(names)
                evidence = ConsolidationEvidence(required, io.StringIO(), env)
                command = go_command(package, names)
                ok = run(command, env, evidence)
                errors = consolidation_exact_errors(evidence, package, required)
                report['gates'].append(dict(tier=tier, package=package, required=required,
                    command=command, status='PASS' if ok and not errors else 'FAIL', errors=errors,
                    exit_code=getattr(evidence, 'exit_code', None),
                    passed=sorted(name for pkg, name in evidence.qualified_passes
                                  if pkg == 'github.com/SAP/astonish/' + package.removeprefix('./'))))
                if not ok or errors:
                    report['status'] = 'FAIL'
                    return 1
            report['status'] = 'PASS'
            return 0
    except (OSError, ValueError, TimeoutError, subprocess.SubprocessError) as error:
        report['errors'] = [str(error)]
        return 2
    finally:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
        print('ACTIVE_BOT_REPORT', path, report['status'])


def main(argv=None):
    arguments = sys.argv[1:] if argv is None else argv
    if arguments and arguments[0] == '--completed-evidence':
        return completed_evidence_main(arguments[1:])
    parser = argparse.ArgumentParser(description="Strict Bot release gate; no deployment or credential loading")
    parser.add_argument("--preflight", action="store_true", help="check prerequisites only; not acceptance")
    parser.add_argument("--local-only", action="store_true", help="partial local evidence; always non-release exit 3")
    stages = parser.add_mutually_exclusive_group()
    stages.add_argument('--stage', choices=('active-bot-status', 'title-confirmation-contract', 'title-confirmation', 'durable-progress', 'file-first', 'chat-consolidation', 'employee'), help='strict scoped acceptance stage')
    parser.add_argument('--employee-phase', choices=EMPLOYEE_PHASES, help='required for employee only')
    parser.add_argument('--phase', choices=CONSOLIDATION_PHASES, help='required for chat-consolidation only')
    stages.add_argument('--skills-stage', choices=('all', 'recovery', 'postgres'), help='strict skills acceptance (separate from full Bot release)')
    stages.add_argument('--agent-stage', choices=('execution', 'progress', 'learning', 'continuity', 'postgres', 'all'), help='strict agent acceptance (separate from full Bot release)')
    parser.add_argument('--config', type=Path, help='explicit isolated skills or agent launcher configuration')
    parser.add_argument('--report', type=Path, help='durable-progress or file-first machine-readable evidence output')
    parser.add_argument('--soak', action='store_true', help='request optional two-hour wall-clock tier (blocks until implemented)')
    parser.add_argument('--real-provider', action='store_true', help='request optional paid-provider tier (blocks until implemented)')
    parser.add_argument('--postgres', action='store_true', help='request dedicated durable-progress PostgreSQL tier')
    parser.add_argument('--defer-kubernetes-postgresql', action='store_true',
                        help='user-requested local profile: report live Kubernetes/PostgreSQL DEFERRED, never PASS; only file-first/chat-consolidation')
    parser.add_argument('--checks-only', action='store_true', help='active-bot-status driver self-checks only; NOT behavior acceptance')
    args = parser.parse_args(argv)
    if args.stage == 'active-bot-status':
        if args.employee_phase or args.phase or args.preflight or args.local_only or args.config or args.soak or args.real_provider or args.postgres or args.defer_kubernetes_postgresql:
            parser.error('active-bot-status forbids partial/config/provider/deployment flags')
        return active_bot_status(args)
    if args.checks_only:
        parser.error('--checks-only requires --stage active-bot-status')
    if args.stage == 'employee':
        if not args.employee_phase or args.phase or args.preflight or args.local_only or args.config or args.soak or args.real_provider or args.postgres or args.defer_kubernetes_postgresql:
            parser.error('employee requires --employee-phase and forbids partial/config/provider/deployment flags')
        return employee(args)
    if args.employee_phase:
        parser.error('--employee-phase requires --stage employee')
    if args.defer_kubernetes_postgresql and args.stage not in ('chat-consolidation', 'file-first'):
        parser.error('--defer-kubernetes-postgresql requires --stage chat-consolidation or file-first')
    if args.stage == 'chat-consolidation':
        if not args.phase or args.preflight or args.local_only or args.config or args.soak or args.real_provider or args.postgres:
            parser.error('chat-consolidation requires --phase and forbids partial/config/provider flags')
        return chat_consolidation(args)
    if args.phase:
        parser.error('--phase requires --stage chat-consolidation')
    if args.stage == 'file-first':
        if args.preflight or args.local_only or args.config or args.soak or args.real_provider or args.postgres:
            parser.error('file-first requires all tiers and forbids partial/config/provider flags')
        return file_first(args)
    if args.stage == 'durable-progress':
        if args.preflight or args.local_only or (args.config and not (args.soak or args.real_provider)):
            parser.error('durable-progress forbids partial modes; --config requires optional soak/provider tier')
        return durable_progress(args)
    if args.report or args.soak or args.real_provider or args.postgres:
        parser.error('--report/--soak/--real-provider/--postgres require --stage durable-progress')
    if args.stage:
        if args.preflight or args.local_only or args.config:
            parser.error('--stage forbids --preflight, --local-only and --config')
        if args.stage == 'title-confirmation':
            return title_confirmation()
        return title_confirmation_contract()
    if args.agent_stage:
        if args.preflight or args.local_only:
            parser.error('--agent-stage forbids --preflight and --local-only')
        from employee_http_acceptance import main as agent_main
        return agent_main(['--agent-stage', args.agent_stage] + (['--config', str(args.config)] if args.config else []))
    if args.skills_stage:
        if args.preflight or args.local_only:
            parser.error('--skills-stage forbids --preflight and --local-only')
        from skills_acceptance_driver import main as skills_main
        return skills_main(['--skills-stage', args.skills_stage] + (['--config', str(args.config)] if args.config else []))
    if args.config:
        parser.error('--config requires --skills-stage all')
    env = clean_env(os.environ)
    statuses = {gate: "NOT RUN" for gate in GATES}
    outcome = "NOT RELEASE READY"
    try:
        errors = prerequisites(env, args.local_only)
        if errors:
            for error in errors:
                print("BLOCKED: " + error)
            return 2
        # Read-only infrastructure probes, never create namespaces/pull images.
        if not args.local_only:
            current = subprocess.run(["kubectl", "config", "current-context"], env=env, capture_output=True, text=True, timeout=20)
            if current.returncode or current.stdout.strip() != env["ASTONISH_BOTS_K8S_CONTEXT"]:
                print("BLOCKED: kubectl current-context must match ASTONISH_BOTS_K8S_CONTEXT; driver never changes contexts")
                return 2
            probes = [["docker", "info", "--format", "{{.ServerVersion}}"],
                      ["docker", "image", "inspect", "--format", "{{.Id}}", IMAGE],
                      ["kubectl", "--context", env["ASTONISH_BOTS_K8S_CONTEXT"], "--request-timeout=15s",
                       "get", "namespace", env["ASTONISH_E2E_SANDBOX_NAMESPACE"]]]
            for probe in probes:
                if not run(probe, env, timeout=30):
                    print("BLOCKED: infrastructure probe failed")
                    return 2
        if args.preflight:
            print("PREREQUISITES CHECKED ONLY; no acceptance tests executed")
            return 0
        # No live/integration flag inherited by make test; opt-in tests have dedicated strict gates below.
        # Local gates must not initialize deployment clients from inherited cluster settings.
        # Keep env intact: prerequisites and the live Kubernetes gate require that config.
        deployment_keys = {"ASTONISH_PG_ACTIVATION_DISPOSABLE", "ASTONISH_TEST_DSN", "KUBECONFIG", "ASTONISH_BOTS_K8S_CONTEXT",
                           "ASTONISH_BOTS_K8S_ISOLATED", "ASTONISH_E2E_SANDBOX_NAMESPACE",
                           "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE", "ASTONISH_E2E_DEDICATED",
                           "ASTONISH_E2E_HOMES_PVC"}
        unit_env = {k: v for k, v in env.items() if k not in deployment_keys}
        with tempfile.TemporaryDirectory(prefix="astonish-bots-acceptance-") as temporary:
            makefile = Path(temporary) / "Makefile"
            makefile.write_text(sanitized_makefile((ROOT / "Makefile").read_text()))
            commands = [
                ("make-test", ["make", "-f", str(makefile), "test"], unit_env, None),
                ("frontend-typecheck", ["npm", "--prefix", "web", "run", "typecheck"], unit_env, None),
                ("frontend-build", ["npm", "--prefix", "web", "run", "build"], unit_env, None),
                ("frontend-lint", ["npm", "--prefix", "web", "run", "lint"], unit_env, None),
                ("make-lint", ["make", "-f", str(makefile), "lint"], unit_env, None),
            ]
            browser_env = dict(unit_env, ASTONISH_BOT_BROWSER_ACCEPTANCE="1", ASTONISH_MEMORY_BROWSER_ACCEPTANCE="1",
                               ASTONISH_STREAM_BROWSER_ACCEPTANCE="1", ASTONISH_MEMORY_BROWSER_PATH=env["ASTONISH_BOT_BROWSER_PATH"],
                               ASTONISH_STREAM_BROWSER_PATH=env["ASTONISH_BOT_BROWSER_PATH"])
            commands += [("browser", go_command("./pkg/api", BROWSER, pattern="^(" + "|".join(BROWSER) + "|TestChatChronologyHTTPAcceptance_.*|TestMemoryHTTPAcceptance_.*)$"), browser_env, GoEvidence(BROWSER)),
                         ("daemon-http", go_command("./pkg/daemon", DAEMON_HTTP), unit_env, GoEvidence(DAEMON_HTTP)),
                         ("cli-pty", go_command("./cmd/astonish", CLI), unit_env, GoEvidence(CLI)),
                         ("sqlite", go_command("./pkg/store/entstore", ["TestBotHomeBindingPersistence", "TestBotScheduledTickSQLite"], pattern="^Test.*Bot"), unit_env, GoEvidence(["TestBotHomeBindingPersistence", "TestBotScheduledTickSQLite"]))]
            if not args.local_only:
                commands += [("postgres", go_command("./pkg/store/entstore", PG, "integration"), env, GoEvidence(PG)),
                             ("docker-home", go_command("./pkg/sandbox/docker", ["TestBotHomeDockerPersistenceLive"]), dict(env, ASTONISH_TEST_DOCKER="1"), GoEvidence(["TestBotHomeDockerPersistenceLive"])),
                             ("kubernetes-home", go_command("./tests/e2e/bots", LIVE_BOTS, "e2e"), dict(env, ASTONISH_E2E_SANDBOX_BACKEND="k8s"), GoEvidence(LIVE_BOTS))]
            successful_evidence = []
            for name, command, child_env, evidence in commands:
                statuses[name] = "RUNNING"
                try:
                    statuses[name] = "PASS" if run(command, child_env, evidence) else "FAIL"
                except (OSError, TimeoutError) as error:
                    statuses[name] = "FAIL"
                    print("ERROR " + redact(str(error), env))
                if statuses[name] == "PASS" and evidence is not None:
                    successful_evidence.append(evidence)
                # Continue independent gates: lint failure is visible, never suppressed.
            coverage_errors = scenario_coverage_errors(successful_evidence)
            statuses["spec-coverage"] = "FAIL" if coverage_errors else "PASS"
            if coverage_errors:
                print("GATE FAILED: full lifecycle acceptance not implemented/evidenced")
                for error in coverage_errors:
                    print("COVERAGE FAILURE " + redact(error, env))
            # Local collection remains useful and retains its documented exit 3;
            # this is NOT a waiver: the full release gate still explicitly fails.
            if any(value == "FAIL" for name, value in statuses.items() if name != "spec-coverage"):
                return 1
            if args.local_only:
                outcome = "PARTIAL LOCAL EVIDENCE ONLY — NOT RELEASE READY"
                return 3
            if all(value == "PASS" for value in statuses.values()):
                outcome = "FULL BOT RELEASE ACCEPTANCE PASSED (OpenShell explicitly deferred)"
                return 0
            return 1
    except (KeyboardInterrupt, OSError, TimeoutError) as error:
        print("ABORTED " + redact(str(error), env))
        return 1
    finally:
        print("\nBOT ACCEPTANCE SUMMARY")
        for name, status in statuses.items():
            print(name + ": " + status)
        print("OpenShell: DEFERRED (not a release gate)")
        print(outcome, flush=True)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    sys.exit(main())