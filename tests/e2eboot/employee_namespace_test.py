import unittest
from unittest.mock import patch

import local_bot_smoke_launcher as launcher
import employee_namespace_reader as transport


class EmployeeNamespaceDispatchTests(unittest.TestCase):
    def setUp(self):
        self.state = dict(root=str(launcher.REPO / '.tmp/astonish-chat-first-namespace'),
                          bot_id='7342a9ab-8785-4b5a-90fe-006f6d6c6961',
                          container='astonish-chat-first-namespace', container_id='a' * 64)
        self.info = dict(Id=self.state['container_id'], State=dict(Running=True, StartedAt='start'),
                         Mounts=[dict(Type='bind', Source=self.state['root'], Destination=self.state['root'])])

    def test_live_entrypoint_executes_exact_owned_namespace_without_host_sqlite(self):
        with patch.object(launcher, 'inspect_owned', return_value=self.info) as owned, \
                patch.object(transport, 'Reader') as reader, \
                patch.object(launcher, 'employee_readonly_local', return_value={'observation_complete': True}) as project, \
                patch.object(launcher.sqlite3, 'connect', side_effect=AssertionError('host SQLite forbidden')):
            actual = launcher.employee_readonly(self.state)
        reader.assert_called_once_with(launcher.REPO, launcher.Path(self.state['root']), self.info)
        project.assert_called_once_with(self.state, reader=reader.return_value)
        reader.return_value.close.assert_called_once()
        self.assertEqual(owned.call_count, 2)
        self.assertEqual(actual['observation_source']['namespace'], 'owned-writer-container')
        self.assertEqual(actual['observation_source']['container_id'], self.state['container_id'])

    def test_ownership_failure_prevents_execution(self):
        with patch.object(launcher, 'inspect_owned', side_effect=RuntimeError('ownership mismatch')), \
                patch.object(transport, 'Reader') as reader:
            with self.assertRaisesRegex(RuntimeError, 'ownership mismatch'):
                launcher.employee_readonly(self.state)
            reader.assert_not_called()

    def test_wrong_mount_or_shadow_mount_prevents_execution(self):
        for mounts in ([], [dict(Type='bind', Source='/wrong', Destination=self.state['root'])],
                       self.info['Mounts'] + [dict(Type='volume', Destination=self.state['root'] + '/data')]):
            with self.subTest(mounts=mounts), patch.object(launcher, 'inspect_owned', return_value=dict(self.info, Mounts=mounts)), \
                    patch.object(transport, 'Reader') as reader:
                with self.assertRaisesRegex(RuntimeError, 'mount'):
                    launcher.employee_readonly(self.state)
                reader.assert_not_called()

    def test_exec_failure_never_falls_back_to_host(self):
        with patch.object(launcher, 'inspect_owned', return_value=self.info), \
                patch.object(transport, 'Reader', side_effect=RuntimeError('exec failure')), \
                patch.object(launcher.sqlite3, 'connect', side_effect=AssertionError('host fallback')):
            with self.assertRaisesRegex(RuntimeError, 'exec failure'):
                launcher.employee_readonly(self.state)

    def test_restart_during_observation_rejected(self):
        after = dict(self.info, State=dict(Running=True, StartedAt='different'))
        with patch.object(launcher, 'inspect_owned', side_effect=[self.info, after]), \
                patch.object(transport, 'Reader'), patch.object(launcher, 'employee_readonly_local', return_value={}):
            with self.assertRaisesRegex(RuntimeError, 'restarted'):
                launcher.employee_readonly(self.state)

    def test_protocol_rows_preserve_blobs_and_large_integers(self):
        reader = transport.Reader.__new__(transport.Reader)
        with patch.object(reader, 'request', return_value=dict(columns=['id', 'data'], rows=[[9007199254740993, {'sqlite_blob_base64': 'AP8='}]])):
            rows = reader.execute('SELECT id, data FROM bots')
        self.assertEqual(rows[0][0], 9007199254740993)
        self.assertEqual(dict(rows[0]), {'id': 9007199254740993, 'data': b'\x00\xff'})
