"""Strict controlled employee HTTP lifecycle gate (not a live daemon drill).

Run: python3 tests/e2eboot/employee_http_acceptance.py
"""
import sys

from bots_acceptance_driver import GoEvidence, clean_env, go_command, run
import os

PACKAGE = "github.com/SAP/astonish/pkg/daemon"
TESTS = ("TestBotEmployeeOperationsHTTPAcceptance", "TestBotAuthorizedScheduleHTTPAcceptance")
REQUIRED = set(TESTS)
for scope in ("personal", "team"):
    REQUIRED.add(f"{TESTS[1]}/{scope}")
    for scenario in ("goal", "paused_goal", "task_once", "priority_pause", "deny", "approve", "expiry"):
        REQUIRED.add(f"{TESTS[0]}/{scope}/{scenario}")


class EmployeeHTTPEvidence(GoEvidence):
    def __init__(self):
        super().__init__(REQUIRED)

    def errors(self):
        errors = super().errors()
        for name in sorted(REQUIRED):
            if (PACKAGE, name) not in self.qualified_passes:
                errors.append(f"missing daemon run/pass evidence: {name}")
        if PACKAGE not in self.packages_passed:
            errors.append("daemon package did not pass")
        return errors


def main():
    return 0 if run(go_command("./pkg/daemon", TESTS), clean_env(os.environ),
                    EmployeeHTTPEvidence()) else 1


if __name__ == "__main__":
    sys.exit(main())
