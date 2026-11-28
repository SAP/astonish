"""No provider or daemon calls: negative acceptance contract tests."""
import unittest
from agent_live_contract import AGENT_CONTRACT, AGENT_REQUIRED, validate_agent_evidence


class AgentLiveContractTests(unittest.TestCase):
    def test_diagnostic_samples_never_satisfy_acceptance(self):
        state = {'binary_sha256': 'a' * 64, 'browser_sha256': 'b' * 64, 'agent_nonce': 'c' * 64}
        for status in ('blocked', 'diagnostic-observed', 'verified'):
            evidence = {'contract': AGENT_CONTRACT, 'status': status,
                        'candidate': state['binary_sha256'], 'browser_candidate': state['browser_sha256'],
                        'nonce': state['agent_nonce'], 'samples': [{'status': 'diagnostic-observed'}] * 3,
                        'unverified': ['adapter-provenance']}
            with self.assertRaises(RuntimeError):
                validate_agent_evidence(state, evidence)

    def test_fabricated_complete_gate_names_are_not_semantic_proof(self):
        state = {'binary_sha256': 'a' * 64, 'browser_sha256': 'b' * 64, 'agent_nonce': 'c' * 64}
        evidence = {'contract': AGENT_CONTRACT, 'status': 'verified',
                    'candidate': state['binary_sha256'], 'browser_candidate': state['browser_sha256'],
                    'nonce': state['agent_nonce'], 'passed': list(AGENT_REQUIRED),
                    'receipts': {name: {'status': 'passed'} for name in AGENT_REQUIRED}}
        with self.assertRaisesRegex(RuntimeError, 'semantic validators'):
            validate_agent_evidence(state, evidence)
        evidence['nonce'] = 'd' * 64
        with self.assertRaisesRegex(RuntimeError, 'invocation identity'):
            validate_agent_evidence(state, evidence)
