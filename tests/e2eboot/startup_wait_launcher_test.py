import hashlib
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch
import local_bot_smoke_launcher as launcher


class StartupWaitTests(unittest.TestCase):
    def test_timeout_bounds(self):
        self.assertEqual(launcher.startup_timeout({}), 300)
        for value in ('0', '901', 'inf', 'nan', '1.5', ''):
            with self.assertRaises(ValueError):
                launcher.startup_timeout({'LOCAL_BOT_STARTUP_TIMEOUT_SECONDS': value})
        self.assertEqual(launcher.startup_timeout({'LOCAL_BOT_STARTUP_TIMEOUT_SECONDS': '900'}), 900)

    def wait(self, status=200, error=None, previous=None, body=b'{"initialized":false,"auth_mode":"builtin"}'):
        clock = [0]
        info = {'State': {'StartedAt': 'new'}}
        response = MagicMock(); response.__enter__.return_value.status = status
        response.__enter__.return_value.read.return_value = body
        def advance(seconds): clock[0] += seconds
        def health(*args, **kwargs):
            if error: raise error
            if clock[0] < 170: raise OSError('initializing')
            return response
        with patch.object(launcher, 'inspect_owned', return_value=info), patch.object(launcher, 'startup_phase', return_value='tool-index initialization'), patch.object(launcher.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(launcher.time, 'sleep', side_effect=advance), patch.object(launcher.urllib.request, 'urlopen', side_effect=health), patch.object(launcher, 'startup_timeout', return_value=300), patch('sys.stderr'):
            return launcher.wait_healthy({'url': 'http://owned'}, previous_start=previous)

    def test_slow_tool_index_then_real_health(self):
        self.assertEqual(self.wait()['State']['StartedAt'], 'new')

    def test_non_200_and_network_errors_never_pass(self):
        for kwargs in ({'status': 503}, {'error': OSError('refused')}, {'previous': 'new'}, {'body': b'<html>SPA</html>'}, {'body': b'{}'}):
            with self.assertRaisesRegex(RuntimeError, 'health timeout'):
                self.wait(**kwargs)

    def test_phase_markers_and_missing_log(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); state = {'root': directory}
            self.assertIn('not created', launcher.startup_phase(state, {}))
            (root/'logs').mkdir(); log = root/'logs/daemon.log'
            log.write_text('Session store: old\nphase=serve-handoff outcome=complete\nSession store: new\nphase=sandbox elapsed=1ms\n')
            self.assertIn('tool-index initialization pending', launcher.startup_phase(state, {}))
            log.write_text(log.read_text()+'phase=tool-index-sync elapsed=170s\n')
            self.assertIn('initialization complete', launcher.startup_phase(state, {}))
            log.write_text(log.read_text()+'phase=serve-handoff outcome=complete\n')
            self.assertIn('serve handoff complete', launcher.startup_phase(state, {}))

    def test_ownership_failure_is_not_retried(self):
        with patch.object(launcher, 'inspect_owned', side_effect=RuntimeError('ownership mismatch')), patch.object(launcher.urllib.request, 'urlopen') as health:
            with self.assertRaisesRegex(RuntimeError, 'ownership mismatch'):
                launcher.wait_healthy({})
            health.assert_not_called()

    def test_resume_requires_canonical_matching_candidate_and_no_legacy_bot(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(launcher, 'inspect_owned'):
            root = Path(directory); binary = root/'astonish'; binary.write_bytes(b'candidate')
            state = {'root': str(root), 'binary_sha256': hashlib.sha256(b'candidate').hexdigest()}
            env = {'LOCAL_BOT_BINARY': str(binary)}
            launcher.validate_employee_resume(state, root/'state.json', env)
            self.assertTrue(state['diagnostic_resume'])
            for changed, path in [({}, root/'copy.json'), ({'binary_sha256': 'bad'}, root/'state.json'), ({'bot_id': 'legacy'}, root/'state.json')]:
                with self.assertRaises(RuntimeError):
                    launcher.validate_employee_resume(dict(state, **changed), path, env)


if __name__ == '__main__': unittest.main()
