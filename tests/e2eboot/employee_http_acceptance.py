"""Strict employee gates; --agent-stage all also requires fresh live acceptance.

Run controlled HTTP lifecycle: python3 tests/e2eboot/employee_http_acceptance.py

--agent-stage postgres (also used by all) creates and removes an owned pgvector
Docker container when BOTH ASTONISH_TEST_DSN and ASTONISH_BOTS_TEST_DB_ISOLATED
are absent. Any supplied DB configuration still requires a nonempty disposable
DSN plus ASTONISH_BOTS_TEST_DB_ISOLATED=1; partial/empty inputs fail closed.
The owned DB uses a random password and an ephemeral loopback-only port.
"""
import sys
import argparse
import json
import re
from pathlib import Path

from bots_acceptance_driver import ROOT, GoEvidence, clean_env, go_command, run
from skills_acceptance_driver import ExactGoEvidence
from owned_postgres_acceptance import postgres_mode, run_owned_postgres
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


CONTENT_TEST = "TestBotContentQualityHTTPAcceptance"
CONTENT_CASES = tuple(f"{scope}/{scenario}" for scope in ("personal", "team")
                      for scenario in ("repair-ai", "broad-tech", "ai-gaming", "repair-exhausted",
                                       "citation-repair", "citation-only", "citation-exhausted",
                                       "citation-observation", "citation-missing-evidence",
                                       "citation-verdict", "citation-explanation", "escaped-xml"))


class QualityEvidence(GoEvidence):
    def __init__(self, stage):
        package, test, cases = QUALITY_TESTS[stage]
        self.package = "github.com/SAP/astonish/" + package.removeprefix("./")
        self.required_names = {test, *(f"{test}/{case}" for case in cases)}
        if stage == "runtime":
            self.required_names.update({CONTENT_TEST, *(f"{CONTENT_TEST}/{case}" for case in CONTENT_CASES)})
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


SKILLS_TESTS = {
    "scheduling": ("./pkg/daemon", "TestBotSkillScheduleHTTPAcceptance", tuple(
        f"{scope}/{scenario}" for scope in ("personal", "team")
        for scenario in ("create-schedule", "duplicate-wake", "feedback-latest", "denied-resource",
                         "task-once", "remove-then-manual", "import-rollback", "foreign-import", "legacy-import"))),
    "runtime": ("./pkg/api", "TestBotSkillRuntimeHTTPAcceptance", tuple(
        f"{scope}/{scenario}" for scope in ("personal", "team")
        for scenario in ("draft-before-tools", "reuse", "feedback", "greeting",
                         "malformed", "sensitive-content", "revoked-orientation"))),
    "storage": ("./pkg/api", "TestBotSkillStorageHTTPAcceptance", (
        "empty", "worker-create-owner-read", "cross-bot", "personal-team",
        "replay-digest", "concurrent-revisions", "expired-lease", "pin-reopen",
        "quota", "schema-upgrade")),
}


class SkillsEvidence(GoEvidence):
    def __init__(self, stage):
        package, test, cases = SKILLS_TESTS[stage]
        self.package = "github.com/SAP/astonish/" + package.removeprefix("./")
        self.required_names = {test, *(f"{test}/{case}" for case in cases)}
        super().__init__(self.required_names)

    def errors(self):
        errors = super().errors()
        for name in sorted(self.required_names):
            if (self.package, name) not in self.qualified_passes:
                errors.append(f"missing skills run/pass evidence: {self.package}/{name}")
        if self.package not in self.packages_passed:
            errors.append("skills package did not pass")
        return errors


def skills_env(source):
    # This gate only uses disposable local SQLite and controlled HTTP identity.
    # It must not inherit a live database, provider credential or cluster config.
    return {k: v for k, v in clean_env(source).items()
            if not k.startswith("ASTONISH_") and not k.endswith("API_KEY")
            and k != "KUBECONFIG"}


AGENT_TESTS = {
    "execution": ("./pkg/api", "TestBotAgentWorkHTTPAcceptance",
                  ("personal", "team", "personal/interactive", "personal/autonomous",
                   "team/interactive", "team/autonomous")),
    "learning": ("./pkg/api", "TestBotAgentLearningHTTPAcceptance", tuple(
        f"{scope}/{mode}" for scope in ("personal", "team")
        for mode in ("interactive", "autonomous"))),
    "progress": ("./pkg/api", "TestBotAgentProgressHTTPAcceptance", tuple(
        f"{scope}/{mode}" for scope in ("personal", "team")
        for mode in ("interactive", "autonomous"))),
}
AGENT_CHECK_TEST = "TestBotAgentCheckHTTPAcceptance"
AGENT_CHECK_CASES = tuple(f"{scope}/{mode}/broken={broken}" for scope in ("personal", "team")
                         for mode in ("interactive", "autonomous") for broken in ("false", "true"))
AGENT_SOURCE_TEST = "TestBotAgentSourceFallbackHTTPAcceptance"
AGENT_SOURCE_CASES = tuple(f"{scope}/{mode}/{source}" for scope in ("personal", "team")
                          for mode in ("interactive", "autonomous")
                          for source in ("rate-limited", "footer-only"))


AGENT_LEARNING_REJECTION_TEST = "TestBotAgentLearningRejectionHTTPAcceptance"
AGENT_LEARNING_REJECTION_CASES = tuple(
    f"{scope}/{mode}/{scenario}" for scope in ("personal", "team")
    for mode in ("interactive", "autonomous")
    for scenario in ("no-skill", "missing-evidence", "evidence-bound",
                     "unsupported-owner-quotes", "unknown-field", "wrong-type"))
AGENT_LEARNING_ELIGIBILITY_TEST = "TestBotAgentLearningEligibilityHTTPAcceptance"
AGENT_LEARNING_ELIGIBILITY_CASES = tuple(
    f"{scope}/{mode}/{scenario}" for scope in ("personal", "team")
    for mode in ("interactive", "autonomous")
    for scenario in ("partial-method", "unsupported-method", "rejected-method",
                     "supported-method", "preselection-only", "postselection-use"))
AGENT_OPTIONAL_SCHEMA_TEST = "TestBotAgentOptionalWorkSchemaHTTPAcceptance"
AGENT_OPTIONAL_SCHEMA_CASES = tuple(
    f"{mode}/{tool}" for mode in ("interactive", "autonomous")
    for tool in ("bot_work_read", "bot_work_update", "bot_evidence_read", "bot_check_work"))
# The base learning suite includes sequential capture, fresh reuse, owner
# correction/replacement, one-off and no-op feedback, and corrected reuse in
# each scope/mode leaf. The other suites have independently required leaves.
AGENT_EXTRA_TESTS = {
    "learning": ((AGENT_LEARNING_REJECTION_TEST, AGENT_LEARNING_REJECTION_CASES),
                 (AGENT_LEARNING_ELIGIBILITY_TEST, AGENT_LEARNING_ELIGIBILITY_CASES),
                 (AGENT_OPTIONAL_SCHEMA_TEST, AGENT_OPTIONAL_SCHEMA_CASES)),
    "progress": ((AGENT_CHECK_TEST, AGENT_CHECK_CASES),
                 (AGENT_SOURCE_TEST, AGENT_SOURCE_CASES)),
}


class AgentEvidence(ExactGoEvidence):
    def __init__(self, stage):
        package, test, cases = AGENT_TESTS[stage]
        self.required_names = {test, *(f"{test}/{case}" for case in cases)}
        for extra_test, extra_cases in AGENT_EXTRA_TESTS.get(stage, ()):
            self.required_names.update({extra_test, *(
                f"{extra_test}/{case}" for case in extra_cases)})
        super().__init__(package.removeprefix("./"), self.required_names)


# Explicit manifests derived from the Go entry points, not a broad name regex:
# a missing/renamed case must fail rather than silently shrink acceptance.
AGENT_CONTINUITY_TESTS = (
    ("pkg/api", "TestBotAgentContinuityHTTPAcceptance",
     ("sqlite-restart-once", "cancel-before-restart-no-replay")),
    ("pkg/daemon", "TestBotAgentScheduleHTTPAcceptance", tuple(
        f"{scope}/{scenario}" for scope in ("personal", "team")
        for scenario in ("create-schedule", "duplicate-wake", "feedback-latest",
                         "remaining-fresh-occurrences-after-restart", "denied-resource", "task-once",
                         "remove-then-manual", "import-rollback", "foreign-import", "legacy-import"))),
)
# Exact implemented integration entry points and leaves. Legacy skill tests are
# NOT a substitute for agent CAS/projection/reopen and restore/quiescence proof.
AGENT_POSTGRES_TESTS = (
    ("pkg/store/entstore", "TestBotAgentPostgresContinuityCASProjection", ("personal", "team")),
    ("pkg/store/entstore", "TestBotAgentPostgresContinuityRestoreQuiescence", tuple(
        f"{scope}/{protocol}/{state}" for scope in ("personal", "team")
        for protocol in ("agent", "legacy") for state in ("running", "waiting"))),
)


def agent_postgres_missing_tests():
    source = "\n".join(path.read_text() for path in
                       (ROOT / "pkg/store/entstore").glob("*_test.go"))
    return [test for _, test, _ in AGENT_POSTGRES_TESTS
            if not re.search(r"(?m)^func " + re.escape(test) + r"\(t \*testing\.T\)", source)]


def run_agent_continuity(stage, source):
    env = skills_env(source)
    suites = AGENT_CONTINUITY_TESTS
    tags = None
    if stage == "postgres":
        try:
            mode = postgres_mode(source)
        except ValueError as error:
            print('BLOCKED: ' + str(error), file=sys.stderr)
            return 2
        missing = agent_postgres_missing_tests()
        if missing:
            print("BLOCKED: missing agent PostgreSQL integration tests: " +
                  ", ".join(missing), file=sys.stderr)
            return 2
        if mode == 'owned':
            return run_owned_postgres([
                sys.executable, '-B', str(ROOT / 'tests/e2eboot/employee_http_acceptance.py'),
                '--agent-stage', 'postgres'], env)
        env.update(ASTONISH_TEST_DSN=source["ASTONISH_TEST_DSN"],
                   ASTONISH_BOTS_TEST_DB_ISOLATED="1",
                   ASTONISH_REQUIRE_SKILL_POSTGRES="1")
        suites, tags = AGENT_POSTGRES_TESTS, "integration"
    for package, test, cases in suites:
        required = {test, *(f"{test}/{case}" for case in cases)}
        if not run(go_command("./" + package, (test,), tags), env,
                   ExactGoEvidence(package, required)):
            return 1
    return 0


def run_agent_stage(stage, source):
    if stage in ('continuity', 'postgres'):
        return run_agent_continuity(stage, source)
    package, test, _ = AGENT_TESTS[stage]
    tests = (test, *(name for name, _ in AGENT_EXTRA_TESTS.get(stage, ())))
    return 0 if run(go_command(package, tests), skills_env(source),
                    AgentEvidence(stage)) else 1


def agent_all_environment(config_path, source):
    """Preflight only: no daemon discovery, credential fallback or provider call."""
    from local_bot_smoke_launcher import config_environment, require_agent_config
    from quality_live_contract import strict_json
    if (not config_path.is_absolute() or config_path.is_symlink()
            or not config_path.is_file() or config_path.stat().st_mode & 0o077):
        raise ValueError('private absolute agent config required')
    config = strict_json(config_path.read_text())
    require_agent_config(config)
    configured = config_environment(config)
    # config_environment also contains ambient values and private provider data:
    # only the explicitly configured browser goes into controlled subprocesses.
    env = skills_env(source)
    env['ASTONISH_BOT_BROWSER_PATH'] = configured['ASTONISH_BOT_BROWSER_PATH']
    return env


def run_agent_all(config_path, source):
    try:
        env = agent_all_environment(config_path, source)
    except Exception:
        # JSON/OS/validator errors can contain credentials or secret-bearing paths.
        print('BLOCKED: explicit agent config preflight failed; private version 1 '
              'config with exact intended provider/model and local assets required', file=sys.stderr)
        return 2
    try:
        # With neither variable present, the postgres stage provisions its own DB.
        # Partial configuration is still rejected before controlled or paid calls.
        postgres_mode(source)
    except ValueError as error:
        print('BLOCKED: ' + str(error), file=sys.stderr)
        return 2
    missing = agent_postgres_missing_tests()
    if missing:
        print('BLOCKED: missing agent PostgreSQL integration tests: ' +
              ', '.join(missing), file=sys.stderr)
        return 2
    # No saved receipts are consumed. Each stage creates its evidence collector;
    # all Go commands use -count=1 and fail on missing, skipped or replayed cases.
    for stage in ('execution', 'progress', 'learning', 'continuity', 'postgres'):
        result = run_agent_stage(stage, source)
        if result:
            return result
    if not run(['npm', 'run', 'build'], env, cwd=ROOT / 'web'):
        return 1
    if not run([sys.executable, str(ROOT / 'tests/e2eboot/bot_skill_surface_acceptance.py'),
                '--browser', '--terminal', '--agent'], env):
        return 1
    # Fresh owned candidate only: no --state, diagnostic replay or legacy mode.
    # Launcher is responsible for validating this invocation's live receipts.
    if not run([sys.executable, str(ROOT / 'tests/e2eboot/local_bot_smoke_launcher.py'),
                '--verify-bot-agent', '--config', str(config_path)], env, timeout=4000):
        return 1
    print(json.dumps({'agent_stage': 'all', 'status': 'passed',
                      'full_agent_acceptance': True}))
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    stages = parser.add_mutually_exclusive_group()
    stages.add_argument("--agent-stage", choices=tuple(AGENT_TESTS)+('continuity', 'postgres', 'all'))
    stages.add_argument("--quality-stage", choices=tuple(QUALITY_TESTS))
    stages.add_argument("--skills-stage", choices=tuple(SKILLS_TESTS)+('all', 'recovery', 'postgres'))
    parser.add_argument('--config', type=Path, help='explicit isolated configuration for --agent-stage all or --skills-stage all')
    args = parser.parse_args(argv)
    if args.skills_stage in ('all', 'recovery', 'postgres'):
        from skills_acceptance_driver import main as skills_main
        return skills_main(['--skills-stage', args.skills_stage] + (['--config', str(args.config)] if args.config else []))
    if args.agent_stage == 'all':
        if not args.config:
            parser.error('--agent-stage all requires --config')
        return run_agent_all(args.config, os.environ)
    if args.config:
        parser.error('--config requires --agent-stage all or --skills-stage all')
    if args.agent_stage:
        return run_agent_stage(args.agent_stage, os.environ)
    if args.skills_stage:
        package, test, _ = SKILLS_TESTS[args.skills_stage]
        return 0 if run(go_command(package, (test,)), skills_env(os.environ),
                        SkillsEvidence(args.skills_stage)) else 1
    if args.quality_stage:
        package, test, _ = QUALITY_TESTS[args.quality_stage]
        tests = (test, CONTENT_TEST) if args.quality_stage == "runtime" else (test,)
        return 0 if run(go_command(package, tests), quality_env(args.quality_stage, os.environ),
                        QualityEvidence(args.quality_stage)) else 1
    return 0 if run(go_command("./pkg/daemon", TESTS), clean_env(os.environ),
                    EmployeeHTTPEvidence()) else 1


if __name__ == "__main__":
    sys.exit(main())
