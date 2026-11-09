#!/usr/bin/env python3
"""Unit safety checks; these are not live acceptance evidence."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class LauncherTests(unittest.TestCase):
    def test_missing_prerequisites_fail_closed(self):
        errors = launcher.prerequisites({})
        for key in ('LOCAL_BOT_BINARY', 'LOCAL_BOT_DOCKER', 'ASTONISH_BOT_BROWSER_PATH', 'LOCAL_BOT_LAYERS'):
            self.assertTrue(any(key in e for e in errors))

    def test_private_checkpoint(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'state.json'
            launcher.private_json(path, {'status': 'blocked'})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_unowned_root_rejected_before_docker(self):
        with patch.object(launcher.subprocess, 'check_output') as command:
            with self.assertRaisesRegex(RuntimeError, 'owned smoke root'):
                launcher.inspect_owned({'root': '/tmp/not-owned'})
            command.assert_not_called()

    def test_whole_journey_required(self):
        self.assertEqual(len(launcher.REQUIRED_EVIDENCE), 9)
        self.assertIn('paused-restart-bytes', launcher.REQUIRED_EVIDENCE)
        self.assertIn('exact-approval-continued', launcher.REQUIRED_EVIDENCE)
        self.assertIn('scheduled-tick-return', launcher.REQUIRED_EVIDENCE)

    def test_preserves_named_browser_blocker(self):
        import json
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            state = root / 'state.json'
            launcher.private_json(state, {'root': str(root), 'bot_id': 'existing'})
            launcher.private_json(root / 'browser-evidence.json', {'stage': 'tool-task-confirm', 'blocker': 'task confirmation rejected'})
            with patch.object(launcher, 'prerequisites', return_value=[]), patch.object(launcher, 'setup'), patch.object(launcher.subprocess, 'run', side_effect=RuntimeError('generic process failed')):
                self.assertEqual(launcher.main(['--verify-chat-first', '--state', str(state)]), 1)
            saved = json.loads(state.read_text())
            self.assertEqual(saved['blocked_stage'], 'tool-task-confirm')
            self.assertEqual(saved['blocker'], 'task confirmation rejected')
            self.assertEqual(saved['bot_id'], 'existing')

    def test_wrapper_filters_own_org_and_preserves_spaces(self):
        script = launcher.wrapper('astonish-chat-first-unit', '/tmp/own alias', '/tmp/layers spaces')
        self.assertIn('label=astonish.org=astonish-chat-first-unit', script)
        self.assertIn("alias='/tmp/own alias'", script)
        self.assertIn("layers='/tmp/layers spaces'", script)
        self.assertNotIn('docker stop', script)
        self.assertNotIn('docker rm', script)

if __name__ == '__main__': unittest.main()
