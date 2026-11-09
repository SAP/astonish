import json
import unittest

from employee_http_acceptance import EmployeeHTTPEvidence, PACKAGE, REQUIRED


class EmployeeHTTPEvidenceTest(unittest.TestCase):
    def evidence(self, package=PACKAGE, omit=None, skip=None):
        evidence = EmployeeHTTPEvidence()
        for name in REQUIRED - {omit}:
            for action in ("run", "skip" if name == skip else "pass"):
                evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
        evidence.feed(json.dumps(dict(Package=package, Action="pass")))
        return evidence

    def test_complete_daemon_evidence(self):
        self.assertEqual([], self.evidence().errors())

    def test_api_package_cannot_satisfy_gate(self):
        self.assertTrue(self.evidence(package="github.com/SAP/astonish/pkg/api").errors())

    def test_no_tests_is_failure(self):
        self.assertTrue(EmployeeHTTPEvidence().errors())

    def test_missing_priority_subtest_is_failure(self):
        self.assertTrue(self.evidence(omit="TestBotEmployeeOperationsHTTPAcceptance/team/priority_pause").errors())

    def test_skipped_subtest_is_failure(self):
        self.assertTrue(self.evidence(skip="TestBotEmployeeOperationsHTTPAcceptance/personal/deny").errors())


if __name__ == "__main__":
    unittest.main()
