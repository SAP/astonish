import copy
import unittest
from uuid import uuid4
from agent_adapter_contract import validate_adapter_records
from agent_live_contract import stamp


class NativeAdapterRecordsTests(unittest.TestCase):
    def setUp(self):
        self.when = '2026-01-01T00:00:01Z'
        self.run = dict(id=str(uuid4()), bot_id=str(uuid4()), session_id=str(uuid4()),
                        provider='New AI Core Provider', model='anthropic--claude-4.6-sonnet',
                        created_at=self.when, started_at=self.when, finished_at=self.when)
        self.base = dict(observation_boundary='native-worker-adapter', wire_verified=False, at=self.when,
                         pid=123, worker_id='worker', adapter_id=str(uuid4()), bot_id=self.run['bot_id'],
                         run_id=self.run['id'], session_id=self.run['session_id'], owner_id=str(uuid4()), scope='personal',
                         provider=self.run['provider'], model=self.run['model'], adapter_type='*sap.Provider',
                         adapter_name=self.run['model'], phase='resolved', stream=False, request_model='', responses=0)
        self.bot = dict(id=self.run['bot_id'], owner_id=self.base['owner_id'], scope='personal')
        self.run['lease'] = dict(worker_id='worker')
        call = str(uuid4())
        self.records = [self.base, dict(self.base, phase='generation-start', call_id=call, ordinal=1, stream=True),
                        dict(self.base, phase='generation-end', call_id=call, ordinal=1, stream=True, responses=2, outcome='complete')]

    def check(self, records=None):
        validate_adapter_records(self.records if records is None else records, {self.run['id']: self.run},
                                 stamp('2026-01-01T00:00:00Z'), stamp('2026-01-01T00:00:02Z'), {self.bot['id']: self.bot})

    def test_missing_lease_does_not_invent_worker_binding(self):
        self.run.pop('lease')
        self.check()

    def test_missing_retained_bot_is_rejected(self):
        with self.assertRaises(KeyError):
            validate_adapter_records(self.records, {self.run['id']: self.run}, stamp(self.when), stamp(self.when), {})

    def test_exact_actual_adapter_passes(self):
        self.check()

    def test_wrong_adapter_same_alias_and_model_rejected(self):
        for field, value in [('adapter_type','*litellm.Provider'), ('adapter_name','other-model'),
                             ('model','other-model'), ('request_model','other-model'), ('wire_verified',True),
                             ('run_id',str(uuid4())), ('session_id',str(uuid4())), ('scope','team'),
                             ('owner_id',str(uuid4())), ('worker_id','foreign-worker'), ('at','2025-01-01T00:00:00Z'), ('prompt','private payload')]:
            with self.subTest(field=field):
                records = copy.deepcopy(self.records)
                for r in records:
                    r[field] = value
                with self.assertRaises((RuntimeError, KeyError)):
                    self.check(records)

    def test_missing_duplicate_or_changed_calls_rejected(self):
        variants = [self.records[:1], self.records[:2], self.records[1:], self.records + [self.records[2]],
                    [self.records[0], self.records[0], *self.records[1:]],
                    [*self.records[:2], dict(self.records[2], pid=124)],
                    [*self.records[:2], dict(self.records[2], outcome='interrupted')],
                    [self.records[0], dict(self.records[1], ordinal=2), dict(self.records[2], ordinal=2)]]
        for records in variants:
            with self.subTest(records=records), self.assertRaises(RuntimeError):
                self.check(records)

    def test_error_cancel_stop_are_observations_not_success_claims(self):
        for outcome in ('error', 'cancelled', 'consumer-stopped'):
            self.check([*self.records[:2], dict(self.records[2], outcome=outcome)])

    def test_manifest_rejects_changed_candidate_nonce_and_digest(self):
        import hashlib
        import json
        import tempfile
        from pathlib import Path
        from agent_adapter_contract import adapter_provenance
        with tempfile.TemporaryDirectory() as root:
            state = dict(root=root, agent_nonce='a'*64, binary_sha256='b'*64, browser_sha256='c'*64)
            doc = dict(contract='bot-native-adapter-v1', nonce='a'*64, candidate='b'*64,
                       browser_candidate='c'*64, log_path='logs/daemon.log')
            for key in ('nonce', 'candidate', 'browser_candidate'):
                changed = dict(doc, **{key:'d'*64})
                raw = json.dumps(changed).encode()
                Path(root, 'agent-native-adapters.json').write_bytes(raw)
                actual = dict(adapter_manifest=dict(Source='agent-native-adapters.json', SourceHash=hashlib.sha256(raw).hexdigest()))
                with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, 'foreign/replayed'):
                    adapter_provenance(state, actual)
            actual['adapter_manifest']['SourceHash'] = 'e'*64
            with self.assertRaisesRegex(RuntimeError, 'altered evidence digest'):
                adapter_provenance(state, actual)

    def test_full_retained_manifest_and_all_run_families(self):
        import hashlib
        import json
        import tempfile
        from pathlib import Path
        from agent_adapter_contract import adapter_provenance
        with tempfile.TemporaryDirectory() as root:
            state = dict(root=root, agent_nonce='a'*64, binary_sha256='b'*64, browser_sha256='c'*64)
            binding = dict(nonce='a'*64, candidate='b'*64, browser_candidate='c'*64)
            def save(name, value):
                raw = json.dumps(value).encode()
                Path(root, name).write_bytes(raw)
                return dict(Source=name, SourceHash=hashlib.sha256(raw).hexdigest())
            actual = dict(samples=[], learning=dict(receipts={}))
            runs = []
            for n in range(1, 4):
                run = dict(self.run, id=str(uuid4()), session_id=str(uuid4()))
                runs.append(run)
                key = 'stock-'+str(n)
                refs = [save('agent-'+key+'-bot.json', self.bot), save('agent-'+key+'-runs.json', dict(runs=[run])),
                        save('agent-'+key+'-model-before.json', dict(effectiveProvider=run['provider'],
                             effectiveModel=run['model'], credentialsAvailable=True))]
                actual['samples'].append(dict(sample=key, run_id=run['id'], bot_id=run['bot_id'],
                                              sources={r['Source']:r['SourceHash'] for r in refs}))
            general = [dict(self.run, id=str(uuid4()), session_id=str(uuid4())) for _ in range(4)]
            runs.extend(general)
            ref = save('agent-general-runs.json', dict(runs=general))
            botref = save('agent-general-bot.json', self.bot)
            actual['general_manifest'] = save('agent-general-manifest.json', dict(binding,
                sources={ref['Source']:ref['SourceHash'], botref['Source']:botref['SourceHash']}, journeys={str(n):dict(run=r) for n,r in enumerate(general)}))
            captures = {}
            for _ in range(7):
                run = dict(self.run, id=str(uuid4()), session_id=str(uuid4()))
                runs.append(run)
                ref = save('agent-skills-run-'+run['id']+'.json', dict(binding, run=run, bot=self.bot))
                captures[run['id']] = ref['SourceHash']
            actual['learning']['receipts']['fixture'] = save('agent-learning-fixture.json', dict(binding, run_captures=captures))
            records = []
            for run in runs:
                aid, call = str(uuid4()), str(uuid4())
                for r in self.records:
                    row = dict(r, adapter_id=aid, run_id=run['id'], session_id=run['session_id'])
                    if 'call_id' in row:
                        row['call_id'] = call
                    records.append(row)
            doc = dict(binding, contract='bot-native-adapter-v1', log_path='logs/daemon.log', records=records,
                       invocation_started_at='2026-01-01T00:00:00Z', captured_at='2026-01-01T00:00:02Z')
            actual['adapter_manifest'] = save('agent-native-adapters.json', doc)
            adapter_provenance(state, actual)
            records.pop()
            actual['adapter_manifest'] = save('agent-native-adapters.json', doc)
            with self.assertRaisesRegex(RuntimeError, 'missing adapter call end'):
                adapter_provenance(state, actual)

    def test_every_required_run_must_be_observed(self):
        run = dict(self.run, id=str(uuid4()))
        with self.assertRaises(RuntimeError):
            validate_adapter_records(self.records, {self.run['id']:self.run, run['id']:run},
                                     stamp(self.when), stamp(self.when), {self.bot['id']: self.bot})
