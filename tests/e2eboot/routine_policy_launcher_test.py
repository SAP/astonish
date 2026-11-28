import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('routine_launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class RoutinePolicyTests(unittest.TestCase):
    def test_exact_restriction(self):
        self.assertEqual(launcher.routine_policy_yaml({'require_approval': ['memory_forget']}), '\nbot_routine_policy:\n  require_approval: ["memory_forget"]\n')
    def test_default_empty(self):
        self.assertIn('require_approval: []', launcher.routine_policy_yaml({'require_approval': []}))
    def test_reject_widening(self):
        for value in (None, {}, {'allow': ['*']}, {'require_approval': ['schedule_job']}, {'require_approval': ['memory_forget', 'memory_forget']}, {'require_approval': 'memory_forget'}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                launcher.routine_policy_yaml(value)
