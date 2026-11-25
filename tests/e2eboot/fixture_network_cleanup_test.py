import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
import fixture_network_cleanup as cleanup


class NetworkCleanupTests(unittest.TestCase):
    def test_exact_empty_network_and_no_foreign_deletion(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            root = repo / '.tmp/astonish-chat-first-owned'
            (root / 'config/astonish').mkdir(parents=True)
            state = {'root': str(root), 'container': 'astonish-chat-first-1234abcd'}
            (root / 'state.json').write_text(json.dumps(state))
            (root / 'config/astonish/config.yaml').write_text('    default_org_slug: astonish-chat-first-1234abcd\n')
            info = {'Id': 'a' * 64, 'Name': 'astonish-org-' + state['container'],
                    'Labels': {'astonish.org': state['container']}, 'Containers': {},
                    'Driver': 'bridge', 'Scope': 'local'}
            with patch.object(cleanup.subprocess, 'check_output', side_effect=['', '', 'abc\n', json.dumps([info]), '']), patch.object(cleanup.subprocess, 'run') as run:
                self.assertEqual(cleanup.cleanup_recorded_network(state, repo)['id'], info['Id'])
                run.assert_called_once_with(['docker', 'network', 'rm', info['Id']], check=True, capture_output=True, timeout=20)
            for change in ({'Name': 'astonish-org-default'}, {'Labels': {'astonish.org': 'my-org'}},
                           {'Containers': {'foreign': {}}}, {'Labels': {'astonish.org': state['container'], 'foreign': 'yes'}}):
                with patch.object(cleanup.subprocess, 'check_output', side_effect=['', '', 'abc\n', json.dumps([dict(info, **change)])]), patch.object(cleanup.subprocess, 'run') as run:
                    with self.assertRaises(RuntimeError):
                        cleanup.cleanup_recorded_network(state, repo)
                    run.assert_not_called()
            with patch.object(cleanup.subprocess, 'check_output', return_value='active-quality-fixture'), patch.object(cleanup.subprocess, 'run') as run:
                with self.assertRaises(RuntimeError):
                    cleanup.cleanup_recorded_network(state, repo)
                run.assert_not_called()
            state['container'] = 'astonish-chat-first-deadbeef'
            with patch.object(cleanup.subprocess, 'run') as run:
                with self.assertRaises(RuntimeError):
                    cleanup.cleanup_recorded_network(state, repo)
                run.assert_not_called()

    def test_launcher_cleanup_is_exclusive(self):
        import local_bot_smoke_launcher as launcher
        with self.assertRaises(SystemExit), patch.object(launcher, 'create') as create:
            launcher.main(['--cleanup-network', '--state', '/not-owned', '--verify-chat-first'])
        create.assert_not_called()


if __name__ == '__main__':
    unittest.main()
