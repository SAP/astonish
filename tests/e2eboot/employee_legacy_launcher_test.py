import json
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest.mock import patch
import local_bot_smoke_launcher as launcher


class EmployeeLegacySetupTests(unittest.TestCase):
    def test_fresh_fixture_uses_only_inert_owner_service_calls(self):
        with tempfile.TemporaryDirectory() as directory:
            state = dict(root=directory)
            bid, home, sid = (str(uuid.uuid4()) for _ in range(3))
            calls = []
            def call(path, body=None, method=None):
                calls.append((path, body, method))
                if len(calls) == 1:
                    self.assertFalse(body['start_from_title'])
                    return dict(id=bid, revision=1)
                if len(calls) == 2:
                    self.assertEqual(method, 'PUT')
                    self.assertFalse(body['definition']['enabled'])
                    self.assertEqual(body['definition']['cron'], '0 0 1 6 *')
                    return dict(version=2, home_session_id=home)
                self.assertIsNone(body)
                return dict(triggers=[dict(calls[1][1]['definition'], schedule_id=sid)])
            with patch.object(launcher, 'inspect_owned'), patch.object(launcher, 'employee_readonly', return_value=dict(observation_complete=True, runs=[])):
                launcher.employee_legacy_setup(state, call)
                self.assertEqual(len(calls), 3)
                self.assertNotIn('bot_id', state)
                self.assertEqual(state['employee_legacy_fixture']['bot_id'], bid)
                self.assertTrue((Path(directory)/'employee-legacy-fixture-disk.json').exists())
                self.assertTrue((Path(directory)/'employee-legacy-fixture-version.json').exists())
                with self.assertRaises(RuntimeError): launcher.employee_legacy_setup(state, call)
                self.assertEqual(len(calls), 3)

    def test_ambiguous_creation_is_not_replayed(self):
        with tempfile.TemporaryDirectory() as directory:
            state = dict(root=directory)
            with patch.object(launcher, 'inspect_owned'), patch.object(launcher, 'employee_readonly') as read:
                def fail(*args): raise RuntimeError('ambiguous response')
                with self.assertRaises(RuntimeError): launcher.employee_legacy_setup(state, fail)
                self.assertTrue(json.loads((Path(directory)/'state.json').read_text())['employee_legacy_attempted'])
                with self.assertRaises(RuntimeError): launcher.employee_legacy_setup(state, fail)
                read.assert_not_called()

if __name__ == '__main__': unittest.main()
