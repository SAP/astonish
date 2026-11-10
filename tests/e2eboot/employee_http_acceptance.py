"""Strict controlled employee HTTP lifecycle gate (not a live daemon drill).

Run: python3 tests/e2eboot/employee_http_acceptance.py
"""
import sys
import argparse
from pathlib import Path

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


QUALITY_TESTS = {
    "publication": ("./pkg/api", "TestBotWorkPublicationHTTPAcceptance", ("personal", "team")),
    "runtime": ("./pkg/api", "TestBotWorkRuntimeHTTPAcceptance", tuple(
        f"{scope}/{scenario}" for scope in ("personal", "team")
        for scenario in ("fail-repair-pass", "limits", "malformed", "forged-verdict",
                         "missing-evidence", "provider-error", "trailing-json", "revoked-during-review"))),
    "browser": ("./pkg/api", "TestBotWorkQualityBrowserAcceptance", ()),
    "terminal": ("./cmd/astonish", "TestBotWorkQualityPTYAcceptance", ()),
}


class QualityEvidence(GoEvidence):
    def __init__(self, stage):
        package, test, cases = QUALITY_TESTS[stage]
        self.package = "github.com/SAP/astonish/" + package.removeprefix("./")
        self.required_names = {test, *(f"{test}/{case}" for case in cases)}
        super().__init__(self.required_names)

    def errors(self):
        errors = super().errors()
        for name in sorted(self.required_names):
            if (self.package, name) not in self.qualified_passes:
                errors.append(f"missing quality run/pass evidence: {self.package}/{name}")
        if self.package not in self.packages_passed:
            errors.append("quality package did not pass")
        return errors


def quality_env(stage, source):
    env = clean_env(source)
    env.pop("ASTONISH_TEST_DSN", None)
    env.pop("KUBECONFIG", None)
    if stage == "browser":
        for key in ("ASTONISH_MEMORY_BROWSER_ACCEPTANCE", "ASTONISH_BOT_BROWSER_PATH"):
            if key in source:
                env[key] = source[key]
        # Native Code-mode macOS verification uses the installed application;
        # an explicit path always wins. Never infer an in-container browser.
        native = Path('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome')
        if sys.platform == 'darwin' and not env.get('ASTONISH_BOT_BROWSER_PATH') and native.is_file() and os.access(native, os.X_OK):
            env['ASTONISH_BOT_BROWSER_PATH'] = str(native)
    return env


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quality-stage", choices=tuple(QUALITY_TESTS))
    args = parser.parse_args(argv)
    if args.quality_stage:
        package, test, _ = QUALITY_TESTS[args.quality_stage]
        return 0 if run(go_command(package, (test,)), quality_env(args.quality_stage, os.environ),
                        QualityEvidence(args.quality_stage)) else 1
    return 0 if run(go_command("./pkg/daemon", TESTS), clean_env(os.environ),
                    EmployeeHTTPEvidence()) else 1


if __name__ == "__main__":
    sys.exit(main())
