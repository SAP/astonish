"""Implementation for bots_acceptance.sh; stdlib only, safe to import in tests."""
import argparse
import json
import os
from pathlib import Path
import queue
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
IMAGE = "ghcr.io/sap/astonish-sandbox-base:dev"
BROWSER = ["TestBotTitleChatBrowserAcceptance", "TestBotAmbiguousTitleQuestionBrowserAcceptance",
           "TestBotThinkingRestoreBrowserAcceptance", "TestBotWorkspaceBrowserAcceptance", "TestBotScheduledWorkspaceBrowserAcceptance",
           "TestBotChatFirstWorkspaceBrowserAcceptance", "TestBotChatFirstOrderedConversationBrowserAcceptance",
           "TestBotChatFirstConversationHTTPAcceptance", "TestBotProposalRoutesStrictHTTP",
           "TestBotSourceIntakeProposalHTTPAcceptance", "TestBotSourceIntakeAdaptiveHTTP",
           "TestBotSourceIntakeTemplateHTTP",
           "TestChatChronologyHTTPAcceptance_Browser", "TestMemoryHTTPAcceptance_Browser"]
CLI = ["TestBotTitleChatPTYAcceptance", "TestBotCLIAndPTYAcceptance", "TestBotParityPTYAcceptance", "TestBotBareCodePTYAcceptance",
       "TestBotCLIExplicitIntentSubprocessAcceptance", "TestBotCLIIntentAmbiguousNameSubprocessAcceptance",
       "TestBotCLIIntentLocalGatingSubprocessAcceptance", "TestBotPTYTerminalQueries",
       "TestBotsCLIExplicitIntents", "TestBotsCLIIntentStateAndServerErrors",
       "TestBotsCLIUnknownCommandDoesNotResolve", "TestBotsStopRun", "TestBotChatFlagBoundaries"]
DAEMON_HTTP = ["TestBotAuthorizedScheduleHTTPAcceptance", "TestBotEmployeeOperationsHTTPAcceptance"]
PG = ["TestIntegrationPostgresBotEmployeeOperations", "TestIntegrationPostgresBotChatModel", "TestIntegrationPostgresBotTitleStartState",
      "TestIntegrationBotPostgresBackupRecovery", "TestIntegrationPostgresBotServicesSpendRace",
      "TestIntegrationPostgresBotServicesSpendSettlementAndCaps", "TestIntegrationPostgresBotServicesCorrection",
      "TestIntegrationPostgresBotServicesCleanup"]
GATES = ["make-test", "frontend-typecheck", "frontend-build", "frontend-lint", "make-lint",
         "browser", "daemon-http", "cli-pty", "sqlite", "postgres", "docker-home", "kubernetes-home", "spec-coverage"]
LIVE_BOTS = ["TestBotsLiveLifecycle", "TestBotsLiveKubernetesRetainedHome"]
RELEASE_SCENARIOS = {f"BOTS-{number:03d}" for number in range(1, 11)}


def redact(text, env):
    secrets = []
    for key, value in env.items():
        if value and any(token in key for token in ("DSN", "PASSWORD", "TOKEN", "API_KEY", "MASTER_KEY", "SECRET")):
            secrets.append(value)
            if "DSN" in key:
                try:
                    parsed = urlsplit(value)
                    secrets.extend([parsed.password or "", unquote(parsed.password or "")])
                except ValueError:
                    pass
                for quoted, plain in re.findall(r"password\s*=\s*'([^']*)'|password\s*=\s*([^\s]+)", value):
                    secrets.append(quoted or plain)
    for secret in sorted((s for s in secrets if isinstance(s, str) and s), key=len, reverse=True):
        text = text.replace(secret, "[REDACTED]")
    text = re.sub(r"(?i)(postgres(?:ql)?://)[^\s\"']+", r"\1[REDACTED]", text)
    return text


def clean_env(source):
    # Explicit allowlist: no inherited production ASTONISH_DSN/config or MAKEFLAGS/GOFLAGS.
    names = {"PATH", "HOME", "TMPDIR", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TERM",
             "GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOPROXY", "GONOSUMDB", "GOPRIVATE",
             "CGO_ENABLED", "CC", "CXX", "PKG_CONFIG_PATH", "DOCKER_HOST", "DOCKER_CONTEXT",
             "DOCKER_CONFIG", "KUBECONFIG", "ASTONISH_TEST_DSN", "ASTONISH_SANDBOX_IMAGE",
             "ASTONISH_BOT_HOME_TEST_LAYERS", "ASTONISH_E2E_SANDBOX_NAMESPACE",
             "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE", "ASTONISH_E2E_DEDICATED", "ASTONISH_E2E_HOMES_PVC",
             "ASTONISH_BOT_BROWSER_PATH", "ASTONISH_MEMORY_BROWSER_PATH", "ASTONISH_STREAM_BROWSER_PATH",
             "BIFROST_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY",
             "ASTONISH_BOTS_K8S_CONTEXT", "ASTONISH_BOTS_K8S_ISOLATED",
             "ASTONISH_BOTS_TEST_DB_ISOLATED", "ASTONISH_PG_ACTIVATION_DISPOSABLE"}
    return {k: v for k, v in source.items() if k in names or k.startswith("ASTONISH_BOTS_LIVE_")}


def prerequisites(env, local=False):
    errors = []
    for tool in ("go", "node", "npm", "make", "golangci-lint") + (() if local else ("docker", "kubectl")):
        if not shutil.which(tool, path=env.get("PATH")):
            errors.append(tool + " must be installed on PATH")
    browser = env.get("ASTONISH_BOT_BROWSER_PATH", "")
    if not browser or not Path(browser).is_absolute() or not os.access(browser, os.X_OK):
        errors.append("ASTONISH_BOT_BROWSER_PATH must name an absolute executable browser")
    if not (ROOT / "web/node_modules").is_dir():
        errors.append("web/node_modules missing; install frontend dependencies explicitly")
    if local:
        return errors
    if env.get("ASTONISH_PG_ACTIVATION_DISPOSABLE") != "1":
        errors.append("ASTONISH_PG_ACTIVATION_DISPOSABLE=1 required for dedicated Chat-model PostgreSQL transactions")
    if env.get("ASTONISH_BOTS_TEST_DB_ISOLATED") != "1":
        errors.append("ASTONISH_BOTS_TEST_DB_ISOLATED=1 required: dedicated disposable PostgreSQL instance, never production")
    dsn = env.get("ASTONISH_TEST_DSN", "")
    if not dsn.strip():
        errors.append("ASTONISH_TEST_DSN required (isolated admin-capable PostgreSQL test DSN)")
    if env.get("ASTONISH_SANDBOX_IMAGE") != IMAGE:
        errors.append("ASTONISH_SANDBOX_IMAGE must explicitly equal " + IMAGE)
    layers = Path(env.get("ASTONISH_BOT_HOME_TEST_LAYERS", ""))
    if not layers.is_absolute() or not (layers / "@base/rootfs/bin/sh").is_file():
        errors.append("ASTONISH_BOT_HOME_TEST_LAYERS must be absolute and contain @base/rootfs/bin/sh")
    if env.get("ASTONISH_BOTS_K8S_ISOLATED") != "1":
        errors.append("ASTONISH_BOTS_K8S_ISOLATED=1 required for the pre-provisioned acceptance namespace")
    if not env.get("ASTONISH_BOTS_K8S_CONTEXT"):
        errors.append("ASTONISH_BOTS_K8S_CONTEXT required; no implicit current-context selection")
    if not env.get("ASTONISH_E2E_SANDBOX_NAMESPACE"):
        errors.append("ASTONISH_E2E_SANDBOX_NAMESPACE required; no default/shared namespace")
    if env.get("ASTONISH_E2E_DEDICATED") != "1":
        errors.append("ASTONISH_E2E_DEDICATED=1 required by the shared safety harness")
    for key in ("ASTONISH_E2E_SANDBOX_NAMESPACE", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"):
        ns = env.get(key, "")
        if not (key.endswith("CONTROL_PLANE_NAMESPACE") and ns == "astonishe2e") and not re.fullmatch(r"(astonishe2e|astonish-e2e)-[a-z0-9]([-a-z0-9]*[a-z0-9])?", ns):
            errors.append(key + " must name an explicit dedicated acceptance namespace")
    if env.get("ASTONISH_E2E_SANDBOX_NAMESPACE") == env.get("ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"):
        errors.append("sandbox and control-plane namespaces must differ")
    if not env.get("KUBECONFIG") or not Path(env["KUBECONFIG"]).is_file():
        errors.append("KUBECONFIG must name an explicit dedicated cluster config file")
    if env.get("ASTONISH_E2E_HOMES_PVC", "") in ("", "astonish-layers", "astonish-uppers"):
        errors.append("ASTONISH_E2E_HOMES_PVC must name an independent pre-provisioned Bound RWX claim")
    if not any(env.get(key) for key in ("BIFROST_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY")):
        errors.append("a supported provider API key is required by the live Kubernetes bootstrap")
    return errors


class GoEvidence:
    def __init__(self, required):
        self.required = set(required)
        self.passed = set()
        self.failed = []
        self.ran = set()
        self.qualified_runs = set()
        self.qualified_passes = set()
        self.packages_passed = set()

    def feed(self, line):
        try:
            event = json.loads(line)
        except (ValueError, TypeError):
            return
        if not isinstance(event, dict):
            return
        name, action = event.get("Test"), event.get("Action")
        package = event.get("Package")
        if isinstance(package, str) and package:
            key = (package, name)
            if name and action == "run":
                self.qualified_runs.add(key)
            if name and action == "pass" and key in self.qualified_runs:
                self.qualified_passes.add(key)
            if not name and action == "pass":
                self.packages_passed.add(package)
        if name and action == "run":
            self.ran.add(name)
        if name and action == "pass":
            self.passed.add(name)
        if action in ("skip", "fail"):
            self.failed.append((name or event.get("Package", "package"), action))

    def errors(self):
        errors = ["requested test did not pass: " + n for n in sorted(self.required - (self.passed & self.ran))]
        errors += ["executed test did not finish: " + n for n in sorted(self.ran - self.passed)]
        errors += [n + ": " + state for n, state in self.failed]
        if not self.ran:
            errors.append("no matching tests executed")
        return errors


def run(command, env, evidence=None, timeout=1200, cwd=ROOT):
    print("COMMAND " + redact(shlex.join([str(x) for x in command]), env), flush=True)
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, text=True, errors="replace", start_new_session=True)
    lines = queue.Queue()
    def consume():
        for line in process.stdout:
            lines.put(line)
        lines.put(None)
    threading.Thread(target=consume, daemon=True).start()
    start = time.monotonic()
    try:
        while True:
            if time.monotonic() - start > timeout:
                raise TimeoutError("gate exceeded timeout")
            try:
                line = lines.get(timeout=min(30, timeout))
            except queue.Empty:
                print("RUNNING (waiting for command output)", flush=True)
                continue
            if line is None:
                break
            if evidence:
                evidence.feed(line)
            print(redact(line.rstrip(), env), flush=True)
        code = process.wait(timeout=10)
    finally:
        if process.poll() is None:
            # Only this driver's newly created process group; never global cleanup.
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        process.stdout.close()
    errors = evidence.errors() if evidence else []
    for error in errors:
        print("EVIDENCE FAILURE " + redact(error, env), flush=True)
    return code == 0 and not errors


def go_command(package, names, tags=None, pattern=None):
    if not names or any(not name for name in names):
        raise ValueError("a gate must require named tests; empty selectors are not acceptance")
    command = ["go", "test", "-json", "-count=1", "-p", "1", "-timeout=15m"]
    if tags:
        command += ["-tags=" + tags]
    return command + [package, "-run", pattern or "^(" + "|".join(names) + ")$"]


def sanitized_makefile(source):
    # Preserve the actual repository targets, omitting ONLY production auto-loads.
    return "\n".join(line for line in source.splitlines()
                     if line.strip() not in ("-include .env", "-include .env.local")) + "\n"


def scenario_coverage_errors(evidences, root=ROOT):
    """Fail closed on catalog gaps and require this invocation's exact Go evidence.

    report.mjs has no exported parser/JSON interface and --strict only checks
    drift, not missing scenarios. Mirror its flat catalog format here, but do
    not trust its optional historical .last-run.json or COVERS comments as proof.
    """
    try:
        catalog = (root / "tests/scenarios/bots.yaml").read_text()
        module = re.search(r"^module\s+(\S+)", (root / "go.mod").read_text(), re.M).group(1)
    except (OSError, AttributeError) as error:
        return ["cannot read Bot scenario catalog/module: " + str(error)]
    scenarios = []
    current = None
    reading_refs = False
    for line in catalog.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        item = re.fullmatch(r'  - id: (BOTS-\d+)', line)
        if item:
            current = {"id": item[1], "status": None, "refs": []}
            scenarios.append(current)
            reading_refs = False
        elif current is not None:
            status = re.fullmatch(r'    status: (\w+)', line)
            if status:
                current["status"] = status[1]
                reading_refs = False
            elif line.startswith("    test_refs:"):
                reading_refs = line == "    test_refs:"
                if line not in ("    test_refs:", "    test_refs: []"):
                    current["refs"].append("invalid test_refs format")
            elif reading_refs:
                ref = re.fullmatch(r'      - "([^"\n]+)"', line)
                if ref:
                    current["refs"].append(ref[1])
                else:
                    current["refs"].append("invalid test_refs format")
    errors = []
    # Count independently so malformed/removed entries cannot shrink the gate.
    ids = [scenario["id"] for scenario in scenarios]
    for missing in sorted(RELEASE_SCENARIOS - set(ids)):
        errors.append(missing + ": required scenario absent")
    if len(ids) != len(set(ids)) or len(re.findall(r"^\s+-\s+id:", catalog, re.M)) != len(ids):
        errors.append("duplicate or malformed scenario IDs")
    for scenario in scenarios:
        label = scenario["id"] + ": "
        if scenario["status"] != "covered":
            errors.append(label + "status is not covered (" + str(scenario["status"]) + ")")
        if not scenario["refs"]:
            errors.append(label + "no deployed test_refs")
        for ref in scenario["refs"]:
            match = re.fullmatch(r"(tests/e2e/[\w/-]+_test\.go)::(Test\w+(?:/[\w/-]+)?)", ref)
            if not match:
                errors.append(label + "invalid deployed test_ref: " + ref)
                continue
            file, test = match.groups()
            try:
                source = (root / file).read_text()
            except OSError:
                source = ""
            if not re.search(r"^func\s+" + re.escape(test.split('/')[0]) + r"\s*\(\s*\w+\s+\*testing\.T\s*\)", source, re.M):
                errors.append(label + "test_ref has no real Go test: " + ref)
                continue
            package = module + "/" + str(Path(file).parent)
            key = (package, test)
            if not any(key in evidence.qualified_passes and package in evidence.packages_passed
                       and not evidence.errors() for evidence in evidences):
                errors.append(label + "no current non-skipped executed evidence: " + ref)
    return errors


def main(argv=None):
    parser = argparse.ArgumentParser(description="Strict Bot release gate; no deployment or credential loading")
    parser.add_argument("--preflight", action="store_true", help="check prerequisites only; not acceptance")
    parser.add_argument("--local-only", action="store_true", help="partial local evidence; always non-release exit 3")
    stages = parser.add_mutually_exclusive_group()
    stages.add_argument('--skills-stage', choices=('all', 'recovery', 'postgres'), help='strict skills acceptance (separate from full Bot release)')
    stages.add_argument('--agent-stage', choices=('execution', 'progress', 'learning', 'continuity', 'postgres', 'all'), help='strict agent acceptance (separate from full Bot release)')
    parser.add_argument('--config', type=Path, help='explicit isolated skills or agent launcher configuration')
    args = parser.parse_args(argv)
    if args.agent_stage:
        if args.preflight or args.local_only:
            parser.error('--agent-stage forbids --preflight and --local-only')
        from employee_http_acceptance import main as agent_main
        return agent_main(['--agent-stage', args.agent_stage] + (['--config', str(args.config)] if args.config else []))
    if args.skills_stage:
        if args.preflight or args.local_only:
            parser.error('--skills-stage forbids --preflight and --local-only')
        from skills_acceptance_driver import main as skills_main
        return skills_main(['--skills-stage', args.skills_stage] + (['--config', str(args.config)] if args.config else []))
    if args.config:
        parser.error('--config requires --skills-stage all')
    env = clean_env(os.environ)
    statuses = {gate: "NOT RUN" for gate in GATES}
    outcome = "NOT RELEASE READY"
    try:
        errors = prerequisites(env, args.local_only)
        if errors:
            for error in errors:
                print("BLOCKED: " + error)
            return 2
        # Read-only infrastructure probes, never create namespaces/pull images.
        if not args.local_only:
            current = subprocess.run(["kubectl", "config", "current-context"], env=env, capture_output=True, text=True, timeout=20)
            if current.returncode or current.stdout.strip() != env["ASTONISH_BOTS_K8S_CONTEXT"]:
                print("BLOCKED: kubectl current-context must match ASTONISH_BOTS_K8S_CONTEXT; driver never changes contexts")
                return 2
            probes = [["docker", "info", "--format", "{{.ServerVersion}}"],
                      ["docker", "image", "inspect", "--format", "{{.Id}}", IMAGE],
                      ["kubectl", "--context", env["ASTONISH_BOTS_K8S_CONTEXT"], "--request-timeout=15s",
                       "get", "namespace", env["ASTONISH_E2E_SANDBOX_NAMESPACE"]]]
            for probe in probes:
                if not run(probe, env, timeout=30):
                    print("BLOCKED: infrastructure probe failed")
                    return 2
        if args.preflight:
            print("PREREQUISITES CHECKED ONLY; no acceptance tests executed")
            return 0
        # No live/integration flag inherited by make test; opt-in tests have dedicated strict gates below.
        # Local gates must not initialize deployment clients from inherited cluster settings.
        # Keep env intact: prerequisites and the live Kubernetes gate require that config.
        deployment_keys = {"ASTONISH_PG_ACTIVATION_DISPOSABLE", "ASTONISH_TEST_DSN", "KUBECONFIG", "ASTONISH_BOTS_K8S_CONTEXT",
                           "ASTONISH_BOTS_K8S_ISOLATED", "ASTONISH_E2E_SANDBOX_NAMESPACE",
                           "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE", "ASTONISH_E2E_DEDICATED",
                           "ASTONISH_E2E_HOMES_PVC"}
        unit_env = {k: v for k, v in env.items() if k not in deployment_keys}
        with tempfile.TemporaryDirectory(prefix="astonish-bots-acceptance-") as temporary:
            makefile = Path(temporary) / "Makefile"
            makefile.write_text(sanitized_makefile((ROOT / "Makefile").read_text()))
            commands = [
                ("make-test", ["make", "-f", str(makefile), "test"], unit_env, None),
                ("frontend-typecheck", ["npm", "--prefix", "web", "run", "typecheck"], unit_env, None),
                ("frontend-build", ["npm", "--prefix", "web", "run", "build"], unit_env, None),
                ("frontend-lint", ["npm", "--prefix", "web", "run", "lint"], unit_env, None),
                ("make-lint", ["make", "-f", str(makefile), "lint"], unit_env, None),
            ]
            browser_env = dict(unit_env, ASTONISH_BOT_BROWSER_ACCEPTANCE="1", ASTONISH_MEMORY_BROWSER_ACCEPTANCE="1",
                               ASTONISH_STREAM_BROWSER_ACCEPTANCE="1", ASTONISH_MEMORY_BROWSER_PATH=env["ASTONISH_BOT_BROWSER_PATH"],
                               ASTONISH_STREAM_BROWSER_PATH=env["ASTONISH_BOT_BROWSER_PATH"])
            commands += [("browser", go_command("./pkg/api", BROWSER, pattern="^(" + "|".join(BROWSER) + "|TestChatChronologyHTTPAcceptance_.*|TestMemoryHTTPAcceptance_.*)$"), browser_env, GoEvidence(BROWSER)),
                         ("daemon-http", go_command("./pkg/daemon", DAEMON_HTTP), unit_env, GoEvidence(DAEMON_HTTP)),
                         ("cli-pty", go_command("./cmd/astonish", CLI), unit_env, GoEvidence(CLI)),
                         ("sqlite", go_command("./pkg/store/entstore", ["TestBotHomeBindingPersistence", "TestBotScheduledTickSQLite"], pattern="^Test.*Bot"), unit_env, GoEvidence(["TestBotHomeBindingPersistence", "TestBotScheduledTickSQLite"]))]
            if not args.local_only:
                commands += [("postgres", go_command("./pkg/store/entstore", PG, "integration"), env, GoEvidence(PG)),
                             ("docker-home", go_command("./pkg/sandbox/docker", ["TestBotHomeDockerPersistenceLive"]), dict(env, ASTONISH_TEST_DOCKER="1"), GoEvidence(["TestBotHomeDockerPersistenceLive"])),
                             ("kubernetes-home", go_command("./tests/e2e/bots", LIVE_BOTS, "e2e"), dict(env, ASTONISH_E2E_SANDBOX_BACKEND="k8s"), GoEvidence(LIVE_BOTS))]
            successful_evidence = []
            for name, command, child_env, evidence in commands:
                statuses[name] = "RUNNING"
                try:
                    statuses[name] = "PASS" if run(command, child_env, evidence) else "FAIL"
                except (OSError, TimeoutError) as error:
                    statuses[name] = "FAIL"
                    print("ERROR " + redact(str(error), env))
                if statuses[name] == "PASS" and evidence is not None:
                    successful_evidence.append(evidence)
                # Continue independent gates: lint failure is visible, never suppressed.
            coverage_errors = scenario_coverage_errors(successful_evidence)
            statuses["spec-coverage"] = "FAIL" if coverage_errors else "PASS"
            if coverage_errors:
                print("GATE FAILED: full lifecycle acceptance not implemented/evidenced")
                for error in coverage_errors:
                    print("COVERAGE FAILURE " + redact(error, env))
            # Local collection remains useful and retains its documented exit 3;
            # this is NOT a waiver: the full release gate still explicitly fails.
            if any(value == "FAIL" for name, value in statuses.items() if name != "spec-coverage"):
                return 1
            if args.local_only:
                outcome = "PARTIAL LOCAL EVIDENCE ONLY — NOT RELEASE READY"
                return 3
            if all(value == "PASS" for value in statuses.values()):
                outcome = "FULL BOT RELEASE ACCEPTANCE PASSED (OpenShell explicitly deferred)"
                return 0
            return 1
    except (KeyboardInterrupt, OSError, TimeoutError) as error:
        print("ABORTED " + redact(str(error), env))
        return 1
    finally:
        print("\nBOT ACCEPTANCE SUMMARY")
        for name, status in statuses.items():
            print(name + ": " + status)
        print("OpenShell: DEFERRED (not a release gate)")
        print(outcome, flush=True)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    sys.exit(main())
