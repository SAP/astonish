import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('coherent_launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class CoherentEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.state = {'binary_sha256': 'candidate', 'bot_id': 'owned-bot', 'url': 'http://127.0.0.1:12345'}
        self.actual = {'passed': list(launcher.REQUIRED_EVIDENCE), 'status': 'verified', 'candidate': 'candidate', 'bot_id': 'owned-bot', 'receipts': {}}
        for name in launcher.REQUIRED_EVIDENCE:
            self.actual['receipts'][name] = {'Candidate': 'candidate', 'BotID': 'owned-bot', 'Home': self.state['url'] + '/#/chat/bots/personal/owned-bot', 'Detail': 'assertion'}
    def test_complete_candidate_receipts(self):
        launcher.validate_evidence(self.state, self.actual)
    def test_partial_never_passes(self):
        self.actual['status'] = 'partial'
        with self.assertRaises(RuntimeError): launcher.validate_evidence(self.state, self.actual)
    def test_pass_list_without_receipts_rejected(self):
        self.actual['receipts'] = {}
        with self.assertRaises(RuntimeError): launcher.validate_evidence(self.state, self.actual)
    def test_every_missing_stage_rejected(self):
        for name in launcher.REQUIRED_EVIDENCE:
            actual = copy.deepcopy(self.actual)
            actual['passed'].remove(name)
            with self.assertRaises(RuntimeError): launcher.validate_evidence(self.state, actual)
    def test_historical_candidate_rejected(self):
        self.actual['receipts']['paused-restart-bytes']['Candidate'] = 'old'
        with self.assertRaises(RuntimeError): launcher.validate_evidence(self.state, self.actual)
    def test_other_home_rejected(self):
        self.actual['receipts']['minimal-conversation']['Home'] += '-other'
        with self.assertRaises(RuntimeError): launcher.validate_evidence(self.state, self.actual)
