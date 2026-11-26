import contextlib
import importlib.util
import io
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("driver", Path(__file__).with_name("bots_acceptance_driver.py"))
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


class ActiveBotAcceptanceTests(unittest.TestCase):
    def test_manifest_has_exact_scopes_and_no_migration(self):
        self.assertEqual(set(driver.ACTIVE_BOT_GATES), {'creation', 'http', 'schedules', 'resume', 'cli-pty', 'browser'})
        for _, names in driver.ACTIVE_BOT_GATES.values():
            self.assertTrue(names)
            self.assertFalse(any('Migration' in n or 'Draft' in n for n in names))
            for name in names:
                if name in driver.ACTIVE_BOT_SCOPED:
                    for scope in ('personal', 'team'):
                        self.assertIn(name + '/' + scope, driver.active_bot_required(names))

    def test_partial_flags_rejected(self):
        for flag in ('--local-only', '--preflight', '--real-provider', '--postgres', '--defer-kubernetes-postgresql'):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                driver.main(['--stage', 'active-bot-status', flag])
        with patch.object(driver, 'active_bot_status', return_value=17) as check:
            self.assertEqual(driver.main(['--stage', 'active-bot-status', '--checks-only']), 17)
            self.assertTrue(check.call_args.args[0].checks_only)

    def test_missing_browser_blocks_before_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'report.json'
            with patch.dict(os.environ, {}, clear=True), patch.object(driver, 'run') as run:
                self.assertEqual(driver.main(['--stage', 'active-bot-status', '--report', str(path)]), 2)
                run.assert_not_called()
            self.assertEqual(json.loads(path.read_text())['status'], 'BLOCKED')

    def test_unknown_skip_wrong_package_missing_child_malformed_rejected(self):
        pkg = 'github.com/SAP/astonish/pkg/api'
        required = ['TestActive', 'TestActive/personal', 'TestActive/team']
        for mode in ('valid', 'skip', 'foreign', 'missing', 'malformed'):
            with self.subTest(mode=mode):
                evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                for name in required:
                    if mode == 'missing' and name.endswith('/team'): continue
                    package = 'foreign' if mode == 'foreign' else pkg
                    evidence.feed(json.dumps(dict(Package=package, Test=name, Action='run')))
                    evidence.feed(json.dumps(dict(Package=package, Test=name, Action='pass')))
                evidence.feed(json.dumps(dict(Package=pkg, Action='pass')))
                if mode == 'skip': evidence.feed(json.dumps(dict(Package=pkg, Test='TestActive/unknown', Action='skip')))
                if mode == 'malformed': evidence.feed('not-json')
                self.assertEqual(bool(driver.consolidation_exact_errors(evidence, './pkg/api', required)), mode != 'valid')


class EmployeeCompletedEvidenceTests(unittest.TestCase):
    def fixture(self, root):
        source = {'fixture.go': 'source-hash'}
        report = dict(schema_version='employee-v1', stage='employee', phase='all',
                      status='PASS', evidence_tier='controlled-local', source_hashes=source,
                      command=driver.employee_invocation(), exit_code=0,
                      unverified=['live Kubernetes/PostgreSQL', 'paid-provider quality and latency',
                                  'deployment and existing user Bots'], gates=[], builds=[])
        for name in ('ui', 'candidate', 'node'):
            log = root / (name + '.log'); log.write_text('built\n')
            command = (['npm', '--prefix', 'web', 'run', 'build'] if name == 'ui' else
                       ['go', 'build', '-o', str(root / ('astonish' if name == 'candidate' else 'astonish-node')), '.'])
            report['builds'].append(dict(id=name, command=command, status='PASS', exit_code=0,
                                         log=str(log), log_sha256=driver.employee_digest(log)))
        report['build_artifacts'] = {}
        for name in ('astonish', 'astonish-node', 'studio.js'):
            artifact = root / name; artifact.write_text(name)
            report['build_artifacts'][str(artifact)] = driver.employee_digest(artifact)
        for phase, package, names in driver.employee_batches(driver.EMPLOYEE_PHASES[:-1]):
            required = driver.employee_required(package, names)
            log = root / ('gate-' + str(len(report['gates'])) + '.jsonl')
            events = []
            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
            for test in required:
                events.append(dict(Package=pkg, Test=test, Action='run'))
                events.append(dict(Package=pkg, Test=test, Action='output',
                                   Output=' '.join(driver.EMPLOYEE_MARKERS.get((package, test), ()))))
                events.append(dict(Package=pkg, Test=test, Action='pass'))
            events.append(dict(Package=pkg, Action='pass'))
            log.write_text(''.join(json.dumps(e) + '\n' for e in events))
            report['gates'].append(dict(phase=phase, package=package, required=required,
                command=driver.go_command(package, names), status='PASS', exit_code=0,
                fresh=True, prerequisites_verified=True, errors=[], log=str(log),
                log_sha256=driver.employee_digest(log), passed=sorted(required)))
        path = root / 'report.json'; path.write_text(json.dumps(report))
        return path, report, source

    def test_latency_parents_have_independent_unchanged_timeouts(self):
        batches = list(driver.employee_batches(('latency',)))
        self.assertEqual([names for _, _, names in batches],
                         [[name] for name in driver.EMPLOYEE_GATES['latency']['./pkg/daemon']])
        for _, package, names in batches:
            self.assertIn('-timeout=15m', driver.go_command(package, names))

    def test_completed_employee_rejects_missing_stale_fake_and_tampered_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for mode in ('valid', 'phase', 'status', 'exit', 'missing', 'duplicate', 'source',
                         'log-hash', 'skip', 'marker', 'binary', 'build-exit', 'command', 'passed'):
                with self.subTest(mode=mode):
                    path, report, source = self.fixture(root)
                    if mode == 'phase': report['phase'] = 'latency'
                    if mode == 'status': report['status'] = 'RUNNING'
                    if mode == 'exit': report['gates'][0]['exit_code'] = 1
                    if mode == 'missing': report['gates'].pop()
                    if mode == 'duplicate': report['gates'].append(report['gates'][0])
                    if mode == 'source': report['source_hashes'] = {'old': 'stale'}
                    if mode == 'log-hash': Path(report['gates'][0]['log']).write_text('tampered')
                    if mode in ('skip', 'marker'):
                        gate = report['gates'][0]; log = Path(gate['log'])
                        text = log.read_text().replace('"pass"', '"skip"') if mode == 'skip' else log.read_text().replace('effective=5', 'effective=6')
                        log.write_text(text); gate['log_sha256'] = driver.employee_digest(log)
                    if mode == 'binary': (root / 'astonish').write_text('stale')
                    if mode == 'build-exit': report['builds'][0]['exit_code'] = 1
                    if mode == 'command': report['command'] += ['--local-only']
                    if mode == 'passed': report['gates'][0]['passed'] = []
                    path.write_text(json.dumps(report))
                    with patch.object(driver, 'completed_source_hashes', return_value=source), patch.object(driver, 'employee_studio_artifacts', return_value=[root / 'studio.js']):
                        if mode == 'valid': self.assertIn(str(path.resolve()), driver.completed_employee_check(path))
                        else:
                            with self.assertRaises((ValueError, OSError)):
                                driver.completed_employee_check(path)

    def test_employee_quality_receipts_are_validated_and_hash_bound(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); path, report, source = self.fixture(root)
            quality = root / 'quality.json'; quality.write_text('quality receipt')
            with patch.object(driver, 'completed_source_hashes', return_value=source), patch.object(driver, 'employee_studio_artifacts', return_value=[root / 'studio.js']), patch.object(driver, 'completed_quality_check', return_value={str(quality)}) as check:
                artifacts = driver.completed_employee_check(path, quality, [path])
                self.assertEqual(artifacts[str(quality)], driver.employee_digest(quality))
                check.assert_called_once_with(quality, [path])
                check.side_effect = ValueError('quality failed')
                with self.assertRaises(ValueError): driver.completed_employee_check(path, quality)

    def test_employee_seal_verify_requires_actual_zero_exit_and_pinned_hash(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); path, report, source = self.fixture(root)
            receipt = root / 'receipt.json'
            base = ['--completed-evidence', 'seal', '--evidence-kind', 'employee',
                    '--employee-report', str(path), '--receipt', str(receipt)]
            with patch.object(driver, 'completed_source_hashes', return_value=source), patch.object(driver, 'employee_studio_artifacts', return_value=[root / 'studio.js']), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(driver.main(base), 2)
                self.assertEqual(driver.main(base + ['--employee-exit', '0']), 0)
                verify = ['--completed-evidence', 'verify', '--evidence-kind', 'employee',
                          '--receipt', str(receipt), '--sha256', driver.employee_digest(receipt)]
                self.assertEqual(driver.main(verify), 0)
                bad_digest = verify[:-1] + ['0' * 64]
                self.assertEqual(driver.main(bad_digest), 2)
                with patch.object(driver, 'completed_source_hashes', return_value={'stale': 'source'}):
                    self.assertEqual(driver.main(verify), 2)
                path.write_text(path.read_text() + '\n')
                self.assertEqual(driver.main(verify), 2)


class EmployeeAcceptanceTests(unittest.TestCase):
    def test_parser_employee_requires_phase_and_rejects_partial_flags(self):
        for argv in (['--stage', 'employee'], ['--employee-phase', 'all'],
                     ['--stage', 'employee', '--employee-phase', 'all', '--local-only'],
                     ['--stage', 'employee', '--employee-phase', 'all', '--real-provider'],
                     ['--stage', 'employee', '--employee-phase', 'all', '--defer-kubernetes-postgresql']):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                driver.main(argv)
        for phase in driver.EMPLOYEE_PHASES:
            with patch.object(driver, 'employee', return_value=17) as run:
                self.assertEqual(driver.main(['--stage', 'employee', '--employee-phase', phase]), 17)
                self.assertEqual(run.call_args.args[0].employee_phase, phase)

    def test_exact_manifest_covers_scopes_modes_and_sufficiency(self):
        self.assertEqual(set(driver.EMPLOYEE_GATES), set(driver.EMPLOYEE_PHASES) - {'all'})
        names = driver.EMPLOYEE_GATES['contracts']['./pkg/launcher']
        required = driver.employee_required('./pkg/launcher', names)
        for child in ('live-six', 'live-eight', 'complete', 'omitted', 'null'):
            self.assertIn('TestBotSharedResearchAdvertisedSchema/' + child, required)
        for scope in ('personal', 'team'):
            for mode in ('chat', 'interactive-bot', 'autonomous-bot'):
                self.assertIn('TestChatRuntimeConfiguredSearchProviderParity/' + scope + '/' + mode, required)
        self.assertEqual(set(driver.EMPLOYEE_SUBTESTS['TestBotEmployeeAdaptiveResultSufficiency']),
                         {'adequate-first', 'non-news', 'read-error', 'no-results', 'useful-partial', 'scheduled', 'latest-owner'})
        self.assertFalse(driver.EMPLOYEE_COVERAGE_GAPS)
        for phase, test in (('delivery', 'TestBotInteractiveAuthorityHTTPAcceptance'),
                            ('delivery', 'TestBotEmployeeObjectiveInquiryHTTP'),
                            ('presentation', 'TestBotEventsHTTPAcceptance'),
                            ('presentation', 'TestBotHistoryStreamHTTPAcceptance')):
            self.assertIn(test, driver.EMPLOYEE_GATES[phase]['./pkg/api'])
        for (package, test), markers in driver.EMPLOYEE_MARKERS.items():
            phase = 'contracts' if package == './pkg/launcher' else 'delivery'
            self.assertIn(test, driver.employee_required(package, driver.EMPLOYEE_GATES[phase][package]))
            self.assertTrue(markers)

    def test_explicit_file_delivery_requires_each_scope_and_runtime_marker(self):
        package = './pkg/api'
        parent = 'TestBotEmployeeExplicitFileHTTP'
        markers = ('employee-explicit-file=true', 'owner-objective=true', 'shared-chat-loop=true',
                   'authorized-write=true', 'trusted-publication=true', 'committed-message=true',
                   'exact-immutable-download=true', 'no-replay=true')
        self.assertIn(parent, driver.EMPLOYEE_GATES['delivery'][package])
        required = driver.employee_required(package, [parent])
        pkg = 'github.com/SAP/astonish/pkg/api'
        for scope in ('personal', 'team'):
            test = parent + '/' + scope
            self.assertIn(test, required)
            self.assertEqual(driver.EMPLOYEE_MARKERS[(package, test)], markers)
            for omitted in markers:
                with self.subTest(scope=scope, omitted=omitted):
                    evidence = driver.EmployeeEvidence(required, io.StringIO(), {})
                    for name in required:
                        evidence.feed(json.dumps(dict(Package=pkg, Test=name, Action='run')))
                        output = ' '.join(m for m in markers if name != test or m != omitted)
                        evidence.feed(json.dumps(dict(Package=pkg, Test=name, Action='output', Output=output)))
                        evidence.feed(json.dumps(dict(Package=pkg, Test=name, Action='pass')))
                    evidence.feed(json.dumps(dict(Package=pkg, Action='pass')))
                    errors = driver.employee_exact_errors(evidence, package, required)
                    self.assertEqual(len(errors), 1, errors)
                    self.assertIn(test + ': ' + omitted, errors[0])

    def test_exact_employee_markers_reject_missing_spoofed_and_unbound_output(self):
        for (package, test), markers in driver.EMPLOYEE_MARKERS.items():
            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
            for mode in ('valid', 'missing', 'foreign', 'wrong-test', 'package-output',
                         'before-run', 'after-pass', 'spoofed', 'skip'):
                with self.subTest(test=test, mode=mode):
                    evidence = driver.EmployeeEvidence([test], io.StringIO(), {})
                    def feed(action, **fields):
                        evidence.feed(json.dumps(dict(Package=pkg, Test=test, Action=action, **fields)) + '\n')
                    output = ' '.join(markers) + '\n'
                    event = dict(Package=pkg, Test=test, Action='output', Output=output)
                    if mode == 'foreign':
                        event['Package'] = 'foreign'
                    if mode == 'wrong-test':
                        event['Test'] += '/unrelated'
                    if mode == 'package-output':
                        del event['Test']
                    if mode == 'spoofed':
                        event['Output'] = ' '.join('spoof-' + marker + '-false' for marker in markers)
                    if mode == 'before-run':
                        evidence.feed(json.dumps(event))
                    feed('run')
                    if mode not in ('missing', 'before-run', 'after-pass'):
                        evidence.feed(json.dumps(event))
                    feed('skip' if mode == 'skip' else 'pass')
                    if mode == 'after-pass':
                        evidence.feed(json.dumps(event))
                    evidence.feed(json.dumps(dict(Package=pkg, Action='pass')))
                    errors = driver.employee_exact_errors(evidence, package, [test])
                    self.assertEqual(bool(errors), mode != 'valid', errors)

    def test_raw_json_rejects_skip_missing_child_and_wrong_package(self):
        package = './pkg/api'
        pkg = 'github.com/SAP/astonish/pkg/api'
        for mode in ('valid', 'skip', 'missing', 'foreign', 'malformed'):
            log = io.StringIO()
            evidence = driver.ConsolidationEvidence(['TestRequired', 'TestRequired/leaf'], log, {})
            events = [dict(Package=pkg, Test='TestRequired', Action='run'),
                      dict(Package=pkg, Test='TestRequired', Action='pass')]
            if mode != 'missing':
                childpkg = 'foreign' if mode == 'foreign' else pkg
                events += [dict(Package=childpkg, Test='TestRequired/leaf', Action='run'),
                           dict(Package=childpkg, Test='TestRequired/leaf', Action='skip' if mode == 'skip' else 'pass')]
            events += [dict(Package=pkg, Action='pass')]
            for event in events:
                evidence.feed(json.dumps(event) + '\n')
            if mode == 'malformed':
                evidence.feed('not json\n')
            errors = driver.consolidation_exact_errors(evidence, package, ['TestRequired', 'TestRequired/leaf'])
            self.assertEqual(bool(errors), mode != 'valid', mode)
            self.assertIn('TestRequired', log.getvalue())

    def test_missing_physical_prerequisites_block_with_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / 'report.json'
            with patch.dict(os.environ, {}, clear=True), patch.object(driver, 'completed_source_hashes', return_value={}), patch.object(driver, 'run') as run:
                self.assertEqual(driver.main(['--stage', 'employee', '--employee-phase', 'all', '--report', str(report)]), 2)
                run.assert_not_called()
            value = json.loads(report.read_text())
            self.assertEqual(value['status'], 'BLOCKED')
            self.assertEqual(value['gates'], [])
            self.assertIn('local', value['errors'])


class CompletedEvidenceTests(unittest.TestCase):
    def test_quality_skip_union_is_exact_fail_closed_and_never_promotes(self):
        package = 'github.com/SAP/astonish/pkg/api'
        def evidence(test='TestRequired/leaf', action='skip', package=package):
            value = driver.ConsolidationEvidence([], io.StringIO(), {})
            for event in (dict(Package=package, Test=test, Action='run'),
                          dict(Package=package, Test=test, Action=action), dict(Package=package, Action='pass')):
                value.feed(json.dumps(event))
            return value
        value = evidence()
        with self.assertRaisesRegex(ValueError, 'TestRequired/leaf'):
            driver.completed_quality_skip_errors(value, {(package, 'TestRequired'): 'parent'})
        with self.assertRaises(ValueError):
            driver.completed_quality_skip_errors(value, {('foreign', 'TestRequired/leaf'): 'wrong package'})
        result = driver.completed_quality_skip_errors(value, {(package, 'TestRequired/leaf'): '/owned/current.jsonl'})
        self.assertIn('exact supplemental RUN/PASS', next(iter(result.values())))
        self.assertEqual(value.qualified_passes, set())
        self.assertTrue(value.errors())  # Original skip remains a skip.
        with self.assertRaises(ValueError):
            driver.completed_quality_skip_errors(evidence(action='fail'), {(package, 'TestRequired/leaf'): 'pass'})
        for name in ('TestBotEnhancedChatNoPythonDocker', 'TestBotEnhancedChatCapturedProxyDocker',
                     'TestBotEnhancedChatExactWorkflowDocker', 'TestBotHomeFilesMissingAndUnsafe'):
            self.assertFalse(any(key[1] == name for key in driver.QUALITY_OPTIONAL_DIAGNOSTICS))
        optional = evidence(test='TestBotCapturedConfiguredReviewerLive')
        self.assertIn('NOT PASS:', next(iter(driver.completed_quality_skip_errors(optional, {}).values())))
        with self.assertRaises(ValueError):
            driver.completed_quality_skip_errors(evidence(test='TestUnknownPaidDiagnostic'), {})

    def test_quality_skip_packages_are_exact_structural_no_tests_only(self):
        value = driver.ConsolidationEvidence([], io.StringIO(), {})
        for event in (dict(Package='example', Test='TestRequired', Action='run'),
                      dict(Package='example', Test='TestRequired', Action='pass'),
                      dict(Package='example', Action='pass'),
                      dict(Package='unknown', Action='skip')):
            value.feed(json.dumps(event))
        with self.assertRaisesRegex(ValueError, 'unknown skipped package'):
            driver.completed_quality_skip_errors(value, {})
        self.assertFalse(any(p.endswith('/k8s') for p in driver.QUALITY_NO_TEST_PACKAGES))

    def test_quality_supplemental_requires_raw_exact_run_pass_and_fresh_command(self):
        import copy
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log = root / 'run.jsonl'
            package = 'github.com/SAP/astonish/pkg/api'
            def raw(action='pass', package=package):
                return ''.join(json.dumps(e) + '\n' for e in (
                    dict(Package=package, Test='TestRequired', Action='run'),
                    dict(Package=package, Test='TestRequired', Action=action), dict(Package=package, Action='pass')))
            log.write_text(raw())
            gate = dict(tier='extra', package='./pkg/api', required=['TestRequired'],
                        fresh=True, prerequisites_verified=True, status='PASS', errors=[], log=str(log),
                        command=driver.go_command('./pkg/api', ['TestRequired']))
            report = root / 'extra.json'
            with patch.dict(driver.QUALITY_LOCAL_EXTRA_TESTS, {'./pkg/api': frozenset({'TestRequired'})}):
                report.write_text(json.dumps(dict(schema_version='skip-coverage-v1', gates=[gate])))
                self.assertIn((package, 'TestRequired'), driver.completed_quality_coverage([report], set()))
            with patch.dict(driver.QUALITY_LOCAL_EXTRA_TESTS, {'./pkg/api': frozenset({'TestRequired'})}):
                for field, bad in (('fresh', False), ('status', 'SKIP'), ('prerequisites_verified', False),
                                   ('command', ['go', 'test', './pkg/api']), ('log_sha256', 'wrong')):
                    payload = copy.deepcopy(gate); payload[field] = bad
                    report.write_text(json.dumps(dict(schema_version='skip-coverage-v1', gates=[payload])))
                    with self.assertRaises(ValueError):
                        driver.completed_quality_coverage([report], set())
                report.write_text(json.dumps(dict(schema_version='skip-coverage-v1', gates=[gate])))
                for action, pkg in (('skip', package), ('pass', 'foreign')):
                    log.write_text(raw(action, pkg))
                    with self.assertRaises(ValueError):
                        driver.completed_quality_coverage([report], set())
                log.write_text(raw())
                forged = dict(gate, tier='http')
                report.write_text(json.dumps(dict(gates=[forged])))
                with self.assertRaisesRegex(ValueError, 'scope mismatch'):
                    driver.completed_quality_coverage([report], set())
                forged = dict(gate, package='./pkg/sandbox/k8s', required=['TestBotHomeFilesMissingAndUnsafe'],
                              command=driver.go_command('./pkg/sandbox/k8s', ['TestBotHomeFilesMissingAndUnsafe']))
                report.write_text(json.dumps(dict(schema_version='skip-coverage-v1', gates=[forged])))
                with self.assertRaisesRegex(ValueError, 'owned Linux runner'):
                    driver.completed_quality_coverage([report], set())

    def test_extra_collector_requires_all_leaves_and_enables_local_fixtures(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            browser = root / 'browser'; browser.write_text('fixture'); browser.chmod(0o700)
            package = 'github.com/SAP/astonish/pkg/launcher'
            name = 'TestBotConfiguredSearchPhysicalMCP'
            source = dict(ASTONISH_TEST_DOCKER='1', ASTONISH_SANDBOX_IMAGE='owned:local',
                          ASTONISH_BOT_BROWSER_PATH=str(browser), ASTONISH_BOT_FILES_TEST_BINARY='/owned/node')
            def execute(command, env, evidence):
                self.assertEqual(env['ASTONISH_TEST_TASK_EXECUTION'], '1')
                self.assertEqual(env['ASTONISH_TEST_CONFIGURED_SEARCH'], '1')
                self.assertEqual(env['ASTONISH_TEST_SEARCH_NODE'], '/owned/node')
                self.assertEqual(env['ASTONISH_CODE_BROWSER_PATH'], str(browser))
                for test, action in ((name, 'run'), (name + '/leaf', 'run'), (name + '/leaf', 'skip'), (name, 'pass')):
                    evidence.feed(json.dumps(dict(Package=package, Test=test, Action=action)))
                evidence.feed(json.dumps(dict(Package=package, Action='pass')))
                return True  # Shell exit alone cannot hide a skipped leaf.
            output = root / 'extra.json'
            with patch.object(driver, 'completed_skip_inventory', return_value=(None, {}, [(package, name)])), \
                 patch.object(driver, 'run', side_effect=execute), patch.dict(os.environ, source, clear=True), \
                 contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaisesRegex(ValueError, 'including every leaf'):
                    driver.completed_collect_skip_coverage('quality', [], output, 'pkg-launcher')
            report = json.loads(output.read_text())
            self.assertEqual(report['gates'][0]['status'], 'FAIL')
            self.assertTrue(any('/leaf' in e for e in report['gates'][0]['errors']))
            with self.assertRaises(ValueError):
                driver.completed_quality_coverage([output], set())

    def test_extra_collector_environment_is_exact_and_provider_free(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            browser = root / 'browser'; browser.write_text('fixture'); browser.chmod(0o700)
            template = 'hash3d82153110fdfd362b17564af16bfaab841c79739c8da161236c69799d46114c'
            source = dict(PATH='/fixture/path', ASTONISH_TEST_DOCKER='1',
                          ASTONISH_SANDBOX_IMAGE='owned:local', ASTONISH_BOT_BROWSER_PATH=str(browser),
                          ASTONISH_BOT_HOME_TEST_LAYERS='/owned/layers', ASTONISH_BOT_HOME_TEST_TEMPLATE=template,
                          ASTONISH_BOT_FILES_TEST_BINARY='/owned/node', ASTONISH_TEST_COMPILED_HOME_FILES='1',
                          ASTONISH_TEST_TASK_CONTINUITY='1')
            expected = dict(source)
            expected.update(ASTONISH_TEST_TASK_WORKSPACE='1', ASTONISH_TEST_TASK_EXECUTION='1',
                            ASTONISH_TEST_CONFIGURED_SEARCH='1', ASTONISH_TEST_SEARCH_NODE='/owned/node',
                            ASTONISH_CODE_BROWSER_PATH=str(browser), ASTONISH_TEST_WORKSPACE_BINARY='/owned/node',
                            ASTONISH_BOT_ARCHIVE_TEST_BINARY='/owned/node')
            expected.update({key: '1' for key in (
                'ASTONISH_MEMORY_BROWSER_ACCEPTANCE', 'ASTONISH_STREAM_BROWSER_ACCEPTANCE',
                'ASTONISH_BOT_DELETE_BROWSER_ACCEPTANCE', 'ASTONISH_LOCAL_BROWSER_ACCEPTANCE',
                'ASTONISH_CODE_BROWSER_ACCEPTANCE', 'ASTONISH_FILE_TOOL_API_LIVE',
                'ASTONISH_BOT_BROWSER_ACCEPTANCE', 'ASTONISH_BROWSER_CONTEXT_TEST')})
            source.update({key: 'forbidden-secret' for key in (
                'ASTONISH_DSN', 'ASTONISH_CONFIG', 'ASTONISH_BOTS_LIVE_API_KEY',
                'ASTONISH_BOT_HOME_TEST_TEMPLATE_UNREVIEWED', 'ASTONISH_UNKNOWN_FIXTURE',
                'OPENAI_API_KEY', 'ANTHROPIC_API_KEY', 'GOOGLE_API_KEY', 'BIFROST_API_KEY',
                'AWS_SECRET_ACCESS_KEY', 'GOFLAGS', 'MAKEFLAGS')})
            for package, name in (
                ('github.com/SAP/astonish/pkg/sandbox/docker', 'TestBotHomeFilesDockerLive'),
                ('github.com/SAP/astonish/pkg/api', 'TestBotTitleChatBrowserAcceptance')):
                def execute(command, env, evidence):
                    self.assertEqual(env, expected)
                    for action in ('run', 'pass'):
                        evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
                    evidence.feed(json.dumps(dict(Package=package, Action='pass')))
                    return True
                output = root / (name + '.json')
                with patch.object(driver, 'completed_skip_inventory', return_value=(None, {}, [(package, name)])), \
                     patch.object(driver, 'run', side_effect=execute), patch.dict(os.environ, source, clear=True), \
                     contextlib.redirect_stdout(io.StringIO()):
                    driver.completed_collect_skip_coverage('quality', [], output,
                        package.removeprefix('github.com/SAP/astonish/').replace('/', '-'))
                self.assertEqual(json.loads(output.read_text())['gates'][0]['status'], 'PASS')

    def test_quality_delta_exact_policy_and_full_replay_validation(self):
        import copy
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            eligible = sorted(driver.QUALITY_DELTA_FILES)
            before = {name: 'old' for name in eligible} | {'pkg/launcher/chat.go': 'same'}
            current = {name: 'new' for name in eligible} | {'pkg/launcher/chat.go': 'same'}
            changes = driver.completed_quality_delta_changes(before, current)
            self.assertEqual([row['path'] for row in changes], eligible)
            commands = driver.completed_quality_delta_commands(changes)
            self.assertEqual(set(commands), {'go-cmd', 'lint-cmd', 'python', 'diff'})
            self.assertNotIn('-run', commands['go-cmd'])
            self.assertIn('-count=1', commands['go-cmd'])
            self.assertEqual(driver.completed_quality_delta_commands([
                dict(path='docs/architecture/terminal-app.md', before='old', after='new')]),
                {'diff': driver.completed_final_quality_commands()['diff']})
            for bad in (before, current | {'pkg/launcher/chat.go': 'changed'},
                        {key: value for key, value in current.items() if key != eligible[0]},
                        current | {'cmd/astonish/new_test.go': 'added'}):
                with self.assertRaises(ValueError):
                    driver.completed_quality_delta_changes(before, bad)
            package = 'github.com/SAP/astonish/cmd/astonish'
            def raw(names, package=package):
                return ''.join(json.dumps(event) + '\n' for name in names for event in (
                    dict(Package=package, Test=name, Action='run'),
                    dict(Package=package, Test=name, Action='pass'))) + json.dumps(dict(Package=package, Action='pass')) + '\n'
            original_log = root / 'original.jsonl'
            original_log.write_text(raw(['TestOriginal']))
            replay_log = root / 'replay.jsonl'
            replay_log.write_text(raw(['TestOriginal', 'TestNew']))
            digest = lambda path: driver.hashlib.sha256(path.read_bytes()).hexdigest()
            gate = lambda identifier, command, log: dict(id=identifier, command=command, cwd=str(driver.ROOT),
                exit_code=0, log=str(log), log_sha256=digest(log))
            base = root / 'base.json'
            base.write_text(json.dumps(dict(source_hashes=before, gates=[
                gate(identifier, command, original_log) for identifier, command in driver.completed_final_quality_commands().items()])))
            delta = dict(schema_version='quality-delta-v1', base_quality=str(base), base_sha256=digest(base),
                base_source_hashes=before, source_hashes=current, changes=changes,
                gates=[gate(identifier, command, replay_log) for identifier, command in commands.items()])
            output = root / 'delta.json'
            def check(payload):
                output.write_text(json.dumps(payload))
                return driver.completed_quality_check(output)
            with patch.object(driver, 'completed_source_hashes', return_value=current):
                artifacts = check(delta)
                self.assertIn(str(base.resolve()), artifacts)
                self.assertIn(str(original_log.resolve()), artifacts)
                self.assertIn(str(replay_log.resolve()), artifacts)
                for field, bad in (('base_sha256', 'stale'), ('base_source_hashes', current),
                                   ('source_hashes', before), ('changes', changes[:-1]),
                                   ('gates', delta['gates'][:-1]), ('gates', delta['gates'] + [delta['gates'][0]])):
                    payload = copy.deepcopy(delta)
                    payload[field] = bad
                    with self.assertRaises(ValueError, msg=field):
                        check(payload)
                for field, bad in (('exit_code', 1), ('command', ['go', 'test', '-run', 'TestNew', './cmd/astonish']),
                                   ('log_sha256', 'stale'), ('cwd', '/tmp')):
                    payload = copy.deepcopy(delta)
                    payload['gates'][0][field] = bad
                    with self.assertRaises(ValueError, msg=field):
                        check(payload)
                for bad_raw in (raw(['TestNew']), raw(['TestOriginal'], package='wrong'), 'PASS\n',
                                raw(['TestOriginal']) + json.dumps(dict(Package=package, Test='TestSkipped', Action='skip')) + '\n',
                                raw(['TestOriginal']).replace('"pass"', '"fail"')):
                    replay_log.write_text(bad_raw)
                    payload = copy.deepcopy(delta)
                    for row in payload['gates']:
                        row['log_sha256'] = digest(replay_log)
                    with self.assertRaises(ValueError):
                        check(payload)
                replay_log.write_text(raw(['TestOriginal', 'TestNew']))
                self.assertTrue(check(delta))
            with patch.object(driver, 'completed_source_hashes', side_effect=[current, before]):
                with self.assertRaisesRegex(ValueError, 'during'):
                    check(delta)

    def test_quality_delta_collector_runs_exact_commands_and_blocks_drift_and_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            before = {name: 'old' for name in driver.QUALITY_DELTA_FILES}
            current = {name: 'new' for name in driver.QUALITY_DELTA_FILES}
            base = root / 'base.json'
            base.write_text(json.dumps(dict(source_hashes=before)))
            original = base.read_bytes()
            calls = []
            def run(command, **kwargs):
                calls.append(command)
                kwargs['stdout'].write(b'actual raw output\n')
                return subprocess.CompletedProcess(command, 0)
            with patch.object(driver, 'completed_quality_base_check'), \
                 patch.object(driver, 'completed_quality_check') as validate, \
                 patch.object(driver, 'completed_source_hashes', return_value=current), \
                 patch.object(driver.subprocess, 'run', side_effect=run), contextlib.redirect_stdout(io.StringIO()):
                output = root / 'delta.json'
                driver.completed_quality_delta_collect(base, output)
                self.assertEqual(calls, list(driver.completed_quality_delta_commands(
                    driver.completed_quality_delta_changes(before, current)).values()))
                validate.assert_called_once_with(output.resolve())
                self.assertEqual(base.read_bytes(), original)
                receipt = json.loads(output.read_text())
                self.assertEqual(receipt['base_source_hashes'], before)
                self.assertEqual(receipt['source_hashes'], current)
                for gate in receipt['gates']:
                    self.assertEqual(Path(gate['log']).read_bytes(), b'actual raw output\n')
                with self.assertRaisesRegex(ValueError, 'new'):
                    driver.completed_quality_delta_collect(base, output)
                with patch.object(driver.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1)):
                    with self.assertRaisesRegex(ValueError, 'failed'):
                        driver.completed_quality_delta_collect(base, root / 'failed.json')
                    self.assertFalse((root / 'failed.json').exists())
                with patch.object(driver, 'completed_source_hashes', side_effect=[current, before]):
                    with self.assertRaisesRegex(ValueError, 'during'):
                        driver.completed_quality_delta_collect(base, root / 'drift.json')
                    self.assertFalse((root / 'drift.json').exists())

    def test_quality_delta_collector_rejects_skipped_original_before_replay(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log = root / 'original.jsonl'
            package = 'github.com/SAP/astonish/cmd/astonish'
            log.write_text(''.join(json.dumps(event) + '\n' for event in (
                dict(Package=package, Test='TestOriginal', Action='run'),
                dict(Package=package, Test='TestOriginal', Action='skip'),
                dict(Package=package, Action='pass'))))
            base = root / 'base.json'
            base.write_text(json.dumps(dict(source_hashes={}, gates=[
                dict(id=identifier, command=command, cwd=str(driver.ROOT), exit_code=0,
                     log=str(log), log_sha256=driver.hashlib.sha256(log.read_bytes()).hexdigest())
                for identifier, command in driver.completed_final_quality_commands().items()])))
            original = base.read_bytes()
            with patch.object(driver.subprocess, 'run') as replay:
                with self.assertRaisesRegex(ValueError, 'broad Go quality raw JSONL'):
                    driver.completed_quality_delta_collect(base, root / 'delta.json')
                replay.assert_not_called()
            self.assertEqual(base.read_bytes(), original)
            self.assertFalse((root / 'delta.json').exists())

    def test_final_integrated_exact_all_baseline_quality_and_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package, names = './pkg/daemon', ['TestBotEnhancedChatNoPythonDocker']
            log = root / 'raw.jsonl'
            raw = ''.join(json.dumps(event) + '\n' for event in (
                dict(Package='github.com/SAP/astonish/pkg/daemon', Test=names[0], Action='run'),
                dict(Package='github.com/SAP/astonish/pkg/daemon', Test=names[0], Action='pass'),
                dict(Package='github.com/SAP/astonish/pkg/daemon', Action='pass')))
            log.write_text(raw)
            scope = driver.acceptance_scope(type('Args', (), {'defer_kubernetes_postgresql': True})())
            gate = dict(package=package, required=names, command=driver.go_command(package, names),
                        fresh=True, errors=[], log=str(log), status='PASS')
            regression = dict(test=names[0], log=str(log), errors=[], status='UNTRUSTED_LABEL')
            baseline = root / 'baseline.json'
            full = root / 'all.json'
            quality = root / 'quality.json'
            requirements = [{'status': 'PASS', 'id': 'fixture'}]
            baseline_report = dict(stage='chat-consolidation', phase='baseline', acceptance_scope=scope,
                                   gates=[gate], regression=regression)
            all_report = dict(stage='chat-consolidation', phase='all', acceptance_scope=scope,
                gates=[dict(gate, tier='baseline-daemon'), dict(regression, tier='baseline-docker-regression'),
                       dict(gate, tier='fixture', command=driver.consolidation_command('fixture', package, names),
                            prerequisites_verified=True)], requirement_evidence=requirements, remaining_requirements=[])
            commands = driver.completed_final_quality_commands()
            quality_report = dict(source_hashes={'source': 'current'}, gates=[dict(id=key, command=command,
                exit_code=0, cwd=str(driver.ROOT), log=str(log),
                log_sha256=driver.hashlib.sha256(log.read_bytes()).hexdigest()) for key, command in commands.items()])
            def save():
                baseline.write_text(json.dumps(baseline_report))
                full.write_text(json.dumps(all_report))
                quality.write_text(json.dumps(quality_report))
            save()
            with patch.object(driver, 'CONSOLIDATION_BASELINE', {package: names}), \
                 patch.object(driver, 'consolidation_manifests', return_value={'runtime': {'fixture': (package, names)}}), \
                 patch.object(driver, 'consolidation_requirement_evidence', return_value=requirements), \
                 patch.object(driver, 'completed_source_hashes', return_value={'source': 'current'}), \
                 contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertTrue(driver.completed_final_check(baseline, full, quality))
                receipt = root / 'receipt.json'
                seal = ['seal', '--evidence-kind', 'final-integrated', '--receipt', str(receipt),
                        '--baseline', str(baseline), '--all', str(full), '--quality', str(quality),
                        '--baseline-exit', '0', '--all-exit', '0']
                self.assertEqual(driver.completed_evidence_main(seal[:-1] + ['1']), 2)
                self.assertEqual(driver.completed_evidence_main(seal), 0)
                verify = ['verify', '--evidence-kind', 'final-integrated', '--receipt', str(receipt),
                          '--sha256', driver.hashlib.sha256(receipt.read_bytes()).hexdigest()]
                self.assertEqual(driver.completed_evidence_main(verify), 0)
                with patch.object(driver, 'completed_source_hashes', return_value={'source': 'stale'}):
                    self.assertEqual(driver.completed_evidence_main(verify), 2)
                    with self.assertRaisesRegex(ValueError, 'snapshot'):
                        driver.completed_final_check(baseline, full, quality)
                for report in (baseline_report, all_report, quality_report):
                    original = list(report['gates'])
                    for changed in (original[:-1], original + [original[0]]):
                        report['gates'] = changed
                        save()
                        with self.assertRaises(ValueError):
                            driver.completed_final_check(baseline, full, quality)
                        self.assertEqual(driver.completed_evidence_main(verify), 2)
                    report['gates'] = original
                for key, bad in (('exit_code', 1), ('command', ['echo', 'PASS']), ('log_sha256', 'stale')):
                    original = quality_report['gates'][0][key]
                    quality_report['gates'][0][key] = bad
                    save()
                    with self.assertRaises(ValueError):
                        driver.completed_final_check(baseline, full, quality)
                    quality_report['gates'][0][key] = original
                save()
                for bad_raw in ('PASS\n', raw.replace('"run"', '"output"'), raw + '{"Package":"x","Action":"skip"}\n'):
                    log.write_text(bad_raw)
                    with self.assertRaises(ValueError):
                        driver.completed_final_check(baseline, full, quality)
                log.write_text(raw)
                self.assertEqual(driver.completed_evidence_main(verify), 0)

    def test_completed_exact_revalidation_ignores_labels_rejects_children_and_corruption(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package, names = './pkg/sandbox', ['TestExact']
            required = names + ['TestExact/personal', 'TestExact/team']
            lines = []
            for name in required:
                for action in ('run', 'pass'):
                    lines.append(json.dumps({'Package': 'github.com/SAP/astonish/pkg/sandbox', 'Test': name, 'Action': action}) + '\n')
            lines.append(json.dumps({'Package': 'github.com/SAP/astonish/pkg/sandbox', 'Action': 'pass'}) + '\n')
            scope = driver.acceptance_scope(type('Args', (), {'defer_kubernetes_postgresql': True})())
            log = root / 'storage.jsonl'
            log.write_text(''.join(lines))
            gate = dict(tier='storage-fixture', package=package, required=required,
                        command=driver.consolidation_command('storage-fixture', package, names), log=str(log),
                        fresh=True, prerequisites_verified=True, errors=[], status='UNTRUSTED_LABEL')
            storage = root / 'storage.json'
            storage.write_text(json.dumps(dict(stage='chat-consolidation', phase='storage', acceptance_scope=scope, gates=[gate])))
            filefirst = root / 'file-first.json'
            filefirst.write_text(json.dumps(dict(stage='file-first', acceptance_scope=scope,
                gates=[dict(tier='local', package=package, required=required, errors=[], status='UNTRUSTED_LABEL')])))
            stdout = root / 'file-first.stdout'
            stdout.write_text('COMMAND ' + driver.shlex.join(driver.go_command(package, names)) + '\n' + ''.join(lines) + 'file-first: PASS\n')
            with patch.object(driver, 'completed_storage_manifest', return_value={'storage-fixture': (package, names)}), \
                 patch.object(driver, 'CONSOLIDATION_SUBTESTS', {'TestExact': ['personal', 'team']}), \
                 patch.object(driver, 'FILE_FIRST_GATES', {'local': (package, names)}), \
                 patch.object(driver, 'FILE_FIRST_SCOPED', {'TestExact'}), \
                 patch.object(driver, 'consolidation_requirement_evidence', return_value=[{'status': 'PASS'}]), \
                 patch.object(driver, 'completed_source_hashes', return_value={'current.go': 'abc'}):
                self.assertTrue(driver.completed_phase6_check(storage, filefirst, stdout))
                original_stdout = stdout.read_text()
                for bad_stdout in (original_stdout.replace(lines[0], 'malformed transport\n' + lines[0]),
                                   original_stdout + original_stdout):
                    stdout.write_text(bad_stdout)
                    with self.assertRaises(ValueError):
                        driver.completed_phase6_check(storage, filefirst, stdout)
                stdout.write_text(original_stdout)
                receipt = root / 'receipt.json'
                seal = ['seal', '--receipt', str(receipt), '--storage', str(storage), '--file-first', str(filefirst),
                        '--file-first-transcript', str(stdout), '--storage-exit', '0', '--file-first-exit', '0']
                with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(driver.completed_evidence_main(seal), 0)
                    digest = driver.hashlib.sha256(receipt.read_bytes()).hexdigest()
                    verify = ['verify', '--receipt', str(receipt), '--sha256', digest]
                    self.assertEqual(driver.completed_evidence_main(verify), 0)
                    self.assertEqual(driver.completed_evidence_main(verify[:-1] + ['0' * 64]), 2)
                    with patch.object(driver, 'completed_source_hashes', return_value={'current.go': 'changed'}):
                        self.assertEqual(driver.completed_evidence_main(verify), 2)
                    for changed in (
                            ''.join(line for line in lines if '/team' not in line),
                            ''.join(line for line in lines if '"run"' not in line),
                            ''.join(lines) + 'not-json\n',
                            ''.join(lines) + '{"Package":"p","Action":"skip"}\n'):
                        log.write_text(changed)
                        with self.assertRaises(ValueError):
                            driver.completed_phase6_check(storage, filefirst, stdout)
                        self.assertEqual(driver.completed_evidence_main(verify), 2)
                log.write_text(''.join(lines))
                fg = json.loads(filefirst.read_text())
                fg['gates'][0].update(command=driver.go_command(package, names), log=str(log),
                    log_sha256=driver.hashlib.sha256(log.read_bytes()).hexdigest())
                filefirst.write_text(json.dumps(fg))
                self.assertTrue(driver.completed_phase6_check(storage, filefirst))
                gate['command'] = ['go', 'test']
                storage.write_text(json.dumps(dict(stage='chat-consolidation', phase='storage', acceptance_scope=scope, gates=[gate])))
                with self.assertRaises(ValueError):
                    driver.completed_phase6_check(storage, filefirst)

    def test_completed_seal_requires_direct_parent_zero_exit(self):
        with tempfile.TemporaryDirectory() as directory, contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(driver.completed_evidence_main(['seal', '--receipt', str(Path(directory) / 'receipt'),
                '--storage-exit', '1', '--file-first-exit', '0']), 2)

    def test_completed_revision_preserves_definition_without_plan_write(self):
        text = '''# Execution Plan
**Goal:** Exact goal
## Plan Metadata

```json
{"plan_id":"owned", "revision":12}
```
## Context

### Problem
Exact problem
## Phases

### ⟳ Parallel group: wave-1

- [!] **infrastructure-acceptance** — Exact phase
  Summary: Exact summary
  Outcome: Exact outcome
  - File (modify): tests/e2eboot/bots_acceptance_driver.py
  Verify: old command
  Verify-Kind: behavior
  Evidence: prior failure, not details
  Exact details without retyping.

### ⟳ Parallel group: wave-2

- [ ] **final-integrated-acceptance** — Final
  Summary: Final summary
  Outcome: Final outcome
  - File (new): final.py
  Verify: final command
  Verify-Kind: behavior
  Exact final details.

Legend: ignored
## What Not To Change

Exact guard
## Verification

Exact acceptance
'''
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'PLAN.md'
            path.write_text(text)
            payload = driver.completed_plan_revision(path, 'short command immutable-sha', 'cap repair')
            self.assertEqual(path.read_text(), text)
            self.assertEqual(payload['revision']['base_revision'], 12)
            self.assertEqual(payload['revision']['steps'][1]['details'], 'Exact final details.')
            self.assertEqual(payload['revision']['steps'][1]['verify'], 'final command')
            self.assertEqual(payload['revision']['steps'][0]['verify'], 'short command immutable-sha')
            self.assertEqual(payload['revision']['document']['context'], '## Problem\nExact problem')
            self.assertNotIn('Evidence:', payload['revision']['steps'][0]['details'])
            final = driver.completed_plan_revision(path, 'final short immutable-sha', 'cap repair', 'final-integrated-acceptance')
            self.assertEqual(final['revision']['steps'][0]['verify'], 'old command')
            self.assertEqual(final['revision']['steps'][1]['verify'], 'final short immutable-sha')
            self.assertEqual(path.read_text(), text)
            with self.assertRaises(StopIteration):
                driver.completed_plan_revision(path, 'short', 'reason', 'absent-step')


class AcceptanceDriverTests(unittest.TestCase):
    def test_phase6_crash_concurrency_exact_manifest_fail_closed(self):
        inventory = {
            'storage-compiled-linux': {
                'TestCompiledHomeFilesResidualStages': [],
                'TestCompiledHomeFilesCancellationProcess': ['abrupt=false', 'abrupt=true'],
                'TestCompiledHomeFilesCrashRecoveryPreservesIndependentWriter': [],
                'TestCompiledHomeFilesRecoveryRejectsSpecialStages': ['symlink', 'hardlink', 'fifo', 'directory'],
                'TestCompiledHomeFilesPrivateRecoveryBeforeQuota': [],
                'TestCompiledWorkspaceStageCancellationAndRead': [],
                'TestCompiledWorkspaceCrashRecovery': ['write', 'publish'],
                'TestCompiledWorkspaceRecoveryPreservesRevisionsAndReads': [],
                'TestCompiledWorkspaceRecoveryRejectsSpecialStages': ['symlink', 'hardlink', 'fifo', 'directory', 'revision-symlink'],
                'TestCompiledWorkspaceRecoveryBeforeQuota': [],
                'TestCompiledWorkspaceRecoveryNestedWriter': [],
                'TestCompiledWorkspaceRecoveryPreservesActiveWriter': [],
            },
            'controller-store': {
                'TestBotSQLiteSQLStorageLockOrderNoSessionReentry': ['personal', 'team'],
                'TestBackupFileFirstSQLiteConcurrentRestoreArbitration': ['personal', 'team',
                    'personal/commit', 'personal/interrupt-sql-cleanup-retry',
                    'team/commit', 'team/interrupt-sql-cleanup-retry'],
            },
        }
        manifests = driver.CONSOLIDATION_STORAGE | driver.CONSOLIDATION_CONTROLLER
        scenarios = [association for rows in driver.CONSOLIDATION_REQUIREMENT_SCENARIOS.values()
                     for association in rows.values()]
        for tier, parents in inventory.items():
            package, names = manifests[tier]
            for parent, children in parents.items():
                self.assertIn(parent, names)
                self.assertIn((tier, parent), scenarios)
                required = driver.consolidation_required_tests(tier, [parent])
                self.assertEqual(required, [parent] + [parent + '/' + child for child in children])
                for omitted in required:
                    evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                    for name in required:
                        if name == omitted:
                            continue
                        for action in ('run', 'pass'):
                            evidence.feed(json.dumps({'Package': 'github.com/SAP/astonish/' + package[2:],
                                'Test': name, 'Action': action}) + '\n')
                    evidence.feed(json.dumps({'Package': 'github.com/SAP/astonish/' + package[2:], 'Action': 'pass'}) + '\n')
                    self.assertTrue(driver.consolidation_exact_errors(evidence, package, required), omitted)
        self.assertIn('storage-compiled-linux', driver.CONSOLIDATION_REQUIREMENT_TIERS['storage-cancel-stream-lifecycle'])

    def test_all_executes_original_baseline_and_physical_gate_fail_closed(self):
        for mode in ('pass', 'zero', 'skip', 'foreign', 'no-run', 'unfinished', 'failed'):
            for regression_status in ('FIXED', 'REPRODUCED', 'BLOCKED', 'FAIL'):
                with self.subTest(mode=mode, regression=regression_status), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    calls = []
                    def execute(command, env, evidence=None, **kwargs):
                        calls.append(command)
                        package = command[-3].replace('./', 'github.com/SAP/astonish/')
                        if mode == 'foreign':
                            package = 'foreign/package'
                        for name in evidence.required:
                            if mode == 'zero':
                                continue
                            if mode != 'no-run':
                                evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}) + '\n')
                            evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}) + '\n')
                        if mode == 'skip':
                            evidence.feed(json.dumps({'Package': package, 'Test': 'child', 'Action': 'skip'}) + '\n')
                        if mode != 'unfinished':
                            evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}) + '\n')
                        return mode != 'failed'
                    # Independent progress gates are synthetic here; this test only
                    # attests baseline inclusion and failure propagation, not live acceptance.
                    def gate(tier, package, names, *args):
                        return {'tier': tier, 'status': 'PASS'}
                    report = {'gates': [], 'unverified': []}
                    args = type('Args', (), {'phase': 'all'})()
                    with patch.object(driver, 'run', side_effect=execute), \
                            patch.object(driver, 'consolidation_docker_regression', return_value={'status': regression_status}) as regression, \
                            patch.object(driver, 'consolidation_progress_gate', side_effect=gate), \
                            patch.object(driver, 'consolidation_requirement_evidence', return_value=[]):
                        result = driver.consolidation_progress(args, {}, root, report)
                    self.assertEqual(calls, [driver.go_command(package, names)
                        for package, names in driver.CONSOLIDATION_BASELINE.items()])
                    regression.assert_called_once_with({}, root)
                    expected = ('FAIL' if mode != 'pass' or regression_status in ('REPRODUCED', 'FAIL')
                                else 'BLOCKED' if regression_status == 'BLOCKED' else 'PASS')
                    self.assertEqual(report['phase_acceptance']['baseline'], expected)
                    self.assertEqual(result, {'PASS': 0, 'FAIL': 1, 'BLOCKED': 2}[expected])
                    self.assertFalse(report['remaining_requirements'])

    def test_storage_linux_runner_explicit_inputs_and_elf_architecture(self):
        with tempfile.TemporaryDirectory() as directory:
            node = Path(directory) / 'node'
            node.write_bytes(b'\x7fELF\x02\x01' + bytes(12) + (183).to_bytes(2, 'little'))
            node.chmod(0o700)
            source = {'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_BOT_FILES_TEST_IMAGE': 'owned:local',
                      'ASTONISH_BOT_FILES_TEST_BINARY': str(node), 'ASTONISH_TEST_COMPILED_HOME_FILES': '1'}
            image = subprocess.CompletedProcess([], 0, json.dumps([
                {'Os': 'linux', 'Architecture': 'arm64', 'Id': 'sha256:' + 'a' * 64}]), '')
            with patch.object(driver.shutil, 'which', return_value='/tool'), \
                    patch.object(driver.subprocess, 'run', return_value=image) as probe:
                for tier in driver.LINUX_FIXTURE_TIERS:
                    runner, errors = driver.consolidation_linux_fixture_prerequisites(source, tier, {})
                    self.assertFalse(errors)
                    self.assertEqual(runner['architecture'], 'arm64')
                node.write_bytes(b'not-linux')
                self.assertIn('Linux ELF', str(driver.consolidation_linux_fixture_prerequisites(
                    source, 'storage-kubernetes-adapter-fixture', {})[1]))
                probe.reset_mock()
                self.assertTrue(driver.consolidation_linux_fixture_prerequisites({}, 'storage-compiled-linux', {})[1])
                probe.assert_not_called()

    def test_storage_linux_runner_exact_transport_and_owned_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tier = 'storage-kubernetes-adapter-fixture'
            package, names = driver.CONSOLIDATION_STORAGE[tier]
            node = root / 'node'
            node.write_bytes(b'node')
            runner = {'kind': 'disposable-linux-container', 'architecture': 'arm64',
                      'image': 'sha256:' + 'a' * 64, 'node_binary': str(node)}
            observed = []
            def execute(command, env, evidence, **kwargs):
                observed.append(command)
                if command[:3] == ['go', 'test', '-c']:
                    self.assertEqual((env['GOOS'], env['GOARCH'], env['CGO_ENABLED']), ('linux', 'arm64', '0'))
                    Path(command[command.index('-o') + 1]).write_bytes(b'tests')
                    return True
                self.assertEqual(command, driver.consolidation_linux_fixture_command(package, names, runner))
                create = driver.consolidation_linux_fixture_create_command(names, runner)
                self.assertIn('--network=none', create)
                self.assertIn('--pull=never', create)
                self.assertIn('--read-only', create)
                self.assertNotIn('--privileged', create)
                self.assertNotIn('--mount', create)
                self.assertNotIn('/var/run/docker.sock', ' '.join(create))
                self.assertNotIn('KUBECONFIG', str(env))
                self.assertIn('-test.v=test2json', create)
                # Simulate an interrupted transport; cleanup must still target only
                # this invocation's name, and no run-pass evidence may be fabricated.
                raise subprocess.TimeoutExpired(command, 1)
            with patch.object(driver, 'run', side_effect=execute), \
                    patch.object(driver.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as cleanup:
                with self.assertRaises(subprocess.TimeoutExpired):
                    driver.consolidation_linux_fixture_run(tier, package, names, runner, {}, root, object())
                self.assertEqual(cleanup.call_count, 4)
                self.assertEqual(cleanup.call_args_list[0].args[0], runner['create_command'])
                self.assertEqual([call.args[0] for call in cleanup.call_args_list[1:3]], runner['copy_commands'])
                self.assertEqual(cleanup.call_args.args[0], ['docker', 'rm', '-f', '-v', runner['container']])
                self.assertRegex(runner['container'], '^astonish-e2e-files-[0-9a-f]{32}$')
                self.assertEqual(len(observed), 2)
                # Receipt hash must name the actual immutable Docker cp source,
                # not a mutable source-tree build that may be rebuilt later.
                self.assertEqual(Path(runner['node_binary']).parent, root)
                self.assertNotEqual(runner['node_binary'], str(node))
                self.assertEqual(Path(runner['node_binary']).read_bytes(), b'node')
                node.write_bytes(b'later-build')
                self.assertEqual(driver.hashlib.sha256(Path(runner['node_binary']).read_bytes()).hexdigest(),
                                 runner['node_sha256'])

    def test_storage_mac_linux_gate_and_requirement_transport_tamper_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tier = 'storage-compiled-linux'
            package, names = driver.CONSOLIDATION_STORAGE[tier]
            runner = {'kind': 'disposable-linux-container', 'architecture': 'arm64',
                      'image': 'sha256:' + 'a' * 64, 'node_binary': None}
            def execute(command, env, evidence=None, **kwargs):
                if command[:3] == ['go', 'test', '-c']:
                    self.assertEqual(evidence.errors(), [])
                    Path(command[command.index('-o') + 1]).write_bytes(b'compiled-current-source')
                    return True
                self.assertEqual(command[:6], ['go', 'tool', 'test2json', '-t', '-p', 'github.com/SAP/astonish/pkg/sandbox'])
                # Test seam reuses the declared names, not a previous receipt.
                return self.consolidation_pass(driver.consolidation_command(tier, package, names), env, evidence)
            with patch.object(driver.sys, 'platform', 'darwin'), \
                    patch.object(driver, 'consolidation_linux_fixture_prerequisites', return_value=(runner, [])), \
                    patch.object(driver, 'run', side_effect=execute), \
                    patch.object(driver.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')):
                gate = driver.consolidation_progress_gate(tier, package, names, {}, root)
            self.assertEqual(gate['status'], 'PASS')
            key = 'storage-fences-and-transport-matrix'
            spec = {'storage': {key: {'tiers': (tier,), 'unresolved': []}}}
            rows = driver.consolidation_requirement_evidence(('storage',), [gate], spec)
            dependency = next(row for row in rows if row['id'] == key)['required_tiers'][0]
            self.assertEqual(dependency['status'], 'PASS')
            gate['runner']['create_command'].append('--privileged')
            rows = driver.consolidation_requirement_evidence(('storage',), [gate], spec)
            dependency = next(row for row in rows if row['id'] == key)['required_tiers'][0]
            self.assertEqual(dependency['status'], 'BLOCKED')
            self.assertIn('transport evidence', str(dependency['errors']))
            gate['runner']['create_command'].pop()
            Path(runner['test_binary']).write_bytes(b'substituted')
            rows = driver.consolidation_requirement_evidence(('storage',), [gate], spec)
            self.assertEqual(next(row for row in rows if row['id'] == key)['required_tiers'][0]['status'], 'BLOCKED')

    def test_storage_container_node_receipt_exact_bytes_and_tampering(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tier = 'storage-kubernetes-adapter-fixture'
            package, names = driver.CONSOLIDATION_STORAGE[tier]
            node = root / 'source-node'
            node.write_bytes(b'fresh-linux-node')
            runner = {'kind': 'disposable-linux-container', 'architecture': 'arm64',
                      'image': 'sha256:' + 'a' * 64, 'node_binary': str(node)}
            def execute(command, env, evidence, **kwargs):
                if command[:3] == ['go', 'test', '-c']:
                    Path(command[command.index('-o') + 1]).write_bytes(b'current-linux-tests')
                    return True
                return self.consolidation_pass(driver.consolidation_command(tier, package, names), env, evidence)
            with patch.object(driver.sys, 'platform', 'darwin'), \
                    patch.object(driver, 'consolidation_linux_fixture_prerequisites', return_value=(runner, [])), \
                    patch.object(driver, 'run', side_effect=execute), \
                    patch.object(driver.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')):
                gate = driver.consolidation_progress_gate(tier, package, names, {}, root)
            self.assertEqual(gate['status'], 'PASS')
            key = 'storage-fences-and-transport-matrix'
            spec = {'storage': {key: {'tiers': (tier,), 'unresolved': []}}}
            def dependency(receipt):
                rows = driver.consolidation_requirement_evidence(('storage',), [receipt], spec)
                return next(row for row in rows if row['id'] == key)['required_tiers'][0]
            # Actual runner shape uses container env and test2json, not native
            # fixture env. Replacing the original build must not stale its copy.
            node.write_bytes(b'later-build')
            self.assertEqual(dependency(gate)['status'], 'PASS')
            for field, value in (
                    ('node_sha256', '0' * 64), ('node_binary', str(node)),
                    ('node_binary', 'relative-node'), ('image', 'mutable:tag'),
                    ('execution_command', ['go', 'test', package]),
                    ('create_command', runner['create_command'] + ['--privileged']),
                    ('copy_commands', runner['copy_commands'][:1]),
                    ('build_command', runner['build_command'] + ['-run', '.*'])):
                with self.subTest(field=field, value=value):
                    receipt = json.loads(json.dumps(gate))
                    receipt['runner'][field] = value
                    self.assertEqual(dependency(receipt)['status'], 'BLOCKED')
            for field, value in (('fresh', False), ('passed', []), ('required', [])):
                with self.subTest(field=field):
                    receipt = json.loads(json.dumps(gate))
                    receipt[field] = value
                    self.assertEqual(dependency(receipt)['status'], 'BLOCKED')
            snapshot = Path(runner['node_binary'])
            snapshot.chmod(0o700)
            snapshot.write_bytes(b'tampered-copy-source')
            self.assertIn('exact fresh Linux node binary', str(dependency(gate)['errors']))
            self.assertEqual(dependency(gate)['status'], 'BLOCKED')

    def test_storage_deferred_profile_accounts_full_dependencies_and_missing_exact_child(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shell = root / '@base/rootfs/bin/sh'
            shell.parent.mkdir(parents=True)
            shell.write_text('fixture')
            node = root / 'node'
            node.write_text('fixture')
            node.chmod(0o700)
            source = {'PATH': os.environ['PATH'], 'ASTONISH_TEST_DOCKER': '1',
                      'ASTONISH_SANDBOX_IMAGE': 'owned:local', 'ASTONISH_BOT_HOME_TEST_LAYERS': str(root),
                      'ASTONISH_TEST_COMPILED_HOME_FILES': '1', 'ASTONISH_BOT_FILES_TEST_BINARY': str(node)}
            for omit in (False, True):
                def execute(command, env, evidence=None, **kwargs):
                    if omit and command[-3] == './pkg/store/entstore':
                        evidence.feed(json.dumps({'Package': 'github.com/SAP/astonish/pkg/store/entstore',
                                                  'Test': 'TestBackupFileFirstRestore', 'Action': 'skip'}) + '\n')
                        return True
                    return self.consolidation_pass(command, env, evidence)
                with patch.dict(os.environ, source, clear=True), patch.object(driver.sys, 'platform', 'linux'), \
                        patch.object(driver, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                    report = root / 'report.json'
                    code = driver.main(['--stage', 'chat-consolidation', '--phase', 'storage',
                                        '--defer-kubernetes-postgresql', '--report', str(report)])
                    data = json.loads(report.read_text())
                self.assertEqual(code, 1 if omit else 0)
                self.assertEqual(data['status'], 'FAIL' if omit else 'PASS')
                tiers = [gate['tier'] for gate in data['gates']]
                self.assertEqual(len(tiers), len(set(tiers)))
                for tier in ('controller-store', 'controller-daemon', 'controller-docker-allmode', 'runtime-kubernetes-bootstrap'):
                    self.assertIn(tier, tiers)
                live = next(row for row in data['requirement_evidence'] if row['id'] == 'storage-live-kubernetes')
                self.assertEqual(live['status'], 'DEFERRED')
                self.assertEqual(live['evidence_status'], 'NOT PASS')
                if omit:
                    self.assertTrue(data['remaining_requirements'])
                else:
                    self.assertFalse(data['remaining_requirements'])
                    for row in data['requirement_evidence']:
                        if row['status'] == 'PASS':
                            self.assertTrue(row['scenarios'])
                            self.assertEqual(row['physical_validation'], 'DEFERRED')

    @staticmethod
    def restore_library_inventory():
        return {
            'backup_filefirst_neutral_test.go': ['TestBackupFileFirstRestoreBackendNeutral'],
            'backup_filefirst_postgres_test.go': ['TestPostgresFileFirstReceiptBinding',
                'TestPostgresFileFirstReceiptReservedBeforeCreate', 'TestPostgresFileFirstNeverAdoptsExistingOrAmbiguousCreate',
                'TestPostgresFileFirstCleanupExactReceiptAndRetry', 'TestPostgresFileFirstTenantBeforePublication',
                'TestPostgresFileFirstPublicationRollbackAndAmbiguousCommit'],
            'backup_filefirst_postgres_import_test.go': ['TestPostgresFileFirstTenantTransactionAndFailure',
                'TestPostgresFileFirstExclusionAndGrantFailures', 'TestPostgresFileFirstPublicCleanupReceiptRetry',
                'TestPostgresFileFirstCapabilityPreflightReadOnly'],
            'tenant_router_generation_test.go': ['TestTenantGenerationRoute', 'TestTenantGenerationRouteTeamSchema',
                'TestStagedPostgresOrgReceiptPreflight'],
        }

    def test_restore_library_exact_source_and_dependency_inventory(self):
        package, names = driver.CONSOLIDATION_CONTROLLER['controller-store']
        self.assertEqual(package, './pkg/store/entstore')
        for filename, parents in self.restore_library_inventory().items():
            source = (driver.ROOT / 'pkg/store/entstore' / filename).read_text()
            self.assertNotIn('//go:build integration', source)
            for name in parents:
                self.assertEqual(names.count(name), 1)
                self.assertIn('func ' + name + '(t *testing.T)', source)
        for requirement in ('controller-cold-cross-backend', 'storage-offline-backup-restore'):
            self.assertIn('controller-store', driver.CONSOLIDATION_REQUIREMENT_TIERS[requirement])
            self.assertIn('controller-kubernetes-home', driver.CONSOLIDATION_REQUIREMENT_TIERS[requirement])
            self.assertIn('storage-docker-backup-cli', driver.CONSOLIDATION_REQUIREMENT_TIERS[requirement])
        self.assertNotIn('TestBackupFileFirstPostgresRestoreStagingBlocker', names)

    def test_restore_library_missing_skip_wrong_package_unfinished_failure_rejects(self):
        names = [name for parents in self.restore_library_inventory().values() for name in parents]
        required = driver.consolidation_required_tests('controller-store', names)
        self.assertEqual(len(required), 58)
        pkg = 'github.com/SAP/astonish/pkg/store/entstore'
        for omitted in required:
            for failure in ('missing', 'skip', 'wrong-package', 'unfinished', 'fail', 'package-missing'):
                with self.subTest(omitted=omitted, failure=failure):
                    evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                    for name in required:
                        if name == omitted and failure == 'missing':
                            continue
                        owner = 'wrong/package' if name == omitted and failure == 'wrong-package' else pkg
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': 'run'}))
                        if name == omitted and failure == 'unfinished':
                            continue
                        action = failure if name == omitted and failure in ('skip', 'fail') else 'pass'
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': action}))
                    if failure != 'package-missing':
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                    self.assertTrue(driver.consolidation_exact_errors(evidence, './pkg/store/entstore', required))

    def test_cold_restore_wired_source_and_mandatory_helper_inventory(self):
        self.assertNotIn('controller-cold-cross-backend', driver.CONSOLIDATION_REQUIREMENT_UNRESOLVED)
        for requirement in ('controller-cold-cross-backend', 'storage-offline-backup-restore'):
            self.assertIn('runtime-kubernetes-bootstrap', driver.CONSOLIDATION_REQUIREMENT_TIERS[requirement])
            self.assertIn('controller-store', driver.CONSOLIDATION_REQUIREMENT_TIERS[requirement])
            self.assertIn('controller-kubernetes-home', driver.CONSOLIDATION_REQUIREMENT_TIERS[requirement])
        source = (driver.ROOT / 'tests/e2eboot/sandbox_k8s_home.go').read_text()
        cold = source.split('func (f *RetainedHomeLifecycle) ColdBackupRestore')[1].split('func retainedHomeArchiveFixture')[0]
        for marker in ('retainedHomePublicRestore', 'newRetainedHomeRestoreSQL', 'retainedHomeRestoreOwner',
                       'retainedHomePausedSchedules', 'validateRetainedHomeRestoredRoute'):
            self.assertIn(marker, cold)
        self.assertNotIn('sqlTarget.PlanPlatformRestore', cold)
        self.assertNotIn('t.Fatal("BLOCKED: detached Kubernetes bytes restored before SQL;', cold)
        source_tests = (driver.ROOT / 'tests/e2eboot/retained_home_restore_test.go').read_text()
        actual = re.findall(r'func (Test\w+)\(t \*testing.T\)', source_tests)
        expected = ['TestRetainedHomePublicRestoreCallsPlanAndRestore',
                    'TestRetainedHomeRestoreOwnershipCleanupOrderSourcePreserved',
                    'TestRetainedHomeRestoreCannotAdoptSource',
                    'TestRetainedHomePausedSchedulesRejectsFabricatedEvidence',
                    'TestRetainedHomeSQLReceiptRejectsRecreatedDatabase',
                    'TestRetainedHomePublicRestoreFailureCleanupOnlyTarget',
                    'TestRetainedHomeColdRestoreHasNoPlanningStub']
        self.assertEqual(actual, expected)
        package, names = driver.CONSOLIDATION_RUNTIME['runtime-kubernetes-bootstrap']
        self.assertEqual(package, './tests/e2eboot')
        input_required = driver.consolidation_required_tests('runtime-kubernetes-bootstrap', ['TestRetainedHomeKubernetesInputs'])
        self.assertEqual(input_required[-2:], ['TestRetainedHomeKubernetesInputs/#00', 'TestRetainedHomeKubernetesInputs/#01'])
        for name in expected:
            self.assertEqual(names.count(name), 1)
            self.assertNotIn(name, driver.CONSOLIDATION_SUBTESTS)

    def test_cold_restore_helper_missing_skip_wrong_package_unfinished_failure_rejects(self):
        package, names = driver.CONSOLIDATION_RUNTIME['runtime-kubernetes-bootstrap']
        required = driver.consolidation_required_tests('runtime-kubernetes-bootstrap', names)
        pkg = 'github.com/SAP/astonish/tests/e2eboot'
        for omitted in names[-7:]:
            for failure in ('missing', 'skip', 'wrong-package', 'unfinished', 'fail', 'package-missing'):
                with self.subTest(omitted=omitted, failure=failure):
                    evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                    for name in required:
                        if name == omitted and failure == 'missing':
                            continue
                        p = 'wrong/package' if name == omitted and failure == 'wrong-package' else pkg
                        evidence.feed(json.dumps({'Package': p, 'Test': name, 'Action': 'run'}))
                        if name == omitted and failure == 'unfinished':
                            continue
                        action = failure if name == omitted and failure in ('skip', 'fail') else 'pass'
                        evidence.feed(json.dumps({'Package': p, 'Test': name, 'Action': action}))
                    if failure != 'package-missing':
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                    self.assertTrue(driver.consolidation_exact_errors(evidence, package, required))

    @staticmethod
    def owner_plan_consent_inventory():
        return {
            'controller-daemon': {'TestBotOwnerPlanConsentHTTP': ('pkg/daemon/bot_prepared_plan_admission_http_test.go', ['personal', 'team'])},
            'controller-api': {'TestBotPlanningOwnerHTTPControls': ('pkg/api/bot_planning_controls_http_test.go', ['personal', 'team'])},
            'controller-store': {'TestBotPlanningDurableOwnerMode': ('pkg/store/entstore/bot_planning_controls_test.go', ['personal', 'team'])},
        }

    def test_owner_plan_consent_exact_source_inventory(self):
        for tier, inventory in self.owner_plan_consent_inventory().items():
            package, names = driver.CONSOLIDATION_CONTROLLER[tier]
            for name, (filename, leaves) in inventory.items():
                self.assertEqual(names.count(name), 1)
                self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
                self.assertIn('func ' + name + '(t *testing.T)', (driver.ROOT / filename).read_text())
                self.assertEqual(driver.consolidation_required_tests(tier, [name]),
                                 [name] + [name + '/' + leaf for leaf in leaves])
        helper = (driver.ROOT / 'pkg/daemon/bot_owner_plan_consent_http_test.go').read_text()
        for marker in ('ApprovedPlanExecution: true', 'post(route, request, 202)', 'verifies != 1',
                       'snap.Document.Results', 'queued.SessionID != pending.SessionID', 'post(route, request, 409)'):
            self.assertIn(marker, helper)
        self.assertNotIn('SetApproved(', helper)
        self.assertIn('TestBotOwnerPlanConsentHTTP', driver.CONSOLIDATION_BOUNDARIES['controller-daemon'])

    def test_owner_plan_consent_missing_skip_wrong_package_unfinished_failure_rejects(self):
        for tier, inventory in self.owner_plan_consent_inventory().items():
            package = driver.CONSOLIDATION_CONTROLLER[tier][0]
            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
            required = driver.consolidation_required_tests(tier, list(inventory))
            for omitted in [None] + required:
                for failure in ('missing', 'skip', 'wrong-package', 'unfinished', 'fail', 'package-missing'):
                    with self.subTest(tier=tier, omitted=omitted, failure=failure):
                        evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                        for name in required:
                            if name == omitted and failure == 'missing':
                                continue
                            owner = 'wrong/package' if name == omitted and failure == 'wrong-package' else pkg
                            evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': 'run'}))
                            if name == omitted and failure == 'unfinished':
                                continue
                            action = failure if name == omitted and failure in ('skip', 'fail') else 'pass'
                            evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': action}))
                        if failure != 'package-missing':
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                        issues = driver.consolidation_exact_errors(evidence, package, required)
                        self.assertEqual(bool(issues), omitted is not None or failure == 'package-missing')

    def test_owner_plan_consent_architecture_accounting_is_finite(self):
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        section = text.split('<!-- owner-consent-phase4-start -->')[1].split('<!-- owner-consent-phase4-end -->')[0]
        rows = [line for line in section.splitlines() if line.startswith('| `')]
        ids = set(driver.CONSOLIDATION_REMAINING['runtime']) | set(driver.CONSOLIDATION_REMAINING['controller'])
        self.assertEqual({row.split('`')[1] for row in rows}, ids)
        self.assertEqual(len(rows), 8)
        for row in rows:
            for tier in driver.CONSOLIDATION_REQUIREMENT_TIERS[row.split('`')[1]]:
                self.assertIn('`' + tier + '`', row)
        for marker in ('TestBotOwnerPlanConsentHTTP', 'TestBotPlanningOwnerHTTPControls', 'TestBotPlanningDurableOwnerMode',
                       '15.288s', '26.261s', 'LOCAL', 'schema/Ent', 'twelve', '403', '400', '409'):
            self.assertIn(marker, section)
        self.assertNotIn('missing admitted HTTP plan consent', section)

    @staticmethod
    def service_capture_inventory():
        return {
            'controller-daemon': {
                'TestBotConfiguredBrowserAdmissionHTTP': ['personal/studio', 'personal/interactive',
                    'personal/scheduled-exact-resume', 'personal/scheduled-membership-drift',
                    'team/interactive', 'team/scheduled-exact-resume', 'team/scheduled-membership-drift'],
                'TestBotPreparedPlanAdmissionHTTP': ['personal', 'team'],
                'TestBotConfiguredServiceAdmissionHTTP': [service + '/' + scope + '/' + mode
                    for service in ('a2a', 'email', 'drill') for scope in ('personal', 'team')
                    for mode in ('studio', 'interactive', 'scheduled-exact-resume')
                    if scope == 'personal' or mode != 'studio'],
                'TestCapturedScheduledDrillDriftHTTP': [scope + '/' + variant
                    for scope in ('personal', 'team') for variant in ('suite', 'tool', 'arguments', 'restricted-shell')],
                'TestScheduledDrillApprovalHasNoCapturedChildAuthority': [],
            },
            'controller-personal-mailbox': {'TestPersonalSettingsBotEmailRoundTripScopeAndClear': []},
            'controller-api': {
                'TestBotAuthenticationEvidenceHTTPAcceptance': ['oauth_capability_deny', 'local_evidence'],
                'TestBotFileDeliveryHTTPNativeAuthorization': [],
                'TestBotContinueInteractiveRouteRequiresAuthentication': [],
                'TestCapturedDrillOccurrenceExactAndNoReplay': ['exact', 'arguments', 'tool', 'owner', 'revoked',
                    'uncaptured', 'shell', 'shell-grant', 'cancelled', 'missing-classifier', 'late-owner',
                    'late-fence', 'late-revoked', 'rebound-policy'],
                'TestCapturedDrillRepeatedOccurrenceConcurrentNoReplay': [],
            },
            'runtime-captured-drill-tools': {'TestCapturedDrillStaticSnapshot': [
                'static', 'dynamic', 'retry', 'browser', 'flow', 'malformed', 'missing-classifier']},
            'runtime-trusted-snapshot': {'TestBotArtifactReviewTrustedSnapshot': [
                'foreign-run', 'foreign-owner', 'different-path', 'corrupt']},
        }

    def test_service_capture_exact_source_inventory_and_required_mailbox(self):
        manifests = {**driver.CONSOLIDATION_RUNTIME, **driver.CONSOLIDATION_CONTROLLER}
        source_files = {
            'TestBotConfiguredBrowserAdmissionHTTP': 'pkg/daemon/bot_configured_browser_admission_test.go',
            'TestBotPreparedPlanAdmissionHTTP': 'pkg/daemon/bot_prepared_plan_admission_http_test.go',
            'TestBotAuthenticationEvidenceHTTPAcceptance': 'pkg/api/bot_auth_evidence_acceptance_test.go',
            'TestBotFileDeliveryHTTPNativeAuthorization': 'pkg/api/bot_file_tickets_test.go',
            'TestBotContinueInteractiveRouteRequiresAuthentication': 'pkg/api/bot_interactive_continuation_test.go',
            'TestBotConfiguredServiceAdmissionHTTP': 'pkg/daemon/bot_configured_service_admission_test.go',
            'TestCapturedScheduledDrillDriftHTTP': 'pkg/daemon/bot_captured_drill_http_test.go',
            'TestScheduledDrillApprovalHasNoCapturedChildAuthority': 'pkg/daemon/bot_scheduled_drill_authority_boundary_test.go',
            'TestPersonalSettingsBotEmailRoundTripScopeAndClear': 'pkg/store/entstore/personal_settings_bot_email_test.go',
            'TestCapturedDrillOccurrenceExactAndNoReplay': 'pkg/api/bot_captured_drill_test.go',
            'TestCapturedDrillRepeatedOccurrenceConcurrentNoReplay': 'pkg/api/bot_captured_drill_test.go',
            'TestCapturedDrillStaticSnapshot': 'pkg/tools/captured_drill_test.go',
            'TestBotArtifactReviewTrustedSnapshot': 'pkg/launcher/bot_artifact_review_test.go',
        }
        for tier, inventory in self.service_capture_inventory().items():
            package, names = manifests[tier]
            for name, leaves in inventory.items():
                self.assertEqual(names.count(name), 1)
                self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
                source = (driver.ROOT / source_files[name]).read_text()
                self.assertIn('func ' + name + '(t *testing.T)', source)
                self.assertEqual(driver.consolidation_required_tests(tier, [name]),
                                 [name] + [name + '/' + leaf for leaf in leaves])
                for leaf in leaves:
                    for piece in leaf.split('/'):
                        if piece not in ('personal', 'team'):
                            literal = piece.replace('_', ' ') if name == 'TestBotAuthenticationEvidenceHTTPAcceptance' else piece
                            self.assertIn('"' + literal + '"', source)
        browser = self.service_capture_inventory()['controller-daemon']['TestBotConfiguredBrowserAdmissionHTTP']
        self.assertEqual(len(browser), 7)
        self.assertNotIn('personal', browser)
        self.assertNotIn('team', browser)
        self.assertNotIn('TestBotConfiguredBrowserAdmissionHTTP7', manifests['controller-daemon'][1])
        self.assertIn('not HTTP plan-selection consent', driver.CONSOLIDATION_BOUNDARIES['controller-daemon'])
        self.assertIn('not physical browser', driver.CONSOLIDATION_BOUNDARIES['controller-daemon'])
        service = self.service_capture_inventory()['controller-daemon']['TestBotConfiguredServiceAdmissionHTTP']
        self.assertEqual(len(service), 15)
        self.assertEqual(len([row for row in service if row.startswith('a2a/')]), 5)
        self.assertEqual(len([row for row in service if row.startswith('email/team/')]), 2)
        self.assertEqual(len([row for row in service if row.startswith('drill/')]), 5)
        self.assertEqual(len([row for row in service if row.startswith('email/personal/')]), 3)
        for key in ('three-mode-capability-parity', 'runtime-production-wiring'):
            self.assertIn('controller-personal-mailbox', driver.CONSOLIDATION_REQUIREMENT_TIERS[key])
        self.assertIn('schema/Ent expansion needs separate authorization',
                      driver.CONSOLIDATION_BOUNDARIES['controller-personal-mailbox'])

    def test_service_capture_missing_skip_wrong_package_unfinished_and_failure_rejects(self):
        manifests = {**driver.CONSOLIDATION_RUNTIME, **driver.CONSOLIDATION_CONTROLLER}
        for tier, inventory in self.service_capture_inventory().items():
            package = manifests[tier][0]
            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
            required = driver.consolidation_required_tests(tier, list(inventory))
            for omitted in [None] + required:
                for failure in ('missing', 'skip', 'wrong-package', 'unfinished', 'fail'):
                    with self.subTest(tier=tier, omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            for name in evidence.required:
                                if name == omitted and failure == 'missing':
                                    continue
                                owner = 'wrong/package' if name == omitted and failure == 'wrong-package' else pkg
                                evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': 'run'}))
                                if name == omitted and failure == 'unfinished':
                                    continue
                                action = failure if name == omitted and failure in ('skip', 'fail') else 'pass'
                                evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': action}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, list(inventory), {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL' if omitted else 'PASS')
                        if omitted:
                            self.assertIn(omitted, str(gate['errors']))

    def test_service_capture_closure_stays_finite_and_full_service_not_suppressed(self):
        ids = set(driver.CONSOLIDATION_REMAINING['runtime']) | set(driver.CONSOLIDATION_REMAINING['controller'])
        self.assertEqual(len(ids), 8)
        for filename in ('docs/architecture/bot-runtime-evidence.md',):
            text = (driver.ROOT / filename).read_text()
            for key in ids:
                self.assertIn('`' + key + '`', text)
            for marker in ('nonzero commands', 'UNVERIFIED, NOT PASS', 'historical evidence',
                           'never recapture or rewrite history'):
                self.assertIn(marker, ' '.join(text.split()))
            self.assertIn('TestPersonalSettingsBotEmailRoundTripScopeAndClear',
                          driver.CONSOLIDATION_CONTROLLER['controller-personal-mailbox'][1])

    def test_admitted_browser_prepared_http_documentation_keeps_consent_and_storage_blockers(self):
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        for marker in ('Controlled models establish wiring', 'safety, not autonomous paid-model quality',
                       'missing prerequisites', 'skips', 'source drift', 'UNVERIFIED, NOT PASS'):
            self.assertIn(marker, ' '.join(text.split()))
        # Source inventory and exact evidence rejection are tested separately;
        # dated receipt counts and superseded closure tables are not contracts.
        for name in ('TestBotConfiguredBrowserAdmissionHTTP', 'TestBotPreparedPlanAdmissionHTTP'):
            self.assertIn(name, driver.CONSOLIDATION_CONTROLLER['controller-daemon'][1])

    @staticmethod
    def process_admission_inventory():
        rows = [scope + '/' + mode + '/' + leaf
                for scope, mode in (('personal', 'studio'), ('personal', 'interactive'), ('team', 'interactive'))
                for leaf in ('lifecycle', 'foreign-process_read', 'foreign-process_write', 'foreign-process_kill')]
        rows += [scope + '/scheduled-exact/' + leaf for scope in ('personal', 'team')
                 for leaf in ('shell_command', 'process_read', 'process_write', 'process_kill',
                              'foreign-process_read', 'foreign-process_write', 'foreign-process_kill', 'membership-loss')]
        return ['TestBotConfiguredProcessAdmissionHTTP'] + ['TestBotConfiguredProcessAdmissionHTTP/' + row for row in rows]

    def test_process_admission_exact_source_inventory(self):
        name = 'TestBotConfiguredProcessAdmissionHTTP'
        package, names = driver.CONSOLIDATION_CONTROLLER['controller-daemon']
        required = self.process_admission_inventory()
        self.assertEqual(len(required), 29)
        self.assertEqual(names.count(name), 1)
        self.assertEqual(driver.consolidation_required_tests('controller-daemon', [name]), required)
        self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
        source = (driver.ROOT / 'pkg/daemon/bot_configured_process_admission_test.go').read_text()
        self.assertIn('func ' + name + '(t *testing.T)', source)
        self.assertIn('t.Run(string(scope)+"/"+mode+"/lifecycle"', source)
        self.assertIn('t.Run(string(scope)+"/scheduled-exact/"+leaf', source)
        for row in required[1:]:
            for piece in row.split('/')[2:]:
                self.assertIn('"' + piece + '"', source)
        self.assertNotIn(name + '/team/studio/lifecycle', required)
        for parent in ('personal', 'team', 'personal/studio', 'personal/interactive',
                       'team/interactive', 'personal/scheduled-exact', 'team/scheduled-exact'):
            self.assertNotIn(name + '/' + parent, required)
        self.assertIn('not continuous scheduled lifecycle', driver.CONSOLIDATION_BOUNDARIES['controller-daemon'])

    def test_process_admission_missing_skip_wrong_package_unfinished_failure_rejects(self):
        required = self.process_admission_inventory()
        pkg = 'github.com/SAP/astonish/pkg/daemon'
        for omitted in [None] + required:
            for failure in ('missing', 'skip', 'wrong-package', 'unfinished', 'fail'):
                with self.subTest(omitted=omitted, failure=failure):
                    evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                    for name in required:
                        if name == omitted and failure == 'missing':
                            continue
                        owner = 'wrong/package' if name == omitted and failure == 'wrong-package' else pkg
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': 'run'}))
                        if name == omitted and failure == 'unfinished':
                            continue
                        action = failure if name == omitted and failure in ('skip', 'fail') else 'pass'
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': action}))
                    evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                    issues = driver.consolidation_exact_errors(evidence, './pkg/daemon', required)
                    self.assertEqual(bool(issues), omitted is not None)
                    if omitted:
                        self.assertIn(omitted, str(issues))
        evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
        for name in required:
            evidence.feed(json.dumps({'Package': pkg, 'Test': name, 'Action': 'run'}))
            evidence.feed(json.dumps({'Package': pkg, 'Test': name, 'Action': 'pass'}))
        self.assertIn('package did not finish with pass: ' + pkg,
                      driver.consolidation_exact_errors(evidence, './pkg/daemon', required))

    def test_process_admission_documentation_finite_closure_and_boundaries(self):
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        for marker in ('owned disposable test fixtures', 'not completed evidence',
                       'UNVERIFIED, NOT PASS', 'never recapture or rewrite history'):
            self.assertIn(marker, ' '.join(text.split()))
        self.assertIn('not continuous scheduled lifecycle', driver.CONSOLIDATION_BOUNDARIES['controller-daemon'])
        self.assertEqual(len(self.process_admission_inventory()), 29)

    @staticmethod
    def production_template_inventory():
        return {
            'TestProductionBotTemplateAssemblySource': [],
            'TestProductionBotTemplateBootstrapCapture': [],
            'TestProductionBotTemplateMissingBundleFailsClosed': [],
            'TestProductionBotTemplateWiringScopedDispatch': [scope + '/' + mode
                for scope in ('personal', 'team') for mode in ('ordinary', 'interactive', 'autonomous')],
        }

    def test_production_template_manifest_requires_bootstrap_handoff_and_scoped_dispatch(self):
        expected = self.production_template_inventory()
        source = (driver.ROOT / 'pkg/daemon/bot_template_wiring_test.go').read_text()
        for tier, manifest in (('runtime-template-wiring', driver.CONSOLIDATION_RUNTIME),
                               ('controller-daemon', driver.CONSOLIDATION_CONTROLLER)):
            package, names = manifest[tier]
            self.assertEqual(package, './pkg/daemon')
            if tier == 'runtime-template-wiring':
                self.assertCountEqual(names, expected)
            for name, leaves in expected.items():
                self.assertIn('func ' + name + '(t *testing.T)', source)
                self.assertEqual(names.count(name), 1)
                self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
                children = driver.CONSOLIDATION_SUBTESTS | driver.CONSOLIDATION_CONTROLLER_SUBTESTS.get(tier, {})
                self.assertEqual(children.get(name, []), leaves)
            with tempfile.TemporaryDirectory() as directory, patch.object(driver, 'run', side_effect=self.consolidation_pass):
                gate = driver.consolidation_progress_gate(tier, package, list(expected), {}, Path(directory))
            self.assertEqual(gate['status'], 'PASS')
            self.assertEqual(len(gate['required']), 10)
        self.assertIn('runtime-template-wiring', driver.CONSOLIDATION_REQUIREMENT_TIERS['runtime-production-wiring'])
        self.assertIn('runtime-production-wiring', driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertIn('three-mode-capability-parity', driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertIn('controller-authority-matrix', driver.CONSOLIDATION_REMAINING['controller'])
        self.assertIn('not physical', driver.CONSOLIDATION_BOUNDARIES['runtime-template-wiring'])
        for filename in ('bot-basic-chat-tools.md', 'bots.md'):
            text = (driver.ROOT / 'docs/architecture' / filename).read_text()
            for marker in ('SharedResources', 'productionBotChatResources', 'borrow',
                           'fail closed', 'runtime-template-wiring', 'controller-daemon',
                           'physical tenant isolation', 'blocked'):
                self.assertIn(marker, text)
            for name in expected:
                self.assertIn(name, text)

    def test_production_template_missing_skipped_or_wrong_package_parent_or_leaf_fails(self):
        expected = self.production_template_inventory()
        for tier in ('runtime-template-wiring', 'controller-daemon'):
            for name, leaves in expected.items():
                for omitted in [name] + [name + '/' + leaf for leaf in leaves]:
                    for failure in ('missing', 'skip', 'wrong-package'):
                        with self.subTest(tier=tier, omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                            def execute(command, env, evidence=None, **kwargs):
                                pkg = 'github.com/SAP/astonish/pkg/daemon'
                                self.assertIn(omitted, evidence.required)
                                for required in evidence.required:
                                    if required == omitted and failure == 'missing':
                                        continue
                                    owner = 'github.com/SAP/astonish/pkg/launcher' if required == omitted and failure == 'wrong-package' else pkg
                                    evidence.feed(json.dumps({'Package': owner, 'Test': required, 'Action': 'run'}))
                                    evidence.feed(json.dumps({'Package': owner, 'Test': required,
                                        'Action': 'skip' if required == omitted and failure == 'skip' else 'pass'}))
                                evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                                return True
                            with patch.object(driver, 'run', side_effect=execute):
                                gate = driver.consolidation_progress_gate(tier, './pkg/daemon', list(expected), {}, Path(directory))
                            self.assertEqual(gate['status'], 'FAIL')
                            self.assertIn(omitted, str(gate['errors']))

    @staticmethod
    def search_retry_inventory():
        return {
            'controller-daemon': {
                'TestBotConfiguredSearchOwnerResumeHTTP': ['personal', 'team'] +
                    [scope + '/' + drift + '-drift' for scope in ('personal', 'team')
                     for drift in ('model', 'provider', 'credential', 'dispatch-credential', 'extra-tool')],
            },
            'controller-api': {
                'TestBotResumeUnusedTargetDenialRestoresExactAction': [],
                'TestBotResumeAmbiguousConsumptionNeverRetryable': [],
                'TestBotResumeNoReplayAfterEffect': ['model', 'extra-tool', 'tool-error'],
                'TestBotChatExactApprovalContinuation': ['#00', 'draft_test'],
                'TestBotResumeModelPreservesExactNumbers': [],
                'TestBotMCPApprovalCheckpointTargetBinding': ['same-target-once', 'changed-target-blocked'],
            },
            'controller-store': {
                'TestBotApprovalResumePreDispatchDenial': ['personal', 'team'],
                'TestBotApprovalResumeFenced': ['personal', 'team'],
                'TestBotApprovalRequeueCurrentAuthority': [scope + '/' + mode for scope in ('personal', 'team')
                    for mode in ('paused', 'expired', 'revoked-membership', 'denied', 'wrong-approval', 'changed-version')],
                'TestBotApprovalConcurrentConsumption': ['personal', 'team'],
                'TestBotApprovalCrashRecoveryEvidence': [scope + '/' + mode for scope in ('personal', 'team')
                    for mode in ('approved-not-dispatched', 'consumed-outcome-unknown')],
                'TestBotApproveContinueAtomic': [scope + '/' + mode for scope in ('personal', 'team')
                    for mode in ('queued', 'paused', 'expired', 'wrong-digest', 'revoked', 'rollback',
                        'requeue-failure', 'stopped', 'deleted', 'changed-version', 'stale-checkpoint',
                        'wrong-actor', 'changed-action', 'denied')],
                'TestBotContentApprovalCodec': ['bot', 'approval', 'run', 'version', 'digest',
                    'tool', 'effect', 'target', 'spend', 'size', 'bodydigest'],
                'TestBotContentApprovalCorruptMissing': [],
                'TestBotContentApprovalSQLiteReopen': ['personal', 'team'],
            },
        }

    def test_search_retry_manifest_exact_source_inventory(self):
        for tier, inventory in self.search_retry_inventory().items():
            package, names = driver.CONSOLIDATION_CONTROLLER[tier]
            sources = '\n'.join(p.read_text() for p in (driver.ROOT / package).glob('*test.go'))
            for name, leaves in inventory.items():
                with self.subTest(tier=tier, name=name):
                    self.assertEqual(names.count(name), 1)
                    self.assertIn('func ' + name + '(t *testing.T)', sources)
                    self.assertEqual(driver.consolidation_required_tests(tier, [name]),
                                     [name] + [name + '/' + leaf for leaf in leaves])
                    self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
        source_files = {
            'TestBotResumeUnusedTargetDenialRestoresExactAction': 'pkg/api/bot_runner_resume_denial_test.go',
            'TestBotResumeAmbiguousConsumptionNeverRetryable': 'pkg/api/bot_runner_resume_denial_test.go',
            'TestBotResumeNoReplayAfterEffect': 'pkg/api/bot_runner_resume_safety_test.go',
            'TestBotChatExactApprovalContinuation': 'pkg/api/bot_runner_resume_test.go',
            'TestBotResumeModelPreservesExactNumbers': 'pkg/api/bot_runner_resume_safety_test.go',
            'TestBotMCPApprovalCheckpointTargetBinding': 'pkg/api/bot_mcp_resume_test.go',
            'TestBotApprovalResumePreDispatchDenial': 'pkg/store/entstore/bot_approval_resume_denial_test.go',
            'TestBotApproveContinueAtomic': 'pkg/store/entstore/bot_approve_continue_test.go',
            'TestBotContentApprovalCodec': 'pkg/store/entstore/bot_content_approval_test.go',
            'TestBotContentApprovalCorruptMissing': 'pkg/store/entstore/bot_content_approval_test.go',
            'TestBotContentApprovalSQLiteReopen': 'pkg/store/entstore/bot_content_approval_test.go',
        }
        for name, path in source_files.items():
            self.assertIn('func ' + name + '(t *testing.T)', (driver.ROOT / path).read_text())
        atomic = (driver.ROOT / source_files['TestBotApproveContinueAtomic']).read_text()
        for leaf in self.search_retry_inventory()['controller-store']['TestBotApproveContinueAtomic']:
            self.assertIn('"' + leaf.split('/')[1] + '"', atomic)
        codec = (driver.ROOT / source_files['TestBotContentApprovalCodec']).read_text()
        for leaf in self.search_retry_inventory()['controller-store']['TestBotContentApprovalCodec']:
            self.assertIn('"' + leaf + '":', codec)
        source = (driver.ROOT / 'pkg/daemon/bot_configured_search_resume_test.go').read_text()
        for drift in ('model', 'provider', 'credential', 'dispatch-credential', 'extra-tool'):
            for scope in ('Personal', 'Team'):
                self.assertIn('{store.BotScope' + scope + ', "' + drift + '"}', source)
        self.assertIn('t.Run(name, func(t *testing.T)', source)
        self.assertIn('name += "/" + scenario.drift + "-drift"', source)

    def test_search_retry_missing_skip_wrong_package_or_unfinished_fails(self):
        for tier, inventory in self.search_retry_inventory().items():
            package = driver.CONSOLIDATION_CONTROLLER[tier][0]
            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
            required = driver.consolidation_required_tests(tier, list(inventory))
            for omitted in [None] + required:
                for failure in ('missing', 'skip', 'wrong-package', 'unfinished'):
                    with self.subTest(tier=tier, omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            for expected in evidence.required:
                                if expected == omitted and failure == 'missing':
                                    continue
                                owner = 'wrong/package' if expected == omitted and failure == 'wrong-package' else pkg
                                evidence.feed(json.dumps({'Package': owner, 'Test': expected, 'Action': 'run'}))
                                if expected == omitted and failure == 'unfinished':
                                    continue
                                evidence.feed(json.dumps({'Package': owner, 'Test': expected,
                                    'Action': 'skip' if expected == omitted and failure == 'skip' else 'pass'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, list(inventory), {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL' if omitted else 'PASS')
                        if omitted:
                            self.assertIn(omitted, str(gate['errors']))

    def test_phase4_fixed_closure_table_matches_current_eight_ids_and_tiers(self):
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        table = text.split('<!-- owner-consent-phase4-start -->')[1].split('<!-- owner-consent-phase4-end -->')[0]
        ids = {key for phase in ('runtime', 'controller') for key in driver.CONSOLIDATION_REMAINING[phase]}
        rows = [line for line in table.splitlines() if line.startswith('| `')]
        self.assertEqual(len(rows), 8)
        self.assertEqual({line.split('`')[1] for line in rows}, ids)
        for row in rows:
            key = row.split('`')[1]
            for tier in driver.CONSOLIDATION_REQUIREMENT_TIERS[key]:
                self.assertIn('`' + tier + '`', row)
            self.assertIn('BLOCKED', row)
        self.assertIn('Genuine infrastructure dependencies', text)
        self.assertIn('changed Bot version', text)

    def test_protocol_production_manifest_requires_exact_nine_source_rows(self):
        tier, name = 'controller-daemon', 'TestBotConfiguredProtocolAdmissionHTTP'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        self.assertEqual(package, './pkg/daemon')
        self.assertEqual(names.count(name), 1)
        source = (driver.ROOT / 'pkg/daemon/bot_configured_protocol_admission_test.go').read_text()
        self.assertIn('func ' + name + '(t *testing.T)', source)
        self.assertIn('modes := []string{"interactive", "scheduled-exact-boundary", "scheduled-mcp-exact-boundary", "scheduled-mcp-stale-config"}', source)
        self.assertIn('if scope == store.BotScopePersonal', source)
        self.assertIn('modes = append([]string{"studio"}, modes...)', source)
        leaves = ['personal/studio', 'personal/interactive',
                  'personal/scheduled-exact-boundary', 'personal/scheduled-mcp-exact-boundary',
                  'personal/scheduled-mcp-stale-config', 'team/interactive',
                  'team/scheduled-exact-boundary', 'team/scheduled-mcp-exact-boundary',
                  'team/scheduled-mcp-stale-config']
        self.assertNotIn('personal', leaves)
        self.assertNotIn('team', leaves)
        # Go creates scope/mode as one t.Run name, not separate scope parents.
        self.assertIn('t.Run(string(scope)+"/"+mode, func(t *testing.T)', source)
        self.assertEqual(driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier][name], leaves)
        self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
        with tempfile.TemporaryDirectory() as directory, patch.object(driver, 'run', side_effect=self.consolidation_pass):
            gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
        self.assertEqual(gate['status'], 'PASS')
        self.assertEqual(gate['required'], [name] + [name + '/' + leaf for leaf in leaves])

    def test_protocol_production_missing_skip_wrong_package_or_unfinished_fails(self):
        tier, name = 'controller-daemon', 'TestBotConfiguredProtocolAdmissionHTTP'
        required = driver.consolidation_required_tests(tier, [name])
        for omitted in required:
            for failure in ('missing', 'skip', 'wrong-package', 'unfinished'):
                with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                    def execute(command, env, evidence=None, **kwargs):
                        pkg = 'github.com/SAP/astonish/pkg/daemon'
                        for expected in evidence.required:
                            if expected == omitted and failure == 'missing':
                                continue
                            owner = 'github.com/SAP/astonish/pkg/launcher' if expected == omitted and failure == 'wrong-package' else pkg
                            evidence.feed(json.dumps({'Package': owner, 'Test': expected, 'Action': 'run'}))
                            if expected == omitted and failure == 'unfinished':
                                continue
                            evidence.feed(json.dumps({'Package': owner, 'Test': expected,
                                'Action': 'skip' if expected == omitted and failure == 'skip' else 'pass'}))
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                        return True
                    with patch.object(driver, 'run', side_effect=execute):
                        gate = driver.consolidation_progress_gate(tier, './pkg/daemon', [name], {}, Path(directory))
                    self.assertEqual(gate['status'], 'FAIL')
                    self.assertIn(omitted, str(gate['errors']))

    def test_protocol_boundary_separates_mcp_resume_from_pending_search_and_preserves_full_requirements(self):
        boundary = driver.CONSOLIDATION_BOUNDARIES['controller-daemon']
        for marker in ('pending-only', 'zero MCP/search dispatch', 'scheduled-mcp-exact-boundary',
                       'scheduled-mcp-stale-config', 'actual MCP HTTP execution',
                       'search owner restoration at stable Bot version',
                       'not continuous scheduled MCP/search execution', 'not physical compute'):
            self.assertIn(marker, boundary)
        for key in ('three-mode-capability-parity', 'runtime-authority-matrix', 'runtime-production-wiring'):
            self.assertIn(key, driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertIn('controller-authority-matrix', driver.CONSOLIDATION_REMAINING['controller'])
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        for marker in ('TestBotConfiguredProtocolAdmissionHTTP', 'scheduled-exact-boundary', 'pending-only',
                       'scheduled-mcp-exact-boundary', 'scheduled-mcp-stale-config',
                       'search owner restoration', 'full runtime/controller acceptance remains **BLOCKED**',
                       'No live probe was invoked', 'All twelve are absent'):
            self.assertIn(marker, text)
        for key in driver.RETAINED_HOME_K8S_ENV:
            self.assertIn(key, text)

    def test_continuous_production_manifest_requires_exact_five_admitted_rows(self):
        tier = 'controller-daemon'
        name = 'TestBotChatConfiguredContinuousAdmissionParityHTTP'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        self.assertEqual(names.count(name), 1)
        self.assertIn('func ' + name + '(t *testing.T)',
                      (driver.ROOT / package / 'bot_chat_configured_continuous_test.go').read_text())
        leaves = ['personal', 'team', 'personal/studio', 'personal/interactive',
                  'personal/autonomous', 'team/interactive', 'team/autonomous']
        self.assertEqual(driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier][name], leaves)
        for omitted in [None, name] + [name + '/' + leaf for leaf in leaves]:
            for failure in ('missing', 'skip'):
                with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                    def execute(command, env, evidence=None, **kwargs):
                        pkg = 'github.com/SAP/astonish/pkg/daemon'
                        for required in evidence.required:
                            if required == omitted and failure == 'missing':
                                continue
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required,
                                'Action': 'skip' if required == omitted else 'pass'}))
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                        return True
                    with patch.object(driver, 'run', side_effect=execute):
                        gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                    self.assertEqual(gate['status'], 'FAIL' if omitted else 'PASS')
        self.assertIn('three-mode-capability-parity', driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertIn('runtime-production-wiring', driver.CONSOLIDATION_REMAINING['runtime'])

    def test_requirement_accounting_full_synthetic_manifests_and_prerequisites(self):
        # Accounting-only test. This synthetic evidence is never physical acceptance.
        with tempfile.TemporaryDirectory() as directory:
            gates = []
            for manifest in driver.consolidation_manifests().values():
                for tier, (package, names) in manifest.items():
                    log = Path(directory) / (tier + '.jsonl')
                    log.touch()
                    command = driver.consolidation_command(tier, package, names)
                    required = driver.consolidation_required_tests(tier, names)
                    pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                    log.write_text('\n'.join(json.dumps({'Package': pkg, 'Test': name, 'Action': action})
                        for name in required for action in ('run', 'pass')) + '\n' +
                        json.dumps({'Package': pkg, 'Action': 'pass'}) + '\n')
                    gates.append({'tier': tier, 'package': package, 'required': required,
                        'passed': list(required), 'status': 'PASS', 'fresh': True,
                        'prerequisites_verified': True, 'errors': [], 'log': str(log), 'command': command})
            phases = tuple(driver.CONSOLIDATION_REMAINING)
            complete = {phase: {key: {'tiers': driver.CONSOLIDATION_REQUIREMENT_TIERS[key], 'unresolved': []}
                               for key in driver.CONSOLIDATION_REMAINING[phase]} for phase in phases}
            self.assertTrue(all(r['status'] == 'PASS' for r in
                driver.consolidation_requirement_evidence(phases, gates, complete)))
            # Wired production mappings must PASS with complete exact synthetic
            # evidence. This is accounting-only, not a physical acceptance receipt.
            actual = driver.consolidation_requirement_evidence(phases, gates)
            for row in actual:
                if row['id'] in driver.CONSOLIDATION_REQUIREMENT_SCENARIOS:
                    expected = driver.CONSOLIDATION_REQUIREMENT_UNRESOLVED.get(row['id'], [])
                    self.assertEqual(row['unresolved'], expected)
                    self.assertEqual(row['status'], 'BLOCKED' if expected else 'PASS')
                    self.assertTrue(all(s['status'] == 'PASS' for s in row['scenarios']))
                else:
                    self.assertEqual(row['status'], 'BLOCKED')
            for mutation in ('missing-tier', 'unknown-tier', 'missing-subcase', 'subset-manifest',
                             'prerequisites', 'stale', 'duplicate', 'command', 'log', 'empty-log', 'missing-id', 'unknown-id',
                             'skip-log', 'foreign-log', 'unfinished-log', 'fail-log', 'missing-package-pass'):
                with self.subTest(mutation=mutation):
                    changed = json.loads(json.dumps(gates))
                    requirements = json.loads(json.dumps(complete))
                    target = next(g for g in changed if g['tier'] == 'controller-daemon')
                    if mutation == 'missing-tier':
                        changed.remove(target)
                    elif mutation == 'unknown-tier':
                        requirements['runtime']['three-mode-capability-parity']['tiers'].append('unknown')
                    elif mutation == 'missing-subcase':
                        target['passed'].pop()
                    elif mutation == 'subset-manifest':
                        target['required'].pop()
                    elif mutation == 'prerequisites':
                        target['prerequisites_verified'] = False
                    elif mutation == 'stale':
                        target['fresh'] = False
                    elif mutation == 'duplicate':
                        changed.append(target)
                    elif mutation == 'command':
                        target['command'] = ['true']
                    elif mutation == 'log':
                        target['log'] = str(Path(directory) / 'absent')
                    elif mutation == 'empty-log':
                        empty = Path(directory) / 'empty.jsonl'
                        empty.touch()
                        target['log'] = str(empty)
                    elif mutation in ('skip-log', 'foreign-log', 'unfinished-log', 'fail-log', 'missing-package-pass'):
                        events = [json.loads(line) for line in Path(target['log']).read_text().splitlines()]
                        if mutation == 'foreign-log':
                            for event in events:
                                event['Package'] = 'foreign/package'
                        elif mutation == 'missing-package-pass':
                            events = [event for event in events if 'Test' in event]
                        else:
                            event = next(event for event in events if event.get('Action') == 'pass' and 'Test' in event)
                            if mutation == 'unfinished-log':
                                events.remove(event)
                            else:
                                event['Action'] = 'skip' if mutation == 'skip-log' else 'fail'
                        bad_log = Path(directory) / (mutation + '.jsonl')
                        bad_log.write_text('\n'.join(json.dumps(event) for event in events) + '\n')
                        target['log'] = str(bad_log)
                    elif mutation == 'missing-id':
                        del requirements['runtime']['three-mode-capability-parity']
                    else:
                        requirements['runtime']['unknown-id'] = {'tiers': ['controller-daemon'], 'unresolved': []}
                    rows = driver.consolidation_requirement_evidence(phases, changed, requirements)
                    self.assertTrue(any(r['status'] == 'BLOCKED' for r in rows))
            runtime_tiers = set(driver.CONSOLIDATION_RUNTIME)
            runtime_only = driver.consolidation_requirement_evidence(('runtime',),
                [gate for gate in gates if gate['tier'] in runtime_tiers])
            self.assertTrue(all(row['status'] == 'BLOCKED' for row in runtime_only))
            self.assertIn('controller-daemon', next(row['reason'] for row in runtime_only
                if row['id'] == 'runtime-authority-matrix'))
            failed = json.loads(json.dumps(gates))
            daemon = next(gate for gate in failed if gate['tier'] == 'controller-daemon')
            daemon.update(status='FAIL', errors=['personal mailbox persistence prerequisite failed'])
            mailbox = next(gate for gate in failed if gate['tier'] == 'controller-personal-mailbox')
            mailbox.update(status='FAIL', errors=['personal mailbox config silently dropped'])
            rows = driver.consolidation_requirement_evidence(('runtime', 'controller'), failed)
            self.assertTrue(all(row['status'] == 'BLOCKED' for row in rows
                if row['id'] not in ('runtime-lazy-session-lifecycle', 'controller-all-mode-no-python', 'controller-team-physical', 'controller-cold-cross-backend')))
            self.assertEqual(next(row for row in rows if row['id'] == 'controller-cold-cross-backend')['status'], 'PASS')
            for row in rows:
                if row['id'] in ('three-mode-capability-parity', 'runtime-production-wiring'):
                    self.assertEqual(row['unresolved'], [])
                    self.assertIn('personal-mailbox-persistence', row['reason'])
                    self.assertIn('personal mailbox config silently dropped', row['reason'])
            prerequisites = json.loads(json.dumps(gates))
            home = next(gate for gate in prerequisites if gate['tier'] == 'controller-kubernetes-home')
            home.update(status='BLOCKED', prerequisites_verified=False,
                        errors=['KUBECONFIG must name one explicit absolute dedicated config file'])
            rows = driver.consolidation_requirement_evidence(('runtime', 'controller'), prerequisites)
            for row in rows:
                if row['id'] in ('runtime-lazy-session-lifecycle', 'controller-all-mode-no-python',
                                 'controller-team-physical', 'controller-cold-cross-backend'):
                    self.assertEqual(row['status'], 'BLOCKED')
                    self.assertIn('KUBECONFIG', row['reason'])
                    self.assertEqual(row['unresolved'], driver.CONSOLIDATION_REQUIREMENT_UNRESOLVED.get(row['id'], []))

    @staticmethod
    def kubernetes_scenario_inventory():
        return {
            'TestBotsLiveKubernetesRetainedHome': [],
            'TestBotsLiveKubernetesAllModeRetainedLifecycle': ['personal', 'team'],
            'TestBotsLiveKubernetesNoPythonScheduledContinuationDelegate': ['personal', 'team'],
            'TestBotsLiveKubernetesTeamLifecycleExactDownloads': [],
            'TestBotsLiveKubernetesStoppedColdBrowseBackupRestore': [],
        }

    def test_kubernetes_scenarios_exact_source_inventory_and_cold_gap(self):
        tier = 'controller-kubernetes-home'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        inventory = self.kubernetes_scenario_inventory()
        source = (driver.ROOT / package / 'bots_test.go').read_text()
        self.assertEqual(names, list(inventory))
        self.assertEqual(len(driver.consolidation_required_tests(tier, names)), 9)
        for name, leaves in inventory.items():
            self.assertIn('func ' + name + '(t *testing.T)', source)
            self.assertEqual(driver.consolidation_required_tests(tier, [name]),
                             [name] + [name + '/' + leaf for leaf in leaves])
            self.assertIsNotNone(re.fullmatch(driver.consolidation_command(tier, package, names)[-1], name))
        for requirement, expected in {
            'runtime-lazy-session-lifecycle': names[1:3],
            'controller-all-mode-no-python': names[1:3],
            'controller-team-physical': names[3:4],
            'controller-cold-cross-backend': names[4:5],
        }.items():
            mapped = [parent for owner, parent in driver.CONSOLIDATION_REQUIREMENT_SCENARIOS[requirement].values()
                      if owner == tier]
            self.assertEqual(mapped, expected)
            self.assertNotIn('harness missing', str(driver.CONSOLIDATION_REQUIREMENT_UNRESOLVED.get(requirement, [])))
        self.assertFalse(set(driver.CONSOLIDATION_REQUIREMENT_UNRESOLVED) &
                         set(driver.CONSOLIDATION_REMAINING['controller']))
        helper = (driver.ROOT / 'tests/e2eboot/retained_home_restore.go').read_text()
        for marker in ('PlanPlatformRestore(', 'plan.Blockers', 'RestorePlatformBackup(', 'retainedHomeRestoreOwner'):
            self.assertIn(marker, helper)
        self.assertEqual(len(driver.RETAINED_HOME_K8S_ENV), 12)

    def test_kubernetes_scenarios_strict_parent_leaf_and_package_rejection(self):
        tier = 'controller-kubernetes-home'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        required = driver.consolidation_required_tests(tier, names)
        pkg = 'github.com/SAP/astonish/tests/e2e/bots'
        # Synthetic accounting only: no cluster/model/daemon subprocess is invoked.
        for omitted in [None] + required:
            for failure in ('missing', 'skip', 'wrong-package', 'unfinished', 'fail', 'package-missing'):
                with self.subTest(omitted=omitted, failure=failure):
                    evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                    for name in required:
                        if name == omitted and failure == 'missing':
                            continue
                        owner = 'foreign/package' if name == omitted and failure == 'wrong-package' else pkg
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': 'run'}))
                        if name == omitted and failure == 'unfinished':
                            continue
                        action = failure if name == omitted and failure in ('skip', 'fail') else 'pass'
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': action}))
                    if failure != 'package-missing':
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                    self.assertEqual(bool(driver.consolidation_exact_errors(evidence, package, required)),
                                     omitted is not None or failure == 'package-missing')

    def test_kubernetes_scenario_documentation_retains_finite_blocked_closure(self):
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        for marker in ('Kubernetes adapter/helper fixtures are **mocked, NONPHYSICAL**',
                       '**user-requested DEFERRED, NOT PASS**',
                       'explicit local-deferred profile', 'rollout remain unverified/unapproved'):
            self.assertIn(marker, ' '.join(text.split()))
        names = driver.CONSOLIDATION_CONTROLLER['controller-kubernetes-home'][1]
        for name in self.kubernetes_scenario_inventory():
            self.assertIn(name, names)

    def test_kubernetes_no_model_gate_blocks_before_ambient_execution(self):
        tier = 'controller-kubernetes-home'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        with tempfile.TemporaryDirectory() as directory, patch.object(driver, 'run') as run, \
                patch.object(driver, 'consolidation_kubernetes_probe') as probe:
            source = {'OPENAI_API_KEY': 'must-not-inherit', 'GOFLAGS': '-run=None'}
            env, issues = driver.consolidation_kubernetes_prerequisites(source)
            self.assertNotIn('OPENAI_API_KEY', env)
            self.assertNotIn('GOFLAGS', env)
            self.assertIn('ASTONISH_E2E_RESOURCE_OWNER', str(issues))
            self.assertIn('ASTONISH_E2E_POSTGRES_INSTANCE', str(issues))
            gate = driver.consolidation_progress_gate(tier, package, names, source, Path(directory))
            self.assertEqual(gate['status'], 'BLOCKED')
            self.assertFalse(gate['prerequisites_verified'])
            self.assertIn('-tags=e2e', gate['command'])
            self.assertIn('-timeout=20m', gate['command'])
            run.assert_not_called()
            probe.assert_not_called()

    @staticmethod
    def retained_kubernetes_env(directory):
        config = Path(directory) / 'dedicated-config'
        config.touch()
        return {'PATH': '/test/bin', 'KUBECONFIG': str(config),
                'ASTONISH_E2E_DEDICATED': '1', 'ASTONISH_E2E_BACKEND': 'postgres',
                'ASTONISH_E2E_SANDBOX_BACKEND': 'k8s',
                'ASTONISH_E2E_SANDBOX_NAMESPACE': 'astonish-e2e-homes',
                'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE': 'astonish-e2e-control',
                'ASTONISH_E2E_KUBE_CONTEXT': 'dedicated-context',
                'ASTONISH_E2E_RESOURCE_OWNER': 'astonish-e2e-homes',
                'ASTONISH_SANDBOX_IMAGE': 'test-owned:current',
                'ASTONISH_E2E_HOMES_PVC': 'homes',
                'ASTONISH_TEST_DSN': 'postgres://fixture:unused@127.0.0.1:55432/postgres?sslmode=disable',
                'ASTONISH_E2E_POSTGRES_INSTANCE': '127.0.0.1:55432'}

    def test_retained_kubernetes_missing_and_foreign_inputs_block_without_probe(self):
        tier = 'controller-kubernetes-home'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        with tempfile.TemporaryDirectory() as directory, patch.object(driver.shutil, 'which', return_value='/test/bin/tool'):
            source = self.retained_kubernetes_env(directory)
            env, issues = driver.consolidation_kubernetes_prerequisites(source)
            self.assertEqual(issues, [])
            mutations = [(key, '') for key in driver.RETAINED_HOME_K8S_ENV] + [
                ('ASTONISH_E2E_RESOURCE_OWNER', 'production'),
                ('ASTONISH_E2E_POSTGRES_INSTANCE', 'foreign:5432'),
                ('ASTONISH_E2E_CONTROL_PLANE_NAMESPACE', 'astonishe2e'),
                ('ASTONISH_E2E_SANDBOX_NAMESPACE', 'default'),
                ('KUBECONFIG', '/a:/b'), ('ASTONISH_E2E_KEEP_ALIVE', '1'),
                ('ASTONISH_E2E_BACKEND', 'sqlite'),
                ('ASTONISH_TEST_DSN', 'postgres://fixture:unused@127.0.0.1:55432/postgres?sslmode=disable&host=foreign'),
                ('ASTONISH_TEST_DSN', 'postgres://fixture@127.0.0.1:55432/postgres?sslmode=disable')]
            for key, value in mutations:
                with self.subTest(key=key, value=value), patch.object(driver, 'run') as run, \
                        patch.object(driver, 'consolidation_kubernetes_probe') as probe:
                    gate = driver.consolidation_progress_gate(tier, package, names, source | {key: value}, Path(directory))
                    self.assertEqual(gate['status'], 'BLOCKED')
                    self.assertFalse(gate['prerequisites_verified'])
                    self.assertFalse(gate['fresh'])
                    self.assertIsNone(gate['log'])
                    run.assert_not_called()
                    probe.assert_not_called()

    def test_retained_kubernetes_owned_probe_and_exact_lifecycle(self):
        tier = 'controller-kubernetes-home'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        with tempfile.TemporaryDirectory() as directory, patch.object(driver.shutil, 'which', return_value='/test/bin/tool'):
            source = self.retained_kubernetes_env(directory) | {'OPENAI_API_KEY': 'provider-secret',
                'PGPASSWORD': 'ambient-password', 'ASTONISH_BOTS_K8S_CONTEXT': 'foreign', 'GOFLAGS': '-run=None'}
            for scenario in ('owned', 'namespace-label', 'claim-label', 'claim-foreign-label', 'deleting',
                             'duplicate-volume', 'unbound', 'rwo', 'foreign-namespace', 'context', 'probe-error'):
                calls = []
                def probe(command, **kwargs):
                    calls.append(command)
                    child = kwargs['env']
                    for forbidden in ('OPENAI_API_KEY', 'PGPASSWORD', 'GOFLAGS', 'ASTONISH_BOTS_K8S_CONTEXT'):
                        self.assertNotIn(forbidden, child)
                    self.assertNotIn('provider-secret', str(command))
                    self.assertNotIn(source['ASTONISH_TEST_DSN'], str(command))
                    if scenario == 'probe-error':
                        raise OSError('provider-secret')
                    if command[1:3] == ['config', 'current-context']:
                        return subprocess.CompletedProcess(command, 0, 'foreign' if scenario == 'context' else 'dedicated-context')
                    self.assertEqual(command[1:4], ['--context', 'dedicated-context', '--request-timeout=15s'])
                    self.assertEqual(command[4], 'get')
                    claim = command[5] == 'pvc'
                    name = command[8] if claim else command[6]
                    metadata = {'name': name, 'labels': {'astonish.sap.com/e2e-owner': 'astonish-e2e-homes'}}
                    if claim:
                        metadata['namespace'] = source['ASTONISH_E2E_SANDBOX_NAMESPACE']
                    resource = {'metadata': metadata, 'spec': {'volumeName': 'pv-' + name, 'accessModes': ['ReadWriteMany']},
                                'status': {'phase': 'Bound'}}
                    if scenario == ('claim-label' if claim else 'namespace-label'):
                        metadata['labels'] = {}
                    if claim and scenario == 'claim-foreign-label':
                        metadata['labels']['astonish.sap.com/e2e-owner'] = 'foreign'
                    if scenario == 'deleting':
                        metadata['deletionTimestamp'] = 'now'
                    if claim and scenario == 'duplicate-volume':
                        resource['spec']['volumeName'] = 'shared'
                    if claim and scenario == 'unbound':
                        resource['status']['phase'] = 'Pending'
                    if claim and scenario == 'rwo':
                        resource['spec']['accessModes'] = ['ReadWriteOnce']
                    if claim and scenario == 'foreign-namespace':
                        metadata['namespace'] = 'default'
                    return subprocess.CompletedProcess(command, 0, json.dumps(resource))
                with self.subTest(scenario=scenario), patch.object(driver.subprocess, 'run', side_effect=probe), \
                        patch.object(driver, 'run', side_effect=self.consolidation_pass) as run:
                    gate = driver.consolidation_progress_gate(tier, package, names, source, Path(directory))
                    self.assertEqual(gate['command'], driver.consolidation_command(tier, package, names))
                    self.assertEqual(gate['status'], 'PASS' if scenario == 'owned' else 'BLOCKED')
                    if scenario == 'owned':
                        self.assertEqual(run.call_count, 2)
                        self.assertEqual(run.call_args_list[-1].args[0], gate['command'])
                        self.assertEqual(gate['bootstrap_evidence']['status'], 'PASS')
                        self.assertEqual(len(calls), 6)
                        self.assertTrue(gate['fresh'])
                        self.assertTrue(gate['prerequisites_verified'])
                        rows = driver.consolidation_requirement_evidence(('controller',), [gate])
                        self.assertTrue(all(row['status'] == 'BLOCKED' for row in rows))
                    else:
                        self.assertEqual(run.call_count, 1)  # safe mandatory helper only
                        self.assertFalse(gate['fresh'])
                        self.assertNotIn('provider-secret', str(gate))

    def test_retained_kubernetes_helper_failure_blocks_before_probe(self):
        tier = 'controller-kubernetes-home'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        with tempfile.TemporaryDirectory() as directory, patch.object(driver.shutil, 'which', return_value='/test/bin/tool'), \
                patch.object(driver, 'run', return_value=True) as run, \
                patch.object(driver, 'consolidation_kubernetes_probe') as probe:
            gate = driver.consolidation_progress_gate(tier, package, names,
                self.retained_kubernetes_env(directory), Path(directory))
            self.assertEqual(gate['status'], 'BLOCKED')
            self.assertEqual(gate['bootstrap_evidence']['status'], 'FAIL')
            self.assertFalse(gate['prerequisites_verified'])
            run.assert_called_once()
            probe.assert_not_called()

    def test_retained_no_model_helper_manifest_requires_exact_evidence(self):
        tier = 'runtime-kubernetes-bootstrap'
        package, names = driver.CONSOLIDATION_RUNTIME[tier]
        self.assertEqual(package, './tests/e2eboot')
        self.assertCountEqual(names, ['TestRetainedHomeNoModelPreflight', 'TestRetainedHomeOwnership',
            'TestRetainedHomeBootstrapNoProviderOrSweep', 'TestRetainedHomeDedicatedFakeClient',
            'TestRetainedHomeDedicatedContextBeforeNetwork', 'TestRetainedHomeGenericProviderGateUnchanged',
            'TestRetainedHomeKubernetesInputs',
            'TestRetainedHomePublicRestoreCallsPlanAndRestore',
            'TestRetainedHomeRestoreOwnershipCleanupOrderSourcePreserved',
            'TestRetainedHomeRestoreCannotAdoptSource',
            'TestRetainedHomePausedSchedulesRejectsFabricatedEvidence',
            'TestRetainedHomeSQLReceiptRejectsRecreatedDatabase',
            'TestRetainedHomePublicRestoreFailureCleanupOnlyTarget',
            'TestRetainedHomeColdRestoreHasNoPlanningStub'])
        for omitted in [None] + driver.consolidation_required_tests(tier, names):
            with self.subTest(omitted=omitted), tempfile.TemporaryDirectory() as directory:
                def execute(command, env, evidence=None, **kwargs):
                    self.assertIn('-tags=e2e', command)
                    self.assertFalse(any(key.startswith('ASTONISH_') for key in env))
                    pkg = 'github.com/SAP/astonish/tests/e2eboot'
                    for required in evidence.required:
                        if required == omitted:
                            continue
                        evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                        evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'pass'}))
                    evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                    return True
                with patch.object(driver, 'run', side_effect=execute):
                    gate = driver.consolidation_progress_gate(tier, package, names, {'ASTONISH_TEST_DSN': 'unused'}, Path(directory))
                self.assertEqual(gate['status'], 'PASS' if omitted is None else 'FAIL')

    @staticmethod
    def delegate_receipt_inventory():
        return {
            'TestDelegateJoinOutcomeBoundToServerChildren': ['joined', 'wrong-child', 'wrong-batch', 'failed', 'missing', 'no-identity'],
            'TestDelegateTasksRealManagerRecordsJoin': [],
            'TestDelegateTasksRealManagerRetryRecordsJoin': [],
            'TestDelegateRetryJoinRejectsAlteredIdentity': ['joined', 'forged', 'json-copy', 'child', 'batch', 'task',
                'parent', 'initial-child', 'attempt-count', 'attempt-number', 'attempt-child', 'attempt-status',
                'attempt-error', 'reordered', 'missing-attempt', 'failed', 'error', 'missing-result', 'wrong-call', 'cancelled'],
            'TestDelegateJoinRequiresWholeBatchSuccess': ['false', 'true'],
        }

    def test_delegate_receipt_manifest_requires_actual_manager_retry_and_exact_leaves(self):
        tier = 'runtime-delegate-receipts'
        package, names = driver.CONSOLIDATION_RUNTIME[tier]
        self.assertEqual(package, './pkg/tools')
        expected = self.delegate_receipt_inventory()
        self.assertCountEqual(names, expected)
        source = (driver.ROOT / package / 'delegate_join_outcome_test.go').read_text()
        for name, leaves in expected.items():
            self.assertIn('func ' + name + '(t *testing.T)', source)
            self.assertEqual(names.count(name), 1)
            self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
            self.assertEqual(driver.CONSOLIDATION_SUBTESTS.get(name, []), leaves)
        with tempfile.TemporaryDirectory() as directory, patch.object(driver, 'run', side_effect=self.consolidation_pass):
            gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
        self.assertEqual(gate['status'], 'PASS')
        self.assertEqual(len(gate['required']), 33)
        self.assertCountEqual(gate['required'], names + [name + '/' + leaf
                              for name, leaves in expected.items() for leaf in leaves])
        self.assertEqual(gate['evidence_scope'], 'implemented subset only')
        self.assertIn('never child prose', gate['boundary'])
        self.assertIn('non-JSON provenance', gate['boundary'])
        self.assertIn('no broader grants', gate['boundary'])
        self.assertTrue(driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertTrue(driver.CONSOLIDATION_REMAINING['controller'])

    def test_delegate_receipt_missing_or_skipped_parent_or_leaf_fails_full_gate(self):
        tier = 'runtime-delegate-receipts'
        package, names = driver.CONSOLIDATION_RUNTIME[tier]
        for name, leaves in self.delegate_receipt_inventory().items():
            for omitted in [name] + [name + '/' + leaf for leaf in leaves]:
                for failure in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            pkg = 'github.com/SAP/astonish/pkg/tools'
                            self.assertIn(omitted, evidence.required)
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required,
                                    'Action': 'skip' if required == omitted else 'pass'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    @staticmethod
    def activation_resume_inventory():
        return {
            'TestBotResumeDisabledScheduleApproval': ('bot_resume_schedule_test.go', ['personal', 'team']),
            'TestBotResumeDisabledScheduleRejectsCorruption': ('bot_resume_schedule_test.go', [scope + '/' + scenario
                for scope in ('personal', 'team') for scenario in ('owner', 'version', 'bot', 'trigger',
                    'cron', 'timezone', 'payload', 'next-run', 'missing', 'active')]),
            'TestBotIndependentScheduleConsent': ('bot_draft_activation_test.go', ['personal', 'team']),
            'TestBotScheduleTargetOnlyEnablement': ('bot_draft_activation_test.go', ['personal', 'team']),
            'TestBotResumeScheduleRejectsCorruption': ('bot_draft_activation_test.go', [scope + '/' + scenario
                for scope in ('personal', 'team') for scenario in ('digest', 'owner', 'bot', 'trigger', 'version',
                    'cron', 'timezone', 'payload', 'missing', 'enabled', 'enabled-next', 'next-run',
                    'wrong-scope', 'readiness', 'transaction-binding')]),
            'TestBotResumeScheduleRollback': ('bot_draft_activation_safety_test.go', ['personal', 'team']),
            'TestBotResumeScheduleLegacyReadinessCAS': ('bot_draft_activation_safety_test.go', [scope + '/' + mode
                for scope in ('personal', 'team') for mode in ('missing-readiness', 'readiness-failed',
                    'binding-changed', 'revision-changed')]),
            'TestBotAuthorizedScheduleQueuedReconciliation': ('bot_authorized_schedule_queue_test.go', [scope + '/' + op
                for scope in ('personal', 'team') for op in ('schedule_edit', 'schedule_pause', 'schedule_remove',
                    'schedule_create', 'schedule_create/disabled-unrelated')]),
        }

    def test_activation_resume_rebinding_manifest_requires_sources_and_exact_inventory(self):
        tier = 'controller-store'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        self.assertEqual(package, './pkg/store/entstore')
        for name, (filename, leaves) in self.activation_resume_inventory().items():
            self.assertIn('func ' + name + '(t *testing.T)', (driver.ROOT / package / filename).read_text())
            self.assertEqual(names.count(name), 1)
            self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
            self.assertEqual(driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier].get(name, []), leaves)
            with tempfile.TemporaryDirectory() as directory, patch.object(driver, 'run', side_effect=self.consolidation_pass):
                gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
            self.assertEqual(gate['status'], 'PASS')
            self.assertEqual(gate['required'], [name] + [name + '/' + leaf for leaf in leaves])
        self.assertTrue(driver.CONSOLIDATION_REMAINING['controller'])

    def test_activation_resume_rebinding_missing_or_skipped_parent_or_leaf_fails_gate(self):
        tier = 'controller-store'
        package, _ = driver.CONSOLIDATION_CONTROLLER[tier]
        for name, (_, leaves) in self.activation_resume_inventory().items():
            for omitted in [name] + [name + '/' + leaf for leaf in leaves]:
                for failure in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            pkg = 'github.com/SAP/astonish/pkg/store/entstore'
                            self.assertIn(omitted, evidence.required)
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required,
                                    'Action': 'skip' if required == omitted else 'pass'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    def test_join_receipt_documentation_preserves_provenance_and_effect_boundaries(self):
        text = (driver.ROOT / 'docs/architecture/bot-basic-chat-tools.md').read_text()
        for marker in ('manager-private', 'not serialized', 'not verified child text',
                       'no broader grants', '33', 'runtime-delegate-receipts'):
            self.assertIn(marker, text)
        for name in self.delegate_receipt_inventory():
            self.assertIn(name, text)
        # Lifecycle inventory is validated against current executable sources,
        # not the superseded Draft-activation discussion in this delegate doc.
        lifecycle = (driver.ROOT / 'docs/architecture/bot-draft-activation.md').read_text()
        self.assertIn('does not enable disabled schedules', lifecycle)

    @staticmethod
    def budget_preflight_inventory():
        return {
            'runtime-construction': ('./pkg/launcher', {
                'TestChatRuntimeCumulativeBudgetContinuity': [scope + '/' + mode
                    for scope in ('personal', 'team')
                    for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')],
            }),
            'runtime-live-authority': ('./pkg/chatruntime', {
                'TestLiveAuthorityModelPreflightDenialClosesReads': [],
                'TestLiveAuthorityModelPreflightReadLifetime': [],
            }),
        }

    def test_budget_preflight_manifest_requires_exact_inventory(self):
        for tier, (package, expected) in self.budget_preflight_inventory().items():
            actual_package, names = driver.CONSOLIDATION_RUNTIME[tier]
            self.assertEqual(actual_package, package)
            selector = driver.go_command(package, names)[-1]
            with tempfile.TemporaryDirectory() as directory, \
                    patch.object(driver, 'run', side_effect=self.consolidation_pass):
                gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
            self.assertEqual(gate['status'], 'PASS')
            self.assertEqual(gate['evidence_scope'], 'implemented subset only')
            for name, leaves in expected.items():
                self.assertIn(name, names)
                self.assertIsNotNone(re.fullmatch(selector, name))
                self.assertEqual(driver.CONSOLIDATION_SUBTESTS.get(name, []), leaves)
                for required in [name] + [name + '/' + leaf for leaf in leaves]:
                    self.assertIn(required, gate['required'])
        # File-ledger continuation is not production entrypoint/controller resume.
        for obligation in ('runtime-production-wiring', 'runtime-authority-matrix', 'runtime-lazy-session-lifecycle'):
            self.assertIn(obligation, driver.CONSOLIDATION_REMAINING['runtime'])
        for obligation in ('controller-authority-matrix', 'controller-all-mode-no-python'):
            self.assertIn(obligation, driver.CONSOLIDATION_REMAINING['controller'])

    def test_budget_preflight_missing_or_skipped_parent_or_leaf_fails_full_gate(self):
        for tier, (package, expected) in self.budget_preflight_inventory().items():
            names = driver.CONSOLIDATION_RUNTIME[tier][1]
            for name, leaves in expected.items():
                for omitted in [name] + [name + '/' + leaf for leaf in leaves]:
                    for failure in ('missing', 'skip'):
                        with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                            def execute(command, env, evidence=None, **kwargs):
                                pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                                self.assertIn(omitted, evidence.required)
                                for required in evidence.required:
                                    if required == omitted and failure == 'missing':
                                        continue
                                    evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                    action = 'skip' if required == omitted else 'pass'
                                    evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                                evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                                return True
                            with patch.object(driver, 'run', side_effect=execute):
                                gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
                            self.assertEqual(gate['status'], 'FAIL')
                            self.assertIn(omitted, str(gate['errors']))

    def test_runtime_manifest_requires_configured_three_mode_execution(self):
        names = driver.CONSOLIDATION_RUNTIME['runtime-construction'][1]
        self.assertIn('TestChatRuntimeCapabilityParityConfiguredHTTPAndFlow', names)
        children = driver.CONSOLIDATION_SUBTESTS['TestChatRuntimeCapabilityParity']
        for scope in ('personal', 'team'):
            for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot'):
                self.assertIn('configured-three-mode-subset/' + scope + '/' + mode, children)
        for name in ('TestChatRuntimeConfiguredSearchProviderParity',
                     'TestChatRuntimeCapabilityParityConfiguredProtocols',
                     'TestChatRuntimeCapabilityParityConfiguredServices',
                     'TestChatRuntimeCapabilityParityConfiguredPlan',
                     'TestChatRuntimeAutonomousPlanRequiresMirrorGrant'):
            self.assertIn(name, names)
        for scope in ('personal', 'team'):
            for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot'):
                self.assertIn(scope + '/' + mode, driver.CONSOLIDATION_SUBTESTS['TestChatRuntimeCapabilityParityConfiguredServices'])

    def test_process_file_browser_manifest_requires_exact_leaves_without_helper(self):
        package, names = driver.CONSOLIDATION_RUNTIME['runtime-construction']
        self.assertEqual(package, './pkg/launcher')
        expected = [scope + '/' + mode for scope in ('personal', 'team')
                    for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')]
        for name in ('TestChatRuntimeConfiguredProcessAndFileExecution',
                     'TestChatRuntimeBrowserArtifactModeParity'):
            with self.subTest(name=name):
                self.assertEqual(names.count(name), 1)
                self.assertEqual(driver.CONSOLIDATION_SUBTESTS[name], expected)
        selector = driver.go_command(package, names)[-1]
        for name in ('TestChatRuntimeConfiguredProcessAndFileExecution',
                     'TestChatRuntimeBrowserArtifactModeParity'):
            self.assertIsNotNone(re.fullmatch(selector, name))
        helper = 'TestChatRuntimeProcessNodeHelper'
        self.assertNotIn(helper, names)
        self.assertNotIn(helper, driver.CONSOLIDATION_SUBTESTS)
        self.assertIsNone(re.fullmatch(selector, helper))
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(driver, 'run', side_effect=self.consolidation_pass):
            gate = driver.consolidation_progress_gate('runtime-construction', package, names, {}, Path(directory))
        self.assertEqual(gate['status'], 'PASS')
        self.assertNotIn(helper, gate['required'])
        for name in ('TestChatRuntimeConfiguredProcessAndFileExecution',
                     'TestChatRuntimeBrowserArtifactModeParity'):
            for leaf in expected:
                self.assertIn(name + '/' + leaf, gate['required'])
        self.assertIn('three-mode-capability-parity', driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertIn('runtime-lazy-session-lifecycle', driver.CONSOLIDATION_REMAINING['runtime'])

    def test_process_file_browser_missing_or_skipped_parent_or_leaf_fails_full_gate(self):
        package, names = driver.CONSOLIDATION_RUNTIME['runtime-construction']
        leaves = [scope + '/' + mode for scope in ('personal', 'team')
                  for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')]
        for name in ('TestChatRuntimeConfiguredProcessAndFileExecution',
                     'TestChatRuntimeBrowserArtifactModeParity'):
            for omitted in [name] + [name + '/' + leaf for leaf in leaves]:
                for failure in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            self.assertIn(omitted, evidence.required)
                            pkg = 'github.com/SAP/astonish/pkg/launcher'
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                action = 'skip' if required == omitted else 'pass'
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate('runtime-construction', package, names, {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    def test_admitted_configured_journeys_require_seven_rows_without_claiming_parity(self):
        name = 'TestBotChatConfiguredAdmissionParityHTTP'
        tier = 'controller-daemon'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        self.assertEqual(package, './pkg/daemon')
        self.assertEqual(names.count(name), 1)
        leaves = ['personal/studio', 'personal/interactive', 'personal/interactive-mandate',
                  'personal/autonomous', 'team/interactive', 'team/interactive-mandate', 'team/autonomous']
        children = driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier][name]
        self.assertCountEqual(children, ['personal', 'team'] + leaves)
        self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
        self.assertNotIn('team/studio', children)
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(driver, 'run', side_effect=self.consolidation_pass):
            gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
        self.assertEqual(gate['status'], 'PASS')
        for child in children:
            self.assertIn(name + '/' + child, gate['required'])
        self.assertIn('three-mode-capability-parity', driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertTrue(driver.CONSOLIDATION_REMAINING['controller'])

    def test_admitted_configured_journey_missing_or_skipped_row_fails_full_gate(self):
        name = 'TestBotChatConfiguredAdmissionParityHTTP'
        tier = 'controller-daemon'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        children = ['personal', 'team', 'personal/studio', 'personal/interactive',
                    'personal/interactive-mandate', 'personal/autonomous', 'team/interactive',
                    'team/interactive-mandate', 'team/autonomous']
        for omitted in [name] + [name + '/' + child for child in children]:
            for failure in ('missing', 'skip'):
                with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                    def execute(command, env, evidence=None, **kwargs):
                        self.assertIn(omitted, evidence.required)
                        pkg = 'github.com/SAP/astonish/pkg/daemon'
                        for required in evidence.required:
                            if required == omitted and failure == 'missing':
                                continue
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                            action = 'skip' if required == omitted else 'pass'
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                        return True
                    with patch.object(driver, 'run', side_effect=execute):
                        gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
                    self.assertEqual(gate['status'], 'FAIL')
                    self.assertIn(omitted, str(gate['errors']))

    def test_child_and_uncertainty_manifests_require_exact_leaves(self):
        runtime = driver.CONSOLIDATION_RUNTIME['runtime-construction'][1]
        for name in ('TestChatRuntimeRestrictedChildAuthority', 'TestChatRuntimeNestedDrillFences', 'TestChatRuntimeChildCancellationJoinedReturn'):
            self.assertIn(name, runtime)
        scopes = ('personal', 'team')
        modes = ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
        self.assertEqual(set(driver.CONSOLIDATION_SUBTESTS['TestChatRuntimeRestrictedChildAuthority']),
                         {s + '/' + m for s in scopes for m in modes})
        self.assertEqual(set(driver.CONSOLIDATION_SUBTESTS['TestChatRuntimeChildCancellationJoinedReturn']),
                         {s + '/' + m for s in scopes for m in modes})
        self.assertEqual(set(driver.CONSOLIDATION_SUBTESTS['TestChatRuntimeNestedDrillFences']),
                         {s + '/' + m + '/' + f for s in scopes for m in modes
                          for f in ('none', 'cancel', 'lease-revoked', 'budget')
                          if m != 'ordinary-chat' or f in ('none', 'cancel')})
        self.assertIn('TestBotUncertainEffectRestartHTTPNoReplay', driver.CONSOLIDATION_CONTROLLER['controller-daemon'][1])
        self.assertEqual(driver.CONSOLIDATION_SUBTESTS['TestBotUncertainEffectRestartHTTPNoReplay'], list(scopes))
        self.assertIn('runtime-authority-matrix', driver.CONSOLIDATION_REMAINING['runtime'])
        self.assertIn('controller-authority-matrix', driver.CONSOLIDATION_REMAINING['controller'])

    def test_missing_child_or_uncertainty_scope_fails_gate(self):
        cases = [
            ('runtime-construction', './pkg/launcher', ['TestChatRuntimeRestrictedChildAuthority'],
             'TestChatRuntimeRestrictedChildAuthority/team/autonomous-bot'),
            ('runtime-construction', './pkg/launcher', ['TestChatRuntimeNestedDrillFences'],
             'TestChatRuntimeNestedDrillFences/personal/interactive-bot/budget'),
            ('controller-daemon', './pkg/daemon', ['TestBotUncertainEffectRestartHTTPNoReplay'],
             'TestBotUncertainEffectRestartHTTPNoReplay/team'),
        ]
        for tier, package, names, omitted in cases:
            with self.subTest(omitted=omitted), tempfile.TemporaryDirectory() as directory:
                def execute(command, env, evidence=None, **kwargs):
                    pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                    for name in evidence.required:
                        if name != omitted:
                            evidence.feed(json.dumps({'Package': pkg, 'Test': name, 'Action': 'run'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Test': name, 'Action': 'pass'}))
                    evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                    return True
                with patch.object(driver, 'run', side_effect=execute):
                    gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
                self.assertEqual(gate['status'], 'FAIL')
                self.assertIn(omitted, str(gate['errors']))

    @staticmethod
    def crash_recovery_inventory():
        return {
            'controller-daemon': ('./pkg/daemon', 'TestBotUncertainEffectCrashHTTPNoReplay',
                                  ['personal', 'team']),
            'controller-store': ('./pkg/store/entstore', 'TestBotApprovalCrashRecoveryEvidence',
                                 [scope + '/' + state for scope in ('personal', 'team')
                                  for state in ('approved-not-dispatched', 'consumed-outcome-unknown')]),
        }

    def test_crash_recovery_manifests_require_exact_evidence_without_worker_helper(self):
        helper = 'TestBotUncertainEffectCrashHTTPWorker'
        for tier, (package, name, children) in self.crash_recovery_inventory().items():
            with self.subTest(tier=tier):
                actual_package, names = driver.CONSOLIDATION_CONTROLLER[tier]
                self.assertEqual(actual_package, package)
                self.assertEqual(names.count(name), 1)
                self.assertEqual(driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier][name], children)
                command = driver.go_command(package, names)
                self.assertIn('-count=1', command)
                self.assertIn('-timeout=15m', command)
                self.assertIsNotNone(re.fullmatch(command[-1], name))
                self.assertIsNone(re.fullmatch(command[-1], helper))
                self.assertNotIn(helper, names)
                self.assertNotIn(helper, driver.CONSOLIDATION_SUBTESTS)
                self.assertNotIn(helper, driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier])
                with tempfile.TemporaryDirectory() as directory, \
                        patch.object(driver, 'run', side_effect=self.consolidation_pass):
                    gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
                self.assertEqual(gate['status'], 'PASS')
                self.assertNotIn(helper, gate['required'])
                for required in [name] + [name + '/' + child for child in children]:
                    self.assertIn(required, gate['required'])
                self.assertEqual(gate['evidence_scope'], 'implemented subset only')
        self.assertIn('controller-authority-matrix', driver.CONSOLIDATION_REMAINING['controller'])

    def test_crash_recovery_missing_or_skipped_parent_or_leaf_fails_full_gate(self):
        for tier, (package, name, children) in self.crash_recovery_inventory().items():
            names = driver.CONSOLIDATION_CONTROLLER[tier][1]
            for omitted in [name] + [name + '/' + child for child in children]:
                for failure in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                            self.assertIn(omitted, evidence.required)
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                action = 'skip' if required == omitted else 'pass'
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    def test_runtime_evidence_refresh_preserves_fixture_and_phase_boundaries(self):
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        refresh = text.split('## Phase 4 authority and schedule verification', 1)[0]
        for marker in ('Phase 4 remains open', 'TestBotPreparedBoundaryUnsettledRecoveryPreservesUncertainOutcome',
                       'TestBotChatFailurePreservesWrappedUncertainOutcome', 'bot_configuration_policy_transaction_test.go',
                       'bot_configuration_policy_http_test.go', 'bot_configuration_resume_test.go',
                       'TestChatRuntimeConfiguredProcessAndFileExecution', 'TestChatRuntimeBrowserArtifactModeParity',
                       'simulated browser', 'not a real browser', '5ecc94ce', '20 / 6',
                       'dedicated', 'continuous-integration', 'not full-phase passes'):
            self.assertIn(marker.lower(), refresh.lower())

    @staticmethod
    def configuration_authority_inventory():
        return {
            'controller-daemon': ('./pkg/daemon', {
                'TestBotConfigurationProductionPolicyHTTP': ('bot_configuration_policy_http_test.go', ['personal', 'team'])}),
            'controller-store': ('./pkg/store/entstore', {
                'TestBotConfigurationPolicyTransaction': ('bot_configuration_policy_transaction_test.go', ['personal', 'team']),
                'TestBotPolicyTeamMCPTransaction': ('bot_configuration_policy_transaction_test.go', []),
                'TestBotReviewedConfigurationRevokesOldGrants': ('bot_configuration_test.go', ['personal', 'team']),
                'TestBotAdaptiveConversionRollback': ('bot_configuration_test.go', ['personal', 'team']),
                'TestBotPausedPolicyRejectsUnreviewedPolicyReplacement': ('bot_configuration_test.go', ['personal', 'team']),
                'TestReviewedConfigurationRejectsStaleScheduleBinding': ('bot_configuration_resume_test.go', [
                    scope + '/' + mutation for scope in ('personal', 'team') for mutation in (
                        'missing', 'mode', 'version', 'owner', 'bot', 'trigger', 'cron', 'timezone',
                        'flow', 'instructions', 'params')]),
                'TestReviewedConfigurationResumeKeepsSchedulesDisabled': ('bot_configuration_resume_test.go', ['personal', 'team']),
                'TestBotPausedStoppedConfiguration': ('bot_paused_configuration_test.go', ['personal', 'team'] + [
                    scope + '/' + status for scope in ('personal', 'team') for status in ('paused', 'stopped')])}),
        }

    def test_configuration_authority_manifest_requires_actual_sources_and_exact_leaves(self):
        for tier, (package, expected) in self.configuration_authority_inventory().items():
            actual_package, names = driver.CONSOLIDATION_CONTROLLER[tier]
            self.assertEqual(actual_package, package)
            for name, (filename, leaves) in expected.items():
                with self.subTest(tier=tier, name=name):
                    self.assertIn('func ' + name + '(t *testing.T)', (driver.ROOT / package / filename).read_text())
                    self.assertEqual(names.count(name), 1)
                    self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
                    self.assertEqual(driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier].get(name, []), leaves)
                    with tempfile.TemporaryDirectory() as directory, patch.object(driver, 'run', side_effect=self.consolidation_pass):
                        gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                    self.assertEqual(gate['status'], 'PASS')
                    self.assertEqual(gate['required'], [name] + [name + '/' + leaf for leaf in leaves])
                    self.assertEqual(gate['evidence_scope'], 'implemented subset only')

    def test_configuration_authority_missing_or_skipped_parent_or_leaf_fails_gate(self):
        for tier, (package, expected) in self.configuration_authority_inventory().items():
            for name, (_, leaves) in expected.items():
                for omitted in [name] + [name + '/' + leaf for leaf in leaves]:
                    for failure in ('missing', 'skip'):
                        with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                            def execute(command, env, evidence=None, **kwargs):
                                self.assertIn(omitted, evidence.required)
                                pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                                for required in evidence.required:
                                    if required == omitted and failure == 'missing':
                                        continue
                                    evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                    action = 'skip' if required == omitted else 'pass'
                                    evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                                evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                                return True
                            with patch.object(driver, 'run', side_effect=execute):
                                gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                            self.assertEqual(gate['status'], 'FAIL')
                            self.assertIn(omitted, str(gate['errors']))

    @staticmethod
    def prepared_inventory():
        return {
            'runtime-prepared-plan': ('./pkg/launcher', {
                'TestBotPreparedPlanAnnouncementResume': ['personal', 'team'],
                'TestBotPreparedPlanVerifierFailureResume': ['personal', 'team']}),
            'runtime-prepared-review': ('./pkg/api', {
                'TestBotPreparedReviewMissingEngineNeverDispatches': ['awaiting_approval', 'published'],
                'TestBotPreparedBoundaryUnsettledRecoveryPreservesUncertainOutcome': ['dispatch_started', 'uncertain'],
                'TestBotChatFailurePreservesWrappedUncertainOutcome': [
                    'bot_revision_or_lease_conflict', 'context_canceled', 'context_deadline_exceeded']}),
            'runtime-prepared-engine': ('./pkg/agent', {
                'TestPlanPreparedTrustedSettlementReconcilesIntent': [],
                'TestPlanPreparedLegacyVerificationAssignsIdentity': [],
                'TestPlanPreparedAnnouncementRestore': [],
                'TestPlanPreparedPublishRestartFailureEvidenceAndDuplicate': ['0', '7'],
                'TestPlanPreparedStaleBase': [],
                'TestPlanPreparedSuspendBeforeVerifyAndUncertainRestart': [],
                'TestPlanPreparedSettledSaveDenialRetainsLocalEvidence': [],
                'TestPlanPreparedConcurrentWaves': ['false', 'true'],
                'TestPlanPreparedCallbackIsolation': [],
                'TestPlanPreparedAnnouncementStableAfterCommitFailure': [],
                'TestPlanPreparedVerificationOnceAfterCommitFailure': []}),
            'runtime-prepared-store': ('./pkg/store/entstore', {
                'TestBotPreparedPlanExactApprovalPublication': ['personal', 'team'],
                'TestBotPreparedPlanSQLiteReopen': ['personal', 'team'],
                'TestBotPreparedPlanApprovalRollbackAndIntegrity': ['personal', 'team'],
                'TestBotPreparedPlanAuthorityAndUncertain': ['personal', 'team']}),
        }

    def test_prepared_manifest_requires_exact_inventory(self):
        for tier, (package, expected) in self.prepared_inventory().items():
            with self.subTest(tier=tier):
                self.assertEqual(driver.CONSOLIDATION_RUNTIME[tier], (package, list(expected)))
                for name, leaves in expected.items():
                    self.assertEqual(driver.CONSOLIDATION_SUBTESTS.get(name, []), leaves)
                    self.assertIn(name, driver.go_command(package, list(expected))[-1])
                with tempfile.TemporaryDirectory() as directory, \
                        patch.object(driver, 'run', side_effect=self.consolidation_pass):
                    gate = driver.consolidation_progress_gate(tier, package, list(expected), {}, Path(directory))
                required = list(expected) + [name + '/' + leaf for name, leaves in expected.items() for leaf in leaves]
                self.assertEqual(gate['required'], required)
                self.assertEqual(gate['status'], 'PASS')

    def test_prepared_missing_or_skipped_parent_or_leaf_fails_gate(self):
        for tier, (package, expected) in self.prepared_inventory().items():
            for name, leaves in expected.items():
                for omitted in [name] + [name + '/' + leaf for leaf in leaves]:
                    for failure in ('missing', 'skip'):
                        with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                            def execute(command, env, evidence=None, **kwargs):
                                self.assertIn(omitted, evidence.required)
                                pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                                for required in evidence.required:
                                    if required == omitted and failure == 'missing':
                                        continue
                                    evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                    action = 'skip' if required == omitted else 'pass'
                                    evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                                evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                                return True
                            with patch.object(driver, 'run', side_effect=execute):
                                gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                            self.assertEqual(gate['status'], 'FAIL')
                            self.assertIn(omitted, str(gate['errors']))

    def test_prepared_documentation_preserves_proven_boundary_and_blockers(self):
        for path in ('docs/architecture/bot-basic-chat-tools.md', 'docs/architecture/bots.md'):
            text = (driver.ROOT / path).read_text()
            for marker in ('TestBotPreparedPlanAnnouncementResume', 'TestBotPreparedPlanVerifierFailureResume',
                           'trusted content-backed prepared record', 'ordinary exact-action resume',
                           'not HTTP routes, scheduler or physical backend acceptance'):
                self.assertIn(marker.lower(), text.lower().replace('\n', ' '))
            self.assertNotIn('Autonomous plan execution is **not** established', text)
            self.assertNotIn('components and standalone verifier/pregrant tests do not establish', text)
        self.assertIn('before saved-leaf effects', driver.CONSOLIDATION_BOUNDARIES['runtime-prepared-review'])
        self.assertEqual(set(driver.CONSOLIDATION_REMAINING['runtime']), {
            'three-mode-capability-parity', 'runtime-authority-matrix', 'runtime-lazy-session-lifecycle',
            'runtime-production-wiring'})
        self.assertEqual(set(driver.CONSOLIDATION_REMAINING['controller']), {
            'controller-all-mode-no-python', 'controller-team-physical',
            'controller-cold-cross-backend', 'controller-authority-matrix'})

    def test_flow_and_plan_authority_manifest_requires_exact_leaves(self):
        package, names = driver.CONSOLIDATION_RUNTIME['runtime-construction']
        self.assertEqual(package, './pkg/launcher')
        expected = {
            'TestChatRuntimeNestedFlowFences': {
                scope + '/' + mode + '/' + fence
                for scope in ('personal', 'team')
                for mode in ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
                for fence in ('none', 'cancel', 'lease-revoked', 'budget')
                if mode != 'ordinary-chat' or fence in ('none', 'cancel')},
            'TestChatRuntimeFlowPreservesNarrowedChildPolicy': {'personal', 'team'},
            'TestChatRuntimeAutonomousPlanExactMirrorPregrantGap': {'personal', 'team'},
            'TestChatRuntimeAutonomousPlanExactVerifierGrant': {'personal', 'team'},
            'TestChatRuntimeAutonomousPlanVerifierAuthority': {'personal', 'team'},
        }
        for name, leaves in expected.items():
            with self.subTest(name=name):
                self.assertEqual(names.count(name), 1)
                self.assertEqual(set(driver.CONSOLIDATION_SUBTESTS[name]), leaves)
                self.assertEqual(len(driver.CONSOLIDATION_SUBTESTS[name]), len(leaves))
                self.assertIn(name, driver.go_command(package, names)[-1])

    def test_flow_and_plan_authority_missing_or_skipped_leaf_fails_gate(self):
        package = './pkg/launcher'
        for name in ('TestChatRuntimeNestedFlowFences',
                     'TestChatRuntimeFlowPreservesNarrowedChildPolicy',
                     'TestChatRuntimeAutonomousPlanExactMirrorPregrantGap',
                     'TestChatRuntimeAutonomousPlanExactVerifierGrant',
                     'TestChatRuntimeAutonomousPlanVerifierAuthority'):
            for child in driver.CONSOLIDATION_SUBTESTS[name]:
                omitted = name + '/' + child
                for failure in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            self.assertIn(omitted, evidence.required)
                            pkg = 'github.com/SAP/astonish/pkg/launcher'
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                action = 'skip' if required == omitted else 'pass'
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(
                                'runtime-construction', package, [name], {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    def test_delegate_lease_and_alias_manifests_require_exact_inventory(self):
        scopes = ('personal', 'team')
        modes = ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
        expected = {
            'TestChatRuntimeActualDelegateReadFreshAuthority': {
                scope + '/' + mode + '/' + fence
                for scope in scopes for mode in modes
                for fence in (('none', 'membership-before-model', 'membership-before-tool')
                              if mode == 'ordinary-chat' else
                              ('none', 'membership-before-model', 'membership-before-tool',
                               'lease-before-model', 'lease-before-tool'))},
            'TestChatRuntimePersistedLeaseTransitions': {
                scope + '/' + mode + '/' + transition
                for scope in scopes for mode in ('interactive-bot', 'autonomous-bot')
                for transition in ('renewed', 'expired', 'replaced')},
            'TestChatRuntimeDelegateAliasCleanupConcurrent': set(),
            'TestChatRuntimeDelegateCleanupJoinsBeforeAliasRemoval': set(),
        }
        self.assertEqual(len(expected['TestChatRuntimeActualDelegateReadFreshAuthority']), 26)
        self.assertEqual(len(expected['TestChatRuntimePersistedLeaseTransitions']), 12)
        package, names = driver.CONSOLIDATION_RUNTIME['runtime-construction']
        for name, leaves in expected.items():
            with self.subTest(name=name):
                self.assertEqual(names.count(name), 1)
                self.assertEqual(set(driver.CONSOLIDATION_SUBTESTS.get(name, [])), leaves)
                self.assertEqual(len(driver.CONSOLIDATION_SUBTESTS.get(name, [])), len(leaves))
                self.assertIn(name, driver.go_command(package, names)[-1])
        package, names = driver.CONSOLIDATION_RUNTIME['runtime-child-alias-leases']
        self.assertEqual(package, './pkg/sandbox')
        self.assertEqual(names, ['TestChildAliasLeasePreservesBorrowedClients',
                                 'TestChildAliasLeaseLeavesReplacementUntouched'])
        self.assertEqual(driver.CONSOLIDATION_SUBTESTS[names[0]], ['backend', 'node-adapter'])
        self.assertIn('not physical', driver.CONSOLIDATION_BOUNDARIES['runtime-child-alias-leases'])

    def test_delegate_lease_documentation_keeps_evidence_boundaries(self):
        text = (driver.ROOT / 'docs/architecture/bot-basic-chat-tools.md').read_text()
        for marker in ('TestChatRuntimeActualDelegateReadFreshAuthority',
                       'TestChatRuntimePersistedLeaseTransitions',
                       'TestChatRuntimeDelegateAliasCleanupConcurrent',
                       'TestChatRuntimeDelegateCleanupJoinsBeforeAliasRemoval',
                       'TestChildAliasLeasePreservesBorrowedClients',
                       'TestChildAliasLeaseLeavesReplacementUntouched',
                       'ToolNodePool.Remove', 'legacy pools', 'fresh exact autonomous grant',
                       'All runtime/controller remaining requirements stay blocked'):
            self.assertIn(marker, text)

    def test_delegate_lease_and_alias_missing_or_skipped_evidence_fails(self):
        cases = [
            ('runtime-construction', 'TestChatRuntimeActualDelegateReadFreshAuthority'),
            ('runtime-construction', 'TestChatRuntimePersistedLeaseTransitions'),
            ('runtime-construction', 'TestChatRuntimeDelegateAliasCleanupConcurrent'),
            ('runtime-construction', 'TestChatRuntimeDelegateCleanupJoinsBeforeAliasRemoval'),
            ('runtime-child-alias-leases', 'TestChildAliasLeasePreservesBorrowedClients'),
            ('runtime-child-alias-leases', 'TestChildAliasLeaseLeavesReplacementUntouched'),
        ]
        for tier, name in cases:
            # Reject every missing leaf, even when the parent and package pass.
            required_names = [name] + [name + '/' + child
                                       for child in driver.CONSOLIDATION_SUBTESTS.get(name, [])]
            for omitted in required_names:
                for failure in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                        package = driver.CONSOLIDATION_RUNTIME[tier][0]
                        def execute(command, env, evidence=None, **kwargs):
                            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                            self.assertIn('-count=1', command)
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                action = 'skip' if required == omitted else 'pass'
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    def test_authority_and_cancellation_required_inventory(self):
        scopes = ('personal', 'team')
        modes = ('ordinary-chat', 'interactive-bot', 'autonomous-bot')
        expected = {
            'runtime-http': {
                'TestStudioChatHTTPScopedAuthorityAdmission': {
                    'missing-team', 'revoked-team', 'revoked-org', 'missing-platform',
                    'missing-router', 'suspended-user', 'bind-directory-unavailable'},
            },
            'runtime-live-authority': {
                'TestLiveAuthorityBuildAndBindFreshMembership': {
                    s + '/' + c for s in scopes
                    for c in ('user-disabled', 'org-disabled', 'org-removed', 'team-removed')},
                'TestLiveAuthorityPersonalOnlyDoesNotRequireTeamMembership': set(),
                'TestLiveAuthorityPromotionDoesNotBroadenCeilingOrBypassTeam': set(),
                'TestLiveAuthorityMissingDependenciesFailClosed': {
                    m + '/' + f for m in ('platform', 'router', 'both') for f in ('0', '1')},
                'TestLiveAuthoritySnapshotCannotBeRetargeted': set(),
                'TestLiveAuthorityContextModelRechecksAtIteration': {
                    s + '/' + c for s in scopes for c in ('org-demoted', 'owner-demoted',
                        'team-demoted', 'platform-demoted', 'team-removed')},
                'TestLiveAuthorityModelPreservesBudgetCancellationAndLocal': {'false', 'true'},
                'TestLiveAuthorityDirectoryFailureAndCancellation': set(),
            },
            'runtime-construction': {
                'TestChatRuntimeLiveMembershipFreshAuthority': {
                    s + '/' + m + '/' + stage for s in scopes for m in modes
                    for stage in ('post-construction', 'post-discovery')},
                'TestChatRuntimeLiveMembershipLazyMCP': {s + '/' + m for s in scopes for m in modes},
                'TestChatRuntimeLiveMembershipModelBoundary': {s + '/' + m for s in scopes for m in modes},
                'TestChatRuntimeMCPExactQualifiedIdentity': {
                    s + '/' + c for s in scopes for c in ('exact', 'bridge', 'exact-capacity',
                        'arguments', 'command', 'server-args', 'url', 'disabled', 'env', 'server-name',
                        'tool-name', 'bare-grant', 'cancelled', 'startup-config', 'startup-lease')},
                'TestChatRuntimeContextFixture': set(),
                'TestChatRuntimeContextSynchronousCancellation': {
                    s + '/' + c for s in ('parent', 'bound') for c in ('Err', 'Done')},
                'TestChatRuntimeContextDerivedCancellation': {'independent-lifetimes', 'same-lifetime'},
                'TestChatRuntimeContextRelease': set(),
                'TestChatRuntimeContextDeadlineAndValues': {'parent', 'bound', 'expired-parent', 'expired-bound'},
                'TestChatRuntimeContextNotification': {'parent', 'bound'},
            },
            'controller-daemon': {
                'TestBotWorkerSharedLiveAuthority': {
                    s + '/' + a for s in scopes for a in ('organization', 'team')},
                'TestBotUncertainEffectRestartHTTPNoReplay': set(scopes),
            },
        }
        manifest = driver.CONSOLIDATION_RUNTIME | driver.CONSOLIDATION_CONTROLLER
        for tier, tests in expected.items():
            package, names = manifest[tier]
            self.assertEqual(len(names), len(set(names)))
            children = driver.CONSOLIDATION_SUBTESTS | driver.CONSOLIDATION_CONTROLLER_SUBTESTS.get(tier, {})
            for name, leaves in tests.items():
                with self.subTest(tier=tier, name=name):
                    self.assertIn(name, names)
                    self.assertEqual(set(children.get(name, [])), leaves)
                    self.assertEqual(len(children.get(name, [])), len(leaves))
                    # Select the parent so Go executes every child, not a passing subset.
                    self.assertIn(name, driver.go_command(package, names)[-1])
        self.assertEqual(manifest['runtime-live-authority'][0], './pkg/chatruntime')
        self.assertEqual(set(driver.CONSOLIDATION_REMAINING['runtime']), {
            'three-mode-capability-parity', 'runtime-authority-matrix',
            'runtime-lazy-session-lifecycle', 'runtime-production-wiring'})
        self.assertEqual(set(driver.CONSOLIDATION_REMAINING['controller']), {
            'controller-all-mode-no-python', 'controller-team-physical',
            'controller-cold-cross-backend', 'controller-authority-matrix'})

    def test_authority_and_cancellation_missing_or_skipped_leaf_fails(self):
        cases = (
            ('runtime-http', 'TestStudioChatHTTPScopedAuthorityAdmission', 'bind-directory-unavailable'),
            ('runtime-live-authority', 'TestLiveAuthorityBuildAndBindFreshMembership', 'team/team-removed'),
            ('runtime-live-authority', 'TestLiveAuthorityContextModelRechecksAtIteration', 'personal/owner-demoted'),
            ('runtime-live-authority', 'TestLiveAuthorityMissingDependenciesFailClosed', 'both/1'),
            ('runtime-live-authority', 'TestLiveAuthorityModelPreservesBudgetCancellationAndLocal', 'false'),
            ('runtime-construction', 'TestChatRuntimeLiveMembershipFreshAuthority', 'team/ordinary-chat/post-discovery'),
            ('runtime-construction', 'TestChatRuntimeLiveMembershipLazyMCP', 'personal/autonomous-bot'),
            ('runtime-construction', 'TestChatRuntimeLiveMembershipModelBoundary', 'team/interactive-bot'),
            ('runtime-construction', 'TestChatRuntimeMCPExactQualifiedIdentity', 'team/startup-lease'),
            ('runtime-construction', 'TestChatRuntimeContextSynchronousCancellation', 'bound/Done'),
            ('runtime-construction', 'TestChatRuntimeContextDerivedCancellation', 'same-lifetime'),
            ('controller-daemon', 'TestBotWorkerSharedLiveAuthority', 'personal/organization'),
            ('controller-daemon', 'TestBotUncertainEffectRestartHTTPNoReplay', 'team'),
        )
        manifest = driver.CONSOLIDATION_RUNTIME | driver.CONSOLIDATION_CONTROLLER
        for tier, name, child in cases:
            for failure in ('missing', 'skip'):
                with self.subTest(tier=tier, name=name, failure=failure), tempfile.TemporaryDirectory() as directory:
                    package = manifest[tier][0]
                    omitted = name + '/' + child
                    def execute(command, env, evidence=None, **kwargs):
                        pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                        self.assertIn('-count=1', command)
                        for required in evidence.required:
                            if required == omitted and failure == 'missing':
                                continue
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                            action = 'skip' if required == omitted else 'pass'
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': action}))
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                        return True
                    with patch.object(driver, 'run', side_effect=execute):
                        gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                    self.assertEqual(gate['status'], 'FAIL')
                    self.assertIn(omitted, str(gate['errors']))

    def test_events_complete_synthetic_inventory_discharges_both_requirement_ids(self):
        # Accounting-only run: synthetic logs do not claim physical acceptance.
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True), \
                patch.object(driver, 'run', side_effect=self.consolidation_pass) as run:
            report = Path(directory) / 'events.json'
            with contextlib.redirect_stdout(io.StringIO()):
                result = driver.main(['--stage', 'chat-consolidation', '--phase', 'events', '--report', str(report)])
            data = json.loads(report.read_text())
            self.assertEqual(result, 0)
            self.assertEqual(data['status'], 'PASS')
            self.assertEqual(data['remaining_requirements'], [])
            self.assertEqual({r['id'] for r in data['requirement_evidence']},
                             set(driver.CONSOLIDATION_REMAINING['events']))
            self.assertEqual(run.call_count, len(driver.CONSOLIDATION_EVENTS))
            self.assertTrue(all(g['status'] == 'PASS' for g in data['gates']))
            self.assertNotIn('events-linked-session-restore', [r['id'] for r in data['remaining_requirements']])
            linked = next(r for r in data['requirement_evidence'] if r['id'] == 'events-linked-session-restore')
            self.assertEqual(linked['status'], 'PASS')

    def test_clients_require_browser_opt_in_and_keep_remaining_matrix(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True), \
                patch.object(driver, 'run', side_effect=self.consolidation_pass) as run:
            report = Path(directory) / 'clients.json'
            with contextlib.redirect_stdout(io.StringIO()):
                result = driver.main(['--stage', 'chat-consolidation', '--phase', 'clients', '--report', str(report)])
            data = json.loads(report.read_text())
            self.assertEqual(result, 2)
            self.assertEqual(data['status'], 'BLOCKED')
            self.assertEqual(run.call_count, 5)
            self.assertEqual(len([g for g in data['gates'] if g['tier'] == 'events-http']), 1)
            projection = [g for g in data['gates'] if g['tier'] == 'events-projection']
            self.assertEqual(len(projection), 1)
            self.assertEqual(projection[0]['status'], 'PASS')
            visual = next(r for r in data['requirement_evidence'] if r['id'] == 'clients-shared-visual-parity')
            self.assertNotIn('required tier missing or duplicated', str(visual))
            browser = next(g for g in data['gates'] if g['tier'] == 'clients-browser')
            self.assertEqual(browser['status'], 'BLOCKED')
            self.assertEqual(browser['required'], driver.consolidation_required_tests(
                'clients-browser', driver.CONSOLIDATION_CLIENTS['clients-browser'][1]))
            self.assertIn('clients-all-mode-conversation', [r['id'] for r in data['remaining_requirements']])

    def test_phase5_exact_inventory_and_negative_receipts(self):
        cases = (
            ('events-http', 'TestBotPresentationMatrixHTTPAcceptance', 10),
            ('events-http', 'TestBotLinkedSessionRestoreProjectionHTTPAcceptance', 30),
            ('clients-browser', 'TestBotConversationPresentationBrowserAcceptance', 2),
            ('clients-pty', 'TestBotConversationPresentationPTYAcceptance', 3),
            ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKActivity', 9),
            ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKDelegation', 0),
            ('clients-browser', 'TestBotClientStatesSharedVisualBrowserAcceptance', 21),
            ('clients-browser', 'TestBotScheduledWorkspaceBrowserAcceptance', 0),
            ('clients-browser', 'TestBotChatFirstOrderedConversationBrowserAcceptance', 0),
        )
        manifests = driver.CONSOLIDATION_EVENTS | driver.CONSOLIDATION_CLIENTS
        for tier, parent, children in cases:
            self.assertIn(parent, manifests[tier][1])
            required = driver.consolidation_required_tests(tier, [parent])
            self.assertEqual(len(required), children + 1)
            for fault in ('missing', 'skip', 'wrong-package', 'no-package-pass'):
                with self.subTest(parent=parent, fault=fault):
                    evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                    pkg = 'github.com/SAP/astonish/' + manifests[tier][0].removeprefix('./')
                    for name in required:
                        if fault == 'missing' and name == required[-1]:
                            continue
                        owner = pkg + '/foreign' if fault == 'wrong-package' else pkg
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': 'run'}) + '\n')
                        action = 'skip' if fault == 'skip' and name == required[-1] else 'pass'
                        evidence.feed(json.dumps({'Package': owner, 'Test': name, 'Action': action}) + '\n')
                    if fault != 'no-package-pass':
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}) + '\n')
                    self.assertTrue(driver.consolidation_exact_errors(evidence, manifests[tier][0], required))
        for phase in ('events', 'clients'):
            for key in driver.CONSOLIDATION_REMAINING[phase]:
                self.assertIn(key, driver.CONSOLIDATION_REQUIREMENT_SCENARIOS)

    def test_phase5_each_direct_projection_and_visual_leaf_is_mandatory(self):
        # Synthetic accounting only: no browser or physical receipt is produced.
        manifests = driver.CONSOLIDATION_EVENTS | driver.CONSOLIDATION_CLIENTS
        for tier, parent in (
                ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKActivity'),
                ('events-projection', 'TestSessionEventProjectorHomeOrdinaryADKDelegation'),
                ('clients-browser', 'TestBotClientStatesSharedVisualBrowserAcceptance')):
            package, _ = manifests[tier]
            required = driver.consolidation_required_tests(tier, [parent])
            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
            for omitted in required:
                for fault in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, fault=fault):
                        evidence = driver.ConsolidationEvidence(required, io.StringIO(), {})
                        for name in required:
                            if name == omitted and fault == 'missing':
                                continue
                            evidence.feed(json.dumps({'Package': pkg, 'Test': name, 'Action': 'run'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Test': name,
                                'Action': 'skip' if name == omitted else 'pass'}))
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                        self.assertIn(omitted, str(driver.consolidation_exact_errors(evidence, package, required)))
        for key in ('clients-all-mode-conversation', 'clients-shared-visual-parity'):
            self.assertNotIn(key, driver.CONSOLIDATION_REQUIREMENT_UNRESOLVED)
            self.assertIn('events-http', driver.CONSOLIDATION_REQUIREMENT_TIERS[key])
            self.assertIn('events-projection', driver.CONSOLIDATION_REQUIREMENT_TIERS[key])
            self.assertIn(('clients-browser', 'TestBotClientStatesSharedVisualBrowserAcceptance'),
                          driver.CONSOLIDATION_REQUIREMENT_SCENARIOS[key].values())
        self.assertIn(('clients-browser', 'TestBotScheduledWorkspaceBrowserAcceptance'),
                      driver.CONSOLIDATION_REQUIREMENT_SCENARIOS['clients-all-mode-conversation'].values())

    def test_home_matrix_required_exact_parent_scopes_and_eight_leaves(self):
        tier, parent = 'events-http', 'TestBotHomePresentationMatrixHTTPAcceptance'
        package, names = driver.CONSOLIDATION_EVENTS[tier]
        self.assertEqual(names.count(parent), 1)
        source = (driver.ROOT / 'pkg/api/bot_presentation_matrix_http_test.go').read_text()
        self.assertIn('func ' + parent + '(t *testing.T)', source)
        self.assertIn('runBotPresentationMatrixHTTPAcceptance(t, true)', source)
        expected = [parent] + [parent + '/' + scope for scope in ('personal', 'team')] + [
            parent + '/' + scope + '/' + state for scope in ('personal', 'team')
            for state in ('live', 'history', 'reconnect', 'revoked')]
        self.assertEqual(driver.consolidation_required_tests(tier, [parent]), expected)
        self.assertEqual(driver.CONSOLIDATION_REQUIREMENT_SCENARIOS['events-full-presentation-matrix'][
            'mixed-home-live-history-reconnect'], (tier, parent))
        self.assertNotIn('events-full-presentation-matrix', driver.CONSOLIDATION_REQUIREMENT_UNRESOLVED)
        doc = (driver.ROOT / 'docs/architecture/chat-rendering-pipeline.md').read_text()
        self.assertIn('`' + parent + '`', doc)
        self.assertIn('DEFERRED, NOT PASS', doc)
        for phase in ('events', 'clients'):
            for requirement in driver.CONSOLIDATION_REMAINING[phase]:
                self.assertIn(requirement, driver.CONSOLIDATION_REQUIREMENT_TIERS)
                self.assertIn(requirement, driver.CONSOLIDATION_REQUIREMENT_SCENARIOS)
                self.assertIn('`' + requirement + '`', doc)
        for omitted in [None] + expected:
            for failure in ('missing', 'skip'):
                with self.subTest(omitted=omitted, failure=failure), tempfile.TemporaryDirectory() as directory:
                    def execute(command, env, evidence=None, **kwargs):
                        pkg = 'github.com/SAP/astonish/pkg/api'
                        for required in evidence.required:
                            if required == omitted and failure == 'missing':
                                continue
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Test': required,
                                'Action': 'skip' if required == omitted else 'pass'}))
                        evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                        return True
                    with patch.object(driver, 'run', side_effect=execute):
                        gate = driver.consolidation_progress_gate(tier, package, names, {}, Path(directory))
                    self.assertEqual(gate['status'], 'FAIL' if omitted else 'PASS')

    def test_clients_unverified_is_derived_from_exact_owned_evidence(self):
        # Synthetic accounting-only logs, never a physical browser/PTY receipt.
        for damaged in (None, 'clients-browser', 'clients-pty'):
            with self.subTest(damaged=damaged), tempfile.TemporaryDirectory() as directory:
                report = {'gates': [], 'unverified': ['paid-provider workflow', 'Kubernetes',
                                                        'PostgreSQL', 'built browser', 'native PTY']}
                def gate(tier, package, names, *args):
                    required = driver.consolidation_required_tests(tier, names)
                    pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                    log = Path(directory) / (tier + '.jsonl')
                    log.write_text('\n'.join(json.dumps({'Package': pkg, 'Test': name, 'Action': action})
                        for name in required for action in ('run', 'pass')) + '\n' +
                        json.dumps({'Package': pkg, 'Action': 'pass'}) + '\n')
                    if tier == damaged:
                        log.write_text('')  # PASS labels alone must not discharge the capability.
                    return {'tier': tier, 'package': package, 'required': required, 'passed': required,
                            'status': 'PASS', 'fresh': True, 'prerequisites_verified': True, 'errors': [],
                            'command': driver.consolidation_command(tier, package, names), 'log': str(log)}
                with patch.object(driver, 'consolidation_progress_gate', side_effect=gate):
                    code = driver.consolidation_progress(driver.argparse.Namespace(
                        phase='clients', defer_kubernetes_postgresql=True), {}, Path(directory), report)
                self.assertEqual(code, 2 if damaged else 0)
                for label, tier in (('built browser', 'clients-browser'), ('native PTY', 'clients-pty')):
                    self.assertEqual(label in report['unverified'], tier == damaged)
                for label in ('paid-provider workflow', 'Kubernetes', 'PostgreSQL'):
                    self.assertIn(label, report['unverified'])

    def test_clients_projection_dependency_executes_once_in_all(self):
        with tempfile.TemporaryDirectory() as directory:
            report = {'gates': [], 'unverified': []}
            def gate(tier, package, names, *args):
                return {'tier': tier, 'status': 'BLOCKED', 'errors': [], 'package': package}
            with patch.object(driver, 'run', side_effect=self.consolidation_pass), \
                    patch.object(driver, 'consolidation_docker_regression', return_value={'status': 'BLOCKED'}), \
                    patch.object(driver, 'consolidation_progress_gate', side_effect=gate) as execute, \
                    patch.object(driver, 'consolidation_requirement_evidence', return_value=[]):
                args = driver.argparse.Namespace(phase='all', defer_kubernetes_postgresql=False)
                driver.consolidation_progress(args, {}, Path(directory), report)
            self.assertEqual([call.args[0] for call in execute.call_args_list].count('events-projection'), 1)
            self.assertEqual([call.args[0] for call in execute.call_args_list].count('events-http'), 1)
            self.assertFalse(any(r['id'] in ('events-full-acceptance', 'clients-full-acceptance')
                                 for r in report['remaining_requirements']))

    def test_clients_browser_requires_matching_built_index(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'web').mkdir()
            (root / 'web/package.json').write_text('{"version":"fixture"}')
            source = {'ASTONISH_MEMORY_BROWSER_ACCEPTANCE': '1', 'ASTONISH_BOT_BROWSER_PATH': sys.executable}
            package, names = driver.CONSOLIDATION_CLIENTS['clients-browser']
            with patch.object(driver, 'ROOT', root), patch.object(driver, 'run', side_effect=self.consolidation_pass) as run:
                missing = driver.consolidation_progress_gate('clients-browser', package, names, source, root)
                self.assertEqual(missing['status'], 'BLOCKED')
                self.assertFalse(missing['prerequisites_verified'])
                run.assert_not_called()
                (root / 'web/dist/fixture').mkdir(parents=True)
                (root / 'web/dist/fixture/index.html').touch()
                built = driver.consolidation_progress_gate('clients-browser', package, names, source, root)
                self.assertEqual(built['status'], 'PASS')
                self.assertEqual(run.call_count, 1)

    def test_consolidation_runtime_requires_distillation_and_plan_lifecycle(self):
        names = driver.CONSOLIDATION_RUNTIME['runtime-construction'][1]
        for name in ('TestChatRuntimeDirectToolsetDistillation',
                     'TestChatRuntimeSharedPlanLifecycle', 'TestStudioChatRuntimeSharedPlanHTTP',
                     'TestBuildChatFlowDistillerNoFallback', 'TestBuildChatFlowDistillerValidation',
                     'TestBuildChatFlowDistillerDryRun'):
            self.assertIn(name, names)
        self.assertIn('missing-plan', driver.CONSOLIDATION_SUBTESTS['TestStudioChatHTTPScopedRuntimeFactory'])

    def test_consolidation_storage_targets_new_format_not_legacy_migration(self):
        requirements = driver.CONSOLIDATION_REMAINING['storage']
        self.assertIn('storage-complete-compiled-implementation', requirements)
        self.assertNotRegex(' '.join(requirements.values()), r'legacy|migration|original format')
        self.assertIn('new-format', requirements['storage-offline-backup-restore'])

    def test_chat_consolidation_controller_executes_without_docker(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True), \
                patch.object(driver, 'run', side_effect=self.consolidation_pass) as run:
            report = Path(directory) / 'report.json'
            with contextlib.redirect_stdout(io.StringIO()):
                result = driver.main(['--stage', 'chat-consolidation', '--phase', 'controller', '--report', str(report)])
            data = json.loads(report.read_text())
            self.assertEqual(result, 2)
            self.assertEqual(data['status'], 'BLOCKED')
            self.assertEqual(run.call_count, 4)
            self.assertEqual([gate['status'] for gate in data['gates']], ['PASS', 'PASS', 'PASS', 'PASS', 'BLOCKED', 'BLOCKED', 'BLOCKED', 'BLOCKED'])
            self.assertEqual(data['phase_acceptance'], {'controller': 'BLOCKED'})
            self.assertTrue(data['remaining_requirements'])

    def test_allmode_docker_manifest_exact_scopes_and_unchanged_deadline(self):
        # NONPHYSICAL inventory test: the runner is mocked, never Docker.
        tier = 'controller-docker-allmode'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        name = 'TestBotAllModeRetainedSessionDocker'
        self.assertEqual((package, names), ('./pkg/daemon', [name]))
        self.assertEqual(driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier][name], ['personal', 'team'])
        source = (driver.ROOT / 'pkg/daemon/bot_all_mode_docker_acceptance_test.go').read_text()
        self.assertIn('func ' + name + '(t *testing.T)', source)
        self.assertIn('store.BotScopePersonal, store.BotScopeTeam', source)
        self.assertNotIn('TestBotDelegatedRetainedSessionDocker', names)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '@base/rootfs/bin').mkdir(parents=True)
            (root / '@base/rootfs/bin/sh').touch()
            env = {'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_SANDBOX_IMAGE': 'test-owned',
                   'ASTONISH_BOT_HOME_TEST_LAYERS': str(root)}
            with patch.object(driver, 'run', side_effect=self.consolidation_pass):
                gate = driver.consolidation_progress_gate(tier, package, names, env, root)
        self.assertEqual(gate['status'], 'PASS')
        self.assertEqual(gate['required'], [name, name + '/personal', name + '/team'])
        self.assertIn('-timeout=15m', gate['command'])
        self.assertIn('-count=1', gate['command'])
        self.assertEqual(gate['evidence_scope'], 'implemented subset only')
        self.assertIn('not live Kubernetes', gate['boundary'])
        for phase, obligation in (('controller', 'controller-all-mode-no-python'),
                                  ('controller', 'controller-team-physical'),
                                  ('controller', 'controller-cold-cross-backend'),
                                  ('runtime', 'runtime-lazy-session-lifecycle'),
                                  ('storage', 'storage-live-kubernetes')):
            self.assertIn('Kubernetes', driver.CONSOLIDATION_REMAINING[phase][obligation])

    def test_allmode_docker_missing_or_skipped_parent_and_scope_fails(self):
        tier = 'controller-docker-allmode'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        name = names[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '@base/rootfs/bin').mkdir(parents=True)
            (root / '@base/rootfs/bin/sh').touch()
            env = {'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_SANDBOX_IMAGE': 'test-owned',
                   'ASTONISH_BOT_HOME_TEST_LAYERS': str(root)}
            for omitted in (name, name + '/personal', name + '/team'):
                for failure in ('missing', 'skip'):
                    with self.subTest(omitted=omitted, failure=failure):
                        def execute(command, env, evidence=None, **kwargs):
                            pkg = 'github.com/SAP/astonish/pkg/daemon'
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required, 'Action': 'run'}))
                                evidence.feed(json.dumps({'Package': pkg, 'Test': required,
                                    'Action': 'skip' if required == omitted else 'pass'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, names, env, root)
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    def test_delegate_docker_manifest_exact_scopes_and_unchanged_deadline(self):
        # NONPHYSICAL: mock runner and temporary layer sentinel, never Docker.
        tier = 'controller-docker-delegate'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        name = 'TestBotDelegatedRetainedSessionDocker'
        self.assertEqual((package, names), ('./pkg/daemon', [name]))
        self.assertEqual(driver.CONSOLIDATION_CONTROLLER_SUBTESTS[tier][name], ['personal', 'team'])
        source = (driver.ROOT / 'pkg/daemon/bot_all_mode_docker_acceptance_test.go').read_text()
        self.assertIn('func ' + name + '(t *testing.T)', source)
        self.assertIn('DELEGATE_FINAL_QUESTION_PENDING', source)
        self.assertNotIn(name, driver.CONSOLIDATION_CONTROLLER['controller-docker-allmode'][1])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '@base/rootfs/bin').mkdir(parents=True)
            (root / '@base/rootfs/bin/sh').touch()
            env = {'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_SANDBOX_IMAGE': 'test-owned',
                   'ASTONISH_BOT_HOME_TEST_LAYERS': str(root)}
            with patch.object(driver, 'run', side_effect=self.consolidation_pass):
                gate = driver.consolidation_progress_gate(tier, package, names, env, root)
            self.assertEqual(gate['status'], 'PASS')
            self.assertEqual(gate['required'], [name, name + '/personal', name + '/team'])
            self.assertIn('-timeout=15m', gate['command'])
            self.assertIn('-count=1', gate['command'])
            self.assertIn('pending ask_user', gate['boundary'])
            self.assertIn('not live Kubernetes', gate['boundary'])
            for missing in env:
                with self.subTest(missing=missing), patch.object(driver, 'run') as run:
                    gate = driver.consolidation_progress_gate(tier, package, names,
                        {key: value for key, value in env.items() if key != missing}, root)
                self.assertEqual(gate['status'], 'BLOCKED')
                run.assert_not_called()

    def test_delegate_docker_missing_skipped_wrong_package_and_unfinished_fails(self):
        tier = 'controller-docker-delegate'
        package, names = driver.CONSOLIDATION_CONTROLLER[tier]
        name = names[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '@base/rootfs/bin').mkdir(parents=True)
            (root / '@base/rootfs/bin/sh').touch()
            env = {'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_SANDBOX_IMAGE': 'test-owned',
                   'ASTONISH_BOT_HOME_TEST_LAYERS': str(root)}
            for omitted in (name, name + '/personal', name + '/team'):
                for failure in ('missing', 'skip', 'wrong-package', 'unfinished'):
                    with self.subTest(omitted=omitted, failure=failure):
                        def execute(command, env, evidence=None, **kwargs):
                            pkg = 'github.com/SAP/astonish/pkg/daemon'
                            for required in evidence.required:
                                if required == omitted and failure == 'missing':
                                    continue
                                event_pkg = pkg + '-wrong' if required == omitted and failure == 'wrong-package' else pkg
                                evidence.feed(json.dumps({'Package': event_pkg, 'Test': required, 'Action': 'run'}))
                                if required == omitted and failure == 'unfinished':
                                    continue
                                evidence.feed(json.dumps({'Package': event_pkg, 'Test': required,
                                    'Action': 'skip' if required == omitted and failure == 'skip' else 'pass'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, names, env, root)
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(omitted, str(gate['errors']))

    def test_allmode_evidence_doc_distinguishes_receipt_inventory_and_blockers(self):
        text = (driver.ROOT / 'docs/architecture/bot-runtime-evidence.md').read_text()
        for marker in ('Physical Docker timing and Studio/native PTY restoration are distinct',
                       'UNVERIFIED, NOT PASS', 'historical and do not certify',
                       'not completed evidence', 'No rollout or existing Bot replay occurred'):
            self.assertIn(marker, ' '.join(text.split()))
        for phase in ('runtime', 'controller'):
            for obligation in driver.CONSOLIDATION_REMAINING[phase]:
                self.assertIn('`' + obligation + '`', text)

    def test_allmode_regression_inventory_requires_real_source_parents(self):
        expected = {
            'runtime-construction': ('pkg/launcher/bot_tool_binding_test.go',
                ['TestBotWorkflowQuestionProgressWithoutAutonomousClassifier']),
            'controller-daemon': ('pkg/daemon/bot_all_mode_docker_acceptance_test.go',
                ['TestAllModeDockerFixtureContract', 'TestAllModeDockerModelScript']),
        }
        for tier, (filename, required) in expected.items():
            manifest = driver.CONSOLIDATION_RUNTIME if tier.startswith('runtime-') else driver.CONSOLIDATION_CONTROLLER
            package, names = manifest[tier]
            source = (driver.ROOT / filename).read_text()
            for name in required:
                self.assertEqual(names.count(name), 1)
                self.assertIn('func ' + name + '(t *testing.T)', source)
                self.assertIsNotNone(re.fullmatch(driver.go_command(package, names)[-1], name))
                for failure in ('missing', 'skip'):
                    with self.subTest(name=name, failure=failure), tempfile.TemporaryDirectory() as directory:
                        def execute(command, env, evidence=None, **kwargs):
                            pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                            if failure != 'missing':
                                evidence.feed(json.dumps({'Package': pkg, 'Test': name, 'Action': 'run'}))
                                evidence.feed(json.dumps({'Package': pkg, 'Test': name, 'Action': 'skip'}))
                            evidence.feed(json.dumps({'Package': pkg, 'Action': 'pass'}))
                            return True
                        with patch.object(driver, 'run', side_effect=execute):
                            gate = driver.consolidation_progress_gate(tier, package, [name], {}, Path(directory))
                        self.assertEqual(gate['status'], 'FAIL')
                        self.assertIn(name, str(gate['errors']))

    def test_controller_manifest_preserves_required_acceptance_mapping(self):
        expected = {
            'controller-api': ('./pkg/api', {'TestBotEnhancedChatBaseline',
                'TestBotTitleConfirmedLaterQuestionContinuation', 'TestBotProgressRecoveryHTTP',
                'TestBotProgressLifecycleHTTP', 'TestBotProgressRestartHTTP',
                'TestBotTitleAnswerAuthorityHTTPRegression', 'TestBotHistoryStreamHTTPAcceptance',
                'TestBotResumeNoReplayAfterEffect', 'TestBotResumeUnusedTargetDenialRestoresExactAction',
                'TestBotResumeAmbiguousConsumptionNeverRetryable', 'TestBotChatExactApprovalContinuation',
                'TestBotResumeModelPreservesExactNumbers', 'TestBotMCPApprovalCheckpointTargetBinding'}),
            'controller-daemon': ('./pkg/daemon', {'TestAllModeDockerFixtureContract', 'TestAllModeDockerModelScript',
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
                 'TestBotConfigurationProductionPolicyHTTP'}),
            'controller-store': ('./pkg/store/entstore', {'TestBotRuntimeSupervisorLeaseLossCancels',
                'TestBotHomeFileCommitFence', 'TestBotApprovalResumeFenced', 'TestBotApprovalRequeueCurrentAuthority',
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
                'TestBackupFileFirstMappingIndependentOwnership', 'TestBackupFileFirstReconciliationPreservesQuiescence'}),
            'controller-docker': ('./pkg/daemon', {'TestBotEnhancedChatNoPythonDocker',
                                                'TestBotNewMessageDockerBetweenCallReproduction',
                                                'TestBotEnhancedChatTeamNoPythonDocker'}),
            'controller-kubernetes-home': ('./tests/e2e/bots', set(self.kubernetes_scenario_inventory())),
            'controller-docker-allmode': ('./pkg/daemon', {'TestBotAllModeRetainedSessionDocker'}),
            'controller-docker-delegate': ('./pkg/daemon', {'TestBotDelegatedRetainedSessionDocker'}),
        }
        expected['controller-personal-mailbox'] = ('./pkg/store/entstore', {'TestPersonalSettingsBotEmailRoundTripScopeAndClear'})
        expected['controller-api'][1].update({'TestBotAuthenticationEvidenceHTTPAcceptance',
            'TestBotFileDeliveryHTTPNativeAuthorization', 'TestBotContinueInteractiveRouteRequiresAuthentication',
            'TestCapturedDrillOccurrenceExactAndNoReplay', 'TestCapturedDrillRepeatedOccurrenceConcurrentNoReplay'})
        expected['controller-daemon'][1].update({'TestBotConfiguredProcessAdmissionHTTP', 'TestBotConfiguredBrowserAdmissionHTTP', 'TestBotPreparedPlanAdmissionHTTP',
            'TestBotConfiguredServiceAdmissionHTTP', 'TestCapturedScheduledDrillDriftHTTP', 'TestScheduledDrillApprovalHasNoCapturedChildAuthority'})
        expected['controller-api'][1].add('TestBotPlanningOwnerHTTPControls')
        expected['controller-daemon'][1].add('TestBotOwnerPlanConsentHTTP')
        expected['controller-store'][1].update({'TestBotPlanningDurableOwnerMode',
            'TestBotSQLiteSQLStorageLockOrderNoSessionReentry', 'TestBackupFileFirstSQLiteConcurrentRestoreArbitration'})
        expected['controller-store'][1].update(name for parents in self.restore_library_inventory().values() for name in parents)
        self.assertEqual(set(driver.CONSOLIDATION_CONTROLLER), set(expected))
        for tier, (package, names) in expected.items():
            actual_package, actual_names = driver.CONSOLIDATION_CONTROLLER[tier]
            self.assertEqual(actual_package, package)
            self.assertEqual(set(actual_names), names)
            self.assertEqual(len(actual_names), len(names))
        children = driver.CONSOLIDATION_CONTROLLER_SUBTESTS
        self.assertEqual(children['controller-store']['TestBotApprovalConcurrentConsumption'], ['personal', 'team'])
        self.assertEqual(len(children['controller-api']['TestBotTitleAnswerAuthorityHTTPRegression']), 16)
        self.assertIn('title_configured_search/team/missing=true', children['controller-daemon']['TestBotEnhancedChatBaseline'])
        self.assertIn('owner_priority_and_control/personal/priority_pause', children['controller-daemon']['TestBotEnhancedChatBaseline'])
        self.assertIn('team/schedule_create/disabled-unrelated', children['controller-store']['TestBotAuthorizedScheduleQueuedReconciliation'])
        self.assertIn('personal/interrupt-sql', children['controller-store']['TestBackupFileFirstRestoreFilesBeforeSQL'])
        self.assertIn('team/interrupt-sql', children['controller-store']['TestBackupFileFirstRestoreFilesBeforeSQL'])

    def test_controller_rejects_missing_scoped_approval_consumption(self):
        for scope in ('personal', 'team'):
            omitted = 'TestBotApprovalConcurrentConsumption/' + scope
            def execute(command, env, evidence=None, **kwargs):
                package = command[-3].replace('./', 'github.com/SAP/astonish/')
                for name in evidence.required:
                    if name == omitted:
                        continue
                    evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
                    evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}))
                evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}))
                return True
            with self.subTest(scope=scope), tempfile.TemporaryDirectory() as directory, \
                    patch.dict(os.environ, {}, clear=True), \
                    patch.object(driver, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                report = Path(directory) / 'report.json'
                result = driver.main(['--stage', 'chat-consolidation', '--phase', 'controller', '--report', str(report)])
                self.assertEqual(result, 1)
                data = json.loads(report.read_text())
                gate = next(g for g in data['gates'] if g['tier'] == 'controller-store')
                self.assertEqual(gate['status'], 'FAIL')
                self.assertIn(omitted, str(gate['errors']))

    def test_controller_bad_evidence_and_secret_scrubbing(self):
        for mode in ('zero', 'foreign', 'no-run', 'skip', 'missing-child', 'unfinished', 'failed',
                     'exception', 'malformed', 'bad-type'):
            def execute(command, env, evidence=None, **kwargs):
                self.assertIn('-count=1', command)
                self.assertFalse({'GOFLAGS', 'OPENAI_API_KEY', 'ASTONISH_TEST_DSN', 'KUBECONFIG',
                                  'ASTONISH_TEST_DOCKER'} & set(env))
                if mode == 'exception':
                    raise TimeoutError('controlled failure')
                package = command[-3].replace('./', 'github.com/SAP/astonish/')
                if mode == 'foreign':
                    package = 'foreign/package'
                for name in evidence.required:
                    if mode == 'zero' or (mode == 'missing-child' and '/' in name):
                        continue
                    if mode != 'no-run':
                        evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
                    evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}))
                if mode == 'skip':
                    evidence.feed(json.dumps({'Package': package, 'Test': 'child', 'Action': 'skip'}))
                if mode == 'malformed':
                    evidence.feed('{broken JSON')
                if mode == 'bad-type':
                    evidence.feed(json.dumps({'Package': package, 'Test': ['invalid'], 'Action': 'pass'}))
                if mode != 'unfinished':
                    evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}))
                return mode != 'failed'
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory, \
                    patch.dict(os.environ, {'PATH': os.environ['PATH'], 'OPENAI_API_KEY': 'secret',
                                            'GOFLAGS': '-run=None', 'KUBECONFIG': '/production'}, clear=True), \
                    patch.object(driver, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                report = Path(directory) / 'report.json'
                self.assertEqual(driver.main(['--stage', 'chat-consolidation', '--phase', 'controller',
                                               '--report', str(report)]), 1)
                self.assertEqual(json.loads(report.read_text())['status'], 'FAIL')

    def test_controller_opt_in_docker_is_fresh_subset_not_completion(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shell = root / '@base/rootfs/bin/sh'
            shell.parent.mkdir(parents=True)
            shell.touch()
            source = {'PATH': os.environ['PATH'], 'ASTONISH_TEST_DOCKER': '1',
                      'ASTONISH_SANDBOX_IMAGE': 'owned:local', 'ASTONISH_BOT_HOME_TEST_LAYERS': str(root)}
            with patch.dict(os.environ, source, clear=True), \
                    patch.object(driver, 'run', side_effect=self.consolidation_pass), contextlib.redirect_stdout(io.StringIO()):
                report = root / 'report.json'
                self.assertEqual(driver.main(['--stage', 'chat-consolidation', '--phase', 'controller',
                                               '--report', str(report)]), 2)
                data = json.loads(report.read_text())
                self.assertTrue(all(gate['status'] == 'PASS' and gate['fresh'] for gate in data['gates']
                                    if gate['tier'] != 'controller-kubernetes-home'))
                self.assertEqual(next(gate for gate in data['gates']
                                      if gate['tier'] == 'controller-kubernetes-home')['status'], 'BLOCKED')
                self.assertEqual(data['status'], 'BLOCKED')
                self.assertEqual(next(gate for gate in data['gates'] if gate['tier'] == 'controller-docker')['required'], ['TestBotEnhancedChatNoPythonDocker',
                                                                'TestBotNewMessageDockerBetweenCallReproduction',
                                                                'TestBotEnhancedChatTeamNoPythonDocker'])
                self.assertEqual(set(item['id'] for item in data['remaining_requirements']),
                    {'controller-all-mode-no-python', 'controller-team-physical',
                     'controller-cold-cross-backend', 'controller-authority-matrix'})

    def test_chat_consolidation_baseline_exact_evidence(self):
        for mode in ('pass', 'zero', 'skip', 'foreign', 'no-run', 'unfinished', 'failed'):
            def execute(command, env, evidence=None, **kwargs):
                self.assertNotIn('OPENAI_API_KEY', env)
                self.assertNotIn('GOFLAGS', env)
                self.assertNotIn('ASTONISH_TEST_DSN', env)
                if evidence:
                    package = command[-3].replace('./', 'github.com/SAP/astonish/')
                    if mode == 'foreign':
                        package = 'foreign/package'
                    for name in evidence.required:
                        if mode == 'zero':
                            continue
                        if mode != 'no-run':
                            evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
                        evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}))
                    if mode == 'skip':
                        evidence.feed(json.dumps({'Package': package, 'Test': 'TestBotEnhancedChatBaseline/child', 'Action': 'skip'}))
                    if mode != 'unfinished':
                        evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}))
                return mode != 'failed'
            with tempfile.TemporaryDirectory() as directory, \
                    patch.dict(os.environ, {'PATH': os.environ['PATH'], 'OPENAI_API_KEY': 'do-not-inherit', 'GOFLAGS': '-run=None'}, clear=True), \
                    patch.object(driver, 'run', side_effect=execute), \
                    patch.object(driver, 'consolidation_docker_regression', return_value={'status': 'FIXED', 'errors': []}), \
                    contextlib.redirect_stdout(io.StringIO()):
                report = Path(directory) / 'report.json'
                result = driver.main(['--stage', 'chat-consolidation', '--phase', 'baseline', '--report', str(report)])
                self.assertEqual(result, 0 if mode == 'pass' else 1, mode)
                data = json.loads(report.read_text())
                self.assertEqual(data['status'], 'PASS' if mode == 'pass' else 'FAIL')
                self.assertEqual(len(data['gates']), 3)
                self.assertEqual(data['regression']['status'], 'FIXED')

    @staticmethod
    def consolidation_pass(command, env, evidence=None, **kwargs):
        package = command[-3].replace('./', 'github.com/SAP/astonish/')
        for name in evidence.required:
            for action in ('run', 'pass'):
                evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': action}) + '\n')
        evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}) + '\n')
        return True

    def test_chat_consolidation_reproduced_is_historical_not_success(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(driver, 'run', side_effect=self.consolidation_pass), \
                patch.object(driver, 'consolidation_docker_regression', return_value={'status': 'REPRODUCED'}), \
                contextlib.redirect_stdout(io.StringIO()):
            report = Path(directory) / 'report.json'
            result = driver.main(['--stage', 'chat-consolidation', '--phase', 'baseline', '--report', str(report)])
            self.assertEqual(result, 1)
            self.assertEqual(json.loads(report.read_text())['status'], 'FAIL')

    def test_chat_consolidation_runtime_fresh_subset_never_completes_phase(self):
        for mode in ('pass', 'zero', 'foreign', 'no-run', 'skip', 'missing-child', 'unfinished', 'failed', 'exception'):
            def execute(command, env, evidence=None, **kwargs):
                self.assertIn('-count=1', command)
                for key in ('GOFLAGS', 'OPENAI_API_KEY', 'ASTONISH_TEST_DSN', 'KUBECONFIG', 'ASTONISH_TEST_DOCKER'):
                    self.assertNotIn(key, env)
                if mode == 'exception':
                    raise TimeoutError('controlled failure')
                package = command[-3].replace('./', 'github.com/SAP/astonish/')
                if mode == 'foreign':
                    package = 'foreign/package'
                for name in evidence.required:
                    if mode == 'zero' or (mode == 'missing-child' and '/' in name):
                        continue
                    if mode != 'no-run':
                        evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
                    evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}))
                if mode == 'skip':
                    evidence.feed(json.dumps({'Package': package, 'Test': 'child', 'Action': 'skip'}))
                if mode != 'unfinished':
                    evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}))
                return mode != 'failed'
            with tempfile.TemporaryDirectory() as directory, \
                    patch.dict(os.environ, {'PATH': os.environ['PATH'], 'GOFLAGS': '-run=None', 'OPENAI_API_KEY': 'secret',
                                            'KUBECONFIG': '/production', 'ASTONISH_TEST_DOCKER': '1'}, clear=True), \
                    patch.object(driver, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                report = Path(directory) / 'report.json'
                result = driver.main(['--stage', 'chat-consolidation', '--phase', 'runtime', '--report', str(report)])
                data = json.loads(report.read_text())
                self.assertEqual(result, 2 if mode == 'pass' else 1, mode)
                self.assertEqual(data['status'], 'BLOCKED' if mode == 'pass' else 'FAIL', mode)
                self.assertEqual(data['phase_acceptance'], {'runtime': 'BLOCKED' if mode == 'pass' else 'FAIL'})
                self.assertEqual({gate['tier'] for gate in data['gates']},
                                 {'runtime-kubernetes-bootstrap', 'runtime-template-wiring', 'runtime-delegate-receipts', 'runtime-http', 'runtime-live-authority', 'runtime-child-alias-leases', 'runtime-construction',
                                  'runtime-prepared-plan', 'runtime-prepared-review',
                                  'runtime-prepared-engine', 'runtime-prepared-store',
                                   'runtime-captured-drill-tools', 'runtime-trusted-snapshot'})
                self.assertTrue(data['remaining_requirements'])
                self.assertTrue(all(item['status'] == 'BLOCKED' for item in data['remaining_requirements']))
                self.assertTrue(all(Path(gate['log']).is_file() for gate in data['gates']))
                self.assertTrue(all(gate['fresh'] for gate in data['gates']))

    def test_storage_backup_cli_requires_all_scope_children(self):
        names = driver.CONSOLIDATION_STORAGE['storage-docker-backup-cli'][1]
        self.assertEqual(names, ['TestBotFileFirstBackupCLI', 'TestBotFileFirstStoppedBackupCLI'])
        for name in names:
            self.assertEqual(driver.CONSOLIDATION_SUBTESTS[name], ['personal', 'team'])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shell = root / '@base/rootfs/bin/sh'
            shell.parent.mkdir(parents=True)
            shell.touch()
            source = {'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_SANDBOX_IMAGE': 'owned:local',
                      'ASTONISH_BOT_HOME_TEST_LAYERS': str(root)}
            def execute(command, env, evidence=None, **kwargs):
                self.assertEqual(env['ASTONISH_TEST_DOCKER'], '1')
                package = 'github.com/SAP/astonish/cmd/astonish'
                for name in evidence.required:
                    if name == 'TestBotFileFirstStoppedBackupCLI/team':
                        continue
                    evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
                    evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}))
                evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}))
                return True
            with patch.object(driver, 'run', side_effect=execute):
                gate = driver.consolidation_progress_gate('storage-docker-backup-cli', './cmd/astonish', names, source, root)
            self.assertEqual(gate['status'], 'FAIL')
            self.assertIn('TestBotFileFirstStoppedBackupCLI/team', str(gate['errors']))

    def test_chat_consolidation_storage_missing_infra_keeps_helpers_required(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True), \
                patch.object(driver, 'run', side_effect=self.consolidation_pass) as run, contextlib.redirect_stdout(io.StringIO()):
            report = Path(directory) / 'report.json'
            self.assertEqual(driver.main(['--stage', 'chat-consolidation', '--phase', 'storage', '--report', str(report)]), 2)
            data = json.loads(report.read_text())
            expected = set(driver.CONSOLIDATION_STORAGE) | {
                tier for key in driver.CONSOLIDATION_REMAINING['storage']
                for tier in driver.CONSOLIDATION_REQUIREMENT_TIERS[key]}
            self.assertEqual({gate['tier'] for gate in data['gates']}, expected)
            self.assertEqual(len(data['gates']), len(expected))
            for gate in data['gates']:
                if gate['tier'] in driver.CONSOLIDATION_STORAGE or gate['tier'] == 'controller-kubernetes-home':
                    self.assertEqual(gate['status'], 'BLOCKED')
                    self.assertFalse(gate['fresh'])
            self.assertIn('storage-live-kubernetes', [r['id'] for r in data['remaining_requirements']])

    def test_chat_consolidation_storage_opt_in_subset_keeps_live_kubernetes_blocked(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shell = root / '@base/rootfs/bin/sh'
            shell.parent.mkdir(parents=True)
            shell.write_text('fixture')
            binary = root / 'node'
            binary.write_text('fixture')
            binary.chmod(0o700)
            source = {'PATH': os.environ['PATH'], 'ASTONISH_TEST_DOCKER': '1',
                      'ASTONISH_SANDBOX_IMAGE': 'explicit-test-image:local',
                      'ASTONISH_BOT_HOME_TEST_LAYERS': str(root),
                      'ASTONISH_TEST_COMPILED_HOME_FILES': '1', 'ASTONISH_BOT_FILES_TEST_BINARY': str(binary),
                      'OPENAI_API_KEY': 'secret', 'KUBECONFIG': '/production', 'ASTONISH_TEST_DSN': 'secret'}
            def execute(command, env, evidence=None, **kwargs):
                for key in ('OPENAI_API_KEY', 'KUBECONFIG', 'ASTONISH_TEST_DSN'):
                    self.assertNotIn(key, env)
                if command[-3].endswith('k8s'):
                    self.assertEqual(env['ASTONISH_BOT_FILES_TEST_BINARY'], str(binary))
                    self.assertNotIn('ASTONISH_TEST_DOCKER', env)
                return self.consolidation_pass(command, env, evidence)
            with patch.dict(os.environ, source, clear=True), patch.object(driver.sys, 'platform', 'linux'), \
                    patch.object(driver, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                report = root / 'report.json'
                self.assertEqual(driver.main(['--stage', 'chat-consolidation', '--phase', 'storage', '--report', str(report)]), 2)
                data = json.loads(report.read_text())
                self.assertTrue(all(gate['status'] == 'PASS' for gate in data['gates']
                                    if gate['tier'] != 'controller-kubernetes-home'))
                self.assertEqual(next(gate for gate in data['gates']
                                      if gate['tier'] == 'controller-kubernetes-home')['status'], 'BLOCKED')
                self.assertEqual(data['status'], 'BLOCKED')
                self.assertEqual(data['phase_acceptance'], {'storage': 'BLOCKED'})
                k8s = next(g for g in data['gates'] if g['tier'] == 'storage-kubernetes-adapter-fixture')
                self.assertIn('NOT live Kubernetes', k8s['boundary'])
                cli = next(g for g in data['gates'] if g['tier'] == 'storage-docker-backup-cli')
                self.assertEqual(cli['required'], ['TestBotFileFirstBackupCLI', 'TestBotFileFirstStoppedBackupCLI',
                    'TestBotFileFirstBackupCLI/personal', 'TestBotFileFirstBackupCLI/team',
                    'TestBotFileFirstStoppedBackupCLI/personal', 'TestBotFileFirstStoppedBackupCLI/team'])

    def test_chat_consolidation_manifests_are_real_and_documented(self):
        for tier, (package, names) in (driver.CONSOLIDATION_RUNTIME | driver.CONSOLIDATION_STORAGE | driver.CONSOLIDATION_CONTROLLER).items():
            source = '\n'.join(p.read_text() for p in (driver.ROOT / package).glob('*test.go'))
            for name in names:
                self.assertRegex(source, rf'func {re.escape(name)}\(t \*testing.T\)', tier)
        text = Path(__file__).with_name('README.bots-acceptance.md').read_text()
        for field in ('remaining_requirements', 'phase_acceptance', 'evidence_policy', 'ASTONISH_TEST_COMPILED_HOME_FILES',
                      'ASTONISH_BOT_FILES_TEST_BINARY', 'storage-live-kubernetes', 'controller-api',
                      'controller-daemon', 'controller-store', 'controller-docker',
                      'controller-files-before-sql', 'controller-authority-matrix'):
            self.assertIn(field, text)

    def test_chat_consolidation_all_cannot_bypass_unimplemented_phases(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True), \
                patch.object(driver, 'run', side_effect=self.consolidation_pass), contextlib.redirect_stdout(io.StringIO()):
            report = Path(directory) / 'report.json'
            self.assertEqual(driver.main(['--stage', 'chat-consolidation', '--phase', 'all', '--report', str(report)]), 2)
            data = json.loads(report.read_text())
            self.assertEqual(len(data['gates']), 36)
            self.assertEqual({gate['tier'] for gate in data['gates'] if gate['phase'] == 'controller'},
                             set(driver.CONSOLIDATION_CONTROLLER))
            self.assertIn('controller-all-mode-no-python', [r['id'] for r in data['remaining_requirements']])
            self.assertNotIn('baseline-full-acceptance', [r['id'] for r in data['remaining_requirements']])
            self.assertEqual(data['phase_acceptance']['baseline'], 'BLOCKED')
            self.assertEqual(data['regression']['status'], 'BLOCKED')
            self.assertEqual(len([g for g in data['gates'] if g['phase'] == 'baseline']), 4)
            for key in ('clients-all-mode-conversation', 'clients-shared-visual-parity'):
                self.assertIn(key, [r['id'] for r in data['remaining_requirements']])
            for row in data['requirement_evidence']:
                if row['phase'] == 'events':
                    self.assertEqual(row['status'], 'PASS')

    def test_chat_consolidation_regression_requires_exact_fresh_fix(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shell = root / '@base/rootfs/bin/sh'
            shell.parent.mkdir(parents=True)
            shell.write_text('fixture')
            source = {'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_SANDBOX_IMAGE': 'test:local',
                      'ASTONISH_BOT_HOME_TEST_LAYERS': str(root)}
            with patch.object(driver, 'run', return_value=True):
                result = driver.consolidation_docker_regression(source, root)
                self.assertEqual(result['status'], 'FAIL')
                self.assertIn('no matching tests executed', result['errors'])
            with patch.object(driver, 'run', side_effect=self.consolidation_pass):
                self.assertEqual(driver.consolidation_docker_regression(source, root)['status'], 'FIXED')

    def test_chat_consolidation_missing_docker_is_blocked_not_accepted_failure(self):
        with patch.dict(os.environ, {'PATH': os.environ['PATH']}, clear=True), patch.object(driver, 'run') as run:
            result = driver.consolidation_docker_regression(dict(os.environ), Path(tempfile.gettempdir()))
            self.assertEqual(result['status'], 'BLOCKED')
            run.assert_not_called()

    def test_chat_consolidation_phase_flag_is_not_a_release_bypass(self):
        for args in (['--phase', 'baseline'], ['--stage', 'chat-consolidation'],
                     ['--stage', 'chat-consolidation', '--phase', 'baseline', '--local-only'],
                     ['--stage', 'chat-consolidation', '--phase', 'baseline', '--real-provider']):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                driver.main(args)

    def test_file_first_manifest_and_documentation(self):
        self.assertEqual(set(driver.FILE_FIRST_GATES), {'http', 'worker', 'browser', 'cli-pty',
                         'docker-worker', 'docker-home', 'continuity', 'backup', 'postgres', 'kubernetes-home'})
        self.assertEqual(driver.FILE_FIRST_GATES['backup'], ('./cmd/astonish', ['TestBotFileFirstBackupCLI']))
        for tier, (package, names) in driver.FILE_FIRST_GATES.items():
            if tier == 'backup':  # Missing implementation must block execution, not these driver self-tests.
                continue
            source = '\n'.join(p.read_text() for p in (driver.ROOT / package).glob('*test.go'))
            for name in names:
                self.assertRegex(source, rf'func {re.escape(name)}\(t \*testing.T\)')
        text = Path(__file__).with_name('README.bots-acceptance.md').read_text()
        for required in ('--stage file-first', 'TestBotFileFirstBackupCLI', 'BLOCKED', 'ASTONISH_TEST_TASK_CONTINUITY=1'):
            self.assertIn(required, text)

    def test_file_first_rejects_partial_flags(self):
        for extra in (['--preflight'], ['--local-only'], ['--config', '/unused'], ['--postgres'], ['--real-provider'], ['--soak']):
            with patch.object(driver, 'file_first') as stage, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    driver.main(['--stage', 'file-first'] + extra)
                stage.assert_not_called()

    def test_file_first_empty_environment_blocks_every_required_tier_without_launch(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True), \
                patch.object(driver, 'run') as run, patch.object(driver.subprocess, 'run') as probe, \
                contextlib.redirect_stdout(io.StringIO()):
            path = Path(directory) / 'report.json'
            self.assertEqual(2, driver.main(['--stage', 'file-first', '--report', str(path)]))
            report = json.loads(path.read_text())
            self.assertEqual(report['status'], 'BLOCKED')
            self.assertEqual(set(report['errors']), {'local', 'postgres', 'kubernetes-home'})
            self.assertEqual(len(report['gates']), len(driver.FILE_FIRST_GATES))
            run.assert_not_called()
            probe.assert_not_called()

    def test_file_first_requires_each_infrastructure_opt_in(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(driver.shutil, 'which', return_value='/tool'), \
                patch.object(driver, 'ROOT', Path(directory)):
            root = Path(directory)
            (root / 'web/node_modules').mkdir(parents=True)
            (root / 'layers/@base/rootfs/bin').mkdir(parents=True)
            (root / 'layers/@base/rootfs/bin/sh').touch()
            (root / 'kubeconfig').touch()
            env = {'PATH': os.environ['PATH'], 'ASTONISH_BOT_BROWSER_PATH': sys.executable,
                   'ASTONISH_SANDBOX_IMAGE': 'owned-image', 'ASTONISH_BOT_HOME_TEST_LAYERS': str(root / 'layers'),
                   'ASTONISH_MEMORY_BROWSER_ACCEPTANCE': '1', 'ASTONISH_TEST_DOCKER': '1',
                   'ASTONISH_TEST_TASK_CONTINUITY': '1', 'ASTONISH_TEST_DSN': 'owned-test-dsn',
                   'ASTONISH_BOTS_TEST_DB_ISOLATED': '1', 'KUBECONFIG': str(root / 'kubeconfig'),
                   'ASTONISH_BOTS_K8S_CONTEXT': 'owned', 'ASTONISH_BOTS_K8S_ISOLATED': '1',
                   'ASTONISH_E2E_DEDICATED': '1', 'ASTONISH_E2E_HOMES_PVC': 'owned-home',
                   'ASTONISH_E2E_SANDBOX_NAMESPACE': 'astonish-e2e-owned',
                   'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE': 'astonish-e2e-control'}
            self.assertEqual(driver.file_first_prerequisites(env), {})
            for key in set(env) - {'PATH'}:
                missing = {k: v for k, v in env.items() if k != key}
                self.assertTrue(driver.file_first_prerequisites(missing), key)
            for key, value in (('ASTONISH_E2E_HOMES_PVC', 'astonish-layers'),
                               ('ASTONISH_E2E_SANDBOX_NAMESPACE', 'default'),
                               ('ASTONISH_BOT_HOME_TEST_LAYERS', 'relative'),
                               ('KUBECONFIG', 'relative'), ('ASTONISH_BOT_BROWSER_PATH', directory)):
                self.assertTrue(driver.file_first_prerequisites(dict(env, **{key: value})), key)

    def test_file_first_exact_evidence_environment_and_required_tiers(self):
        unsafe = {'OPENAI_API_KEY': 'secret', 'ANTHROPIC_API_KEY': 'secret', 'ASTONISH_DSN': 'production',
                  'ASTONISH_CONFIG': 'production', 'GOFLAGS': '-run=Nothing', 'MAKEFLAGS': '-i',
                  'ASTONISH_E2E_KEEP_ALIVE': '1', 'ASTONISH_BOTS_LIVE_PASSWORD': 'secret',
                  'ASTONISH_PROGRESS_PROCESS_STAGE': 'restart-settled'}
        env = dict(unsafe, PATH=os.environ['PATH'], ASTONISH_SANDBOX_IMAGE='owned-image',
                   ASTONISH_TEST_DSN='postgres://owned:secret@localhost/test', ASTONISH_BOTS_TEST_DB_ISOLATED='1',
                   KUBECONFIG='/dedicated/config', ASTONISH_TEST_TASK_CONTINUITY='1')
        for mode in ('pass', 'zero', 'skip-child', 'foreign', 'pass-without-run', 'package-unfinished',
                     'missing-child', 'backup-absent', 'postgres-absent', 'k8s-absent', 'nonzero'):
            def execute(command, child_env, evidence=None, **kwargs):
                self.assertFalse(set(unsafe) & set(child_env))
                if '-tags=integration' not in command and '-tags=e2e' not in command:
                    self.assertFalse(driver.FILE_FIRST_PG_ENV & set(child_env))
                if '-tags=e2e' not in command:
                    self.assertFalse(driver.FILE_FIRST_K8S_ENV & set(child_env))
                if evidence:
                    package = command[-3].replace('./', 'github.com/SAP/astonish/')
                    if mode == 'foreign':
                        package = 'foreign/package'
                    for name in evidence.required:
                        if mode == 'zero' or (mode == 'missing-child' and '/' in name) or \
                                (mode == 'backup-absent' and name == 'TestBotFileFirstBackupCLI') or \
                                (mode == 'postgres-absent' and '-tags=integration' in command) or \
                                (mode == 'k8s-absent' and '-tags=e2e' in command):
                            continue
                        if mode != 'pass-without-run':
                            evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
                        evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}))
                    if mode == 'skip-child':
                        evidence.feed(json.dumps({'Package': package, 'Test': 'TestSelected/child', 'Action': 'skip'}))
                    if mode != 'package-unfinished':
                        evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}))
                    if '-tags=e2e' in command:
                        self.assertIn('-timeout=20m', command)
                    if 'TestBotTaskContinuityHTTPAcceptance' in evidence.required:
                        self.assertEqual(child_env['ASTONISH_TEST_TASK_CONTINUITY'], '1')
                return mode != 'nonzero' or evidence is None
            with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, env, clear=True), \
                    patch.object(driver, 'file_first_prerequisites', return_value={}), \
                    patch.object(driver, 'file_first_kubernetes_probe', return_value=[]), \
                    patch.object(driver, 'run', side_effect=execute) as run, contextlib.redirect_stdout(io.StringIO()):
                path = Path(directory) / 'report.json'
                expected = 0 if mode == 'pass' else (2 if mode == 'backup-absent' else 1)
                self.assertEqual(expected, driver.main(['--stage', 'file-first', '--report', str(path)]), mode)
                report = json.loads(path.read_text())
                self.assertEqual(len(report['gates']), len(driver.FILE_FIRST_GATES))
                self.assertEqual(report['status'], {0: 'PASS', 1: 'FAIL', 2: 'BLOCKED'}[expected])
                if mode == 'backup-absent':
                    self.assertIn('backup', report['unverified'])
                self.assertIn(['npm', '--prefix', 'web', 'run', 'build'], [c.args[0] for c in run.call_args_list])
                self.assertEqual(dict(os.environ), env)

    def test_file_first_kubernetes_probes_are_read_only_and_fail_closed(self):
        env = {'ASTONISH_BOTS_K8S_CONTEXT': 'owned', 'ASTONISH_E2E_SANDBOX_NAMESPACE': 'astonish-e2e-owned',
               'ASTONISH_E2E_CONTROL_PLANE_NAMESPACE': 'astonish-e2e-control', 'ASTONISH_E2E_HOMES_PVC': 'owned-home'}
        for mode in ('pass', 'context', 'pending', 'rwo', 'error'):
            def execute(command, **kwargs):
                self.assertNotIn('apply', command)
                self.assertNotIn('delete', command)
                self.assertNotIn('use-context', command)
                if command[1:] == ['config', 'current-context']:
                    output = 'other' if mode == 'context' else 'owned'
                elif 'pvc' in command:
                    output = json.dumps({'status': {'phase': 'Pending' if mode == 'pending' else 'Bound'},
                                         'spec': {'accessModes': ['ReadWriteOnce' if mode == 'rwo' else 'ReadWriteMany']}})
                else:
                    output = 'namespace/owned'
                return subprocess.CompletedProcess(command, 1 if mode == 'error' else 0, output)
            with patch.object(driver.subprocess, 'run', side_effect=execute):
                if mode == 'error':
                    with self.assertRaises(OSError):
                        driver.file_first_kubernetes_probe(env)
                else:
                    self.assertEqual(bool(driver.file_first_kubernetes_probe(env)), mode != 'pass')

    def test_file_first_shell_blocks_without_infrastructure(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / 'report.json'
            result = subprocess.run([sys.executable, '-B', str(Path(__file__).with_name('bots_acceptance_driver.py')),
                                     '--stage', 'file-first', '--report', str(report)],
                                    env={'PATH': os.environ['PATH']}, capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
            self.assertEqual(json.loads(report.read_text())['status'], 'BLOCKED')

    def test_durable_progress_names_are_real_and_include_required_boundaries(self):
        for package, names in driver.DURABLE_PROGRESS_CASES.items():
            source = '\n'.join(p.read_text() for p in (driver.ROOT / package).glob('*test.go'))
            self.assertEqual(len(names), len(set(names)))
            for name in names:
                self.assertRegex(source, rf'func {re.escape(name)}\(t \*testing.T\)')
        for name in ('TestBotProgressSixHourClockAcceptance', 'TestBotProgressProcessRestartAcceptance',
                     'TestBotProgressBrowserAcceptance', 'TestBotTitleConfirmationDockerBrowserAcceptance'):
            self.assertIn(name, driver.DURABLE_PROGRESS_CASES['./pkg/api'])
        self.assertIn('TestBotProgressPTYAcceptance', driver.DURABLE_PROGRESS_CASES['./cmd/astonish'])
        self.assertIn('TestBotProgressScheduledContinuation', driver.DURABLE_PROGRESS_CASES['./pkg/store/entstore'])

    def test_durable_progress_exact_evidence_no_skips_or_empty_pass(self):
        env = {'PATH': os.environ['PATH'], 'ASTONISH_MEMORY_BROWSER_ACCEPTANCE': '1',
               'ASTONISH_TEST_DOCKER': '1', 'ASTONISH_BOT_BROWSER_PATH': sys.executable,
               'ASTONISH_SANDBOX_IMAGE': 'owned-image', 'OPENAI_API_KEY': 'forbidden',
               'ASTONISH_DSN': 'production', 'GOFLAGS': '-run=Nothing',
               'ASTONISH_PROGRESS_PROCESS_STAGE': 'restart-settled'}
        for mode in ('pass', 'zero', 'skip-child', 'foreign', 'pass-without-run', 'package-unfinished', 'missing-child'):
            def execute(command, child_env, evidence=None, **kwargs):
                for key in ('OPENAI_API_KEY', 'ASTONISH_DSN', 'GOFLAGS', 'ASTONISH_PROGRESS_PROCESS_STAGE'):
                    self.assertNotIn(key, child_env)
                if evidence:
                    package = command[-3].replace('./', 'github.com/SAP/astonish/')
                    if mode == 'foreign':
                        package = 'foreign/package'
                    for name in evidence.required:
                        if mode == 'zero' or (mode == 'missing-child' and '/' in name):
                            continue
                        if mode != 'pass-without-run':
                            evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'run'}))
                        evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': 'pass'}))
                    if mode == 'skip-child':
                        evidence.feed(json.dumps({'Package': package, 'Test': 'TestRequired/child', 'Action': 'skip'}))
                    if mode != 'package-unfinished':
                        evidence.feed(json.dumps({'Package': package, 'Action': 'pass'}))
                return True
            with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, env, clear=True), \
                    patch.object(driver.shutil, 'which', return_value='/tool'), \
                    patch.object(driver, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                report = Path(directory) / 'evidence.json'
                code = driver.main(['--stage', 'durable-progress', '--report', str(report)])
                self.assertEqual(0 if mode == 'pass' else 1, code, mode)
                result = json.loads(report.read_text())
                self.assertEqual('PASS' if mode == 'pass' else 'FAIL', result['status'])
                self.assertIn('two-hour wall-clock soak', result['unverified'])
                self.assertEqual(len(driver.DURABLE_PROGRESS_CASES), len(result['gates']))

    def test_durable_progress_optional_and_missing_prerequisites_block_without_launch(self):
        for extra in ([], ['--soak'], ['--real-provider', '--config', '/tmp/not-read'], ['--postgres']):
            with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True), \
                    patch.object(driver, 'run') as run, contextlib.redirect_stdout(io.StringIO()):
                report = Path(directory) / 'blocked.json'
                self.assertEqual(2, driver.main(['--stage', 'durable-progress', '--report', str(report)] + extra))
                self.assertEqual('BLOCKED', json.loads(report.read_text())['status'])
                run.assert_not_called()

    def test_durable_progress_shell_missing_prerequisites_blocks(self):
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(['bash', str(Path(__file__).with_name('bots_acceptance.sh')),
                                     '--stage', 'durable-progress', '--report', directory + '/report.json'],
                                    env={'PATH': os.environ['PATH']}, capture_output=True, text=True, timeout=20)
            self.assertEqual(2, result.returncode)
            self.assertEqual('BLOCKED', json.loads(Path(directory, 'report.json').read_text())['status'])

    def test_title_confirmation_requires_team_search_followup_and_restart_evidence(self):
        self.assertIn('TestBotTitleConfirmationProcessRestart', driver.TITLE_CONFIRMATION_CASES['./pkg/store/entstore'])
        self.assertIn('TestBotTitleTeamOptionBrowserAcceptance', driver.TITLE_CONFIRMATION_BROWSER)
        self.assertIn('TestBotTitleTeamCustomBrowserAcceptance', driver.TITLE_CONFIRMATION_BROWSER)
        self.assertIn('TestBotTitleConfiguredSearchHTTP', driver.TITLE_CONFIRMATION_CASES['./pkg/daemon'])
        self.assertIn('TestBotTitleConfirmationStateHTTPAcceptance', driver.TITLE_CONFIRMATION_CASES['./pkg/api'])
        self.assertIn('TestBotTitleConfirmedLaterQuestionContinuation', driver.TITLE_CONFIRMATION_CASES['./pkg/api'])

    def test_title_confirmation_requires_explicit_browser_and_docker(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(driver, 'run') as run:
            self.assertEqual(2, driver.title_confirmation())
            run.assert_not_called()

    def test_title_confirmation_rejects_partial_modes(self):
        for extra in (['--preflight'], ['--local-only'], ['--config', '/tmp/not-used']):
            with contextlib.redirect_stderr(io.StringIO()), patch.object(driver, 'title_confirmation') as stage:
                with self.assertRaises(SystemExit):
                    driver.main(['--stage', 'title-confirmation'] + extra)
                stage.assert_not_called()

    def test_title_confirmation_exact_evidence_and_environment(self):
        env = {'PATH': os.environ['PATH'], 'ASTONISH_MEMORY_BROWSER_ACCEPTANCE': '1',
               'ASTONISH_BOT_BROWSER_PATH': sys.executable, 'ASTONISH_SANDBOX_IMAGE': 'owned-image',
               'OPENAI_API_KEY': 'not-for-controlled-tests', 'ASTONISH_DSN': 'production'}
        for mode in ('pass', 'missing', 'skip', 'foreign'):
            def execute(command, child_env, evidence=None, **kwargs):
                self.assertNotIn('OPENAI_API_KEY', child_env)
                self.assertNotIn('ASTONISH_DSN', child_env)
                if evidence:
                    package = command[-3].replace('./', 'github.com/SAP/astonish/')
                    if mode == 'foreign':
                        package = 'foreign/package'
                    for name in evidence.required:
                        if mode == 'missing':
                            continue
                        for action in ('run', 'skip' if mode == 'skip' else 'pass'):
                            evidence.feed(json.dumps({'Package': package, 'Test': name, 'Action': action}))
                    return not evidence.errors()
                return True
            with patch.dict(os.environ, env, clear=True), patch.object(driver.shutil, 'which', return_value='/tool'), patch.object(driver, 'run', side_effect=execute) as run:
                self.assertEqual(0 if mode == 'pass' else 1, driver.title_confirmation(), mode)
                if mode == 'pass':
                    commands = [c.args[0] for c in run.call_args_list]
                    self.assertTrue(any('TestBotTitleConfirmationDockerBrowserAcceptance' in ' '.join(c) for c in commands))
                    self.assertTrue(any('TestBotTitleChatPTYAcceptance' in ' '.join(c) for c in commands))
                    self.assertIn(['npm', '--prefix', 'web', 'run', 'build'], commands)

    def test_title_confirmation_shell_missing_prerequisites_fails(self):
        result = subprocess.run(['bash', str(Path(__file__).with_name('bots_acceptance.sh')),
                                 '--stage', 'title-confirmation'],
                                env={'PATH': os.environ['PATH'], 'HOME': os.environ.get('HOME', '/tmp')},
                                capture_output=True, text=True, timeout=20)
        self.assertEqual(2, result.returncode)
        self.assertIn('ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 required', result.stdout)
        self.assertIn('ASTONISH_BOT_BROWSER_PATH', result.stdout)
        self.assertIn('ASTONISH_SANDBOX_IMAGE', result.stdout)

    def test_title_confirmation_docs_keep_evidence_boundaries(self):
        for path in ('tests/e2eboot/README.bots-acceptance.md', 'docs/architecture/bot-release-checklist.md'):
            text = (driver.ROOT / path).read_text()
            for required in ('--stage title-confirmation', 'ASTONISH_BOT_BROWSER_PATH',
                             'ASTONISH_SANDBOX_IMAGE', 'pending' if 'checklist' in path else 'unverified'):
                self.assertIn(required, text)

    def test_title_confirmation_routes_without_release_fallback(self):
        with patch.object(driver, 'title_confirmation', return_value=2) as stage, patch.object(driver, 'prerequisites') as release:
            self.assertEqual(2, driver.main(['--stage', 'title-confirmation']))
            stage.assert_called_once_with()
            release.assert_not_called()

    def test_title_confirmation_contract_rejects_missing_or_skipped_evidence(self):
        for output in ('', json.dumps({'Action': 'skip', 'Test': 'TestBotTitleConfirmationHTTPAcceptance'})):
            with patch.object(driver.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, output)):
                self.assertEqual(1, driver.title_confirmation_contract())

    def test_title_confirmation_contract_requires_factory_journey_and_restart(self):
        def result(command, **kwargs):
            names = command[-1][2:-2].split('|')
            if './pkg/launcher' in command:
                self.assertIn('TestBotTitleConfirmationFactoryJourney', names)
            if './pkg/store/entstore' in command:
                self.assertIn('TestBotTitleConfirmationProcessRestart', names)
            return subprocess.CompletedProcess(command, 0, '\n'.join(json.dumps({'Action': 'pass', 'Test': name}) for name in names))
        with patch.object(driver.subprocess, 'run', side_effect=result) as run:
            self.assertEqual(0, driver.title_confirmation_contract())
            self.assertTrue(any('./pkg/launcher' in call.args[0] for call in run.call_args_list))
            self.assertTrue(any('./pkg/store/entstore' in call.args[0] for call in run.call_args_list))

    def test_title_confirmation_stage_routes_without_release_fallback(self):
        with patch.object(driver, 'title_confirmation_contract', return_value=1) as contract:
            self.assertEqual(1, driver.main(['--stage', 'title-confirmation-contract']))
            contract.assert_called_once_with()

    def test_agent_stage_routes_exact_arguments_and_propagates_status(self):
        for stage in ('execution', 'progress', 'learning', 'continuity', 'postgres', 'all'):
            argv = ['--agent-stage', stage]
            if stage == 'all':
                argv += ['--config', '/explicit/private-agent.json']
            for result in (0, 1, 2):
                with self.subTest(stage=stage, result=result), patch(
                        'employee_http_acceptance.main', return_value=result) as agent, patch.object(
                        driver, 'run') as run, patch.object(driver, 'prerequisites') as prerequisites:
                    self.assertEqual(result, driver.main(argv))
                    agent.assert_called_once_with(argv)
                    run.assert_not_called()
                    prerequisites.assert_not_called()

    def test_agent_cannot_use_partial_release_flags_or_other_modes(self):
        for extra in (['--preflight'], ['--local-only'], ['--skills-stage', 'all']):
            with self.subTest(extra=extra), patch('employee_http_acceptance.main') as agent, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as error:
                    driver.main(['--agent-stage', 'all', '--config', '/explicit/config.json'] + extra)
                self.assertEqual(2, error.exception.code)
                agent.assert_not_called()

    def test_agent_missing_config_fails_without_release_fallback(self):
        with patch.object(driver, 'run') as run, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                driver.main(['--agent-stage', 'all'])
            self.assertEqual(2, error.exception.code)
            run.assert_not_called()

    def test_agent_all_docs_keep_current_invocation_and_partial_boundary(self):
        text = (driver.ROOT / 'tests/e2eboot/README.bots-acceptance.md').read_text()
        for required in ('--agent-stage all', '--verify-bot-agent', '--browser --terminal --agent',
                         'ASTONISH_BOTS_TEST_DB_ISOLATED=1', 'No historical receipts', 'not Kubernetes'):
            self.assertIn(required, text)

    def test_employee_gates_require_real_named_tests_in_correct_package(self):
        command = driver.go_command("./pkg/daemon", driver.DAEMON_HTTP)
        self.assertIn("-json", command)
        self.assertIn("./pkg/daemon", command)
        for name in driver.DAEMON_HTTP:
            self.assertNotIn(name, driver.BROWSER)
            self.assertTrue(any(re.search(r"func " + name + r"\(", f.read_text())
                                for f in (driver.ROOT / "pkg/daemon").glob("*_test.go")))
        self.assertIn("TestIntegrationPostgresBotEmployeeOperations", driver.PG)
        with self.assertRaises(ValueError):
            driver.go_command("./pkg/api", [])
        evidence = driver.GoEvidence(driver.DAEMON_HTTP)
        evidence.feed(json.dumps({"Action": "pass", "Package": "wrong/package"}))
        self.assertTrue(evidence.errors())

    def test_corrected_chat_model_gates_and_docs(self):
        self.assertTrue({"TestBotTitleChatBrowserAcceptance",
                         "TestBotAmbiguousTitleQuestionBrowserAcceptance",
                         "TestBotThinkingRestoreBrowserAcceptance"} <= set(driver.BROWSER))
        self.assertIn("TestBotTitleChatPTYAcceptance", driver.CLI)
        self.assertIn("TestIntegrationPostgresBotChatModel", driver.PG)
        self.assertIn("TestIntegrationPostgresBotTitleStartState", driver.PG)
        self.assertEqual(driver.RELEASE_SCENARIOS, {f"BOTS-{n:03d}" for n in range(1, 11)})
        self.assertEqual(driver.clean_env({"ASTONISH_PG_ACTIVATION_DISPOSABLE": "1"}),
                         {"ASTONISH_PG_ACTIVATION_DISPOSABLE": "1"})
        for name in ("bots", "bot-chat-first", "bot-home-setup"):
            text = (driver.ROOT / "docs/architecture" / (name + ".md")).read_text()
            self.assertIn("Active", text, name)
        lifecycle = (driver.ROOT / "docs/architecture/bot-draft-activation.md").read_text()
        self.assertIn("unreleased", lifecycle.lower())
        for marker in ("Active, Paused, Stopped, Deleted", "separately authorized exact schedule operation",
                       "does not enable disabled schedules", "No Draft upgrade/data conversion"):
            self.assertIn(marker, lifecycle)

    # Scope deliberately excludes provider docs, README and the public website.
    NEUTRAL_DOCS = (
        "docs/research/bot-chat-first-ux.md",
        "docs/reports/astonish-bots-implementation-plan.md",
        "docs/architecture/agent-engine.md",
        "docs/architecture/code-intelligence.md",
        "docs/architecture/fleet-tool-efficiency.md",
        "docs/architecture/fleet-generic-orchestration.md",
        "docs/architecture/terminal-app.md",
        "docs/architecture/bot-chat-first.md",
    )

    def test_research_docs_are_product_neutral(self):
        forbidden = re.compile(
            r"\b(?:grok|muse|openclaude|openclaw|opencode|codex|perplexity)\b"
            r"|(?-i:\bCursor\b)"
            r"|claude[- ]code|docs\.x\.ai|muse\.ai|meta\.com/help/superintelligence"
            r"|(?:~/Downloads/system_prompt\.md|/tmp/[^\s`]*research|/root/(?:grok|openclaude|openclaw))",
            re.IGNORECASE,
        )
        for name in self.NEUTRAL_DOCS:
            with self.subTest(document=name):
                text = (driver.ROOT / name).read_text()
                self.assertIsNone(forbidden.search(text), name)

    def test_chat_first_research_preserves_limits_and_supersession(self):
        text = (driver.ROOT / self.NEUTRAL_DOCS[0]).read_text()
        for required in ("not a hands-on visual audit", "not independently verified",
                         "supersedes", "not implementation", "tenant isolation",
                         "explicit approval", "conversation"):
            self.assertIn(required, text)
        self.assertNotRegex(text, r"https?://|/tmp/|~/Downloads|(?m:^>\s*[“\"])")

    def test_neutral_document_links_have_existing_targets_and_anchors(self):
        targets = {(driver.ROOT / name).resolve() for name in self.NEUTRAL_DOCS}
        # Inspect all documentation inbound links, not only edited files.
        for source in (driver.ROOT / "docs").rglob("*.md"):
            if any(part in ("node_modules", ".vitepress") for part in source.parts):
                continue
            for link in re.findall(r"\]\(([^\s)]+)\)", source.read_text()):
                path, _, anchor = link.partition("#")
                if "://" in path or path.startswith("/"):
                    continue
                target = (source.parent / path).resolve() if path else source.resolve()
                if target not in targets:
                    continue
                self.assertTrue(target.is_file(), f"{source}: {link}")
                if anchor:
                    headings = re.findall(r"^#{1,6}\s+(.+)$", target.read_text(), re.MULTILINE)
                    slugs = {re.sub(r"[^\w\- ]", "", heading.lower()).replace(" ", "-")
                             for heading in headings}
                    self.assertIn(anchor, slugs, f"{source}: {link}")

    def test_empty_environment_fails_closed(self):
        errors = driver.prerequisites({})
        for key in ("ASTONISH_TEST_DSN", "ASTONISH_BOTS_TEST_DB_ISOLATED", "ASTONISH_BOT_BROWSER_PATH",
                    "ASTONISH_BOTS_K8S_CONTEXT", "ASTONISH_BOT_HOME_TEST_LAYERS"):
            self.assertTrue(any(key in error for error in errors), key)

    def test_shell_preflight_without_credentials_is_nonzero(self):
        result = subprocess.run(["bash", str(Path(__file__).with_name("bots_acceptance.sh")), "--preflight"],
                                env={"PATH": os.environ["PATH"], "HOME": os.environ.get("HOME", "/tmp")},
                                capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn("NOT RELEASE READY", result.stdout)
        self.assertIn("postgres: NOT RUN", result.stdout)
        self.assertIn("ASTONISH_BOTS_K8S_CONTEXT required", result.stdout)
        self.assertIn("KUBECONFIG must name", result.stdout)
        self.assertIn("kubernetes-home: NOT RUN", result.stdout)

    def test_missing_and_skipped_tests_never_pass(self):
        required_browser = {"TestBotChatFirstWorkspaceBrowserAcceptance",
                            "TestBotChatFirstOrderedConversationBrowserAcceptance",
                            "TestBotChatFirstConversationHTTPAcceptance",
                            "TestBotProposalRoutesStrictHTTP",
                            "TestBotSourceIntakeProposalHTTPAcceptance",
                            "TestBotSourceIntakeAdaptiveHTTP", "TestBotSourceIntakeTemplateHTTP",
                            "TestBotWorkspaceBrowserAcceptance", "TestBotScheduledWorkspaceBrowserAcceptance"}
        self.assertTrue(required_browser <= set(driver.BROWSER))
        self.assertIn("TestBotParityPTYAcceptance", driver.CLI)
        self.assertIn("TestBotCLIExplicitIntentSubprocessAcceptance", driver.CLI)
        for package, names in (("pkg/api", driver.BROWSER), ("cmd/astonish", driver.CLI)):
            source = "\n".join(p.read_text() for p in (driver.ROOT / package).glob("*test.go"))
            for name in names:
                self.assertRegex(source, rf"func {re.escape(name)}\(t \*testing.T\)")
                evidence = driver.GoEvidence(names)
                for selected in names:
                    evidence.feed(json.dumps({"Action": "run", "Test": selected}))
                    evidence.feed(json.dumps({"Action": "skip" if selected == name else "pass", "Test": selected}))
                self.assertTrue(evidence.errors(), name)
        for events in ([], [{"Action": "skip", "Test": "TestRequired"}],
                       [{"Action": "run", "Test": "TestRequired"}, {"Action": "pass", "Test": "TestRequired"},
                        {"Action": "skip", "Test": "TestRequired/child"}]):
            evidence = driver.GoEvidence(["TestRequired"])
            for event in events:
                evidence.feed(json.dumps(event))
            self.assertTrue(evidence.errors())

    def test_exact_required_test_and_package_failure(self):
        evidence = driver.GoEvidence(["TestRequired"])
        for action in ("run", "pass"):
            evidence.feed(json.dumps({"Action": action, "Test": "TestRequired"}))
        self.assertEqual(evidence.errors(), [])
        evidence.feed('{"Action":"fail","Package":"example"}')
        self.assertTrue(evidence.errors())

    def test_dsn_and_decoded_password_redaction(self):
        env = {"ASTONISH_TEST_DSN": "postgres://owner:private%21@host/db", "OPENAI_API_KEY": "sample-secret"}
        result = driver.redact("postgres://owner:private%21@host/db private! sample-secret", env)
        for secret in ("owner", "private", "sample-secret"):
            self.assertNotIn(secret, result)

    def test_makefile_does_not_read_env_and_preserves_lint(self):
        source = "-include .env\n-include .env.local\nexport\nlint:\n\tgolangci-lint run\n"
        result = driver.sanitized_makefile(source)
        self.assertNotIn("include", result)
        self.assertIn("golangci-lint run", result)

    def test_production_overrides_and_opt_ins_not_inherited(self):
        env = driver.clean_env({"ASTONISH_DSN": "production", "GOFLAGS": "-run=Nothing",
                                "MAKEFLAGS": "-i", "ASTONISH_TEST_DOCKER": "1", "ASTONISH_E2E_KEEP_ALIVE": "1"})
        self.assertEqual(env, {})

    def test_local_checks_exclude_deployment_kubernetes_environment(self):
        deployment = {
            "KUBECONFIG": "/missing/acceptance-kubeconfig",
            "ASTONISH_BOTS_K8S_CONTEXT": "isolated",
            "ASTONISH_BOTS_K8S_ISOLATED": "1",
            "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-sandbox",
            "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE": "astonish-e2e-control",
            "ASTONISH_E2E_DEDICATED": "1",
            "ASTONISH_E2E_HOMES_PVC": "acceptance-homes",
        }
        for args in (["--local-only"], []):
            with self.subTest(args=args), \
                 patch.dict(os.environ, dict(deployment, ASTONISH_BOT_BROWSER_PATH="/bin/sh"), clear=True), \
                 patch.object(driver, "prerequisites", return_value=[]), \
                 patch.object(driver.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "isolated\n")), \
                 patch.object(driver, "run", return_value=True) as commands, \
                 contextlib.redirect_stdout(io.StringIO()):
                driver.main(args)
                local = [call for call in commands.call_args_list
                         if call.args[0][0] in ("make", "npm") or
                         any(package in call.args[0] for package in ("./pkg/api", "./cmd/astonish", "./pkg/store/entstore"))
                         and "-tags=integration" not in call.args[0]]
                self.assertEqual(len(local), 8)
                for call in local:
                    self.assertFalse(set(deployment) & set(call.args[1]), call.args[0])
                for key, value in deployment.items():
                    self.assertEqual(os.environ[key], value)

    def test_full_kubernetes_gate_preserves_validated_config(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "kubeconfig"
            config.write_text("acceptance config fixture")
            env = {"KUBECONFIG": str(config), "ASTONISH_BOT_BROWSER_PATH": "/bin/sh",
                   "ASTONISH_BOTS_K8S_CONTEXT": "isolated",
                   "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-sandbox"}
            self.assertFalse(any("KUBECONFIG" in error for error in driver.prerequisites(env)))
            def validated(child_env, local):
                self.assertFalse(local)
                self.assertEqual(child_env["KUBECONFIG"], str(config))
                self.assertTrue(Path(child_env["KUBECONFIG"]).is_file())
                return []
            with patch.dict(os.environ, env, clear=True), \
                 patch.object(driver, "prerequisites", side_effect=validated) as checks, \
                 patch.object(driver.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "isolated\n")) as context, \
                 patch.object(driver, "run", return_value=True) as commands, \
                 contextlib.redirect_stdout(io.StringIO()):
                driver.main([])
            checks.assert_called_once()
            self.assertEqual(context.call_args.kwargs["env"]["KUBECONFIG"], str(config))
            live = [call for call in commands.call_args_list if "./tests/e2e/bots" in call.args[0]]
            self.assertEqual(len(live), 1)
            self.assertEqual(live[0].args[1]["KUBECONFIG"], str(config))
            self.assertEqual(live[0].args[1]["ASTONISH_E2E_SANDBOX_BACKEND"], "k8s")
            required = {"TestBotsLiveLifecycle", "TestBotsLiveKubernetesRetainedHome"}
            self.assertEqual(live[0].args[2].required, required)
            command = live[0].args[0]
            self.assertIn("-tags=e2e", command)
            self.assertEqual(command[command.index("-run") + 1],
                             "^(TestBotsLiveLifecycle|TestBotsLiveKubernetesRetainedHome)$")
            config.unlink()
            self.assertTrue(any("KUBECONFIG" in error for error in driver.prerequisites(env)))

    def test_live_gate_requires_both_non_skipped_names(self):
        required = {"TestBotsLiveLifecycle", "TestBotsLiveKubernetesRetainedHome"}
        self.assertEqual(set(driver.LIVE_BOTS), required)
        for absent in required:
            for state in ("missing", "skip", "pass"):
                with self.subTest(absent=absent, state=state):
                    evidence = driver.GoEvidence(driver.LIVE_BOTS)
                    for name in required:
                        if name == absent and state == "missing":
                            continue
                        for action in ("run", state if name == absent else "pass"):
                            evidence.feed(json.dumps({"Package": "example/tests/e2e/bots",
                                                      "Test": name, "Action": action}))
                    self.assertEqual(bool(evidence.errors()), state != "pass")

    def test_actual_catalog_remains_missing_after_partial_lifecycle(self):
        evidence = driver.GoEvidence(driver.LIVE_BOTS)
        for name in driver.LIVE_BOTS:
            for action in ("run", "pass"):
                evidence.feed(json.dumps({"Package": "example/tests/e2e/bots", "Test": name, "Action": action}))
        errors = driver.scenario_coverage_errors([evidence])
        self.assertEqual(len(errors), 20)
        for scenario in driver.RELEASE_SCENARIOS:
            self.assertIn(scenario + ": status is not covered (missing)", errors)
            self.assertIn(scenario + ": no deployed test_refs", errors)
        readme = Path(__file__).with_name("README.bots-acceptance.md").read_text()
        for name in driver.LIVE_BOTS:
            self.assertIn(name, readme)

    def test_local_success_remains_partial(self):
        with patch.dict(os.environ, {"ASTONISH_BOT_BROWSER_PATH": "/bin/sh"}, clear=True), \
             patch.object(driver, "prerequisites", return_value=[]), \
             patch.object(driver, "run", return_value=True) as commands, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(driver.main(["--local-only"]), 3)
        self.assertEqual(commands.call_count, 9)
        self.assertIn("spec-coverage: FAIL", output.getvalue())
        self.assertIn("NOT RELEASE READY", output.getvalue())
        self.assertIn("kubernetes-home: NOT RUN", output.getvalue())

    def test_components_passing_cannot_release_missing_spec_scenarios(self):
        env = {"ASTONISH_BOT_BROWSER_PATH": "/bin/sh", "ASTONISH_BOTS_K8S_CONTEXT": "isolated",
               "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-test"}
        with patch.dict(os.environ, env, clear=True), \
             patch.object(driver, "prerequisites", return_value=[]), \
             patch.object(driver.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "isolated\n")), \
             patch.object(driver, "run", return_value=True), contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(driver.main([]), 1)
        self.assertIn("kubernetes-home: PASS", output.getvalue())
        self.assertIn("spec-coverage: FAIL", output.getvalue())
        self.assertIn("full lifecycle acceptance not implemented/evidenced", output.getvalue())
        self.assertNotIn("FULL BOT RELEASE ACCEPTANCE PASSED", output.getvalue())

    def test_catalog_requires_real_refs_and_current_exact_non_skipped_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tests/scenarios").mkdir(parents=True)
            (root / "tests/e2e/bots").mkdir(parents=True)
            (root / "go.mod").write_text("module example\n")
            source = root / "tests/e2e/bots/lifecycle_test.go"
            source.write_text("func TestLifecycle(t *testing.T) {}\n")
            catalog = root / "tests/scenarios/bots.yaml"
            content = "scenarios:\n" + "".join(
                f'  - id: {sid}\n    status: covered\n    test_refs:\n'
                '      - "tests/e2e/bots/lifecycle_test.go::TestLifecycle"\n'
                for sid in sorted(driver.RELEASE_SCENARIOS))
            catalog.write_text(content)
            def evidence(package="example/tests/e2e/bots", actions=("run", "pass"), skip=False, finished=True):
                result = driver.GoEvidence(["TestLifecycle"])
                for action in actions:
                    result.feed(json.dumps({"Package": package, "Test": "TestLifecycle", "Action": action}))
                if skip:
                    result.feed(json.dumps({"Package": package, "Test": "TestLifecycle/child", "Action": "skip"}))
                if finished:
                    result.feed(json.dumps({"Package": package, "Action": "pass"}))
                return result
            self.assertEqual(driver.scenario_coverage_errors([evidence()], root), [])
            for bad in ([], [evidence(package="example/pkg/api")], [evidence(actions=("pass",))],
                        [evidence(skip=True)], [evidence(finished=False)]):
                self.assertTrue(driver.scenario_coverage_errors(bad, root))
            catalog.write_text(content.replace("status: covered", "status: missing", 1))
            self.assertTrue(driver.scenario_coverage_errors([evidence()], root))
            catalog.write_text(content.replace("BOTS-001", "BOTS-002", 1))
            self.assertTrue(driver.scenario_coverage_errors([evidence()], root))
            catalog.write_text(content)
            source.unlink()
            self.assertTrue(driver.scenario_coverage_errors([evidence()], root))
            catalog.write_text("scenarios:\n")
            self.assertEqual(len(driver.scenario_coverage_errors([], root)), 10)

    def test_actual_makefile_filter_removes_only_production_includes(self):
        source = (driver.ROOT / "Makefile").read_text()
        expected = [line for line in source.splitlines() if line.strip() not in ("-include .env", "-include .env.local")]
        self.assertEqual(driver.sanitized_makefile(source).splitlines(), expected)
        self.assertIn("test: test-unit", expected)
        self.assertIn("lint:", expected)

    def test_preflight_diagnostics_never_print_secret_values(self):
        env = {"ASTONISH_TEST_DSN": "secret-dsn", "OPENAI_API_KEY": "secret-provider",
               "ASTONISH_BOTS_LIVE_PASSWORD": "secret-live"}
        self.assertFalse(any("secret-" in error for error in driver.prerequisites(env)))
        self.assertNotIn("secret-", driver.redact("secret-dsn secret-provider secret-live", env))

    def test_real_subprocess_output_is_redacted_and_empty_go_gate_fails(self):
        env = dict(os.environ, ASTONISH_TEST_DSN="postgres://owner:sensitive@host/db")
        with contextlib.redirect_stdout(io.StringIO()) as output:
            ok = driver.run([sys.executable, "-c", "import os; print(os.environ['ASTONISH_TEST_DSN'])"],
                            env, driver.GoEvidence(["TestRequired"]), timeout=5)
        self.assertFalse(ok)
        self.assertNotIn("sensitive", output.getvalue())
        self.assertIn("no matching tests executed", output.getvalue())

    def test_connection_string_password_is_redacted(self):
        env = {"ASTONISH_TEST_DSN": "host=localhost password='sensitive phrase' dbname=postgres"}
        self.assertNotIn("sensitive", driver.redact("error sensitive phrase", env))

    def test_lint_failure_cannot_be_suppressed(self):
        def run(command, *_):
            return command[-1] != "lint"
        with patch.dict(os.environ, {"ASTONISH_BOT_BROWSER_PATH": "/bin/sh"}, clear=True), \
             patch.object(driver, "prerequisites", return_value=[]), patch.object(driver, "run", side_effect=run), \
             contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(driver.main(["--local-only"]), 1)
        self.assertIn("make-lint: FAIL", output.getvalue())
        self.assertIn("NOT RELEASE READY", output.getvalue())


class DeferredInfrastructureTests(unittest.TestCase):
    def test_cli_scope_is_explicit_and_restricted(self):
        for argv in ([], ['--stage', 'durable-progress'], ['--agent-stage', 'all']):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                driver.main(argv + ['--defer-kubernetes-postgresql'])
        for phase in driver.CONSOLIDATION_PHASES:
            with patch.object(driver, 'chat_consolidation', return_value=7) as call:
                self.assertEqual(driver.main(['--stage', 'chat-consolidation', '--phase', phase,
                                             '--defer-kubernetes-postgresql']), 7)
                self.assertTrue(call.call_args.args[0].defer_kubernetes_postgresql)
        with patch.object(driver, 'file_first', return_value=7) as call:
            self.assertEqual(driver.main(['--stage', 'file-first', '--defer-kubernetes-postgresql']), 7)
            self.assertTrue(call.call_args.args[0].defer_kubernetes_postgresql)

    def test_scope_documentation_matches_cli_and_preserves_nonphysical_obligations(self):
        for filename in ('docs/architecture/bot-runtime-evidence.md', 'tests/e2eboot/README.bots-acceptance.md'):
            text = (driver.ROOT / filename).read_text()
            for marker in ('--defer-kubernetes-postgresql', 'local-deferred-kubernetes-postgresql',
                           'NOT PASS', 'Docker-only', 'mailbox', 'default', 'user-requested'):
                self.assertIn(marker, text)

    def test_local_prerequisites_do_not_lookup_cluster_or_require_dsn(self):
        def which(tool, **kwargs):
            self.assertNotEqual(tool, 'kubectl')
            return '/test/' + tool
        with patch.object(driver.shutil, 'which', side_effect=which):
            errors = driver.file_first_prerequisites({}, defer_kubernetes_postgresql=True)
        self.assertEqual(set(errors), {'local'})
        self.assertFalse(any('DSN' in message or 'KUBECONFIG' in message for message in errors['local']))

    def test_live_gate_deferred_before_prerequisites_or_network(self):
        package, names = driver.CONSOLIDATION_CONTROLLER['controller-kubernetes-home']
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(driver, 'consolidation_kubernetes_prerequisites', side_effect=AssertionError('no DSN')), \
             patch.object(driver, 'consolidation_kubernetes_probe', side_effect=AssertionError('no cluster')), \
             patch.object(driver, 'run', side_effect=AssertionError('no subprocess')):
            gate = driver.consolidation_progress_gate('controller-kubernetes-home', package, names,
                {'KUBECONFIG': '/production', 'ASTONISH_TEST_DSN': 'production'}, Path(directory), True)
        self.assertEqual(gate['status'], 'DEFERRED')
        self.assertEqual(gate['evidence_status'], 'NOT PASS')
        self.assertFalse(gate['blocking'])
        self.assertFalse(gate['fresh'])
        self.assertIsNone(gate['log'])
        self.assertIn('user-requested', gate['reason'])

    def test_helpers_remain_required_without_infrastructure_environment(self):
        for tier in ('runtime-kubernetes-bootstrap', 'controller-store', 'controller-personal-mailbox'):
            manifest = {**driver.CONSOLIDATION_RUNTIME, **driver.CONSOLIDATION_CONTROLLER}
            package, names = manifest[tier]
            def execute(command, env, evidence, **kwargs):
                self.assertNotIn('KUBECONFIG', env)
                self.assertNotIn('ASTONISH_TEST_DSN', env)
                self.assertFalse(any(k.startswith('PG') for k in env))
                self.assertEqual(command, driver.consolidation_command(tier, package, names))
                return False  # Required mocked tests must fail, never be waived.
            with tempfile.TemporaryDirectory() as directory, patch.object(driver, 'run', side_effect=execute):
                gate = driver.consolidation_progress_gate(tier, package, names,
                    {'KUBECONFIG': '/production', 'ASTONISH_TEST_DSN': 'production', 'PGHOST': 'production'},
                    Path(directory), True)
            self.assertEqual(gate['status'], 'FAIL')
            self.assertTrue(gate['errors'])

    def test_physical_only_mapping_deferred_but_other_unresolved_not_waived(self):
        results = driver.consolidation_requirement_evidence(['storage', 'runtime', 'controller'], [],
                                                           defer_kubernetes_postgresql=True)
        live = next(r for r in results if r['id'] == 'storage-live-kubernetes')
        self.assertEqual(live['status'], 'DEFERRED')
        self.assertEqual(live['evidence_status'], 'NOT PASS')
        cold = next(r for r in results if r['id'] == 'controller-cold-cross-backend')
        self.assertIn('Docker-only', cold['backend_scope'])
        self.assertEqual(cold['status'], 'BLOCKED')
        for row in results:
            if row is not live:
                self.assertEqual(row['status'], 'BLOCKED')
        strict = driver.consolidation_requirement_evidence(['storage'], [])
        self.assertEqual(next(r for r in strict if r['id'] == 'storage-live-kubernetes')['status'], 'BLOCKED')

    def test_remaining_exact_evidence_passes_only_local_profile_and_mailbox_failure_blocks(self):
        # Synthetic receipts test accounting only, never claim physical execution.
        phases = ('runtime', 'controller')
        with tempfile.TemporaryDirectory() as directory:
            gates = []
            for manifest in driver.consolidation_manifests().values():
                for tier, (package, names) in manifest.items():
                    if tier in driver.DEFERRED_INFRA_TIERS:
                        continue
                    required = driver.consolidation_required_tests(tier, names)
                    pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                    log = Path(directory) / (tier + '.jsonl')
                    events = [json.dumps({'Action': action, 'Package': pkg, 'Test': name})
                              for name in required for action in ('run', 'pass')]
                    log.write_text('\n'.join(events + [json.dumps({'Action': 'pass', 'Package': pkg})]) + '\n')
                    gates.append(dict(tier=tier, package=package, required=required, passed=required,
                        status='PASS', fresh=True, prerequisites_verified=True, errors=[], log=str(log),
                        command=driver.consolidation_command(tier, package, names)))
            rows = driver.consolidation_requirement_evidence(phases, gates, defer_kubernetes_postgresql=True)
            self.assertTrue(all(row['status'] == 'PASS' for row in rows), rows)
            self.assertTrue(any(d['status'] == 'DEFERRED' for row in rows for d in row['required_tiers']))
            strict = driver.consolidation_requirement_evidence(phases, gates)
            self.assertTrue(any(row['status'] == 'BLOCKED' for row in strict))
            mailbox = next(g for g in gates if g['tier'] == 'controller-personal-mailbox')
            mailbox.update(status='FAIL', errors=['mailbox persistence failed'])
            rows = driver.consolidation_requirement_evidence(phases, gates, defer_kubernetes_postgresql=True)
            for key in ('three-mode-capability-parity', 'runtime-production-wiring'):
                row = next(r for r in rows if r['id'] == key)
                self.assertEqual(row['status'], 'BLOCKED')
                self.assertIn('mailbox persistence failed', row['reason'])

    def test_file_first_deferred_profile_keeps_local_failures_and_reports_scope(self):
        for failure in (None, 'http', 'backup'):
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / 'report.json'
                def execute(command, env, evidence=None, **kwargs):
                    self.assertNotIn('KUBECONFIG', env)
                    self.assertNotIn('ASTONISH_TEST_DSN', env)
                    self.assertNotIn('kubectl', command)
                    self.assertNotIn('-tags=integration', command)
                    if evidence is not None:
                        package = next(p for p in command if p.startswith('./'))
                        pkg = 'github.com/SAP/astonish/' + package.removeprefix('./')
                        bad = failure and package == driver.FILE_FIRST_GATES[failure][0]
                        for name in evidence.required:
                            evidence.feed(json.dumps({'Action': 'run', 'Package': pkg, 'Test': name}))
                            evidence.feed(json.dumps({'Action': 'fail' if bad else 'pass', 'Package': pkg, 'Test': name}))
                        evidence.feed(json.dumps({'Action': 'fail' if bad else 'pass', 'Package': pkg}))
                        return not bad
                    return True
                with patch.dict(os.environ, {'ASTONISH_SANDBOX_IMAGE': 'test-owned'}, clear=True), \
                     patch.object(driver, 'file_first_prerequisites', return_value={}) as pre, \
                     patch.object(driver, 'file_first_kubernetes_probe', side_effect=AssertionError('no probe')), \
                     patch.object(driver, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                    code = driver.main(['--stage', 'file-first', '--defer-kubernetes-postgresql', '--report', str(path)])
                report = json.loads(path.read_text())
                self.assertEqual(code, 0 if failure is None else 1)
                self.assertEqual(pre.call_args.kwargs, {'defer_kubernetes_postgresql': True})
                self.assertFalse(report['acceptance_scope']['full_infrastructure_acceptance'])
                self.assertEqual({g['tier'] for g in report['gates'] if g['status'] == 'DEFERRED'}, {'postgres', 'kubernetes-home'})
                self.assertTrue(all(g['status'] == 'PASS' for g in report['gates'] if g['status'] != 'DEFERRED') if failure is None else report['status'] == 'FAIL')


if __name__ == "__main__":
    unittest.main()
