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
    def test_agent_mode_launches_fresh_journey_but_diagnostic_is_not_acceptance(self):
        import io
        import json
        import hashlib
        from provider_startup_launcher_test import agent_config
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'config.json'
            path.write_text(json.dumps(agent_config())); path.chmod(0o600)
            (root / 'bin').mkdir()
            (root / 'bin/chat-first-browser').write_bytes(b'candidate-browser')
            state = {'root': str(root), 'binary_sha256': 'a' * 64,
                     'browser_sha256': hashlib.sha256(b'candidate-browser').hexdigest()}
            output = io.StringIO()
            def run(command, **kwargs):
                self.assertEqual(command, [str(root / 'bin/chat-first-browser')])
                self.assertEqual(kwargs['env']['BOT_AGENT_ACCEPTANCE'], '1')
                self.assertNotIn('BOT_QUALITY_ACCEPTANCE', kwargs['env'])
                staged = json.loads((root / 'state.json').read_text())
                self.assertEqual(staged['agent_intended'], launcher.AGENT_TARGET)
                self.assertEqual(len(staged['agent_nonce']), 64)
                launcher.private_json(root / 'browser-evidence.json', {
                    'contract': 'bot-agent-live-v1', 'status': 'blocked',
                    'stage': 'agent-proof-obligations', 'unverified': ['adapter-provenance'],
                    'blocker': 'candidate samples retained; full proof unavailable'})
            with patch.object(launcher, 'config_environment', return_value={}), patch.object(launcher, 'prerequisites', return_value=[]), patch.object(launcher, 'create', return_value=state) as create, patch.object(launcher, 'setup') as setup, patch.object(launcher.subprocess, 'run', side_effect=run) as browser, patch.dict(launcher.os.environ, {'BOT_QUALITY_ACCEPTANCE': '1'}), patch('sys.stderr', output):
                self.assertEqual(launcher.main(['--verify-bot-agent', '--config', str(path)]), 1)
            create.assert_called_once(); setup.assert_called_once(); browser.assert_called_once()
            self.assertEqual(json.loads((root / 'state.json').read_text())['status'], 'blocked')
            self.assertIn('full proof unavailable', output.getvalue())
            self.assertNotIn('SECRET_SENTINEL', output.getvalue())

    def test_agent_rejects_resume_legacy_config_and_public_secrets(self):
        import io
        from provider_startup_launcher_test import agent_config
        import json
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.json'
            for text, mode, extra in [('{}', 0o600, []),
                                       (json.dumps(agent_config()), 0o644, []),
                                       (json.dumps(agent_config()), 0o600, ['--state', '/not-owned']),
                                       ('{"schema_version":1,"schema_version":"SECRET_SENTINEL"}', 0o600, [])]:
                path.write_text(text); path.chmod(mode)
                output = io.StringIO()
                with patch.object(launcher, 'create') as create, patch('sys.stderr', output), self.assertRaises(SystemExit) as error:
                    launcher.main(['--verify-bot-agent', '--config', str(path)] + extra)
                self.assertEqual(error.exception.code, 2)
                create.assert_not_called()
                self.assertNotIn('SECRET_SENTINEL', output.getvalue())

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
