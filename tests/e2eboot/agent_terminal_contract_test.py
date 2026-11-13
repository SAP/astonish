"""Synthetic offline fixtures test proof validation, not live acceptance."""
import base64
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from uuid import uuid4
from agent_terminal_contract import records, terminal_parity, screen


def encoded(text):
    return dict(encoding='base64', data=base64.b64encode(text.encode()).decode(), rows=24, cols=100)


class TerminalContractTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bot, self.home, self.rid, self.session, self.skill = [str(uuid4()) for _ in range(5)]
        self.state = dict(root=self.tmp.name, binary_sha256='a'*64, browser_sha256='b'*64,
                          agent_nonce='c'*64, agent_cli_sha256='d'*64, bot_id=self.bot,
                          url='http://127.0.0.1:12345')
        self.text = 'Saved the current result.'
        work = dict(schema_version=1, protocol='agent_led_v1', bot_id=self.bot, run_id=self.rid,
                    work_revision=1, phase='finished', disposition='answered', candidate_digest='e'*64)
        skill = dict(id=self.skill, bot_id=self.bot, head_revision=1, head_digest='f'*64, title='Daily report')
        receipt = dict(schema_version=1, bot_id=self.bot, run_id=self.rid, skill_id=self.skill,
                       used_revision=1, digest='f'*64, kind='selected')
        run = dict(id=self.rid, bot_id=self.bot, session_id=self.session, status='succeeded',
                   created_at='2026-01-01T00:00:00Z', started_at='2026-01-01T00:00:01Z',
                   finished_at='2026-01-01T00:00:02Z', work=work,
                   report=dict(summary=self.text, employee_message=dict(origin='agent', text=self.text), skill=receipt))
        projection = dict(schema_version=1, bot_id=self.bot, run_id=self.rid, session_id=self.session,
                          home_session_id=self.home, status='succeeded', state_sequence=4, work=work)
        snapshot = dict(bot=dict(id=self.bot, home_session_id=self.home), run_states=[projection],
                        latest_sequence=4, history_cursor=2, latest_outcome=run)
        self.events = [dict(actions=dict(stateDelta=dict(bot_run_state=projection))),
                       dict(invocationId='bot-report:'+self.rid, content=dict(role='model', parts=[dict(text=self.text+'\n')]))]
        base = '/api/studio/bots/'+self.bot
        self.rows = [dict(path=base+'/snapshot?scope=personal', status=200, body=json.dumps(snapshot)),
                     dict(path=base+'/skills?scope=personal', status=200, body=json.dumps(dict(skills=[skill])))]
        self.rows += [dict(path=base+'/sessions/'+self.home+'/events?scope=personal', status=200,
                          body=f'id: {self.home}:{i}\nevent: transcript\ndata: '+json.dumps(event))
                      for i, event in enumerate(self.events, 1)]
        binding = dict(nonce='c'*64, candidate='a'*64, browser_candidate='b'*64, bot_id=self.bot, url=self.state['url'])
        frames = {k: encoded('\x1b[2J\x1b[H'+v) for k, v in dict(home=self.text, status='Next run: none\r\nAnswer provided', skills='Skills\r\nDaily report revision 1\r\n'+'f'*64, bots='Bots').items()}
        self.terminal = dict(binding, contract='bot-agent-terminal-v1', cli_sha256='d'*64,
                             blocked_mutations=0, comparison_error='', passes=[dict(frames=copy.deepcopy(frames), exchanges=copy.deepcopy(self.rows)) for _ in range(2)])
        # Synthetic wire ordering only: first connection receives the run after
        # replay; reconnect receives the same durable records during replay.
        self.terminal['passes'][0]['exchanges'].insert(2, dict(path=self.rows[2]['path'], status=200,
                 body=f'id: {self.home}:0\nevent: replay_complete\ndata: {{}}'))
        self.studio = dict(binding, contract='bot-agent-studio-v1', passes=[dict(exchanges=copy.deepcopy(self.rows),
                           rendered=dict(visible=True, invocation_id='bot-report:'+self.rid, text=self.text,
                                         run_states=[projection], skills=[skill])) for _ in range(2)])
        self.actual = dict(contract='bot-agent-live-v1', nonce='c'*64, candidate='a'*64, browser_candidate='b'*64)
        self.save()

    def save(self):
        for key, name, doc in (('terminal_manifest', 'agent-terminal.json', self.terminal), ('studio_manifest', 'agent-studio.json', self.studio)):
            raw = json.dumps(doc).encode()
            Path(self.tmp.name, name).write_bytes(raw)
            self.actual[key] = dict(Source=name, SourceHash=hashlib.sha256(raw).hexdigest())

    def test_replay_only_connections_never_pass_live_parity(self):
        rows = self.terminal['passes'][0]['exchanges']
        barrier = rows.pop(2)
        rows.append(barrier)
        self.save()
        with self.assertRaisesRegex(RuntimeError, 'replay-only'):
            terminal_parity(self.state, self.actual)
        rows.pop()
        self.save()
        with self.assertRaisesRegex(RuntimeError, 'replay-only'):
            terminal_parity(self.state, self.actual)

    def test_terminal_scroll_regions_preserve_outside_rows(self):
        frame = encoded('top\r\none\r\ntwo\r\nbottom\x1b[2;3r\x1b[3;1H\nnew\x1b[r')
        frame.update(rows=4, cols=12)
        self.assertEqual(screen(frame), 'top\ntwo\nnew\nbottom')
        for control in ('\x1b[3;2r', '\x1b[1;5r', '\x1b[1;2;3r'):
            with self.subTest(control=control), self.assertRaises(RuntimeError):
                screen(dict(encoded(control), rows=4))
        frame = encoded('top\r\none\r\ntwo\r\nbottom\x1b[2;3r\x1b[2;1H\x1b[M')
        frame.update(rows=4, cols=12)
        self.assertEqual(screen(frame), 'top\ntwo\n\nbottom')
        for command, expected in (('S', 'top\ntwo\n\nbottom'), ('T', 'top\n\none\nbottom')):
            frame = encoded('top\r\none\r\ntwo\r\nbottom\x1b[2;3r\x1b[' + command)
            frame.update(rows=4, cols=12)
            self.assertEqual(screen(frame), expected)

    def test_terminal_line_edits_erase_hidden_history(self):
        frame = encoded('secret\r\npublic\x1b[H\x1b[M'); frame.update(rows=2, cols=20)
        self.assertEqual(screen(frame), 'public')
        frame = encoded('old\r\nsecret\x1b[H\x1b[Lnew'); frame.update(rows=2, cols=20)
        self.assertEqual(screen(frame), 'new\nold')
        self.assertEqual(screen(encoded('secret\r\x1b[6Xpublic')), 'public')

    def test_terminal_startup_queries_do_not_become_visible_text(self):
        startup = '\x1b]11;?\x1b\\\x1b[6n\x1b[c\x1b[?1049h\x1b[2J\x1b[H'
        self.assertEqual(screen(encoded(startup + 'Actual answer')), 'Actual answer')
        for control in ('\x1b]0;spoofed title\a', '\x1b]52;c;YW5zd2Vy\a', '\x1b[6;1n', '\x1b[1c'):
            with self.subTest(control=control), self.assertRaises(RuntimeError):
                screen(encoded(control + 'Actual answer'))

    def test_terminal_exact_width_and_wrap_are_emulated(self):
        frame = encoded('abcde\rnext'); frame.update(rows=2, cols=5)
        self.assertEqual(screen(frame), 'nexte')
        frame = encoded('abcdef'); frame.update(rows=2, cols=5)
        self.assertEqual(screen(frame), 'abcde\nf')
        frame = encoded('abcdef\r\nlast'); frame.update(rows=2, cols=5)
        self.assertEqual(screen(frame), 'f\nlast')

    def test_terminal_renderer_modes_and_concealed_style(self):
        wire = '\x1b[?2004h\x1b[?1004h\x1b[?2026h\x1b[7mvisible\x1b[0m\x1b[?2026l'
        self.assertEqual(screen(encoded(wire)), 'visible')
        for control in ('\x1b[8m', '\x1b[38;5;240m', '\x1b[38;5;999m', '\x1b[38;2;0;0;0m', '\x1b[?6h'):
            with self.subTest(control=control), self.assertRaises(RuntimeError):
                screen(encoded(control + 'hidden'))

    def test_records_decode_and_compare(self):
        endpoints, events, snapshot = records(self.rows, self.bot)
        self.assertEqual(len(events), 2)
        self.assertEqual(snapshot['latest_outcome']['id'], self.rid)
        self.assertEqual(len(endpoints), 2)

    def test_stale_cursor_and_state_sequence_fail(self):
        for mutate in (lambda rows: rows[-1].update(body=rows[-1]['body'].replace(':2\n', ':1\n')),
                       lambda rows: rows[2].update(body=rows[2]['body'].replace('"state_sequence": 4', '"state_sequence": 0'))):
            rows = copy.deepcopy(self.rows); mutate(rows)
            with self.assertRaises(RuntimeError):
                records(rows, self.bot)

    def test_cross_bot_endpoint_and_projection_fail(self):
        for index in (0, 2):
            rows = copy.deepcopy(self.rows)
            rows[index]['body'] = rows[index]['body'].replace(self.bot, str(uuid4()))
            with self.assertRaises(RuntimeError):
                records(rows, self.bot)
        rows = copy.deepcopy(self.rows); rows[0]['path'] = rows[0]['path'].replace(self.bot, str(uuid4()))
        with self.assertRaises(RuntimeError):
            records(rows, self.bot)

    def test_manifest_identity_hash_and_source_forgeries_fail(self):
        for field in ('candidate', 'nonce', 'browser_candidate', 'cli_sha256', 'bot_id', 'url'):
            with self.subTest(field=field):
                original = self.terminal[field]
                self.terminal[field] = 'forged'; self.save()
                with self.assertRaises((RuntimeError, ValueError)):
                    terminal_parity(self.state, self.actual)
                self.terminal[field] = original
        self.save()
        self.actual['terminal_manifest']['SourceHash'] = '0'*64
        with self.assertRaisesRegex(RuntimeError, 'digest'):
            terminal_parity(self.state, self.actual)
        self.save(); self.actual['terminal_manifest']['Source'] = '../agent-terminal.json'
        with self.assertRaises(RuntimeError):
            terminal_parity(self.state, self.actual)

    def test_no_studio_or_raw_frames_is_not_proof(self):
        del self.actual['studio_manifest']
        with self.assertRaisesRegex(RuntimeError, 'studio_manifest'):
            terminal_parity(self.state, self.actual)
        self.save()
        self.terminal['passes'][0]['frames']['home'] = self.text; self.save()
        with self.assertRaisesRegex(RuntimeError, 'raw encoded'):
            terminal_parity(self.state, self.actual)

    def test_complete_synthetic_proof_passes(self):
        terminal_parity(self.state, self.actual)

    def test_missing_changed_erased_or_hidden_final_frame_fails(self):
        for frame in (encoded('No result'), encoded(self.text+'\r\x1b[2K'),
                      encoded('\x1b[8m'+self.text), encoded('\x1b]0;'+self.text+'\a'),
                      dict(encoding='base64', data='!', rows=24, cols=100)):
            with self.subTest(frame=frame):
                self.terminal['passes'][0]['frames']['home'] = frame; self.save()
                with self.assertRaises((RuntimeError, ValueError)):
                    terminal_parity(self.state, self.actual)
        del self.terminal['passes'][0]['frames']['home']; self.save()
        with self.assertRaisesRegex(RuntimeError, 'missing terminal frame'):
            terminal_parity(self.state, self.actual)

    def test_studio_hidden_text_or_final_invocation_differs(self):
        rendered = self.studio['passes'][0]['rendered']
        for field, value in (('invocation_id', 'bot-report:'+str(uuid4())), ('text', 'Different answer'), ('visible', False)):
            original = rendered[field]; rendered[field] = value; self.save()
            with self.assertRaises(RuntimeError):
                terminal_parity(self.state, self.actual)
            rendered[field] = original

    def test_matching_reconnect_records_cannot_hide_wrong_final_invocation(self):
        for doc in (self.terminal, self.studio):
            for capture in doc['passes']:
                capture['exchanges'][-1]['body'] = capture['exchanges'][-1]['body'].replace('bot-report:'+self.rid, 'bot-report:'+self.session)
        self.save()
        with self.assertRaisesRegex(RuntimeError, 'final invocation'):
            terminal_parity(self.state, self.actual)

    def test_rehashed_skill_and_reconnect_drift_fail(self):
        self.terminal['passes'][1]['exchanges'][1]['body'] = self.rows[1]['body'].replace('f'*64, '0'*64)
        self.save()
        with self.assertRaisesRegex(RuntimeError, 'records differ'):
            terminal_parity(self.state, self.actual)
        for doc in (self.terminal, self.studio):
            for capture in doc['passes']:
                capture['exchanges'][1]['body'] = self.rows[1]['body'].replace('f'*64, '0'*64)
        self.save()
        with self.assertRaisesRegex(RuntimeError, 'skill reference'):
            terminal_parity(self.state, self.actual)

    def test_terminal_screen_reconstructs_erase_overwrite_and_scroll(self):
        self.assertEqual(screen(encoded('old\rnew')), 'new')
        self.assertEqual(screen(encoded('secret\r\x1b[2Kpublic')), 'public')
        self.assertEqual(screen(encoded('secret\x1b[2J\x1b[Hpublic')), 'public')
        frame = encoded('old\r\nnew'); frame['rows'] = 1
        self.assertEqual(screen(frame), 'new')
        self.assertEqual(screen(encoded('\x1b[1manswer\x1b[0m')), 'answer')

    def test_terminal_gate_does_not_clear_unsupported_live_gates(self):
        from agent_live_contract import agent_evidence_results, validate_agent_evidence, AGENT_REQUIRED
        result = agent_evidence_results(self.state, self.actual)
        self.assertEqual(result['studio-terminal-parity'], dict(status='passed'))
        self.assertEqual(sum(r['status'] == 'passed' for r in result.values()), 1)
        self.assertIn('unsupported proof interface', result['adapter-provenance']['reason'])
        self.assertIn('missing private agent-grounding-assessment.json', result['source-grounding']['reason'])
        self.actual.update(status='verified', passed=list(AGENT_REQUIRED), receipts=result,
                           grounding_verified=True, adapter_verified=True)
        with self.assertRaisesRegex(RuntimeError, 'unsupported proof interface'):
            validate_agent_evidence(self.state, self.actual)

    def test_malformed_container_fails_closed_in_evaluator(self):
        from agent_live_contract import agent_evidence_results
        self.terminal['passes'][0]['frames']['home'] = None; self.save()
        self.assertEqual(agent_evidence_results(self.state, self.actual)['studio-terminal-parity']['status'], 'unverified')

    def test_inspector_headings_without_work_or_skills_do_not_pass(self):
        for name, text in (('status', 'Next run: none'), ('skills', 'Skills')):
            frame = self.terminal['passes'][0]['frames'][name]
            self.terminal['passes'][0]['frames'][name] = encoded(text); self.save()
            with self.assertRaisesRegex(RuntimeError, 'not rendered'):
                terminal_parity(self.state, self.actual)
            self.terminal['passes'][0]['frames'][name] = frame

    def test_mutation_flags_and_missing_cli_binding_fail(self):
        self.terminal['blocked_mutations'] = 1; self.save()
        with self.assertRaisesRegex(RuntimeError, 'mutation'):
            terminal_parity(self.state, self.actual)
        self.terminal['blocked_mutations'] = 0; self.save()
        del self.state['agent_cli_sha256']
        with self.assertRaisesRegex(RuntimeError, 'CLI hash'):
            terminal_parity(self.state, self.actual)

    def test_studio_dataset_staleness_fails(self):
        self.studio['passes'][0]['rendered']['run_states'][0]['state_sequence'] = 3
        self.save()
        with self.assertRaisesRegex(RuntimeError, 'dataset differs'):
            terminal_parity(self.state, self.actual)
