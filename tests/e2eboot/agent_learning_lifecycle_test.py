"""Mocked owned lifecycle checks; no Docker, network or model calls."""
import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import patch
from local_bot_smoke_launcher_test import launcher


class AgentLearningLifecycleTests(unittest.TestCase):
    def test_restart_keeps_nonce_and_uses_only_owned_container(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = dict(root=directory, workflow='bot-agent-live-v1', agent_nonce='nonce',
                         agent_learning_bot_id='bot', bot_id='bot', binary_sha256='candidate',
                         browser_sha256='browser', container_id='owned-candidate',
                         agent_intended={'model': 'intended'})
            checkpoint = dict(contract=state['workflow'], nonce='nonce', bot_id='bot',
                              candidate='candidate', browser_candidate='browser',
                              actions={'restart-tick': True}, receipts={'schedule-natural': {}})
            launcher.private_json(root / 'agent-learning-checkpoint.json', checkpoint)
            disk = dict(observation_complete=True, database='owned.db',
                        home_session_id='home', related_session_ids=['home'])
            with patch.object(launcher, 'inspect_owned', return_value={'State': {'StartedAt': 'before'}}), patch.object(launcher, 'wait_healthy', return_value={'State': {'StartedAt': 'after'}}), patch.object(launcher, 'employee_readonly', return_value=disk), patch.object(launcher.subprocess, 'check_output', side_effect=['', b'log']), patch.object(launcher.subprocess, 'run') as restart, redirect_stdout(io.StringIO()):
                launcher.employee_command(state, 'restart-tick')
                self.assertEqual(restart.call_args.args[0], ['docker', 'restart', 'owned-candidate'])
                saved = json.loads((root / 'state.json').read_text())
                self.assertEqual(saved['agent_nonce'], 'nonce')
                self.assertEqual(saved['agent_intended'], {'model': 'intended'})
                self.assertEqual(saved['restart-tick'], 'verified')
                self.assertEqual(disk['restarted_at'], 'after')
                with self.assertRaisesRegex(RuntimeError, 'never replayed'):
                    # Closed-process check happens before the replay guard.
                    with patch.object(launcher.subprocess, 'check_output', return_value=''):
                        launcher.employee_command(state, 'restart-tick')
                self.assertEqual(restart.call_count, 1)

    def test_foreign_nonce_fails_before_observation_or_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = dict(root=directory, workflow='bot-agent-live-v1', agent_nonce='new',
                         agent_learning_bot_id='bot', bot_id='bot')
            launcher.private_json(root / 'agent-learning-checkpoint.json', {'nonce': 'old'})
            with patch.object(launcher, 'inspect_owned'), patch.object(launcher, 'employee_readonly') as read, patch.object(launcher.subprocess, 'run') as restart:
                with self.assertRaisesRegex(RuntimeError, 'identity/action mismatch'):
                    launcher.employee_command(state, 'read')
                read.assert_not_called()
                restart.assert_not_called()
