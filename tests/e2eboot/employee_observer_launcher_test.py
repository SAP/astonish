import json
import sqlite3
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest.mock import patch
import local_bot_smoke_launcher as launcher


class EmployeeObserverTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)
        self.root = self.repo / '.tmp/astonish-chat-first-test'
        (self.root / 'data').mkdir(parents=True)
        self.path = self.root / 'data/personal.db'
        self.bot = str(uuid.uuid4())
        self.home = str(uuid.uuid4())
        self.state = dict(root=str(self.root), bot_id=self.bot)
        self.patch = patch.object(launcher, 'REPO', self.repo)
        self.patch.start(); self.addCleanup(self.patch.stop)
        with sqlite3.connect(self.path) as db:
            db.executescript('CREATE TABLE bots(id TEXT, home_session_id TEXT); CREATE TABLE session_events(id INTEGER,session_id TEXT,event_data TEXT); CREATE TABLE bot_runs(id TEXT,bot_id TEXT,data TEXT); CREATE TABLE scheduled_jobs(id TEXT,mode TEXT,payload TEXT);')
            db.execute('INSERT INTO bots VALUES (?,?)', (self.bot, self.home))
            db.execute('INSERT INTO bots VALUES (?,?)', ('peer', 'peer-home'))
            for status in ('pending', 'applied'):
                event = dict(actions=dict(stateDelta=dict(bot_operation=dict(status=status))))
                db.execute('INSERT INTO session_events VALUES (1,?,?)', (self.home, json.dumps(event)))
            db.execute('INSERT INTO bot_runs VALUES (?,?,?)', ('mine', self.bot, '{}'))
            db.execute('INSERT INTO bot_runs VALUES (?,?,?)', ('peer-run', 'peer', '{}'))
            for target in (self.bot, 'peer'):
                db.execute('INSERT INTO scheduled_jobs VALUES (?,?,?)', (target, 'bot_trigger', json.dumps(dict(bot_id=target))))

    def test_owned_execution_barrier_projection(self):
        with sqlite3.connect(self.path) as db:
            for table in ('bots', 'bot_runs'):
                db.execute(f'ALTER TABLE {table} ADD COLUMN lease_expires_at TEXT')
                db.execute(f'ALTER TABLE {table} ADD COLUMN worker_id TEXT')
            db.execute('ALTER TABLE bots ADD COLUMN active_run_id TEXT')
            db.execute("UPDATE bots SET active_run_id='peer-secret', lease_expires_at='peer-secret' WHERE id='peer'")
        for active, bot_lease, run_lease in ((None, None, None), ('mine', None, None), (None, '2020-01-01', '2020-01-01')):
            with self.subTest(active=active, bot_lease=bot_lease, run_lease=run_lease):
                with sqlite3.connect(self.path) as db:
                    db.execute('UPDATE bots SET active_run_id=?, lease_expires_at=? WHERE id=?', (active, bot_lease, self.bot))
                    db.execute("UPDATE bot_runs SET lease_expires_at=? WHERE id='mine'", (run_lease,))
                before = self.path.read_bytes()
                data = launcher.employee_readonly_local(self.state)
                self.assertEqual(data['bots'][0]['active_run_id'], active)
                self.assertEqual(data['bots'][0]['lease_expires_at'], bot_lease)
                self.assertEqual(data['runs'][0]['lease_expires_at'], run_lease)
                self.assertNotIn('peer-secret', json.dumps(data))
                self.assertNotIn('worker_id', data['bots'][0])
                self.assertEqual(self.path.read_bytes(), before)

    def test_persisted_home_projection_personal_and_team(self):
        import base64
        for scope in ('personal', 'team'):
            for blob in (False, True):
                with self.subTest(scope=scope, blob=blob):
                    binding = json.dumps({'bot_document': json.dumps({
                        'bot_id': self.bot, 'scope': scope, 'storage_key': json.dumps({
                            'backend': 'docker', 'ref': 'owned-home',
                            'scope': {'bot_id': self.bot, 'user_id': self.home} if scope == 'personal'
                                     else {'bot_id': self.bot, 'org_slug': 'test-org', 'team_slug': 'test-team'}})})})
                    value = binding.encode() if blob else binding
                    with sqlite3.connect(self.path) as db:
                        if 'home_binding' not in {r[1] for r in db.execute('PRAGMA table_info(bots)')}:
                            db.execute('ALTER TABLE bots ADD COLUMN home_binding JSON')
                            db.execute('ALTER TABLE bots ADD COLUMN credentials BLOB')
                            db.execute('ALTER TABLE bots ADD COLUMN embedding BLOB')
                            db.execute('ALTER TABLE bot_runs ADD COLUMN home_binding JSON')
                        db.execute('UPDATE bots SET home_binding=?, credentials=?, embedding=? WHERE id=?',
                                   (value, b'SECRET', b'VECTOR', self.bot))
                        db.execute('UPDATE bots SET home_binding=? WHERE id=?', ('PEER-HOME-SECRET', 'peer'))
                        db.execute('UPDATE bot_runs SET home_binding=?', ('WRONG-TABLE-SECRET',))
                    scoped = self.root / 'data' / scope / 'scope.db'
                    scoped.parent.mkdir(exist_ok=True)
                    scoped.write_bytes(self.path.read_bytes())
                    state = dict(self.state, employee_database=str(scoped))
                    before = scoped.read_bytes()
                    data = launcher.employee_readonly_local(state)
                    self.assertEqual(data['bots'][0]['home_binding'], binding)
                    self.assertEqual([r['id'] for r in data['bots']], [self.bot])
                    self.assertNotIn('SECRET', json.dumps(data))
                    self.assertNotIn('embedding', data['bots'][0])
                    self.assertEqual(before, scoped.read_bytes())
                    if blob:
                        evidence = next(b for b in data['blob_evidence'] if b['column'] == 'home_binding')
                        self.assertEqual(base64.b64decode(evidence['value']['base64']), value)

    def test_persisted_run_envelopes_and_peer_sessions(self):
        # Both Ent scopes persist the same indexed session columns and Run wrapper.
        for scope in ('personal', 'team'):
            for wrapper in ('flat', 'run', 'Run'):
                for blob in (False, True):
                    with self.subTest(scope=scope, wrapper=wrapper, blob=blob):
                        run = dict(id='mine', bot_id=self.bot, session_id='execution', status='queued', history_cursor=9007199254740993)
                        doc = run if wrapper == 'flat' else {wrapper: run, 'ExternalAdmissionValidated': False, 'OccurrenceAt': '2026-11-09T02:27:00Z'}
                        raw = json.dumps({'bot_document': json.dumps(doc)})
                        with sqlite3.connect(self.path) as db:
                            if 'session_id' not in {r[1] for r in db.execute('PRAGMA table_info(bot_runs)')}:
                                db.execute('ALTER TABLE bot_runs ADD COLUMN session_id TEXT')
                            db.execute('CREATE TABLE IF NOT EXISTS sessions(id TEXT, parent_id TEXT, bot_id TEXT, bot_run_id TEXT)')
                            db.execute('DELETE FROM sessions')
                            for sid, parent, owner, rid in ((self.home, None, self.bot, None), ('execution', None, self.bot, 'mine'), ('child', 'execution', None, None), ('peer-home', self.home, 'peer', None), ('peer-execution', 'execution', 'peer', 'peer-run')):
                                db.execute('INSERT INTO sessions VALUES (?,?,?,?)', (sid, parent, owner, rid))
                            db.execute('UPDATE bot_runs SET data=?, session_id=? WHERE id=?', (raw.encode() if blob else raw, 'execution', 'mine'))
                            db.execute('UPDATE bot_runs SET session_id=? WHERE id=?', ('peer-execution', 'peer-run'))
                            db.execute('DELETE FROM session_events WHERE id=2')
                            for sid in ('execution', 'child', 'peer-home', 'peer-execution'):
                                event = {'Actions': {'StateDelta': {'bot_operation': {'status': 'applied', 'bot_id': self.bot if sid in ('execution', 'child') else 'peer'}}}}
                                db.execute('INSERT INTO session_events VALUES (2,?,?)', (sid, json.dumps(event)))
                        scoped = self.root / 'data' / scope / 'scope.db'
                        scoped.parent.mkdir(exist_ok=True)
                        scoped.write_bytes(self.path.read_bytes())
                        before = scoped.read_bytes()
                        data = launcher.employee_readonly_local(dict(self.state, employee_database=str(scoped)))
                        self.assertTrue(data['observation_complete'], data['observation_errors'])
                        self.assertEqual(set(data['related_session_ids']), {self.home, 'execution', 'child'})
                        self.assertEqual({r['id'] for r in data['sessions']}, {self.home, 'execution', 'child'})
                        self.assertEqual({r['session_id'] for r in data['events']}, {self.home, 'execution', 'child'})
                        self.assertEqual(len(data['operations']), 4)
                        self.assertEqual(data['runs'][0]['data'], raw)
                        self.assertEqual(scoped.read_bytes(), before)

    def test_ambiguous_run_identity_cannot_discover_sessions(self):
        run = dict(id='mine', bot_id=self.bot, session_id='untrusted')
        cases = [
            {'Run': dict(run, id='other')}, {'Run': dict(run, bot_id='peer')},
            {'Run': None}, {'Run': []}, {'Run': run, 'run': run},
            {'Run': run, 'session_id': 'untrusted'}, {'Run': run, 'version': 99},
            {'Run': dict(run, id=None)}, {'Run': dict(run, bot_id=None)},
            {'Run': {'session_id': 'untrusted'}},
            {'Run': {'Run': run}}, {'bot_document': json.dumps({'Run': run}), 'other': 1},
            {'bot_document': json.dumps({'bot_document': json.dumps({'Run': run})})},
            {'Run': dict(run, session_id=['untrusted'])},
        ]
        raw_cases = [json.dumps(v) for v in cases]
        raw_cases += ['{"Run":' + json.dumps(run) + ',"Run":' + json.dumps(run) + '}',
                      '{"Run":{"id":"mine","id":"other","session_id":"untrusted"}}']
        for raw in raw_cases:
            with self.subTest(raw=raw):
                with sqlite3.connect(self.path) as db:
                    db.execute('UPDATE bot_runs SET data=? WHERE id=?', (raw, 'mine'))
                data = launcher.employee_readonly_local(self.state)
                self.assertFalse(data['observation_complete'])
                self.assertEqual(data['related_session_ids'], [self.home])
                self.assertEqual(data['runs'][0]['data'], raw)

    def test_row_session_disagreement_and_retained_peer_ids_fail_closed(self):
        with sqlite3.connect(self.path) as db:
            db.execute('ALTER TABLE bot_runs ADD COLUMN session_id TEXT')
            db.execute('UPDATE bot_runs SET session_id=?,data=? WHERE id=?', ('row-session', json.dumps({'Run': dict(id='mine', bot_id=self.bot, session_id='peer-home')}), 'mine'))
            db.execute('INSERT INTO session_events VALUES (2,?,?)', ('peer-home', '{}'))
        data = launcher.employee_readonly_local(dict(self.state, employee_related_sessions=['peer-home']))
        self.assertFalse(data['observation_complete'])
        self.assertNotIn('peer-home', data['related_session_ids'])
        self.assertNotIn('row-session', data['related_session_ids'])
        self.assertEqual({r['session_id'] for r in data['events']}, {self.home})

    def test_peer_selection_does_not_reuse_employee_session_cache(self):
        peer = str(uuid.uuid4())
        with sqlite3.connect(self.path) as db:
            db.execute('UPDATE bots SET id=? WHERE id=?', (peer, 'peer'))
            db.execute('INSERT INTO session_events VALUES (2,?,?)', ('peer-home', '{}'))
        state = dict(self.state, bot_id=peer, employee_database=str(self.path),
                     employee_home_session=self.home, employee_related_sessions=[self.home])
        data = launcher.employee_readonly_local(state)
        self.assertTrue(data['observation_complete'])
        self.assertEqual(data['related_session_ids'], ['peer-home'])
        self.assertEqual({r['session_id'] for r in data['events']}, {'peer-home'})

    def test_event_alias_ambiguity_blocks_operation_evidence(self):
        for event in ({'Actions': {}, 'actions': {}},
                      {'Actions': {'StateDelta': {}, 'stateDelta': {}}}):
            with self.subTest(event=event):
                with sqlite3.connect(self.path) as db:
                    db.execute('UPDATE session_events SET event_data=?', (json.dumps(event),))
                data = launcher.employee_readonly_local(self.state)
                self.assertFalse(data['observation_complete'])
                self.assertEqual(data['operations'], [])

    def test_approval_wrappers_remain_scoped_opaque_evidence(self):
        raw = json.dumps({'bot_document': json.dumps({'id': 'approval', 'bot_id': self.bot, 'status': 'denied'})})
        with sqlite3.connect(self.path) as db:
            db.execute('CREATE TABLE bot_approvals(id TEXT,bot_id TEXT,run_id TEXT,status TEXT,data JSON)')
            db.execute('INSERT INTO bot_approvals VALUES (?,?,?,?,?)', ('approval', self.bot, 'mine', 'denied', raw))
            db.execute('INSERT INTO bot_approvals VALUES (?,?,?,?,?)', ('other', 'peer', 'peer-run', 'approved', 'PEER-SECRET'))
        before = self.path.read_bytes()
        data = launcher.employee_readonly_local(self.state)
        self.assertEqual(data['approvals'], [dict(id='approval', bot_id=self.bot, run_id='mine', status='denied', data=raw)])
        self.assertNotIn('PEER-SECRET', json.dumps(data))
        self.assertEqual(before, self.path.read_bytes())

    def test_projection_columns_match_both_ent_sources(self):
        import re
        source = Path(__file__).resolve().parents[2] / 'ent'
        names = {'bots': 'bot', 'bot_runs': 'bot_run', 'bot_goals': 'bot_goal',
                 'bot_versions': 'bot_version', 'bot_approvals': 'bot_approval',
                 'sessions': 'session', 'session_events': 'session_event',
                 'scheduled_jobs': 'scheduled_job'}
        for scope in ('personal', 'team'):
            for table, name in names.items():
                with self.subTest(scope=scope, table=table):
                    schema = (source / scope / 'schema' / (name + '.go')).read_text()
                    columns = set(re.findall(r'field\.\w+\("([^"]+)"', schema))
                    self.assertLessEqual(launcher.EMPLOYEE_EVIDENCE_COLUMNS[table], columns)
        run_source = (source.parent / 'pkg/store/bots.go').read_text().split('type BotRun struct {', 1)[1].split('\n}', 1)[0]
        public = set(re.findall(r'json:"([^",]+)', run_source)) - {'-'}
        self.assertEqual(launcher.EMPLOYEE_RUN_FIELDS, public)

    def test_work_projection_and_private_envelope_remain_distinct(self):
        run = dict(id='mine', bot_id=self.bot, session_id='execution',
                   work={'protocol': 'agent_led_v1', 'status': 'working'})
        # The repository's work is private BotWorkState, not Run.work's public
        # BotWorkProjection. It must never supply the observed session identity.
        private_work = dict(session_id='private-session', working_notes='PRIVATE-NOTES')
        for wrapper in ('run', 'Run'):
            document = {wrapper: run, 'work': private_work,
                        'CallerCeiling': {'session_id': 'private-session'},
                        'MandateRef': {'id': 'private-mandate'}}
            errors = []
            row = dict(id='mine', bot_id=self.bot, data=json.dumps(document))
            self.assertEqual(launcher.employee_run_session(row, errors), 'execution')
            self.assertEqual(errors, [])
            without_session = {key: value for key, value in run.items() if key != 'session_id'}
            row['data'] = json.dumps(dict(document, **{wrapper: without_session}))
            errors = []
            self.assertIsNone(launcher.employee_run_session(row, errors))
            self.assertEqual(errors, [])
            # Other public fields beside Run remain an ambiguous second run.
            for field in ('skill_id', 'invocation', 'session_id'):
                with self.subTest(wrapper=wrapper, field=field):
                    row['data'] = json.dumps(dict(document, **{field: 'conflict'}))
                    errors = []
                    self.assertIsNone(launcher.employee_run_session(row, errors))
                    self.assertTrue(errors)
                    self.assertNotIn('PRIVATE-NOTES', json.dumps(errors))
        self.assertTrue({'caller_ceiling', 'callerceiling', 'mandate_ref',
                         'mandateref', 'credentials', 'working_notes'}.isdisjoint(
                             launcher.EMPLOYEE_RUN_FIELDS))

    def test_run_session_canonical_shapes_exact_identity(self):
        run = dict(id=str(uuid.uuid4()), bot_id=self.bot, session_id=str(uuid.uuid4()), version=9007199254740993)
        for nested in (run, {'run': run}, {'Run': run, 'ExternalAdmissionValidated': False}):
            for document in (nested, {'bot_document': json.dumps(nested)}):
                for indexed in (False, True):
                    with self.subTest(document=document, indexed=indexed):
                        row = dict(id=run['id'], bot_id=self.bot, data=json.dumps(document))
                        if indexed: row['session_id'] = run['session_id']
                        errors = []
                        self.assertEqual(launcher.employee_run_session(row, errors), run['session_id'])
                        self.assertEqual(errors, [])
                        self.assertEqual(run['version'], 9007199254740993)

    def test_scoped_readonly_observation_and_persisted_transition(self):
        before = self.path.read_bytes()
        data = launcher.employee_readonly_local(self.state)
        self.assertEqual([r['id'] for r in data['runs']], ['mine'])
        self.assertEqual([r['id'] for r in data['jobs']], [self.bot])
        self.assertEqual([r['status'] for r in data['operations']], ['pending', 'applied'])
        self.assertEqual(self.path.read_bytes(), before)

    def test_related_sessions_remain_observable_after_bot_and_runs_deleted(self):
        with sqlite3.connect(self.path) as db:
            db.execute('CREATE TABLE sessions(id TEXT,parent_id TEXT)')
            for sid, parent in ((self.home, None), ('run-session', self.home), ('descendant', 'run-session'), ('peer-home', None)):
                db.execute('INSERT INTO sessions VALUES (?,?)', (sid, parent))
            db.execute('UPDATE bot_runs SET data=? WHERE id=?', (json.dumps(dict(id='mine', bot_id=self.bot, session_id='run-session')), 'mine'))
            db.execute('INSERT INTO session_events VALUES (2,?,?)', ('descendant', '{}'))
        before = launcher.employee_readonly_local(self.state)
        self.assertEqual(set(before['related_session_ids']), {self.home, 'run-session', 'descendant'})
        self.state.update(employee_database=str(self.path), employee_home_session=self.home, employee_related_sessions=before['related_session_ids'])
        with sqlite3.connect(self.path) as db:
            db.execute('DELETE FROM bots WHERE id=?', (self.bot,))
            db.execute('DELETE FROM bot_runs WHERE bot_id=?', (self.bot,))
            db.execute('DELETE FROM sessions WHERE id=?', (self.home,))
        after = launcher.employee_readonly_local(self.state)
        self.assertEqual({r['id'] for r in after['sessions']}, {'run-session', 'descendant'})
        self.assertIn('descendant', {r['session_id'] for r in after['events']})

    def test_absence_only_after_pinned_database(self):
        with sqlite3.connect(self.path) as db:
            db.execute('DELETE FROM bots WHERE id=?', (self.bot,))
        with self.assertRaises(RuntimeError): launcher.employee_readonly_local(self.state)
        self.state.update(employee_database=str(self.path), employee_home_session=self.home)
        self.assertEqual(launcher.employee_readonly_local(self.state)['bots'], [])

    def test_outside_and_ambiguous_database_rejected(self):
        self.state['employee_database'] = str(self.repo / 'outside.db')
        with self.assertRaises(RuntimeError): launcher.employee_readonly_local(self.state)
        self.state.pop('employee_database')
        (self.root / 'data/duplicate.db').write_bytes(self.path.read_bytes())
        with self.assertRaises(RuntimeError): launcher.employee_readonly_local(self.state)

    def test_restart_timestamp_is_owned_process_time_and_never_authenticates(self):
        self.state.update(binary_sha256='candidate', browser_sha256='browser', container_id='owned', url='http://127.0.0.1:1234')
        cp = dict(contract=launcher.EMPLOYEE_CONTRACT, bot_id=self.bot, candidate='candidate', browser_candidate='browser', actions={'restart-tick': True}, receipts={'schedule-natural': {}})
        (self.root / 'employee-checkpoint.json').write_text(json.dumps(cp))
        before = {'State': {'StartedAt': '2026-11-08T10:00:00Z'}}
        after = {'State': {'StartedAt': '2026-11-08T10:01:00Z'}}
        from unittest.mock import MagicMock
        response = MagicMock(); response.__enter__.return_value.status = 200
        response.__enter__.return_value.read.return_value = b'{"initialized":true,"auth_mode":"builtin"}'
        with patch.object(launcher, 'employee_readonly', side_effect=launcher.employee_readonly_local), patch.object(launcher, 'inspect_owned', side_effect=[before, before, after, after, after]), patch.object(launcher, 'startup_phase', return_value='initialization'), patch.object(launcher.subprocess, 'check_output', side_effect=['', b'owned log']), patch.object(launcher.subprocess, 'run') as run, patch.object(launcher.urllib.request, 'urlopen', return_value=response) as request, patch('builtins.print') as output:
            launcher.employee_command(self.state, 'restart-tick')
        result = json.loads(output.call_args.args[0])
        self.assertEqual(result['restarted_at'], after['State']['StartedAt'])
        self.assertEqual(request.call_args.args[0], self.state['url'] + '/api/auth/setup-status')
        run.assert_called_once()
        self.assertEqual(run.call_args.kwargs['stdout'], launcher.subprocess.DEVNULL)
        with patch.object(launcher, 'inspect_owned', return_value=after), patch.object(launcher.subprocess, 'check_output', return_value=''), patch.object(launcher.subprocess, 'run') as rerun:
            with self.assertRaisesRegex(RuntimeError, 'never replayed'):
                launcher.employee_command(self.state, 'restart-tick')
            rerun.assert_not_called()

    def test_blob_json_roundtrip_scope_and_determinism(self):
        import base64
        event = json.dumps({'Actions': {'StateDelta': {'bot_operation': {'status': 'applied'}}}}, ensure_ascii=False).encode('utf-8')
        run = json.dumps({'bot_document': json.dumps({'id': 'mine', 'bot_id': self.bot, 'session_id': 'execution'})}).encode()
        with sqlite3.connect(self.path) as db:
            db.execute('UPDATE session_events SET event_data=?', (event,))
            db.execute('UPDATE bot_runs SET data=? WHERE id=?', (run, 'mine'))
            db.execute('ALTER TABLE bots ADD COLUMN credentials BLOB')
            db.execute('UPDATE bots SET credentials=?', (b'SECRET-CREDENTIAL',))
            db.execute('ALTER TABLE bots ADD COLUMN embedding BLOB')
            db.execute('UPDATE bots SET embedding=?', (b'x' * 1000000,))
        before = self.path.read_bytes()
        data = launcher.employee_readonly_local(self.state)
        self.assertTrue(data['observation_complete'])
        self.assertIn('execution', data['related_session_ids'])
        self.assertEqual(data['operations'][0]['status'], 'applied')
        self.assertEqual(base64.b64decode(data['blob_evidence'][0]['value']['base64'], validate=True), run)
        self.assertEqual(json.dumps(data, sort_keys=True), json.dumps(launcher.employee_readonly_local(self.state), sort_keys=True))
        self.assertNotIn('SECRET-CREDENTIAL', json.dumps(data))
        self.assertNotIn('embedding', data['bots'][0])
        self.assertEqual(before, self.path.read_bytes())

    def test_binary_and_malformed_rows_retained_fail_closed(self):
        import base64
        for value in (b'\xff\x00', b'{broken', b'[]', 'null', '{bad'):
            with self.subTest(value=value):
                with sqlite3.connect(self.path) as db:
                    db.execute('UPDATE session_events SET event_data=?', (value,))
                before = self.path.read_bytes()
                data = launcher.employee_readonly_local(self.state)
                self.assertEqual(len(data['events']), 2)
                self.assertFalse(data['observation_complete'])
                self.assertEqual(data['blocker'], 'session_events.event_data: invalid UTF-8 JSON object')
                if isinstance(value, bytes):
                    self.assertEqual(base64.b64decode(data['blob_evidence'][0]['value']['base64']), value)
                json.dumps(data)
                self.assertEqual(before, self.path.read_bytes())

    def test_nonprotocol_blob_hash_and_utf8_exact_bytes(self):
        import hashlib
        value = b'\x00\xff' * 10000
        row = launcher.employee_evidence_row({'id': 'x', 'name': value, 'credentials': value}, 'bots', [], [])
        self.assertEqual(row['name'], dict(type='sqlite_blob', size=len(value), sha256=hashlib.sha256(value).hexdigest()))
        self.assertNotIn('credentials', row)
        blobs = []
        value = '{"history":"café"}'.encode('utf-8')
        row = launcher.employee_evidence_row({'id': 'x', 'data': value}, 'bot_runs', [], blobs)
        self.assertEqual(row['data'].encode('utf-8'), value)

    def test_failure_capture_prevents_success_output(self):
        self.state.update(binary_sha256='candidate', browser_sha256='browser')
        cp = dict(contract=launcher.EMPLOYEE_CONTRACT, bot_id=self.bot, candidate='candidate', browser_candidate='browser')
        (self.root / 'employee-checkpoint.json').write_text(json.dumps(cp))
        with sqlite3.connect(self.path) as db:
            db.execute('UPDATE session_events SET event_data=?', (b'\xff',))
        before = self.path.read_bytes()
        with patch.object(launcher, 'employee_readonly', side_effect=launcher.employee_readonly_local), patch.object(launcher, 'inspect_owned'), patch('builtins.print') as output:
            with self.assertRaisesRegex(RuntimeError, 'employee observation incomplete: session_events.event_data'):
                launcher.employee_command(self.state, 'read')
            output.assert_not_called()
        failure = self.root / 'employee-observer-failure.json'
        self.assertFalse(json.loads(failure.read_text())['observation_complete'])
        self.assertEqual(failure.stat().st_mode & 0o777, 0o600)
        self.assertEqual(before, self.path.read_bytes())

    def test_callback_cannot_launch_or_configure(self):
        with patch.object(launcher, 'create') as create, patch.object(launcher, 'setup') as setup:
            with self.assertRaises(SystemExit): launcher.main(['--employee-command', 'read'])
            create.assert_not_called(); setup.assert_not_called()

if __name__ == '__main__': unittest.main()
