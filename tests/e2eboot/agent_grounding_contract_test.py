"""Synthetic contract examples only; these are not live source assessments."""
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from uuid import uuid4

from agent_grounding_contract import (ASSESSMENT_FILE, CONTRACT, STOCKS, fetched_sources,
                                      payload_sha, sha, source_grounding)
from agent_live_contract import AGENT_CONTRACT, agent_evidence_results


class GroundingContractTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.state = dict(root=self.tmp.name, binary_sha256='a'*64, browser_sha256='b'*64, agent_nonce='c'*64)
        self.actual = dict(contract=AGENT_CONTRACT, candidate='a'*64, browser_candidate='b'*64, nonce='c'*64, samples=[])
        self.doc = dict(contract=CONTRACT, candidate='a'*64, browser_candidate='b'*64, nonce='c'*64,
                        evaluator=dict(identity='synthetic-test-only', kind='human', method='Synthetic fixture inspection'),
                        inspection_started_at='2026-01-02T12:01:00Z', assessed_at='2026-01-02T12:02:00Z',
                        coverage_policy='all-answer-content-v1', samples=[])
        self.answer = 'At 2026-01-02 the index stood at 100.'
        self.payload = dict(url='https://example.test/market', status=200,
                            content='Market update 2026-01-02: the index stood at 100.')
        parts = [dict(functionCall=dict(id='fetch-1', name='web_fetch')),
                 dict(functionResponse=dict(id='fetch-1', name='web_fetch', response=self.payload))]
        self.stream = 'data: ' + json.dumps(dict(persisted=True, content=dict(parts=parts))) + '\n\n'
        for key in STOCKS:
            bot, home, rid, session = (str(uuid4()) for _ in range(4))
            run = dict(id=rid, bot_id=bot, session_id=session, status='succeeded',
                       created_at='2026-01-02T12:00:00Z', started_at='2026-01-02T12:00:00Z', finished_at='2026-01-02T12:00:30Z',
                       provider='New AI Core Provider', model='anthropic--claude-4.6-sonnet',
                       work=dict(schema_version=1, protocol='agent_led_v1', bot_id=bot, run_id=rid,
                                 work_revision=1, phase='finished', disposition='answered', candidate_digest='d'*64),
                       report=dict(summary='Not the full answer', employee_message=dict(origin='agent', text=self.answer)))
            sample = dict(sample=key, prompt='Stock Market News', bot_id=bot, run_id=rid, sources={})
            self.actual['samples'].append(sample)
            self.put(sample, 'bot.json', dict(id=bot, home_session_id=home))
            for name in ('runs.json', 'runs-after.json'):
                self.put(sample, name, dict(runs=[run]))
            for when in ('before', 'after'):
                self.put(sample, 'model-'+when+'.json', dict(effectiveProvider=run['provider'], effectiveModel=run['model'], credentialsAvailable=True))
                self.put(sample, 'history-'+when+'.json', dict(id=home, messages=[dict(type='agent', invocationId='bot-report:'+rid, content=self.answer+'\n')]))
            self.put(sample, 'worker-events.txt', self.stream, raw=True)
            claim = dict(id='index', start=0, end=len(self.answer), quote=self.answer, verdict='supported',
                         rationale='Exact dated index value in synthetic source.',
                         evidence=[dict(call_id='fetch-1', url=self.payload['url'], payload_sha256=payload_sha(self.payload),
                                        field='content', quote='the index stood at 100.', rationale='Same index value and date.')],
                         temporal=dict(kind='current', verdict='aligned', as_of='2026-01-02', date_quote='2026-01-02', rationale='Same observation day.'))
            self.doc['samples'].append(dict(sample=key, bot_id=bot, run_id=rid, session_id=session,
                                            answer_sha256=sha(self.answer), sources=copy.deepcopy(sample['sources']),
                                            task_adequacy=dict(task=sample['prompt'], useful_full_result=True,
                                                               rationale='Synthetic positive judgment for Stock Market News; not a real assessment.',
                                                               limitations='Synthetic minimal fixture tests structure only, not real task adequacy.'),
                                            claims=[claim], nonclaims=[], coverage_statement='Every assertion in the synthetic answer inventoried.'))
        self.save()

    def put(self, sample, name, value, raw=False):
        data = value.encode() if raw else json.dumps(value).encode()
        name = 'agent-'+sample['sample']+'-'+name
        Path(self.tmp.name, name).write_bytes(data)
        sample['sources'][name] = hashlib.sha256(data).hexdigest()

    def save(self):
        self.path = Path(self.tmp.name, ASSESSMENT_FILE)
        self.path.write_text(json.dumps(self.doc))
        self.path.chmod(0o600)

    def check(self):
        source_grounding(self.state, self.actual)

    def test_go_persisted_sse_shape_and_lowercase_decode_identically(self):
        # Shape observed in .tmp/astonish-chat-first-x2ohs33e: the Go ADK
        # envelope exports Content; genai parts keep lower-camel JSON tags.
        # Values are synthetic, not a source/task-adequacy assessment.
        from agent_live_contract import retrieval
        parts = json.loads(self.stream[6:])['content']['parts']
        for field in ('Content', 'content'):
            with self.subTest(field=field):
                events = [dict(Content=None, Author='bot_runtime', Actions=dict(StateDelta={})),
                          {field: dict(role='model', parts=[parts[0]]), 'Author': 'chat'},
                          {field: dict(role='user', parts=[parts[1]]), 'Author': 'web_fetch'},
                          dict(persisted=True)]
                stream = ''.join('event: transcript\ndata: ' + json.dumps(e) + '\n\n' for e in events)
                self.assertEqual(fetched_sources(stream), fetched_sources(self.stream))
                retrieval(stream)

    def test_conflicting_content_aliases_are_rejected_by_both_consumers(self):
        from agent_live_contract import retrieval
        content = json.loads(self.stream[6:])['content']
        for first, second in ((content, None), (None, content), (content, dict(parts=[]))):
            stream = 'data: ' + json.dumps(dict(Content=first, content=second, persisted=True)) + '\n\n'
            for consumer in (fetched_sources, retrieval):
                with self.subTest(consumer=consumer.__name__, first=first), self.assertRaisesRegex((RuntimeError, ValueError), 'alias'):
                    consumer(stream)

    def test_actual_shape_capture_exact_publication_without_assessment(self):
        from agent_grounding_contract import stock_capture
        # Reproduce the retained capture's two differences together, with no
        # real answer/source bodies and no evaluator judgment manufactured.
        parts = json.loads(self.stream[6:])['content']['parts']
        stream = ''.join('id: ' + str(n) + '\nevent: transcript\ndata: ' + json.dumps(event) + '\n\n'
                         for n, event in enumerate([
                             dict(Content=None, Author='bot_runtime', Actions=dict(StateDelta={})),
                             dict(Content=dict(role='model', parts=[parts[0]]), Author='chat'),
                             dict(Content=dict(role='user', parts=[parts[1]]), Author='web_fetch'),
                             dict(persisted=True)], 1))
        self.path.unlink()
        for sample in self.actual['samples']:
            key = sample['sample']
            for label in ('before', 'after'):
                history = json.loads(Path(self.tmp.name, 'agent-' + key + '-history-' + label + '.json').read_text())
                history['messages'][0]['content'] = self.answer
                self.put(sample, 'history-' + label + '.json', history)
            self.put(sample, 'worker-events.txt', stream, raw=True)
            _, _, answer, sources = stock_capture(self.state, self.actual, key)
            self.assertEqual(answer, self.answer)
            self.assertEqual(sources['fetch-1']['bodies']['content'], self.payload['content'])
        with self.assertRaisesRegex(RuntimeError, 'missing private'):
            self.check()
        results = agent_evidence_results(self.state, self.actual)
        for key in (*STOCKS, 'source-grounding'):
            self.assertEqual(results[key]['status'], 'unverified')

    def test_stock_publication_mutations_and_restore_drift_rejected(self):
        from agent_grounding_contract import stock_capture
        sample = self.actual['samples'][0]
        history = json.loads(Path(self.tmp.name, 'agent-stock-1-history-before.json').read_text())
        for content in (' ' + self.answer, self.answer + ' ', self.answer + '\n\n',
                        self.answer + '\r\n', self.answer[:-1], 'Different answer'):
            with self.subTest(content=content):
                history['messages'][0]['content'] = content
                for label in ('before', 'after'):
                    self.put(sample, 'history-' + label + '.json', history)
                with self.assertRaisesRegex(RuntimeError, 'full published answer'):
                    stock_capture(self.state, self.actual, 'stock-1')
        for label, suffix in (('before', ''), ('after', '\n')):
            history['messages'][0]['content'] = self.answer + suffix
            self.put(sample, 'history-' + label + '.json', history)
        with self.assertRaisesRegex(RuntimeError, 'changed restored history'):
            stock_capture(self.state, self.actual, 'stock-1')

    def test_complete_synthetic_three_sample_assessment(self):
        self.check()

    def test_missing_task_adequacy_rejected_for_each_sample(self):
        original = copy.deepcopy(self.doc)
        for key in STOCKS:
            with self.subTest(sample=key):
                self.doc = copy.deepcopy(original)
                row = next(row for row in self.doc['samples'] if row['sample'] == key)
                row.pop('task_adequacy', None)
                self.save()
                with self.assertRaisesRegex(RuntimeError, 'missing task-adequacy judgment'):
                    self.check()

    def test_supported_peripheral_facts_do_not_override_negative_task_adequacy(self):
        # Every claim remains supported: a negative task-level judgment still fails.
        for row in self.doc['samples']:
            row['task_adequacy'] = dict(task='Stock Market News', useful_full_result=False,
                                       rationale='Only a peripheral index fact, not a useful market-news result.',
                                       limitations='Missing the market-news context needed to answer the title task.')
        self.save()
        with self.assertRaisesRegex(RuntimeError, 'stock-1: not a useful full result'):
            self.check()

    def test_task_adequacy_mutations_rejected(self):
        mutations = {
            'missing-verdict': lambda a: a.pop('useful_full_result'),
            'false': lambda a: a.update(useful_full_result=False),
            'string-true': lambda a: a.update(useful_full_result='true'),
            'integer-true': lambda a: a.update(useful_full_result=1),
            'null': lambda a: a.update(useful_full_result=None),
            'different-task': lambda a: a.update(task='Index fact'),
            'blank-rationale': lambda a: a.update(rationale=' '),
            'missing-rationale': lambda a: a.pop('rationale'),
            'blank-limitations': lambda a: a.update(limitations=' '),
            'missing-limitations': lambda a: a.pop('limitations'),
            'unknown-field': lambda a: a.update(passed=True),
        }
        original = copy.deepcopy(self.doc)
        for label, change in mutations.items():
            with self.subTest(label=label):
                self.doc = copy.deepcopy(original)
                change(self.doc['samples'][0]['task_adequacy']); self.save()
                with self.assertRaises(RuntimeError):
                    self.check()

    def test_negative_adequacy_blocks_stock_and_grounding_gates(self):
        self.doc['samples'][1]['task_adequacy']['useful_full_result'] = False
        self.save()
        with patch('agent_adapter_contract.adapter_provenance', return_value=None):
            results = agent_evidence_results(self.state, self.actual)
        self.assertEqual(results['stock-1']['status'], 'passed')
        self.assertEqual(results['stock-2']['status'], 'unverified')
        self.assertEqual(results['stock-3']['status'], 'passed')
        self.assertEqual(results['source-grounding']['status'], 'unverified')

    def test_v1_assessments_do_not_satisfy_v2_contract(self):
        self.doc['contract'] = 'bot-agent-grounding-v1'; self.save()
        with self.assertRaisesRegex(RuntimeError, 'unknown assessment contract'):
            self.check()

    def test_binding_and_coverage_mutations_rejected(self):
        mutations = {
            'candidate': lambda d: d.update(candidate='e'*64),
            'nonce': lambda d: d.update(nonce='e'*64),
            'browser': lambda d: d.update(browser_candidate='e'*64),
            'run': lambda d: d['samples'][0].update(run_id=str(uuid4())),
            'session': lambda d: d['samples'][0].update(session_id=str(uuid4())),
            'answer-digest': lambda d: d['samples'][0].update(answer_sha256='f'*64),
            'source-digest': lambda d: d['samples'][0]['sources'].update({'agent-stock-1-worker-events.txt': 'e'*64}),
            'empty-claims': lambda d: d['samples'][0].update(claims=[]),
            'missing-sample': lambda d: d['samples'].pop(),
            'duplicate-sample': lambda d: d['samples'].__setitem__(1, copy.deepcopy(d['samples'][0])),
            'blank-evaluator': lambda d: d['evaluator'].update(identity=' '),
            'pre-answer': lambda d: d.update(inspection_started_at='2025-01-01T00:00:00Z'),
            'future': lambda d: d.update(assessed_at='2099-01-01T00:00:00Z'),
            'blanket-pass': lambda d: d.update(status='passed'),
        }
        original = copy.deepcopy(self.doc)
        for label, change in mutations.items():
            with self.subTest(label=label):
                self.doc = copy.deepcopy(original); change(self.doc); self.save()
                with self.assertRaises((RuntimeError, ValueError)):
                    self.check()

    def test_claim_mutations_rejected(self):
        mutations = {
            'unsupported': lambda c: c.update(verdict='unsupported'),
            'unverified': lambda c: c.update(verdict='unverified'),
            'summary-only': lambda c: c.update(end=2, quote=self.answer[:2]),
            'incorrect-quote': lambda c: c.update(quote='Index was 999'),
            'no-evidence': lambda c: c.update(evidence=[]),
            'absent-body-quote': lambda c: c['evidence'][0].update(quote='at 999'),
            'wrong-payload': lambda c: c['evidence'][0].update(payload_sha256='e'*64),
            'wrong-url': lambda c: c['evidence'][0].update(url='https://elsewhere.test'),
            'wrong-call': lambda c: c['evidence'][0].update(call_id='fetch-other'),
            'metadata-not-body': lambda c: c['evidence'][0].update(field='url'),
            'stale-judgment': lambda c: c['temporal'].update(verdict='stale'),
            'old-current': lambda c: c['temporal'].update(as_of='2024-01-01'),
            'future-date': lambda c: c['temporal'].update(as_of='2026-01-03'),
            'absent-date': lambda c: c['temporal'].update(date_quote='2024-01-01'),
            'historical-unlabeled': lambda c: c['temporal'].update(kind='historical', as_of='2024-01-01'),
        }
        original = copy.deepcopy(self.doc)
        for label, change in mutations.items():
            with self.subTest(label=label):
                self.doc = copy.deepcopy(original); change(self.doc['samples'][0]['claims'][0]); self.save()
                with self.assertRaises((RuntimeError, ValueError)):
                    self.check()

    def test_private_missing_symlink_and_duplicate_json_rejected(self):
        self.path.chmod(0o644)
        with self.assertRaisesRegex(RuntimeError, '0600'):
            self.check()
        self.path.unlink()
        with self.assertRaisesRegex(RuntimeError, 'missing private'):
            self.check()
        target = Path(self.tmp.name, 'other.json'); target.write_text(json.dumps(self.doc)); target.chmod(0o600)
        self.path.symlink_to(target)
        with self.assertRaisesRegex(RuntimeError, 'missing private'):
            self.check()
        self.path.unlink(); self.save()
        self.path.write_text('{"contract":"one","contract":"two"}')
        with self.assertRaises((RuntimeError, ValueError)):
            self.check()

    def test_changed_retained_bytes_rejected_even_when_assessment_unchanged(self):
        Path(self.tmp.name, 'agent-stock-1-worker-events.txt').write_text(self.stream+'tampered')
        with self.assertRaisesRegex(RuntimeError, 'altered evidence digest'):
            self.check()

    def test_search_snippets_failed_empty_unpaired_and_unpersisted_sources_rejected(self):
        for stream in (self.stream.replace('web_fetch', 'web_search'),
                       self.stream.replace('"status": 200', '"status": 403'),
                       self.stream.replace('"persisted": true', '"persisted": false'),
                       self.stream.replace(self.payload['content'], ''),
                       self.stream.replace('"functionCall"', '"notCall"')):
            with self.subTest(stream=stream), self.assertRaises(RuntimeError):
                fetched_sources(stream)

    def test_nonclaims_can_only_explain_uncovered_text_not_replace_claim_inventory(self):
        for row in self.doc['samples']:
            row['claims'][0].update(start=3, quote=self.answer[3:])
            row['nonclaims'] = [dict(start=0, end=2, quote='At', reason='Grammatical introduction, not an independent assertion.')]
        self.save(); self.check()
        self.doc['samples'][0]['claims'] = []; self.save()
        with self.assertRaisesRegex(RuntimeError, 'no claim inventory'):
            self.check()

    def test_gate_integration_requires_grounding_and_adapter_not_producer_flags(self):
        with patch('agent_adapter_contract.adapter_provenance', return_value=None):
            results = agent_evidence_results(self.state, self.actual)
        for key in (*STOCKS, 'source-grounding'):
            self.assertEqual(results[key]['status'], 'passed')
        results = agent_evidence_results(self.state, self.actual)
        self.assertEqual(results['source-grounding']['status'], 'passed')
        self.assertTrue(all(results[k]['status'] == 'unverified' for k in STOCKS))
        self.doc['samples'][1]['claims'][0]['verdict'] = 'unsupported'; self.save()
        with patch('agent_adapter_contract.adapter_provenance', return_value=None):
            results = agent_evidence_results(self.state, self.actual)
        self.assertEqual(results['stock-2']['status'], 'unverified')
        self.assertEqual(results['source-grounding']['status'], 'unverified')


if __name__ == '__main__':
    unittest.main()
