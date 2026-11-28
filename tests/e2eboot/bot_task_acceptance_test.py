import contextlib
import io
import json
import unittest
from unittest.mock import patch

import bot_task_acceptance as gate
from skills_acceptance_driver import ExactGoEvidence


def feed_passes(evidence):
    for name in sorted(evidence.allowed):
        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'run', 'Test': name}))
        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass', 'Test': name}))
    evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass'}))
    return True


class BotTaskAcceptanceTest(unittest.TestCase):
    def test_exact_current_invocation_evidence(self):
        for package, names in gate.COMPATIBILITY.items():
            evidence = ExactGoEvidence(package, names)
            self.assertTrue(evidence.errors())
            feed_passes(evidence)
            self.assertEqual([], evidence.errors())
            evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'skip', 'Test': next(iter(names))}))
            self.assertTrue(evidence.errors())

    def test_missing_replayed_and_foreign_rejected(self):
        evidence = ExactGoEvidence('pkg/api', gate.COMPATIBILITY['pkg/api'])
        feed_passes(evidence)
        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass', 'Test': 'TestBotAgentWorkHTTPAcceptance'}))
        self.assertTrue(evidence.errors())
        evidence = ExactGoEvidence('pkg/api', gate.COMPATIBILITY['pkg/api'])
        evidence.feed(json.dumps({'Package': 'foreign', 'Action': 'pass'}))
        self.assertTrue(evidence.errors())

    def test_all_requires_explicit_private_config_before_any_work(self):
        with patch.object(gate, 'run') as run, contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(2, gate.main(['--stage', 'all']))
            self.assertEqual(2, gate.main(['--stage', 'all', '--config', '/absent/task-config.json']))
            run.assert_not_called()

    def test_all_runs_controlled_stages_in_dependency_order_then_distinct_launcher(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / 'config.json'
            config.write_text('{}')
            config.chmod(0o600)
            events = []
            def controlled(stage):
                events.append(stage)
                return 0
            def live(command, env):
                events.append('live')
                self.assertEqual(['--verify-bot-tasks', '--config', str(config)], command[-3:])
                self.assertNotIn('--state', command)
                self.assertEqual(str(config), env['LOCAL_BOT_TASK_CONFIG'])
                return True
            with patch.object(gate, 'controlled_stage', side_effect=controlled), patch.object(gate, 'run', side_effect=live), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(0, gate.main(['--stage', 'all', '--config', str(config)]))
            self.assertEqual(list(gate.CONTROLLED_STAGES) + ['live'], events)
            for failure in gate.CONTROLLED_STAGES:
                events.clear()
                def blocked(stage):
                    events.append(stage)
                    return 1 if stage == failure else 0
                with patch.object(gate, 'controlled_stage', side_effect=blocked), patch.object(gate, 'run') as run, contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(1, gate.main(['--stage', 'all', '--config', str(config)]))
                    run.assert_not_called()
                self.assertEqual(list(gate.CONTROLLED_STAGES[:gate.CONTROLLED_STAGES.index(failure) + 1]), events)

    def test_all_launcher_failure_stays_blocked_and_config_is_not_logged(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / 'private-name.json'
            config.write_text('{}'); config.chmod(0o600)
            output = io.StringIO()
            with patch.object(gate, 'controlled_stage', return_value=0), patch.object(gate, 'run', return_value=False), contextlib.redirect_stderr(output), contextlib.redirect_stdout(output):
                self.assertEqual(1, gate.main(['--stage', 'all', '--config', str(config)]))
            self.assertNotIn(str(config), output.getvalue())
            self.assertNotIn('"status": "passed"', output.getvalue())

    def test_execution_requires_physical_exact_cases(self):
        def runner(command, env, evidence):
            self.assertEqual('1', env['ASTONISH_TEST_TASK_EXECUTION'])
            self.assertIn('-count=1', command)
            return feed_passes(evidence)
        with patch.object(gate, 'run', side_effect=runner), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(0, gate.main(['--stage', 'execution']))
        with patch.object(gate, 'run', return_value=True), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(1, gate.main(['--stage', 'execution']))
        for package, names in gate.EXECUTION.items():
            for omitted in names:
                evidence = ExactGoEvidence(package, names)
                for name in sorted(evidence.allowed - {omitted}):
                    evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'run', 'Test': name}))
                    evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass', 'Test': name}))
                evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass'}))
                self.assertTrue(evidence.errors(), omitted)

    def test_compatibility_drives_exact_http_tests(self):
        def runner(command, env, evidence):
            self.assertIn('-count=1', command)
            self.assertFalse(any(key.endswith('API_KEY') or key.startswith('ASTONISH_') for key in env))
            return feed_passes(evidence)
        with patch.object(gate, 'run', side_effect=runner) as run, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(0, gate.main(['--stage', 'compatibility']))
            self.assertEqual(2, run.call_count)

    def test_no_output_is_failure_even_exit_zero(self):
        with patch.object(gate, 'run', return_value=0), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(1, gate.main(['--stage', 'compatibility']))

    def test_workspace_requires_physical_exact_evidence(self):
        def runner(command, env, evidence):
            self.assertEqual('1', env['ASTONISH_TEST_TASK_WORKSPACE'])
            self.assertIn('-count=1', command)
            return feed_passes(evidence)
        with patch.object(gate, 'run', side_effect=runner) as run, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(0, gate.main(['--stage', 'workspace']))
            self.assertEqual(len(gate.WORKSPACE), run.call_count)
        with patch.object(gate, 'run', return_value=0), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(1, gate.main(['--stage', 'workspace']))

    def test_workspace_rejects_baseline_without_adversarial_docker_cases(self):
        for package in ('pkg/api', 'pkg/launcher'):
            names = gate.WORKSPACE[package]
            evidence = ExactGoEvidence(package, names)
            for name in sorted(names):
                if name.endswith(('/held', '/aborted', '/cancelled-write')):
                    continue
                evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'run', 'Test': name}))
                evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass', 'Test': name}))
            evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass'}))
            self.assertTrue(evidence.errors())
            self.assertEqual(6, sum(name.endswith(('/held', '/aborted', '/cancelled-write')) for name in names))

    def test_search_requires_exact_owned_candidate_evidence(self):
        env = {'ASTONISH_TEST_CONFIGURED_SEARCH': '1', 'ASTONISH_TEST_SEARCH_NODE': '/owned/node'}
        def runner(command, actual_env, evidence):
            self.assertEqual(env, actual_env)
            self.assertIn('-count=1', command)
            return feed_passes(evidence)
        with patch.object(gate, 'search_candidate', return_value=contextlib.nullcontext(env)), patch.object(gate, 'run', side_effect=runner) as run, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(0, gate.main(['--stage', 'search']))
            self.assertEqual(len(gate.SEARCH), run.call_count)
        evidence = ExactGoEvidence('pkg/launcher', gate.SEARCH['pkg/launcher'])
        feed_passes(evidence)
        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'skip', 'Test': 'TestBotConfiguredSearchPhysicalMCP'}))
        self.assertTrue(evidence.errors())
        with patch.object(gate, 'search_candidate', return_value=contextlib.nullcontext(env)), patch.object(gate, 'run', return_value=True), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(1, gate.main(['--stage', 'search']))

    def test_search_command_rejects_missing_or_skipped_physical_evidence(self):
        env = {'ASTONISH_TEST_CONFIGURED_SEARCH': '1', 'ASTONISH_TEST_SEARCH_NODE': '/owned/node'}
        for action in ('missing', 'skip'):
            with self.subTest(action=action):
                def runner(command, actual_env, evidence):
                    if evidence.package != 'github.com/SAP/astonish/pkg/launcher':
                        return feed_passes(evidence)
                    for name in sorted(evidence.allowed):
                        if name == 'TestBotConfiguredSearchPhysicalMCP':
                            if action == 'skip':
                                evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'run', 'Test': name}))
                                evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'skip', 'Test': name}))
                            continue
                        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'run', 'Test': name}))
                        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass', 'Test': name}))
                    evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass'}))
                    return True
                output = io.StringIO()
                with patch.object(gate, 'search_candidate', return_value=contextlib.nullcontext(env)), patch.object(gate, 'run', side_effect=runner), contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(output):
                    self.assertEqual(1, gate.main(['--stage', 'search']))
                self.assertNotIn('"status": "passed"', output.getvalue())

    def test_search_candidate_build_and_architecture_fail_closed(self):
        with patch.object(gate.subprocess, 'run') as inspect, patch.object(gate, 'run', return_value=True) as build:
            inspect.return_value.stdout = 'arm64\n'
            with gate.search_candidate({'PATH': '/bin'}) as env:
                self.assertEqual('1', env['ASTONISH_TEST_CONFIGURED_SEARCH'])
                self.assertEqual('arm64', build.call_args.args[1]['GOARCH'])
                self.assertEqual('0', build.call_args.args[1]['CGO_ENABLED'])
                self.assertEqual('.', build.call_args.args[0][-1])
            inspect.return_value.stdout = 'unknown\n'
            with self.assertRaises(ValueError), gate.search_candidate({'PATH': '/bin'}):
                pass
            inspect.return_value.stdout = 'arm64\n'
            build.return_value = False
            with self.assertRaises(RuntimeError), gate.search_candidate({'PATH': '/bin'}):
                pass

    def test_search_restart_leaves_are_mandatory(self):
        required = {
            'pkg/launcher': {'TestBotSearchFirstRestart', *(f'TestBotSearchFirstRestart/{case}' for case in ('reopen-no-unlock', 'child-cancel-no-mutation', 'stale-cancel-no-mutation', 'cancel-no-late-unlock'))},
            'pkg/store/entstore': {'TestBotPublicResearchRestartQuota', *(f'TestBotPublicResearchRestartQuota/{scope}' for scope in ('personal', 'team')), *(f'TestBotPublicResearchRestartQuota/{scope}/{case}' for scope in ('personal', 'team') for case in ('reopen-ceiling', 'reopen-provenance', 'released-no-replay'))},
            'pkg/daemon': {*(f'TestBotConfiguredSearchHTTPAcceptance/{scope}/restart-receipts' for scope in ('personal', 'team'))},
        }
        for package, restart_names in required.items():
            self.assertTrue(restart_names.issubset(gate.SEARCH.get(package, set())))
            for omitted in restart_names:
                evidence = ExactGoEvidence(package, gate.SEARCH[package])
                for name in sorted(evidence.allowed - {omitted}):
                    evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'run', 'Test': name}))
                    evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass', 'Test': name}))
                evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass'}))
                self.assertTrue(evidence.errors(), omitted)

    def test_continuity_exact_leaves_no_skips(self):
        for package, names in gate.CONTINUITY.items():
            for omitted in names:
                for action in ('missing', 'skip'):
                    evidence = ExactGoEvidence(package, names)
                    for name in sorted(names):
                        if name == omitted and action == 'missing':
                            continue
                        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'run', 'Test': name}))
                        evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'skip' if name == omitted else 'pass', 'Test': name}))
                    evidence.feed(json.dumps({'Package': evidence.package, 'Action': 'pass'}))
                    self.assertTrue(evidence.errors(), (omitted, action))

    def test_continuity_owns_postgres_and_reuses_scheduler(self):
        env = {'ASTONISH_TEST_TASK_CONTINUITY': '1', 'ASTONISH_TEST_TASK_POSTGRES_OWNED': '1'}
        def runner(command, actual_env, evidence):
            self.assertIn('-count=1', command)
            if evidence.package.endswith('/entstore'):
                self.assertIn('-tags=integration', command)
            if 'TestBotTaskScheduleHTTPAcceptance' in evidence.allowed:
                self.assertEqual('1', actual_env['ASTONISH_TEST_TASK_EXECUTION'])
            return feed_passes(evidence)
        output = io.StringIO()
        with patch.object(gate, 'continuity_postgres', return_value=contextlib.nullcontext(env)), patch.object(gate, 'run', side_effect=runner) as run, contextlib.redirect_stdout(output):
            self.assertEqual(0, gate.main(['--stage', 'continuity']))
            self.assertEqual(len(gate.CONTINUITY) + len(gate.EXECUTION), run.call_count)
        report = json.loads(output.getvalue())
        self.assertIn('postgres-full-home-export-restore', report['journeys'])
        self.assertEqual(['kubernetes-physical-home'], report['unverified'])
        with patch.object(gate, 'continuity_postgres', side_effect=RuntimeError('unavailable')), patch.object(gate, 'run') as run, contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(1, gate.main(['--stage', 'continuity']))
            run.assert_not_called()

    def test_owned_postgres_loopback_label_cleanup(self):
        from types import SimpleNamespace
        calls = []
        name = None
        def docker(command, **kwargs):
            nonlocal name
            calls.append(command)
            self.assertNotIn('prune', command)
            if command[1] == 'run':
                name = command[command.index('--name') + 1]
                self.assertEqual('127.0.0.1::5432', command[command.index('--publish') + 1])
                self.assertIn('astonish.task-continuity-owner=' + name, command)
                self.assertEqual('pgvector/pgvector:pg16', command[-1])
                self.assertFalse(any(arg in command for arg in ('--volume', '-v', '--mount', '--privileged')))
            output = '127.0.0.1:15432\n' if command[1] == 'port' else name + '\n' if command[1] == 'inspect' else ''
            return SimpleNamespace(stdout=output, returncode=0)
        with patch.object(gate.subprocess, 'run', side_effect=docker):
            with gate.continuity_postgres({'PATH': '/bin'}) as env:
                self.assertIn('@127.0.0.1:15432/', env['ASTONISH_TEST_DSN'])
                self.assertEqual('1', env['ASTONISH_TEST_TASK_POSTGRES_OWNED'])
        self.assertEqual(['docker', 'rm', '--force', name], calls[-1])

    def test_continuity_manifest_has_all_owned_journeys(self):
        for scope in ('personal', 'team'):
            for case in ('before-restart', 'process-restart-files-state'):
                self.assertIn(f'TestBotTaskContinuityHTTPAcceptance/{scope}/{case}', gate.CONTINUITY['pkg/daemon'])
            for case in ('committed-pin', 'uncertain-effect', 'cancel-approval-revoke', 'reopen-no-replay'):
                self.assertIn(f'TestBotTaskPostgresContinuity/{scope}/{case}', gate.CONTINUITY['pkg/store/entstore'])
            self.assertIn(f'TestBotTaskWorkspaceArchivePostgresDocker/{scope}', gate.CONTINUITY['pkg/store/entstore'])
            self.assertIn(f'TestBotTaskWorkspaceArchiveDocker/{scope}', gate.CONTINUITY['pkg/launcher'])
            for case in ('schedule-three-ticks', 'delete-manual'):
                self.assertIn(f'TestBotTaskScheduleHTTPAcceptance/{scope}/{case}', gate.EXECUTION['pkg/daemon'])
        for field in ('digest', 'task_id', 'bot_id', 'authority_constraint_ref', 'publication_state'):
            self.assertIn(f'TestBotTaskRestoreValidation/{field}', gate.CONTINUITY['pkg/store/entstore'])

    def test_owned_postgres_cleanup_refuses_foreign_label(self):
        from types import SimpleNamespace
        calls = []
        def docker(command, **kwargs):
            calls.append(command)
            output = '127.0.0.1:15432\n' if command[1] == 'port' else 'foreign\n' if command[1] == 'inspect' else ''
            return SimpleNamespace(stdout=output, returncode=0)
        with patch.object(gate.subprocess, 'run', side_effect=docker):
            with self.assertRaisesRegex(RuntimeError, 'ownership label mismatch'):
                with gate.continuity_postgres({'PATH': '/bin'}):
                    pass
        self.assertFalse(any(command[1] == 'rm' for command in calls))

    def test_safe_environment(self):
        self.assertEqual({'PATH': '/bin'}, gate.compatibility_env({'PATH': '/bin', 'ASTONISH_TEST_DSN': 'secret', 'ANTHROPIC_API_KEY': 'secret', 'KUBECONFIG': 'secret', 'ASTONISH_CONFIG': 'secret', 'GOFLAGS': 'unsafe', 'DOCKER_HOST': 'unsafe'}))


if __name__ == '__main__':
    unittest.main()
