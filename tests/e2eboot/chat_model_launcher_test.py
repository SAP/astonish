"""Strict title-first launcher contracts; no live-provider substitutes."""
import importlib.util
from pathlib import Path
import unittest
spec = importlib.util.spec_from_file_location('launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class ChatModelLauncherTests(unittest.TestCase):
    def test_restart_and_positive_schedule_are_required(self):
        self.assertIn('question-restart', launcher.CHAT_MODEL_REQUIRED)
        self.assertIn('schedule-consent', launcher.CHAT_MODEL_REQUIRED)

    def test_missing_config_rejected(self):
        with self.assertRaisesRegex(ValueError, 'fields'):
            launcher.config_environment({})
    def test_unknown_config_rejected_without_value_leak(self):
        with self.assertRaises(ValueError) as error:
            launcher.config_environment({'secret': 'DO_NOT_PRINT'})
        self.assertNotIn('DO_NOT_PRINT', str(error.exception))
    def test_partial_cannot_pass(self):
        with self.assertRaises(RuntimeError):
            launcher.validate_chat_model_evidence({'binary_sha256':'a'}, {'status':'verified', 'passed':['title-thinking']})
    def test_old_policy_cannot_pass(self):
        with self.assertRaises(RuntimeError):
            launcher.validate_chat_model_evidence({'binary_sha256':'a'}, {'status':'verified','passed':list(launcher.REQUIRED_EVIDENCE)})
