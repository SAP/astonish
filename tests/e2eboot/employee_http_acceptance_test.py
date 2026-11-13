import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest

from employee_http_acceptance import (AGENT_TESTS, AgentEvidence, EmployeeHTTPEvidence,
                                      PACKAGE, REQUIRED, QualityEvidence,
                                      SkillsEvidence, SKILLS_TESTS, main)
from unittest.mock import patch


class EmployeeHTTPEvidenceTest(unittest.TestCase):
    def evidence(self, package=PACKAGE, omit=None, skip=None):
        evidence = EmployeeHTTPEvidence()
        for name in REQUIRED - {omit}:
            for action in ("run", "skip" if name == skip else "pass"):
                evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
        evidence.feed(json.dumps(dict(Package=package, Action="pass")))
        return evidence

    def test_complete_daemon_evidence(self):
        self.assertEqual([], self.evidence().errors())

    def test_api_package_cannot_satisfy_gate(self):
        self.assertTrue(self.evidence(package="github.com/SAP/astonish/pkg/api").errors())

    def test_no_tests_is_failure(self):
        self.assertTrue(EmployeeHTTPEvidence().errors())

    def test_missing_priority_subtest_is_failure(self):
        self.assertTrue(self.evidence(omit="TestBotEmployeeOperationsHTTPAcceptance/team/priority_pause").errors())

    def test_skipped_subtest_is_failure(self):
        self.assertTrue(self.evidence(skip="TestBotEmployeeOperationsHTTPAcceptance/personal/deny").errors())


class QualityEvidenceTest(unittest.TestCase):
    def test_runtime_requires_verdict_and_explanation_recovery(self):
        for scope in ("personal", "team"):
            for scenario in ("citation-verdict", "citation-explanation"):
                missing = f"TestBotContentQualityHTTPAcceptance/{scope}/{scenario}"
                evidence = QualityEvidence("runtime")
                self.assertIn(missing, evidence.required_names)
                for name in evidence.required_names - {missing}:
                    for action in ("run", "pass"):
                        evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
                self.assertTrue(evidence.errors())

    def test_runtime_requires_missing_evidence_recovery_in_both_scopes(self):
        for scope in ("personal", "team"):
            missing = f"TestBotContentQualityHTTPAcceptance/{scope}/citation-missing-evidence"
            evidence = QualityEvidence("runtime")
            self.assertIn(missing, evidence.required_names)
            for name in evidence.required_names - {missing}:
                for action in ("run", "pass"):
                    evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
            evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
            self.assertTrue(evidence.errors())

    def test_runtime_requires_observation_regression_in_both_scopes(self):
        for scope in ("personal", "team"):
            missing = f"TestBotContentQualityHTTPAcceptance/{scope}/citation-observation"
            evidence = QualityEvidence("runtime")
            self.assertIn(missing, evidence.required_names)
            for name in evidence.required_names - {missing}:
                for action in ("run", "pass"):
                    evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
            evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
            self.assertTrue(evidence.errors())
        with patch("employee_http_acceptance.run", return_value=True) as run:
            self.assertEqual(0, main(["--quality-stage", "runtime"]))
            self.assertIn("TestBotContentQualityHTTPAcceptance", " ".join(run.call_args.args[0]))

    def test_browser_environment_is_explicit_and_scoped(self):
        from employee_http_acceptance import quality_env
        source = {"ASTONISH_MEMORY_BROWSER_ACCEPTANCE": "1", "ASTONISH_BOT_BROWSER_PATH": "/explicit/browser", "ASTONISH_TEST_DSN": "must-not-leak"}
        env = quality_env("browser", source)
        self.assertEqual("1", env["ASTONISH_MEMORY_BROWSER_ACCEPTANCE"])
        self.assertEqual("/explicit/browser", env["ASTONISH_BOT_BROWSER_PATH"])
        self.assertNotIn("ASTONISH_TEST_DSN", env)
        self.assertNotIn("ASTONISH_MEMORY_BROWSER_ACCEPTANCE", quality_env("runtime", source))

    def test_quality_requires_exact_package_and_all_cases(self):
        for stage in ("publication", "runtime", "browser", "terminal"):
            for mode in ("valid", "missing", "skip", "foreign"):
                evidence = QualityEvidence(stage)
                names = sorted(evidence.required_names)
                for name in names:
                    if mode == "missing" and name == names[-1]:
                        continue
                    package = "foreign/package" if mode == "foreign" else evidence.package
                    for action in ("run", "skip" if mode == "skip" else "pass"):
                        evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
                self.assertEqual(mode != "valid", bool(evidence.errors()), (stage, mode))


class SkillsEvidenceTest(unittest.TestCase):
    def test_scheduling_manifest_and_strict_evidence(self):
        cases = {f"{scope}/{case}" for scope in ("personal", "team") for case in
                 ("create-schedule", "duplicate-wake", "feedback-latest", "denied-resource",
                  "task-once", "remove-then-manual", "import-rollback", "foreign-import", "legacy-import")}
        self.assertEqual(cases, set(SKILLS_TESTS["scheduling"][2]))
        for mode in ("valid", "missing", "skip", "fail", "foreign", "pass-only", "no-package"):
            evidence = SkillsEvidence("scheduling")
            for name in sorted(evidence.required_names):
                if mode == "missing" and name.endswith("/task-once"):
                    continue
                package = "foreign/package" if mode == "foreign" else evidence.package
                actions = ("pass",) if mode == "pass-only" else ("run", mode if mode in ("skip", "fail") else "pass")
                for action in actions:
                    evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
            if mode != "no-package":
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
            self.assertEqual(mode != "valid", bool(evidence.errors()), mode)
        self.assertTrue(SkillsEvidence("scheduling").errors())
        with patch("employee_http_acceptance.run", return_value=True) as run:
            self.assertEqual(0, main(["--skills-stage", "scheduling"]))
            self.assertIn("./pkg/daemon", run.call_args.args[0])

    def test_storage_manifest_is_complete(self):
        self.assertEqual({"empty", "worker-create-owner-read", "cross-bot", "personal-team",
                          "replay-digest", "concurrent-revisions", "expired-lease", "pin-reopen",
                          "quota", "schema-upgrade"}, set(SKILLS_TESTS["storage"][2]))

    def test_every_named_case_must_run_and_pass_in_api_package(self):
        for mode in ("valid", "missing", "skip", "fail", "foreign", "pass-only", "no-package"):
            evidence = SkillsEvidence("storage")
            names = sorted(evidence.required_names)
            for name in names:
                if mode == "missing" and name == names[-1]:
                    continue
                package = "foreign/package" if mode == "foreign" else evidence.package
                actions = ("pass",) if mode == "pass-only" else ("run", mode if mode in ("skip", "fail") else "pass")
                for action in actions:
                    evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
            if mode != "no-package":
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
            self.assertEqual(mode != "valid", bool(evidence.errors()), mode)
        self.assertTrue(SkillsEvidence("storage").errors())

    def test_storage_command_and_environment(self):
        with patch("employee_http_acceptance.run", return_value=True) as run, patch.dict(
                "os.environ", {"ASTONISH_TEST_DSN": "forbidden", "OPENAI_API_KEY": "forbidden",
                               "KUBECONFIG": "forbidden"}):
            self.assertEqual(0, main(["--skills-stage", "storage"]))
            command, env, evidence = run.call_args.args
            self.assertIn("./pkg/api", command)
            self.assertIsInstance(evidence, SkillsEvidence)
            for key in ("ASTONISH_TEST_DSN", "OPENAI_API_KEY", "KUBECONFIG"):
                self.assertNotIn(key, env)
        with patch("employee_http_acceptance.run", return_value=False):
            self.assertEqual(1, main(["--skills-stage", "storage"]))
        with self.assertRaises(SystemExit):
            main(["--skills-stage", "storage", "--quality-stage", "runtime"])


class AgentEvidenceTest(unittest.TestCase):
    stage = "execution"
    run_pattern = "^(TestBotAgentWorkHTTPAcceptance)$"

    def test_execution_manifest(self):
        cases = ("personal", "team", "personal/interactive", "personal/autonomous",
                 "team/interactive", "team/autonomous")
        self.assertEqual({"execution", "progress", "learning"}, set(AGENT_TESTS))
        self.assertEqual(("./pkg/api", "TestBotAgentWorkHTTPAcceptance", cases), AGENT_TESTS["execution"])
        self.assertEqual({"TestBotAgentWorkHTTPAcceptance", *(
            "TestBotAgentWorkHTTPAcceptance/" + case for case in cases)},
            AgentEvidence("execution").required_names)

    def test_each_named_case_requires_run_and_pass_in_api_package(self):
        modes = ("valid", "missing", "skip", "fail", "foreign", "pass-only",
                 "run-only", "pass-before-run", "split-package", "no-package",
                 "package-fail", "package-skip")
        for target in sorted(AgentEvidence(self.stage).required_names):
            for mode in modes:
                with self.subTest(target=target, mode=mode):
                    evidence = AgentEvidence(self.stage)
                    for name in sorted(evidence.required_names):
                        if name == target and mode == "missing":
                            continue
                        actions = ("run", "pass")
                        if name == target:
                            actions = {
                                "skip": ("run", "skip"), "fail": ("run", "fail"),
                                "pass-only": ("pass",), "run-only": ("run",),
                                "pass-before-run": ("pass", "run"),
                            }.get(mode, actions)
                        for action in actions:
                            package = evidence.package
                            if name == target and (mode == "foreign" or
                                                   (mode == "split-package" and action == "run")):
                                package = "foreign/package"
                            evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
                    if mode != "no-package":
                        action = {"package-fail": "fail", "package-skip": "skip"}.get(mode, "pass")
                        evidence.feed(json.dumps(dict(Package=evidence.package, Action=action)))
                    self.assertEqual(mode != "valid", bool(evidence.errors()))
        self.assertTrue(AgentEvidence(self.stage).errors())

    def test_execution_command_environment_and_exit_status(self):
        with patch("employee_http_acceptance.run", return_value=True) as run, patch.dict(
                "os.environ", {"ASTONISH_TEST_DSN": "forbidden", "OPENAI_API_KEY": "forbidden",
                               "ASTONISH_CONFIG": "forbidden", "GOFLAGS": "forbidden",
                               "KUBECONFIG": "forbidden"}):
            self.assertEqual(0, main(["--agent-stage", self.stage]))
            command, env, evidence = run.call_args.args
            self.assertIn("./pkg/api", command)
            self.assertIn("-json", command)
            self.assertIn("-count=1", command)
            self.assertEqual(self.run_pattern, command[command.index("-run") + 1])
            run.assert_called_once()
            self.assertIsInstance(evidence, AgentEvidence)
            self.assertEqual(AgentEvidence(self.stage).required_names, evidence.required_names)
            for key in ("ASTONISH_TEST_DSN", "OPENAI_API_KEY", "ASTONISH_CONFIG", "GOFLAGS", "KUBECONFIG"):
                self.assertNotIn(key, env)
        with patch("employee_http_acceptance.run", return_value=False):
            self.assertEqual(1, main(["--agent-stage", self.stage]))

    def test_execution_rejects_other_stages_config_and_unimplemented_choices(self):
        invalid = [
            ["--agent-stage", self.stage, "--quality-stage", "runtime"],
            ["--agent-stage", self.stage, "--skills-stage", "storage"],
            ["--agent-stage", self.stage, "--config", "/isolated/config.yaml"],
        ]
        invalid.extend(["--agent-stage", stage] for stage in
                       ("all",))
        for argv in invalid:
            with self.subTest(argv=argv), patch("employee_http_acceptance.run") as run:
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as error:
                    main(argv)
                self.assertEqual(2, error.exception.code)
                run.assert_not_called()


class AgentLearningEvidenceTest(AgentEvidenceTest):
    stage = "learning"
    run_pattern = "^(TestBotAgentLearningHTTPAcceptance|TestBotAgentLearningRejectionHTTPAcceptance|TestBotAgentLearningEligibilityHTTPAcceptance|TestBotAgentOptionalWorkSchemaHTTPAcceptance)$"

    def test_learning_manifest(self):
        modes = tuple(f"{scope}/{mode}" for scope in ("personal", "team")
                      for mode in ("interactive", "autonomous"))
        self.assertEqual(("./pkg/api", "TestBotAgentLearningHTTPAcceptance", modes), AGENT_TESTS["learning"])
        expected = {"TestBotAgentLearningHTTPAcceptance",
                    "TestBotAgentLearningRejectionHTTPAcceptance",
                    "TestBotAgentLearningEligibilityHTTPAcceptance",
                    "TestBotAgentOptionalWorkSchemaHTTPAcceptance"}
        expected.update(f"TestBotAgentLearningHTTPAcceptance/{mode}" for mode in modes)
        expected.update(f"TestBotAgentLearningRejectionHTTPAcceptance/{mode}/{scenario}"
                        for mode in modes for scenario in (
                            "no-skill", "missing-evidence", "evidence-bound",
                            "unsupported-owner-quotes", "unknown-field", "wrong-type"))
        expected.update(f"TestBotAgentLearningEligibilityHTTPAcceptance/{mode}/{scenario}"
                        for mode in modes for scenario in (
                            "partial-method", "unsupported-method", "rejected-method",
                            "supported-method", "preselection-only", "postselection-use"))
        expected.update(f"TestBotAgentOptionalWorkSchemaHTTPAcceptance/{mode}/{tool}"
                        for mode in ("interactive", "autonomous") for tool in (
                            "bot_work_read", "bot_work_update", "bot_evidence_read", "bot_check_work"))
        self.assertEqual(expected, AgentEvidence("learning").required_names)

    def test_learning_cannot_pass_without_every_suite(self):
        for omitted in ("TestBotAgentLearningHTTPAcceptance",
                        "TestBotAgentLearningRejectionHTTPAcceptance",
                        "TestBotAgentLearningEligibilityHTTPAcceptance",
                        "TestBotAgentOptionalWorkSchemaHTTPAcceptance"):
            def incomplete_run(command, env, evidence):
                for name in evidence.required_names:
                    if name == omitted or name.startswith(omitted + "/"):
                        continue
                    for action in ("run", "pass"):
                        evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
                return not evidence.errors()

            with self.subTest(omitted=omitted), patch("employee_http_acceptance.run", side_effect=incomplete_run):
                self.assertEqual(1, main(["--agent-stage", "learning"]))


class AgentProgressEvidenceTest(AgentEvidenceTest):
    # Exercise the same strict evidence, isolation and CLI contract for both stages.
    stage = "progress"
    run_pattern = "^(TestBotAgentProgressHTTPAcceptance|TestBotAgentCheckHTTPAcceptance|TestBotAgentSourceFallbackHTTPAcceptance)$"

    def test_progress_manifest_requires_all_modes_checks_and_sources(self):
        modes = tuple(f"{scope}/{mode}" for scope in ("personal", "team")
                      for mode in ("interactive", "autonomous"))
        self.assertEqual(("./pkg/api", "TestBotAgentProgressHTTPAcceptance", modes),
                         AGENT_TESTS["progress"])
        expected = {"TestBotAgentProgressHTTPAcceptance", "TestBotAgentCheckHTTPAcceptance",
                    "TestBotAgentSourceFallbackHTTPAcceptance"}
        expected.update(f"TestBotAgentProgressHTTPAcceptance/{mode}" for mode in modes)
        expected.update(f"TestBotAgentCheckHTTPAcceptance/{mode}/broken={broken}"
                        for mode in modes for broken in ("false", "true"))
        expected.update(f"TestBotAgentSourceFallbackHTTPAcceptance/{mode}/{source}"
                        for mode in modes for source in ("rate-limited", "footer-only"))
        self.assertEqual(expected, AgentEvidence("progress").required_names)

    def test_progress_cannot_pass_with_only_one_suite(self):
        for omitted in ("TestBotAgentProgressHTTPAcceptance", "TestBotAgentCheckHTTPAcceptance",
                        "TestBotAgentSourceFallbackHTTPAcceptance"):
            def incomplete_run(command, env, evidence):
                for name in evidence.required_names:
                    if name == omitted or name.startswith(omitted + "/"):
                        continue
                    for action in ("run", "pass"):
                        evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
                return not evidence.errors()

            with self.subTest(omitted=omitted), patch("employee_http_acceptance.run", side_effect=incomplete_run):
                self.assertEqual(1, main(["--agent-stage", "progress"]))

    def test_skip_or_failure_is_fatal_even_after_required_passes(self):
        for action in ("skip", "fail"):
            with self.subTest(action=action):
                evidence = AgentEvidence("progress")
                for name in evidence.required_names:
                    for event in ("run", "pass"):
                        evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=event)))
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
                evidence.feed(json.dumps(dict(Package=evidence.package, Test="unexpected", Action=action)))
                self.assertTrue(evidence.errors())


class AgentContinuityDriverTest(unittest.TestCase):
    def test_continuity_manifest_requires_current_restart_occurrences(self):
        from employee_http_acceptance import AGENT_CONTINUITY_TESTS
        cases = tuple(f'{scope}/{scenario}' for scope in ('personal', 'team')
                      for scenario in ('create-schedule', 'duplicate-wake', 'feedback-latest',
                                       'remaining-fresh-occurrences-after-restart',
                                       'denied-resource', 'task-once', 'remove-then-manual',
                                       'import-rollback', 'foreign-import', 'legacy-import'))
        self.assertEqual((
            ('pkg/api', 'TestBotAgentContinuityHTTPAcceptance',
             ('sqlite-restart-once', 'cancel-before-restart-no-replay')),
            ('pkg/daemon', 'TestBotAgentScheduleHTTPAcceptance', cases),
        ), AGENT_CONTINUITY_TESTS)

    def test_continuity_rejects_unknown_cases_even_after_complete_passes(self):
        for scope in ('personal', 'team'):
            for scenario in ('third-fresh-occurrence', 'unknown-occurrence'):
                def execute(command, env, evidence):
                    for name in sorted(evidence.allowed):
                        for action in ('run', 'pass'):
                            evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
                    if evidence.package.endswith('/pkg/daemon'):
                        name = f'TestBotAgentScheduleHTTPAcceptance/{scope}/{scenario}'
                        for action in ('run', 'pass'):
                            evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
                    evidence.feed(json.dumps(dict(Package=evidence.package, Action='pass')))
                    return not evidence.errors()

                with self.subTest(scope=scope, scenario=scenario), patch(
                        'employee_http_acceptance.run', side_effect=execute) as run:
                    self.assertEqual(1, main(['--agent-stage', 'continuity']))
                    self.assertEqual(2, run.call_count)

    def test_continuity_requires_both_packages_and_every_case(self):
        from employee_http_acceptance import AGENT_CONTINUITY_TESTS
        for target_package, target_test, cases in AGENT_CONTINUITY_TESTS:
            for target in (target_test, *(target_test + '/' + case for case in cases)):
                for mode in ('valid', 'missing', 'skip', 'fail', 'foreign', 'pass-only',
                             'duplicate', 'no-package', 'package-skip', 'package-fail'):
                    def execute(command, env, evidence):
                        for name in sorted(evidence.allowed):
                            if name == target and mode == 'missing':
                                continue
                            actions = ('run', 'pass')
                            if name == target:
                                actions = {'skip': ('run', 'skip'), 'fail': ('run', 'fail'),
                                           'pass-only': ('pass',),
                                           'duplicate': ('run', 'pass', 'run', 'pass')}.get(mode, actions)
                            for action in actions:
                                package = 'foreign/package' if name == target and mode == 'foreign' else evidence.package
                                evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
                        if not (mode == 'no-package' and evidence.package.endswith(target_package)):
                            action = {'package-skip': 'skip', 'package-fail': 'fail'}.get(mode, 'pass')
                            evidence.feed(json.dumps(dict(Package=evidence.package, Action=action)))
                        return not evidence.errors()

                    with self.subTest(target=target, mode=mode), patch('employee_http_acceptance.run', side_effect=execute) as run:
                        self.assertEqual(0 if mode == 'valid' else 1, main(['--agent-stage', 'continuity']))
                        if mode == 'valid':
                            self.assertEqual(2, run.call_count)
                            self.assertEqual(['./pkg/api', './pkg/daemon'], [
                                call.args[0][-3] for call in run.call_args_list])

    def test_continuity_commands_and_environment(self):
        from employee_http_acceptance import AGENT_CONTINUITY_TESTS
        with patch('employee_http_acceptance.run', return_value=True) as run, patch.dict(
                'os.environ', {'ASTONISH_TEST_DSN': 'forbidden', 'ASTONISH_BOTS_TEST_DB_ISOLATED': '1',
                               'ASTONISH_CONFIG': 'forbidden', 'OPENAI_API_KEY': 'forbidden',
                               'GOFLAGS': 'forbidden', 'KUBECONFIG': 'forbidden'}, clear=True):
            self.assertEqual(0, main(['--agent-stage', 'continuity']))
            for call, (package, test, cases) in zip(run.call_args_list, AGENT_CONTINUITY_TESTS):
                command, env, evidence = call.args
                self.assertIn('./' + package, command)
                self.assertEqual('^(' + test + ')$', command[-1])
                self.assertIn('-count=1', command)
                self.assertIn('-json', command)
                self.assertFalse(any(arg.startswith('-tags') for arg in command))
                self.assertEqual({}, env)
                self.assertEqual({test, *(test+'/'+case for case in cases)}, evidence.required)
        with patch('employee_http_acceptance.run', side_effect=(True, False)) as run:
            self.assertEqual(1, main(['--agent-stage', 'continuity']))
            self.assertEqual(2, run.call_count)

    def test_postgres_manifest_matches_implemented_suites_and_all_leaves(self):
        from employee_http_acceptance import AGENT_POSTGRES_TESTS, agent_postgres_missing_tests
        self.assertEqual((
            ('pkg/store/entstore', 'TestBotAgentPostgresContinuityCASProjection', ('personal', 'team')),
            ('pkg/store/entstore', 'TestBotAgentPostgresContinuityRestoreQuiescence', (
                'personal/agent/running', 'personal/agent/waiting',
                'personal/legacy/running', 'personal/legacy/waiting',
                'team/agent/running', 'team/agent/waiting',
                'team/legacy/running', 'team/legacy/waiting')),
        ), AGENT_POSTGRES_TESTS)
        self.assertEqual([], agent_postgres_missing_tests())

    def test_postgres_every_exact_case_requires_current_run_pass_and_package(self):
        from employee_http_acceptance import AGENT_POSTGRES_TESTS
        modes = ('valid', 'missing', 'skip', 'fail', 'foreign', 'pass-only',
                 'run-only', 'pass-before-run', 'split-package', 'duplicate',
                 'renamed', 'no-package', 'package-skip', 'package-fail',
                 'unexpected-skip', 'legacy-substitute')
        for _, test, cases in AGENT_POSTGRES_TESTS:
            for target in (test, *(test + '/' + case for case in cases)):
                for mode in modes:
                    def execute(command, env, evidence):
                        targeted = target in evidence.required
                        for name in sorted(evidence.allowed):
                            if name == target and mode == 'missing':
                                continue
                            actions = ('run', 'pass')
                            if name == target:
                                actions = {
                                    'skip': ('run', 'skip'), 'fail': ('run', 'fail'),
                                    'pass-only': ('pass',), 'run-only': ('run',),
                                    'pass-before-run': ('pass', 'run'),
                                    'duplicate': ('run', 'pass', 'run', 'pass'),
                                }.get(mode, actions)
                            for action in actions:
                                package, emitted = evidence.package, name
                                if name == target:
                                    if mode == 'foreign' or (mode == 'split-package' and action == 'run'):
                                        package = 'foreign/package'
                                    if mode == 'renamed':
                                        emitted += '-unexpected'
                                    if mode == 'legacy-substitute':
                                        emitted = 'TestBotSkillPostgresRecovery/team'
                                evidence.feed(json.dumps(dict(Package=package, Test=emitted, Action=action)))
                        if targeted and mode == 'unexpected-skip':
                            evidence.feed(json.dumps(dict(Package=evidence.package, Test='unexpected', Action='skip')))
                        if not (targeted and mode == 'no-package'):
                            action = {'package-skip': 'skip', 'package-fail': 'fail'}.get(mode, 'pass') if targeted else 'pass'
                            evidence.feed(json.dumps(dict(Package=evidence.package, Action=action)))
                        return not evidence.errors()

                    with self.subTest(target=target, mode=mode), patch.dict('os.environ', {
                            'ASTONISH_TEST_DSN': 'explicit-disposable',
                            'ASTONISH_BOTS_TEST_DB_ISOLATED': '1'}, clear=True), patch(
                            'employee_http_acceptance.agent_postgres_missing_tests', return_value=[]), patch(
                            'employee_http_acceptance.run', side_effect=execute) as run:
                        self.assertEqual(0 if mode == 'valid' else 1, main(['--agent-stage', 'postgres']))
                        self.assertEqual(2 if mode == 'valid' or test == AGENT_POSTGRES_TESTS[1][1] else 1,
                                         run.call_count)

    def test_postgres_rejects_partial_or_empty_disposable_inputs_before_any_command(self):
        for source in ({'ASTONISH_TEST_DSN': ''}, {'ASTONISH_BOTS_TEST_DB_ISOLATED': ''},
                       {'ASTONISH_TEST_DSN': 'secret'}, {'ASTONISH_BOTS_TEST_DB_ISOLATED': '1'},
                       {'ASTONISH_TEST_DSN': 'secret', 'ASTONISH_BOTS_TEST_DB_ISOLATED': 'true'},
                       {'ASTONISH_TEST_DSN': ' ', 'ASTONISH_BOTS_TEST_DB_ISOLATED': '1'}):
            stderr = io.StringIO()
            with self.subTest(source=source), patch.dict('os.environ', source, clear=True), patch(
                    'employee_http_acceptance.run') as run, patch(
                    'employee_http_acceptance.agent_postgres_missing_tests') as missing, contextlib.redirect_stderr(stderr):
                self.assertEqual(2, main(['--agent-stage', 'postgres']))
                run.assert_not_called()
                missing.assert_not_called()
                self.assertIn('ASTONISH_BOTS_TEST_DB_ISOLATED=1', stderr.getvalue())
                self.assertNotIn('secret', stderr.getvalue())

    def test_postgres_without_either_input_runs_owned_strict_child(self):
        import sys
        from employee_http_acceptance import ROOT
        for status in (0, 1, 2):
            with patch.dict('os.environ', {'OPENAI_API_KEY': 'secret'}, clear=True), patch(
                    'employee_http_acceptance.run_owned_postgres', return_value=status) as owned, patch(
                    'employee_http_acceptance.agent_postgres_missing_tests', return_value=[]), patch(
                    'employee_http_acceptance.run') as run:
                self.assertEqual(status, main(['--agent-stage', 'postgres']))
                owned.assert_called_once_with([
                    sys.executable, '-B', str(ROOT / 'tests/e2eboot/employee_http_acceptance.py'),
                    '--agent-stage', 'postgres'], {})
                run.assert_not_called()

    def test_postgres_source_preflight_requires_exact_agent_declarations(self):
        from employee_http_acceptance import AGENT_POSTGRES_TESTS, agent_postgres_missing_tests
        names = [test for _, test, _ in AGENT_POSTGRES_TESTS]
        for declared in ([], names[:1], names):
            with self.subTest(declared=declared), patch('employee_http_acceptance.ROOT') as root:
                store = root.__truediv__.return_value
                source = store.glob.return_value = [unittest.mock.Mock()]
                source[0].read_text.return_value = '\n'.join(
                    'func ' + name + '(t *testing.T) {}' for name in declared) + (
                    '\nfunc TestBotSkillPostgresRecovery(t *testing.T) {}'
                    '\n// func ' + names[-1] + '(t *testing.T) {}')
                self.assertEqual([name for name in names if name not in declared],
                                 agent_postgres_missing_tests())
                root.__truediv__.assert_called_once_with('pkg/store/entstore')
                store.glob.assert_called_once_with('*_test.go')

    def test_postgres_missing_agent_tests_cannot_use_legacy_evidence(self):
        from employee_http_acceptance import AGENT_POSTGRES_TESTS
        names = [test for _, test, _ in AGENT_POSTGRES_TESTS]
        stderr = io.StringIO()
        with patch.dict('os.environ', {'ASTONISH_TEST_DSN': 'secret', 'ASTONISH_BOTS_TEST_DB_ISOLATED': '1'}, clear=True), patch(
                'employee_http_acceptance.agent_postgres_missing_tests', return_value=names), patch(
                'employee_http_acceptance.run') as run, contextlib.redirect_stderr(stderr):
            self.assertEqual(2, main(['--agent-stage', 'postgres']))
            run.assert_not_called()
        for name in names:
            self.assertIn(name, stderr.getvalue())
        self.assertNotIn('secret', stderr.getvalue())

    def test_postgres_integration_command_preserves_only_explicit_test_db(self):
        from employee_http_acceptance import AGENT_POSTGRES_TESTS
        for result in (True, False):
            with patch.dict('os.environ', {'ASTONISH_TEST_DSN': 'explicit-disposable',
                    'ASTONISH_BOTS_TEST_DB_ISOLATED': '1', 'ASTONISH_DSN': 'forbidden',
                    'OPENAI_API_KEY': 'forbidden', 'GOFLAGS': 'forbidden', 'KUBECONFIG': 'forbidden'}, clear=True), patch(
                    'employee_http_acceptance.agent_postgres_missing_tests', return_value=[]), patch(
                    'employee_http_acceptance.run', return_value=result) as run:
                self.assertEqual(0 if result else 1, main(['--agent-stage', 'postgres']))
                self.assertEqual(2 if result else 1, run.call_count)
                for call, (package, test, cases) in zip(run.call_args_list, AGENT_POSTGRES_TESTS):
                    command, env, evidence = call.args
                    self.assertIn('./' + package, command)
                    self.assertIn('-json', command)
                    self.assertIn('-count=1', command)
                    self.assertIn('-tags=integration', command)
                    self.assertEqual('^(' + test + ')$', command[-1])
                    self.assertEqual({'ASTONISH_TEST_DSN': 'explicit-disposable',
                                      'ASTONISH_BOTS_TEST_DB_ISOLATED': '1',
                                      'ASTONISH_REQUIRE_SKILL_POSTGRES': '1'}, env)
                    self.assertEqual({test, *(test+'/'+case for case in cases)}, evidence.required)

    def test_postgres_skipped_required_case_is_failure(self):
        def skipped(command, env, evidence):
            for name in evidence.allowed:
                for action in ('run', 'skip' if name.endswith('/team') else 'pass'):
                    evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
            evidence.feed(json.dumps(dict(Package=evidence.package, Action='pass')))
            return not evidence.errors()

        with patch.dict('os.environ', {'ASTONISH_TEST_DSN': 'explicit-disposable', 'ASTONISH_BOTS_TEST_DB_ISOLATED': '1'}, clear=True), patch(
                'employee_http_acceptance.agent_postgres_missing_tests', return_value=[]), patch(
                'employee_http_acceptance.run', side_effect=skipped):
            self.assertEqual(1, main(['--agent-stage', 'postgres']))


class AgentAllDriverTest(unittest.TestCase):
    source = {'ASTONISH_TEST_DSN': 'explicit-disposable',
              'ASTONISH_BOTS_TEST_DB_ISOLATED': '1',
              'OPENAI_API_KEY': 'must-not-inherit', 'KUBECONFIG': 'must-not-inherit',
              'LOCAL_BOT_STATE': '/historical/state.json'}
    config = '/private/agent-config.json'

    def test_all_requires_every_current_stage_then_build_surfaces_and_live(self):
        from employee_http_acceptance import ROOT
        import sys
        for invocation in range(2):
            with self.subTest(invocation=invocation), patch.dict('os.environ', self.source, clear=True), patch(
                    'employee_http_acceptance.agent_all_environment', return_value={
                        'ASTONISH_BOT_BROWSER_PATH': '/explicit/browser'}), patch(
                    'employee_http_acceptance.agent_postgres_missing_tests', return_value=[]), patch(
                    'employee_http_acceptance.run', return_value=True) as run, contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(0, main(['--agent-stage', 'all', '--config', self.config]))
                self.assertEqual(10, run.call_count)
                go_calls = run.call_args_list[:7]
                expected = ('TestBotAgentWorkHTTPAcceptance', 'TestBotAgentProgressHTTPAcceptance',
                            'TestBotAgentLearningHTTPAcceptance', 'TestBotAgentContinuityHTTPAcceptance',
                            'TestBotAgentScheduleHTTPAcceptance', 'TestBotAgentPostgresContinuityCASProjection',
                            'TestBotAgentPostgresContinuityRestoreQuiescence')
                for call, name in zip(go_calls, expected):
                    command, env, evidence = call.args
                    self.assertIn(name, command[-1])
                    self.assertIn('-count=1', command)
                    self.assertTrue(evidence.errors())  # new collector, never cached evidence
                    for forbidden in ('OPENAI_API_KEY', 'KUBECONFIG', 'LOCAL_BOT_STATE'):
                        self.assertNotIn(forbidden, env)
                    if 'Postgres' not in name:
                        self.assertNotIn('ASTONISH_TEST_DSN', env)
                    else:
                        self.assertEqual('explicit-disposable', env['ASTONISH_TEST_DSN'])
                self.assertEqual(['npm', 'run', 'build'], run.call_args_list[7].args[0])
                self.assertEqual(ROOT / 'web', run.call_args_list[7].kwargs['cwd'])
                self.assertEqual([sys.executable, str(ROOT / 'tests/e2eboot/bot_skill_surface_acceptance.py'),
                                  '--browser', '--terminal', '--agent'], run.call_args_list[8].args[0])
                self.assertEqual([sys.executable, str(ROOT / 'tests/e2eboot/local_bot_smoke_launcher.py'),
                                  '--verify-bot-agent', '--config', self.config], run.call_args_list[9].args[0])
                self.assertNotIn('ASTONISH_TEST_DSN', run.call_args_list[9].args[1])
                self.assertTrue(json.loads(output.getvalue())['full_agent_acceptance'])

    def test_failure_at_any_gate_stops_without_full_pass(self):
        for failing in range(10):
            with self.subTest(failing=failing), patch.dict('os.environ', self.source, clear=True), patch(
                    'employee_http_acceptance.agent_all_environment', return_value={}), patch(
                    'employee_http_acceptance.agent_postgres_missing_tests', return_value=[]), patch(
                    'employee_http_acceptance.run', side_effect=[True] * failing + [False]) as run, contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(1, main(['--agent-stage', 'all', '--config', self.config]))
                self.assertEqual(failing + 1, run.call_count)
                self.assertNotIn('full_agent_acceptance', output.getvalue())

    def test_missing_config_cannot_fall_back_to_ambient_config_or_receipts(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict('os.environ', {
                **self.source, 'LOCAL_BOT_AGENT_CONFIG': '/ambient/config.json'}), patch(
                'employee_http_acceptance.run') as run, patch(
                'local_bot_smoke_launcher.config_environment') as configured, contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(2, main(['--agent-stage', 'all', '--config', str(Path(directory) / 'missing-secret.json')]))
            run.assert_not_called()
            configured.assert_not_called()
            self.assertNotIn('missing-secret', stderr.getvalue())
            self.assertIn('config preflight failed', stderr.getvalue())

    def test_config_preflight_validates_identity_assets_and_drops_ambient_values(self):
        from employee_http_acceptance import agent_all_environment
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / 'agent.json'
            config.write_text('{"schema_version": 1}')
            config.chmod(0o600)
            with patch('local_bot_smoke_launcher.require_agent_config') as identity, patch(
                    'local_bot_smoke_launcher.config_environment', return_value={
                        'ASTONISH_BOT_BROWSER_PATH': '/explicit/browser',
                        'OPENAI_API_KEY': 'private', 'LOCAL_BOT_STATE': 'old'}) as assets:
                self.assertEqual({'ASTONISH_BOT_BROWSER_PATH': '/explicit/browser'},
                                 agent_all_environment(config, self.source))
                identity.assert_called_once_with({'schema_version': 1})
                assets.assert_called_once_with({'schema_version': 1})
                for path in (Path('relative.json'), config.with_name('link.json'), config):
                    if path.name == 'link.json':
                        path.symlink_to(config)
                    if path == config:
                        config.chmod(0o644)
                    with self.subTest(path=path), self.assertRaises(ValueError):
                        agent_all_environment(path, self.source)
                identity.assert_called_once()

    def test_invalid_config_is_redacted_and_blocks_before_commands(self):
        for failure in (ValueError('secret-provider'), OSError('secret-path')):
            with patch('employee_http_acceptance.agent_all_environment', side_effect=failure), patch(
                    'employee_http_acceptance.run') as run, contextlib.redirect_stderr(io.StringIO()) as stderr:
                self.assertEqual(2, main(['--agent-stage', 'all', '--config', self.config]))
                run.assert_not_called()
                self.assertNotIn('secret-', stderr.getvalue())

    def test_all_without_postgres_configuration_defers_to_owned_stage(self):
        for status in (0, 1, 2):
            with patch.dict('os.environ', {}, clear=True), patch(
                    'employee_http_acceptance.agent_all_environment', return_value={}), patch(
                    'employee_http_acceptance.agent_postgres_missing_tests', return_value=[]), patch(
                    'employee_http_acceptance.run_owned_postgres', return_value=status) as owned, patch(
                    'employee_http_acceptance.run', return_value=True) as run, contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(status, main(['--agent-stage', 'all', '--config', self.config]))
                owned.assert_called_once()
                self.assertEqual(8 if status == 0 else 5, run.call_count)

    def test_postgres_preflight_blocks_before_controlled_or_paid_calls(self):
        for source, missing in (({'ASTONISH_TEST_DSN': 'secret'}, []),
                                ({'ASTONISH_BOTS_TEST_DB_ISOLATED': '1'}, []),
                                (self.source, ['TestMissing'])):
            with patch.dict('os.environ', source, clear=True), patch(
                    'employee_http_acceptance.agent_all_environment', return_value={}), patch(
                    'employee_http_acceptance.agent_postgres_missing_tests', return_value=missing), patch(
                    'employee_http_acceptance.run') as run, contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(2, main(['--agent-stage', 'all', '--config', self.config]))
                run.assert_not_called()

    def test_agent_evidence_rejects_duplicate_foreign_and_non_json_output(self):
        for stage in AGENT_TESTS:
            for bad in ('duplicate', 'foreign', 'non-json'):
                evidence = AgentEvidence(stage)
                for name in evidence.allowed:
                    for action in ('run', 'pass'):
                        evidence.feed(json.dumps(dict(Package=evidence.package, Test=name, Action=action)))
                evidence.feed(json.dumps(dict(Package=evidence.package, Action='pass')))
                self.assertEqual([], evidence.errors())
                event = {'Package': evidence.package, 'Test': next(iter(evidence.required)), 'Action': 'pass'}
                if bad == 'foreign':
                    event['Test'] = 'TestForeign'
                evidence.feed('cached receipt' if bad == 'non-json' else json.dumps(event))
                self.assertTrue(evidence.errors(), (stage, bad))


if __name__ == "__main__":
    unittest.main()
