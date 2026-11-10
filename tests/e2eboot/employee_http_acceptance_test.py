import json
import unittest

from employee_http_acceptance import EmployeeHTTPEvidence, PACKAGE, REQUIRED, QualityEvidence


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


class QualityEvidenceTest(unittest.TestCase):
    def test_browser_environment_is_explicit_and_scoped(self):
        from employee_http_acceptance import quality_env
        source = {"ASTONISH_MEMORY_BROWSER_ACCEPTANCE": "1", "ASTONISH_BOT_BROWSER_PATH": "/explicit/browser", "ASTONISH_TEST_DSN": "must-not-leak"}
        env = quality_env("browser", source)
        self.assertEqual("1", env["ASTONISH_MEMORY_BROWSER_ACCEPTANCE"])
        self.assertEqual("/explicit/browser", env["ASTONISH_BOT_BROWSER_PATH"])
        self.assertNotIn("ASTONISH_TEST_DSN", env)
        self.assertNotIn("ASTONISH_MEMORY_BROWSER_ACCEPTANCE", quality_env("runtime", source))

    def test_quality_requires_exact_package_and_all_cases(self):
        for stage in ("publication", "runtime", "browser", "terminal"):
            for mode in ("valid", "missing", "skip", "foreign"):
                evidence = QualityEvidence(stage)
                names = sorted(evidence.required_names)
                for name in names:
                    if mode == "missing" and name == names[-1]:
                        continue
                    package = "foreign/package" if mode == "foreign" else evidence.package
                    for action in ("run", "skip" if mode == "skip" else "pass"):
                        evidence.feed(json.dumps(dict(Package=package, Test=name, Action=action)))
                evidence.feed(json.dumps(dict(Package=evidence.package, Action="pass")))
                self.assertEqual(mode != "valid", bool(evidence.errors()), (stage, mode))


if __name__ == "__main__":
    unittest.main()
