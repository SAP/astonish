import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import local_bot_smoke_launcher as launcher


class LegacyScopedStorageTests(unittest.TestCase):
    def test_whole_store_jobs_are_scoped_before_proof_without_primary_cache(self):
        fixture = json.loads((Path(__file__).parent / 'chat_first_browser/testdata/employee_legacy_schedule.json').read_text())
        legacy = fixture['bot']['id']
        primary = '7342a9ab-8785-4b5a-90fe-006f6d6c6961'
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            root = repo / '.tmp/astonish-chat-first-scoped'
            (root / 'data').mkdir(parents=True)
            path = root / 'data/personal.db'
            with sqlite3.connect(path) as db:
                db.executescript('CREATE TABLE bots(id TEXT, home_session_id TEXT); CREATE TABLE bot_runs(id TEXT, bot_id TEXT, data TEXT); CREATE TABLE scheduled_jobs(id TEXT, mode TEXT, status TEXT, schedule TEXT, next_run_at TEXT, payload BLOB);')
                db.executemany('INSERT INTO bots VALUES (?,?)', [(primary, 'primary-home'), (legacy, fixture['bot']['home_session_id'])])
                for row in fixture['jobs']:
                    db.execute('INSERT INTO scheduled_jobs VALUES (?,?,?,?,?,?)', tuple(row[k].encode() if k == 'payload' else row[k] for k in ('id','mode','status','schedule','next_run_at','payload')))
                # Deliberate third-row interference fixture, not invented live evidence.
                db.execute('INSERT INTO scheduled_jobs VALUES (?,?,?,?,?,?)', ('primary-job', 'bot_trigger', 'active', '* * * * *', None, json.dumps({'bot_id':primary})))
            state = dict(root=str(root), bot_id=primary, employee_database=str(path), employee_home_session='primary-home', employee_related_sessions=['primary-home'])
            original = dict(state)
            before = path.read_bytes()
            with patch.object(launcher, 'REPO', repo):
                scoped = launcher.employee_readonly_local(dict(state, bot_id=legacy))
                primary_read = launcher.employee_readonly_local(state)
            self.assertTrue(scoped['observation_complete'], scoped['observation_errors'])
            self.assertEqual({r['id'] for r in scoped['jobs']}, {r['id'] for r in fixture['jobs']})
            self.assertEqual(scoped['related_session_ids'], [fixture['bot']['home_session_id']])
            self.assertEqual([r['id'] for r in primary_read['jobs']], ['primary-job'])
            self.assertEqual(state, original)
            self.assertEqual(path.read_bytes(), before)
