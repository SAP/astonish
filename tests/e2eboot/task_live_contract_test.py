import contextlib
import hashlib
import io
import json
import tempfile
import unittest
from pathlib import Path

import task_live_contract as contract


class TaskLiveContractTest(unittest.TestCase):
    def fixture(self, directory):
        state = dict(root=directory, workflow=contract.CONTRACT, task_nonce='a' * 64,
                     binary_sha256='b' * 64, browser_sha256='c' * 64,
                     task_config_verified=True, task_attempted=True,
                     task_intended=dict(provider=contract.PROVIDER, provider_type='sap_ai_core', model=contract.MODEL),
                     task_search=dict(engine='tavily', provider='', model='', tool='tavily_search', fingerprint='d' * 64))
        evidence = dict(contract=contract.CONTRACT, nonce=state['task_nonce'],
                        candidate=state['binary_sha256'], browser_candidate=state['browser_sha256'],
                        configured_search=state['task_search'], invocation_started_at='2026-01-01T00:00:00Z', samples=[])
        return state, evidence

    def test_exact_cases_missing_evidence_stays_unverified(self):
        with tempfile.TemporaryDirectory() as directory:
            state, evidence = self.fixture(directory)
            result = contract.evaluate(state, evidence)
            self.assertEqual(set(contract.REQUIRED), set(result))
            self.assertTrue(all(set(row) == {'status', 'reason'} and row['status'] == 'unverified'
                                and row['reason'] for row in result.values()))

    def test_model_booleans_and_unrelated_controlled_fixtures_cannot_prove_lifecycle(self):
        with tempfile.TemporaryDirectory() as directory:
            state, evidence = self.fixture(directory)
            evidence.update(grounding_verified=True, workspace_observed=True, passed=list(contract.REQUIRED),
                            receipts={name: {'status': 'passed'} for name in contract.REQUIRED},
                            controlled_stages={name: True for name in contract.REQUIRED})
            result = contract.evaluate(state, evidence)
            self.assertFalse(any(row['status'] == 'passed' for row in result.values()))

    def test_missing_sample_capture_is_unverified_not_fabricated_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            state, evidence = self.fixture(directory)
            evidence['samples'] = [dict(sample='stock-1', prompt='Stock Market News', status='running', sources={})]
            self.assertEqual('unverified', contract.evaluate(state, evidence)['stock-1']['status'])

    def test_binding_or_resume_is_failure_for_all_exact_cases(self):
        with tempfile.TemporaryDirectory() as directory:
            for field, value in [('nonce', 'e' * 64), ('contract', 'bot-agent-live-v1'), ('candidate', '')]:
                state, evidence = self.fixture(directory)
                evidence[field] = value
                self.assertTrue(all(row['status'] == 'failed' for row in contract.evaluate(state, evidence).values()))
            state, evidence = self.fixture(directory)
            state['diagnostic_resume'] = True
            self.assertTrue(all(row['status'] == 'failed' for row in contract.evaluate(state, evidence).values()))

    def test_failed_sample_and_changed_file_fail_not_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            state, evidence = self.fixture(directory)
            evidence['samples'] = [dict(sample='stock-1', prompt='Stock Market News', status='failed', blocker='source failed')]
            self.assertEqual('failed', contract.evaluate(state, evidence)['stock-1']['status'])
            path = Path(directory) / 'task-stock-1-run.json'
            path.write_text('{}')
            evidence['samples'][0] = dict(sample='stock-1', prompt='Stock Market News', status='diagnostic-observed',
                bot_id='11111111-1111-1111-1111-111111111111', run_id='22222222-2222-2222-2222-222222222222',
                sources={path.name: '0' * 64}, grounding_verified=True)
            result = contract.evaluate(state, evidence)
            self.assertEqual('failed', result['stock-1']['status'])
            self.assertIn('digest', result['stock-1']['reason'])

    def test_source_path_escape_symlink_and_aliases_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            state, _ = self.fixture(directory)
            target = Path(directory) / 'target.json'; target.write_text('{}')
            alias = Path(directory) / 'alias.json'; alias.symlink_to(target)
            for name in ('../target.json', str(target), alias.name):
                with self.assertRaises((ValueError, RuntimeError)):
                    contract.source(state, name, hashlib.sha256(b'{}').hexdigest())
            for raw in ('{"state":{},"State":{},"evidence":{}}', '{"state":{},"evidence":{},"extra":1}', '{"state":{},"evidence":{}} {}'):
                with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(2, contract.main(io.StringIO(raw)))

    def test_cli_exact_stdout_protocol_and_no_calls(self):
        with tempfile.TemporaryDirectory() as directory:
            state, evidence = self.fixture(directory)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(0, contract.main(io.StringIO(json.dumps(dict(state=state, evidence=evidence)))))
            self.assertEqual(set(contract.REQUIRED), set(json.loads(output.getvalue())))

    def transcript(self, casing='Content', bad_order=False, error=False):
        parts = [
            {'functionCall': {'id': 'search', 'name': 'perplexity_web_search'}},
            {'functionResponse': {'id': 'search', 'name': 'perplexity_web_search', 'response':
                {'provider': 'tavily', 'query': 'Stock Market News', 'citations': ['https://example.org/news'], **({'error': '429'} if error else {})}}},
            {'functionCall': {'id': 'fetch', 'name': 'web_fetch'}},
            {'functionResponse': {'id': 'fetch', 'name': 'web_fetch', 'response':
                {'url': 'https://example.org/news', 'content': 'Decisive retained source body', 'status': 200}}},
        ]
        if bad_order:
            parts[0], parts[2] = parts[2], parts[0]
        return ''.join('data: ' + json.dumps({casing: {'parts': [part]}, 'persisted': True}) + '\n\n' for part in parts)

    def test_search_rechecks_raw_pairing_order_route_and_actual_content_casing(self):
        with tempfile.TemporaryDirectory() as directory:
            state, evidence = self.fixture(directory)
            for casing in ('Content', 'content'):
                contract.search_first(self.transcript(casing), state['task_search'])
            for stream in (self.transcript(bad_order=True), self.transcript(error=True),
                           self.transcript().replace('tavily', 'perplexity'),
                           self.transcript().replace('"fetch", "name": "web_fetch", "response"', '"unknown", "name": "web_fetch", "response"'),
                           self.transcript('CONTENT')):
                with self.assertRaises(RuntimeError):
                    contract.search_first(stream, state['task_search'])

    def sample_files(self, state, key, index):
        bot_id = f'{index:08d}-1111-1111-1111-111111111111'
        run_id = f'{index:08d}-2222-2222-2222-222222222222'
        session_id = f'{index:08d}-3333-3333-3333-333333333333'
        home_id = f'{index:08d}-4444-4444-4444-444444444444'
        run = dict(id=run_id, bot_id=bot_id, session_id=session_id, provider=contract.PROVIDER,
                   model=contract.MODEL, status='succeeded', created_at='2026-01-01T00:00:01Z',
                   started_at='2026-01-01T00:00:02Z', finished_at='2026-01-01T00:00:03Z',
                   report=dict(summary='News', employee_message=dict(text='An answer', origin='agent')),
                   work=dict(schema_version=1, protocol='agent_led_v1', bot_id=bot_id, run_id=run_id,
                             work_revision=1, phase='finished', disposition='answered', candidate_digest='f' * 64))
        history = dict(id=home_id, messages=[dict(invocationId='bot-report:' + run_id, type='agent', content='An answer\n')])
        documents = {'bot.json': dict(id=bot_id, home_session_id=home_id), 'run.json': run,
                     'runs.json': dict(runs=[run]),
                     'model-before.json': dict(effectiveProvider=contract.PROVIDER, effectiveModel=contract.MODEL, credentialsAvailable=True),
                     'history-before.json': history, 'history-after.json': history,
                     'worker-events.txt': self.transcript()}
        files = {}
        for label, document in documents.items():
            raw = (document if isinstance(document, str) else json.dumps(document)).encode()
            name = 'task-' + key + '-' + label
            (Path(state['root']) / name).write_bytes(raw)
            files[name] = hashlib.sha256(raw).hexdigest()
        return dict(sample=key, prompt='Stock Market News', bot_id=bot_id, run_id=run_id,
                    status='diagnostic-observed', sources=files, grounding_verified=True,
                    workspace_observed=True, search_first_observed=True)

    def test_three_real_shaped_captures_only_discharge_search_not_grounding_or_lifecycle(self):
        with tempfile.TemporaryDirectory() as directory:
            state, evidence = self.fixture(directory)
            evidence['samples'] = [self.sample_files(state, key, index) for index, key in enumerate(contract.STOCKS, 1)]
            result = contract.evaluate(state, evidence)
            self.assertEqual(['configured-search-first'], [key for key, row in result.items() if row['status'] == 'passed'])
            self.assertTrue(all(result[key]['status'] == 'unverified' for key in contract.STOCKS))
            name = 'task-stock-1-history-after.json'
            path = Path(directory) / name
            doc = json.loads(path.read_text()); doc['messages'][0]['content'] = 'An answer\n\n'
            raw = json.dumps(doc).encode(); path.write_bytes(raw)
            evidence['samples'][0]['sources'][name] = hashlib.sha256(raw).hexdigest()
            result = contract.evaluate(state, evidence)
            self.assertEqual('failed', result['stock-1']['status'])
            self.assertNotEqual('passed', result['configured-search-first']['status'])

    def test_required_names_match_go_harness(self):
        import re
        go = (Path(__file__).parent / 'chat_first_browser/task_live.go').read_text()
        names = re.search(r'var taskRequired = \[\]string\{(.*?)\n\}', go, re.S).group(1)
        self.assertEqual(tuple(re.findall(r'"([^"]+)"', names)), contract.REQUIRED)


if __name__ == '__main__':
    unittest.main()
