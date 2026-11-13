"""Synthetic records exercise validators, never claim a live acceptance pass."""
import unittest
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from uuid import uuid4
from agent_live_contract import (AGENT_CONTRACT, AGENT_REQUIRED, LEARNING_CONTRACT,
                                 agent_evidence_results)
from skills_live_contract import SKILLS_REQUIRED


class AgentSemanticRecordsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.state = dict(root=self.tmp.name, binary_sha256='a'*64, browser_sha256='b'*64,
                          agent_nonce='c'*64, url='http://127.0.0.1:12345')
        self.evidence = dict(contract=AGENT_CONTRACT, candidate='a'*64, browser_candidate='b'*64,
                             nonce='c'*64, general_sources={}, journeys={})
        self.bot, self.home, self.volume = (str(uuid4()) for _ in range(3))
        self.rows = []
        self.put('general-bot.json', dict(id=self.bot, home_session_id=self.home))
        self.put('general-model.json', dict(effectiveProvider='New AI Core Provider',
                                         effectiveModel='anthropic--claude-4.6-sonnet', credentialsAvailable=True))

    def put(self, name, value, text=False):
        raw = value.encode() if text else json.dumps(value).encode()
        name = 'agent-' + name
        Path(self.tmp.name, name).write_bytes(raw)
        self.evidence['general_sources'][name] = hashlib.sha256(raw).hexdigest()
        self.manifest()

    def manifest(self):
        doc = dict(contract=AGENT_CONTRACT, nonce=self.state['agent_nonce'], candidate=self.state['binary_sha256'],
                   browser_candidate=self.state['browser_sha256'], sources=self.evidence['general_sources'], journeys=self.evidence['journeys'])
        raw = json.dumps(doc).encode()
        Path(self.tmp.name, 'agent-general-manifest.json').write_bytes(raw)
        self.evidence['general_manifest'] = dict(Source='agent-general-manifest.json', SourceHash=hashlib.sha256(raw).hexdigest())

    def record_run(self, index=1):
        rid = str(uuid4())
        when = f'2026-01-01T00:{index:02d}:00Z'
        return dict(id=rid, bot_id=self.bot, session_id=str(uuid4()), status='succeeded',
                    created_at=when, started_at=when, finished_at=when,
                    provider='New AI Core Provider', model='anthropic--claude-4.6-sonnet',
                    trigger='manual', idempotency_key=str(uuid4()),
                    work=dict(schema_version=1, protocol='agent_led_v1', bot_id=self.bot, run_id=rid,
                              work_revision=1, phase='finished', disposition='answered', candidate_digest='d'*64),
                    report=dict(summary='Saved result', artifacts=[], employee_message=dict(origin='agent', text='Saved result')))

    def journey(self, key, path, body):
        run = self.record_run()
        run['report']['artifacts'] = [dict(id=str(uuid4()), home_id=self.volume, relative_path=path,
                                         sha256=hashlib.sha256(body.encode()).hexdigest())]
        self.rows.append(run)
        self.evidence['journeys'][key] = dict(run=run, status='result-observed')
        self.put('general-runs.json', dict(runs=self.rows))
        history = dict(id=self.home, messages=[dict(type='agent', invocationId='bot-report:' + run['id'], content='Saved result\n')])
        for label in ('published', 'restored'):
            self.put(key + '-' + label + '-history.json', history)
        return run

    def document(self):
        before = '# Operations brief\n\nRecord: ' + self.state['agent_nonce'] + '\n\nReview window: 14 days.\n\nBudget: unchanged.\n'
        after = before.replace('14 days', '21 days')
        path = 'reports/agent-' + self.state['agent_nonce'][:12] + '-brief.md'
        self.journey('document-seed', path, before)
        self.journey('document-revision', path, after)
        self.put('document-before-artifact.txt', before, True)
        self.put('document-after-artifact.txt', after, True)

    def comparison(self):
        stem = 'agent-' + self.state['agent_nonce'][:12]
        n = ord('c') + 100
        sources = [dict(id=stem+'-'+label, as_of=date, units=n+delta) for label, date, delta in (
            ('archive', '2024-01-10T00:00:00Z', 0), ('current', '2025-02-20T00:00:00Z', 17), ('interim', '2024-12-01T00:00:00Z', 3))]
        answer = dict(selected_source=sources[1]['id'], as_of=sources[1]['as_of'], units=n+17, conflict=True,
                      stale_sources=[sources[0]['id'], sources[2]['id']], citations=[s['id'] for s in sources])
        raw, output = json.dumps(sources), json.dumps(answer)
        self.journey('comparison-seed', 'reports/'+stem+'-sources.json', raw)
        self.journey('conflicting-source-comparison', 'reports/'+stem+'-comparison.json', output)
        self.put('comparison-sources-artifact.txt', raw, True)
        self.put('comparison-sources-after-artifact.txt', raw, True)
        self.put('comparison-answer-artifact.txt', output, True)

    def test_general_publication_exact_or_one_renderer_newline_only(self):
        from agent_live_contract import general
        self.document()
        key = 'document-revision'
        run = self.evidence['journeys'][key]['run']
        for content, accepted in (('Saved result', True), ('Saved result\n', True),
                                  (' Saved result', False), ('Saved result ', False),
                                  ('Saved result\n\n', False), ('Saved result\r\n', False),
                                  ('Saved resul', False), ('Changed result', False)):
            with self.subTest(content=content):
                history = dict(id=self.home, messages=[dict(type='agent', invocationId='bot-report:' + run['id'], content=content)])
                for label in ('published', 'restored'):
                    self.put(key + '-' + label + '-history.json', history)
                if accepted:
                    general(self.state, self.evidence, key)
                else:
                    with self.assertRaisesRegex(RuntimeError, 'canonical publication'):
                        general(self.state, self.evidence, key)

    def test_publication_comparator_preserves_answer_whitespace(self):
        from agent_live_contract import published_answer_matches
        for answer in ('  Meaningful spacing  ', '# Heading\n\nBody\n', 'Result\n\n'):
            with self.subTest(answer=answer):
                self.assertTrue(published_answer_matches(answer, answer))
                self.assertTrue(published_answer_matches(answer + '\n', answer))
                self.assertFalse(published_answer_matches(answer.strip(), answer))
                self.assertFalse(published_answer_matches(answer + '\n\n', answer))
        self.assertFalse(published_answer_matches(None, 'Result'))

    def test_general_publication_restore_drift_still_rejected(self):
        from agent_live_contract import general
        self.document()
        key = 'document-revision'
        run = self.evidence['journeys'][key]['run']
        history = dict(id=self.home, messages=[dict(type='agent', invocationId='bot-report:' + run['id'], content='Saved result')])
        self.put(key + '-published-history.json', history)
        with self.assertRaisesRegex(RuntimeError, 'changed restored publication'):
            general(self.state, self.evidence, key)

    def test_persisted_content_decoder_rejects_ambiguous_and_malformed_shapes(self):
        from agent_live_contract import persisted_content_parts
        for event in (dict(Content={}, content={}), dict(CONTENT={}), dict(Content=[]),
                      dict(Content=False), dict(Content=dict(parts=None)),
                      dict(Content=dict(parts=[None])), []):
            with self.subTest(event=event), self.assertRaises(RuntimeError):
                persisted_content_parts(event)

    def test_document_and_comparison_records_pass_only_their_gates(self):
        self.document(); self.comparison()
        results = agent_evidence_results(self.state, self.evidence)
        self.assertEqual([g for g, r in results.items() if r['status'] == 'passed'],
                         ['document-revision', 'conflicting-source-comparison'])
        self.assertEqual(results['adapter-provenance']['status'], 'unverified')
        self.assertIn('adapter_manifest', results['adapter-provenance']['reason'])
        self.assertIn('missing private agent-grounding-assessment.json', results['source-grounding']['reason'])
        self.assertIn('missing terminal_manifest', results['studio-terminal-parity']['reason'])

    def test_forged_flags_do_not_clear_any_gate(self):
        self.evidence['journeys'] = {g+'-outcome': {'status': 'verified'} for g in AGENT_REQUIRED}
        self.evidence['receipts'] = {g: {'status': 'passed'} for g in AGENT_REQUIRED}
        self.assertTrue(all(r['status'] == 'unverified' for r in agent_evidence_results(self.state, self.evidence).values()))

    def test_diagnostic_factory_observation_cannot_certify_native_adapter(self):
        # Even genuine observations from the isolated HTTP diagnostic belong to
        # that process, not the source-built native candidate's three workers.
        observation = dict(provider='New AI Core Provider', provider_type='sap_ai_core',
                           model='anthropic--claude-4.6-sonnet', adapter='*sap.Provider',
                           adapter_model='anthropic--claude-4.6-sonnet',
                           observation_boundary='diagnostic-worker-factory-return', wire_verified=False)
        self.evidence['adapter_observation'] = observation
        self.evidence['samples'] = [dict(sample='stock-' + str(n), provenance=observation,
                                         adapter_verified=True) for n in range(1, 4)]
        self.put('diagnostic-worker-model-identity.json', observation)
        results = agent_evidence_results(self.state, self.evidence)
        self.assertEqual(results['adapter-provenance']['status'], 'unverified')
        self.assertIn('unsupported proof interface', results['adapter-provenance']['reason'])
        for n in range(1, 4):
            self.assertEqual(results['stock-' + str(n)]['status'], 'unverified')

    def test_altered_missing_symlink_and_traversal_files_fail(self):
        self.document()
        path = Path(self.tmp.name, 'agent-document-after-artifact.txt')
        original = path.read_bytes()
        path.write_bytes(original + b'forged')
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['document-revision']['status'], 'passed')
        path.unlink()
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['document-revision']['status'], 'passed')
        target = Path(self.tmp.name, 'elsewhere'); target.write_bytes(original); path.symlink_to(target)
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['document-revision']['status'], 'passed')
        from agent_live_contract import source
        with self.assertRaises(RuntimeError):
            source(self.state, '../elsewhere', hashlib.sha256(original).hexdigest())

    def test_rehashed_wrong_edit_and_foreign_run_fail(self):
        self.document()
        self.put('document-after-artifact.txt', 'invented document', True)
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['document-revision']['status'], 'passed')
        self.document()
        self.evidence['journeys']['document-revision']['run']['bot_id'] = str(uuid4())
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['document-revision']['status'], 'passed')

    def test_rehashed_wrong_comparison_and_duplicate_json_fail(self):
        self.comparison()
        path = Path(self.tmp.name, 'agent-comparison-answer-artifact.txt')
        answer = json.loads(path.read_text()); answer['units'] = 0
        self.put('comparison-answer-artifact.txt', json.dumps(answer), True)
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['conflicting-source-comparison']['status'], 'passed')
        self.put('comparison-answer-artifact.txt', '{"units": 216, "units": 0}', True)
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['conflicting-source-comparison']['status'], 'passed')

    def test_offline_evaluator_entry_point(self):
        self.document()
        completed = subprocess.run([sys.executable, '-B', str(Path(__file__).with_name('agent_live_contract.py'))],
                                   input=json.dumps(dict(state=self.state, evidence=self.evidence)), text=True, capture_output=True, check=True)
        self.assertEqual(json.loads(completed.stdout)['document-revision']['status'], 'passed')

    def build_learning(self):
        self.state['agent_learning_bot_id'] = self.bot
        skill = str(uuid4())
        baseline = dict(skill_id=skill, bot_id=self.bot, revision=1, digest='1'*64, expectations=[])
        runs = [self.record_run(n) for n in (1, 2, 3, 5, 6, 7, 9)]
        runs[2]['user_event_id'] = 'owner-feedback-event'
        updated = dict(baseline, revision=2, digest='2'*64, expectations=[dict(id='humidity', text='Include humidity', provenance=dict(level='owner_specified', run_id=runs[2]['id'], event_id='owner-feedback-event'))])
        trigger = str(uuid4())
        works = []
        for i, run in enumerate(runs):
            revision = baseline if i < 3 else updated
            receipt = dict(schema_version=1, bot_id=self.bot, run_id=run['id'], skill_id=skill,
                           used_revision=revision['revision'], digest=revision['digest'], kind='selected')
            run['report']['skill'] = receipt
            work = dict(protocol='agent_led_v1', phase='finished', revision=1, candidate=dict(digest='d'*64),
                        agent_work=dict(disposition='answered'), skill_state=dict(receipt=receipt),
                        contract=dict(bot_id=self.bot, run_id=run['id'], skill=dict(skill_id=skill,
                                      revision=revision['revision'], digest=revision['digest'], snapshot=revision)))
            works.append(work)
            if 3 <= i <= 5:
                run.update(trigger='cron', trigger_id=trigger)
        runs[2]['report']['skill'].update(kind='updated', committed_revision=2, digest=updated['digest'])
        works[2]['skill_state']['stage'] = 'learning_committed'
        def disk(indices):
            return dict(Jobs=[], Runs=[dict(id=runs[i]['id'], bot_id=self.bot, data=json.dumps(dict(run=runs[i], work=works[i]))) for i in indices])
        ids = [r['id'] for r in runs[3:6]]
        observations = {
            'assignment-without-schedule': dict(head=baseline, work=works[0], disk=disk([0])),
            'reuse-pinned-skill': dict(run=runs[1], work=works[1]),
            'owner-feedback-revision': dict(before=baseline, after=updated, old_readback=baseline, source_run=runs[2]),
            'owned-restart-three-scheduled': dict(restart='2026-01-01T00:04:00Z', trigger=trigger, disk=disk(range(6))),
            'manual-and-scheduled-outcomes': [works[i] for i in (1, 3, 4, 5)],
            'schedule-removal-manual': dict(run=runs[6], work=works[6], head=updated, disk=disk(range(7))),
            'skill-inspection-readonly': dict(head=updated, history=dict(revisions=[baseline, updated]), runs=dict(runs=runs)),
        }
        identities = {'owned-restart-three-scheduled': dict(RunIDs=ids),
                      'manual-and-scheduled-outcomes': dict(ManualRunID=runs[1]['id'], RunIDs=ids),
                      'schedule-removal-manual': dict(ManualRunID=runs[6]['id'])}
        binding = dict(contract=LEARNING_CONTRACT, nonce=self.state['agent_nonce'], candidate=self.state['binary_sha256'], browser_candidate=self.state['browser_sha256'])
        def save(name, doc):
            raw = json.dumps(doc).encode(); Path(self.tmp.name, name).write_bytes(raw)
            return hashlib.sha256(raw).hexdigest()
        stream = '\n\n'.join('data: '+json.dumps(event) for event in [
            dict(content=dict(parts=[dict(functionCall=dict(id='fetch', name='web_fetch'))])),
            dict(content=dict(parts=[dict(functionResponse=dict(id='fetch', name='web_fetch', response=dict(url='https://example.test/weather', content='weather evidence', status=200)))])),
            dict(persisted=True)])
        captures = {r['id']: save('agent-skills-run-'+r['id']+'.json', dict(binding, run=r, stream=stream, captured_at='2026-01-01T00:10:00Z')) for r in runs}
        receipts = {}
        for gate, obs in observations.items():
            name = 'agent-skills-'+gate+'.json'
            doc = dict(binding, gate=gate, bot_id=self.bot, observation=obs, invocation_started_at='2026-01-01T00:00:00Z', captured_at='2026-01-01T00:11:00Z', run_captures=captures, identities=identities.get(gate, {}))
            receipts[gate] = dict(SchemaVersion=1, Candidate=binding['candidate'], BrowserCandidate=binding['browser_candidate'], Nonce=binding['nonce'], BotID=self.bot, Home=self.state['url']+'/#/chat/bots/personal/'+self.bot, Status='passed', Source=name, SourceHash=save(name, doc), **identities.get(gate, {}))
        self.evidence['learning'] = dict(contract=LEARNING_CONTRACT, nonce=binding['nonce'], status='verified', passed=list(SKILLS_REQUIRED), receipts=receipts)
        return runs

    def test_learning_actual_records_pass(self):
        self.build_learning()
        results = agent_evidence_results(self.state, self.evidence)
        for gate in ('feedback-restart', 'skill-three-scheduler-occurrences', 'schedule-delete-manual-reuse'):
            self.assertEqual(results[gate], {'status': 'passed'})

    def test_complete_implemented_proofs_do_not_clear_unsupported_gates(self):
        from agent_live_contract import validate_agent_evidence
        self.document(); self.comparison(); self.build_learning()
        results = agent_evidence_results(self.state, self.evidence)
        self.assertEqual(sum(r['status'] == 'passed' for r in results.values()), 5)
        self.evidence.update(status='verified', passed=list(AGENT_REQUIRED),
                             receipts={g: {'status': 'passed'} for g in AGENT_REQUIRED})
        with self.assertRaisesRegex(RuntimeError, 'unsupported proof interface'):
            validate_agent_evidence(self.state, self.evidence)
        for gate in ('adapter-provenance', 'source-grounding', 'studio-terminal-parity'):
            self.assertEqual(agent_evidence_results(self.state, self.evidence)[gate]['status'], 'unverified')

    def test_unregistered_gate_cannot_inherit_learning_success(self):
        from unittest.mock import patch
        self.build_learning()
        with patch('agent_live_contract.AGENT_REQUIRED', AGENT_REQUIRED + ('future-proof',)):
            result = agent_evidence_results(self.state, self.evidence)['future-proof']
        self.assertEqual(result['status'], 'unverified')
        self.assertIn('unknown semantic gate', result['reason'])

    def test_rehashed_learning_forgeries_fail(self):
        mutations = [
            ('owner-feedback-revision', lambda d: d['observation']['after'].update(expectations=[])),
            ('owned-restart-three-scheduled', lambda d: d['observation'].update(restart='2027-01-01T00:00:00Z')),
            ('schedule-removal-manual', lambda d: d['observation']['disk'].update(Jobs=[{'id': 'still-active'}])),
            ('reuse-pinned-skill', lambda d: d['observation']['work']['contract']['skill'].update(digest='f'*64)),
            ('assignment-without-schedule', lambda d: d.update(nonce='d'*64)),
        ]
        for gate, mutate in mutations:
            with self.subTest(gate=gate):
                self.build_learning()
                receipt = self.evidence['learning']['receipts'][gate]
                path = Path(self.tmp.name, receipt['Source'])
                doc = json.loads(path.read_text()); mutate(doc)
                raw = json.dumps(doc).encode(); path.write_bytes(raw)
                receipt['SourceHash'] = hashlib.sha256(raw).hexdigest()
                self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['feedback-restart']['status'], 'passed')

    def test_current_nonce_cannot_relabel_an_old_manifest(self):
        self.document()
        self.state['agent_nonce'] = 'e'*64
        self.evidence['nonce'] = 'e'*64
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['document-revision']['status'], 'passed')

    def test_duplicate_scheduler_sessions_and_unpaired_stream_fail(self):
        from agent_live_contract import retrieval
        with self.assertRaises(RuntimeError):
            retrieval('data: '+json.dumps(dict(content=dict(parts=[dict(functionResponse=dict(id='unpaired', name='web_fetch', response=dict(content='x'*100)))])))+'\n\ndata: {"persisted":true}')
        self.build_learning()
        receipt = self.evidence['learning']['receipts']['owned-restart-three-scheduled']
        path = Path(self.tmp.name, receipt['Source'])
        doc = json.loads(path.read_text())
        ids = doc['identities']['RunIDs']
        doc['identities']['RunIDs'] = [ids[0], ids[0], ids[2]]
        receipt['RunIDs'] = doc['identities']['RunIDs']
        raw = json.dumps(doc).encode(); path.write_bytes(raw)
        receipt['SourceHash'] = hashlib.sha256(raw).hexdigest()
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['skill-three-scheduler-occurrences']['status'], 'passed')

    def test_missing_capture_and_unknown_learning_gate_fail(self):
        runs = self.build_learning()
        Path(self.tmp.name, 'agent-skills-run-'+runs[0]['id']+'.json').unlink()
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['feedback-restart']['status'], 'passed')
        self.build_learning()
        self.evidence['learning']['receipts']['invented'] = {'status': 'passed'}
        self.assertNotEqual(agent_evidence_results(self.state, self.evidence)['feedback-restart']['status'], 'passed')
