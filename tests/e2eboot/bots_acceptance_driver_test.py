import contextlib
import importlib.util
import io
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("driver", Path(__file__).with_name("bots_acceptance_driver.py"))
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


class AcceptanceDriverTests(unittest.TestCase):
    def test_agent_stage_routes_exact_arguments_and_propagates_status(self):
        for stage in ('execution', 'progress', 'learning', 'continuity', 'postgres', 'all'):
            argv = ['--agent-stage', stage]
            if stage == 'all':
                argv += ['--config', '/explicit/private-agent.json']
            for result in (0, 1, 2):
                with self.subTest(stage=stage, result=result), patch(
                        'employee_http_acceptance.main', return_value=result) as agent, patch.object(
                        driver, 'run') as run, patch.object(driver, 'prerequisites') as prerequisites:
                    self.assertEqual(result, driver.main(argv))
                    agent.assert_called_once_with(argv)
                    run.assert_not_called()
                    prerequisites.assert_not_called()

    def test_agent_cannot_use_partial_release_flags_or_other_modes(self):
        for extra in (['--preflight'], ['--local-only'], ['--skills-stage', 'all']):
            with self.subTest(extra=extra), patch('employee_http_acceptance.main') as agent, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as error:
                    driver.main(['--agent-stage', 'all', '--config', '/explicit/config.json'] + extra)
                self.assertEqual(2, error.exception.code)
                agent.assert_not_called()

    def test_agent_missing_config_fails_without_release_fallback(self):
        with patch.object(driver, 'run') as run, contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                driver.main(['--agent-stage', 'all'])
            self.assertEqual(2, error.exception.code)
            run.assert_not_called()

    def test_agent_all_docs_keep_current_invocation_and_partial_boundary(self):
        text = (driver.ROOT / 'tests/e2eboot/README.bots-acceptance.md').read_text()
        for required in ('--agent-stage all', '--verify-bot-agent', '--browser --terminal --agent',
                         'ASTONISH_BOTS_TEST_DB_ISOLATED=1', 'No historical receipts', 'not Kubernetes'):
            self.assertIn(required, text)

    def test_employee_gates_require_real_named_tests_in_correct_package(self):
        command = driver.go_command("./pkg/daemon", driver.DAEMON_HTTP)
        self.assertIn("-json", command)
        self.assertIn("./pkg/daemon", command)
        for name in driver.DAEMON_HTTP:
            self.assertNotIn(name, driver.BROWSER)
            self.assertTrue(any(re.search(r"func " + name + r"\(", f.read_text())
                                for f in (driver.ROOT / "pkg/daemon").glob("*_test.go")))
        self.assertIn("TestIntegrationPostgresBotEmployeeOperations", driver.PG)
        with self.assertRaises(ValueError):
            driver.go_command("./pkg/api", [])
        evidence = driver.GoEvidence(driver.DAEMON_HTTP)
        evidence.feed(json.dumps({"Action": "pass", "Package": "wrong/package"}))
        self.assertTrue(evidence.errors())

    def test_corrected_chat_model_gates_and_docs(self):
        self.assertTrue({"TestBotTitleChatBrowserAcceptance",
                         "TestBotAmbiguousTitleQuestionBrowserAcceptance",
                         "TestBotThinkingRestoreBrowserAcceptance"} <= set(driver.BROWSER))
        self.assertIn("TestBotTitleChatPTYAcceptance", driver.CLI)
        self.assertIn("TestIntegrationPostgresBotChatModel", driver.PG)
        self.assertIn("TestIntegrationPostgresBotTitleStartState", driver.PG)
        self.assertEqual(driver.RELEASE_SCENARIOS, {f"BOTS-{n:03d}" for n in range(1, 11)})
        self.assertEqual(driver.clean_env({"ASTONISH_PG_ACTIVATION_DISPOSABLE": "1"}),
                         {"ASTONISH_PG_ACTIVATION_DISPOSABLE": "1"})
        for name in ("bots", "bot-chat-first", "bot-release-checklist", "bot-home-setup",
                     "testing", "studio-ui-system"):
            text = (driver.ROOT / "docs/architecture" / (name + ".md")).read_text()
            self.assertIn("pending", text, name)
            self.assertRegex(text, r"historical|Historical", name)
        activation = (driver.ROOT / "docs/architecture/bot-draft-activation.md").read_text()
        self.assertIn("not consent to recurring work", activation)

    # Scope deliberately excludes provider docs, README and the public website.
    NEUTRAL_DOCS = (
        "docs/research/bot-chat-first-ux.md",
        "docs/reports/astonish-bots-implementation-plan.md",
        "docs/architecture/agent-engine.md",
        "docs/architecture/code-intelligence.md",
        "docs/architecture/fleet-tool-efficiency.md",
        "docs/architecture/fleet-generic-orchestration.md",
        "docs/architecture/terminal-app.md",
        "docs/architecture/bot-chat-first.md",
    )

    def test_research_docs_are_product_neutral(self):
        forbidden = re.compile(
            r"\b(?:grok|muse|openclaude|openclaw|opencode|codex|perplexity)\b"
            r"|(?-i:\bCursor\b)"
            r"|claude[- ]code|docs\.x\.ai|muse\.ai|meta\.com/help/superintelligence"
            r"|(?:~/Downloads/system_prompt\.md|/tmp/[^\s`]*research|/root/(?:grok|openclaude|openclaw))",
            re.IGNORECASE,
        )
        for name in self.NEUTRAL_DOCS:
            with self.subTest(document=name):
                text = (driver.ROOT / name).read_text()
                self.assertIsNone(forbidden.search(text), name)

    def test_chat_first_research_preserves_limits_and_supersession(self):
        text = (driver.ROOT / self.NEUTRAL_DOCS[0]).read_text()
        for required in ("not a hands-on visual audit", "not independently verified",
                         "supersedes", "not implementation", "tenant isolation",
                         "explicit approval", "conversation"):
            self.assertIn(required, text)
        self.assertNotRegex(text, r"https?://|/tmp/|~/Downloads|(?m:^>\s*[“\"])")

    def test_neutral_document_links_have_existing_targets_and_anchors(self):
        targets = {(driver.ROOT / name).resolve() for name in self.NEUTRAL_DOCS}
        # Inspect all documentation inbound links, not only edited files.
        for source in (driver.ROOT / "docs").rglob("*.md"):
            if any(part in ("node_modules", ".vitepress") for part in source.parts):
                continue
            for link in re.findall(r"\]\(([^\s)]+)\)", source.read_text()):
                path, _, anchor = link.partition("#")
                if "://" in path or path.startswith("/"):
                    continue
                target = (source.parent / path).resolve() if path else source.resolve()
                if target not in targets:
                    continue
                self.assertTrue(target.is_file(), f"{source}: {link}")
                if anchor:
                    headings = re.findall(r"^#{1,6}\s+(.+)$", target.read_text(), re.MULTILINE)
                    slugs = {re.sub(r"[^\w\- ]", "", heading.lower()).replace(" ", "-")
                             for heading in headings}
                    self.assertIn(anchor, slugs, f"{source}: {link}")

    def test_empty_environment_fails_closed(self):
        errors = driver.prerequisites({})
        for key in ("ASTONISH_TEST_DSN", "ASTONISH_BOTS_TEST_DB_ISOLATED", "ASTONISH_BOT_BROWSER_PATH",
                    "ASTONISH_BOTS_K8S_CONTEXT", "ASTONISH_BOT_HOME_TEST_LAYERS"):
            self.assertTrue(any(key in error for error in errors), key)

    def test_shell_preflight_without_credentials_is_nonzero(self):
        result = subprocess.run(["bash", str(Path(__file__).with_name("bots_acceptance.sh")), "--preflight"],
                                env={"PATH": os.environ["PATH"], "HOME": os.environ.get("HOME", "/tmp")},
                                capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn("NOT RELEASE READY", result.stdout)
        self.assertIn("postgres: NOT RUN", result.stdout)
        self.assertIn("ASTONISH_BOTS_K8S_CONTEXT required", result.stdout)
        self.assertIn("KUBECONFIG must name", result.stdout)
        self.assertIn("kubernetes-home: NOT RUN", result.stdout)

    def test_missing_and_skipped_tests_never_pass(self):
        required_browser = {"TestBotChatFirstWorkspaceBrowserAcceptance",
                            "TestBotChatFirstOrderedConversationBrowserAcceptance",
                            "TestBotChatFirstConversationHTTPAcceptance",
                            "TestBotProposalRoutesStrictHTTP",
                            "TestBotSourceIntakeProposalHTTPAcceptance",
                            "TestBotSourceIntakeAdaptiveHTTP", "TestBotSourceIntakeTemplateHTTP",
                            "TestBotWorkspaceBrowserAcceptance", "TestBotScheduledWorkspaceBrowserAcceptance"}
        self.assertTrue(required_browser <= set(driver.BROWSER))
        self.assertIn("TestBotParityPTYAcceptance", driver.CLI)
        self.assertIn("TestBotCLIExplicitIntentSubprocessAcceptance", driver.CLI)
        for package, names in (("pkg/api", driver.BROWSER), ("cmd/astonish", driver.CLI)):
            source = "\n".join(p.read_text() for p in (driver.ROOT / package).glob("*test.go"))
            for name in names:
                self.assertRegex(source, rf"func {re.escape(name)}\(t \*testing.T\)")
                evidence = driver.GoEvidence(names)
                for selected in names:
                    evidence.feed(json.dumps({"Action": "run", "Test": selected}))
                    evidence.feed(json.dumps({"Action": "skip" if selected == name else "pass", "Test": selected}))
                self.assertTrue(evidence.errors(), name)
        for events in ([], [{"Action": "skip", "Test": "TestRequired"}],
                       [{"Action": "run", "Test": "TestRequired"}, {"Action": "pass", "Test": "TestRequired"},
                        {"Action": "skip", "Test": "TestRequired/child"}]):
            evidence = driver.GoEvidence(["TestRequired"])
            for event in events:
                evidence.feed(json.dumps(event))
            self.assertTrue(evidence.errors())

    def test_exact_required_test_and_package_failure(self):
        evidence = driver.GoEvidence(["TestRequired"])
        for action in ("run", "pass"):
            evidence.feed(json.dumps({"Action": action, "Test": "TestRequired"}))
        self.assertEqual(evidence.errors(), [])
        evidence.feed('{"Action":"fail","Package":"example"}')
        self.assertTrue(evidence.errors())

    def test_dsn_and_decoded_password_redaction(self):
        env = {"ASTONISH_TEST_DSN": "postgres://owner:private%21@host/db", "OPENAI_API_KEY": "sample-secret"}
        result = driver.redact("postgres://owner:private%21@host/db private! sample-secret", env)
        for secret in ("owner", "private", "sample-secret"):
            self.assertNotIn(secret, result)

    def test_makefile_does_not_read_env_and_preserves_lint(self):
        source = "-include .env\n-include .env.local\nexport\nlint:\n\tgolangci-lint run\n"
        result = driver.sanitized_makefile(source)
        self.assertNotIn("include", result)
        self.assertIn("golangci-lint run", result)

    def test_production_overrides_and_opt_ins_not_inherited(self):
        env = driver.clean_env({"ASTONISH_DSN": "production", "GOFLAGS": "-run=Nothing",
                                "MAKEFLAGS": "-i", "ASTONISH_TEST_DOCKER": "1", "ASTONISH_E2E_KEEP_ALIVE": "1"})
        self.assertEqual(env, {})

    def test_local_checks_exclude_deployment_kubernetes_environment(self):
        deployment = {
            "KUBECONFIG": "/missing/acceptance-kubeconfig",
            "ASTONISH_BOTS_K8S_CONTEXT": "isolated",
            "ASTONISH_BOTS_K8S_ISOLATED": "1",
            "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-sandbox",
            "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE": "astonish-e2e-control",
            "ASTONISH_E2E_DEDICATED": "1",
            "ASTONISH_E2E_HOMES_PVC": "acceptance-homes",
        }
        for args in (["--local-only"], []):
            with self.subTest(args=args), \
                 patch.dict(os.environ, dict(deployment, ASTONISH_BOT_BROWSER_PATH="/bin/sh"), clear=True), \
                 patch.object(driver, "prerequisites", return_value=[]), \
                 patch.object(driver.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "isolated\n")), \
                 patch.object(driver, "run", return_value=True) as commands, \
                 contextlib.redirect_stdout(io.StringIO()):
                driver.main(args)
                local = [call for call in commands.call_args_list
                         if call.args[0][0] in ("make", "npm") or
                         any(package in call.args[0] for package in ("./pkg/api", "./cmd/astonish", "./pkg/store/entstore"))
                         and "-tags=integration" not in call.args[0]]
                self.assertEqual(len(local), 8)
                for call in local:
                    self.assertFalse(set(deployment) & set(call.args[1]), call.args[0])
                for key, value in deployment.items():
                    self.assertEqual(os.environ[key], value)

    def test_full_kubernetes_gate_preserves_validated_config(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "kubeconfig"
            config.write_text("acceptance config fixture")
            env = {"KUBECONFIG": str(config), "ASTONISH_BOT_BROWSER_PATH": "/bin/sh",
                   "ASTONISH_BOTS_K8S_CONTEXT": "isolated",
                   "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-sandbox"}
            self.assertFalse(any("KUBECONFIG" in error for error in driver.prerequisites(env)))
            def validated(child_env, local):
                self.assertFalse(local)
                self.assertEqual(child_env["KUBECONFIG"], str(config))
                self.assertTrue(Path(child_env["KUBECONFIG"]).is_file())
                return []
            with patch.dict(os.environ, env, clear=True), \
                 patch.object(driver, "prerequisites", side_effect=validated) as checks, \
                 patch.object(driver.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "isolated\n")) as context, \
                 patch.object(driver, "run", return_value=True) as commands, \
                 contextlib.redirect_stdout(io.StringIO()):
                driver.main([])
            checks.assert_called_once()
            self.assertEqual(context.call_args.kwargs["env"]["KUBECONFIG"], str(config))
            live = [call for call in commands.call_args_list if "./tests/e2e/bots" in call.args[0]]
            self.assertEqual(len(live), 1)
            self.assertEqual(live[0].args[1]["KUBECONFIG"], str(config))
            self.assertEqual(live[0].args[1]["ASTONISH_E2E_SANDBOX_BACKEND"], "k8s")
            required = {"TestBotsLiveLifecycle", "TestBotsLiveKubernetesRetainedHome"}
            self.assertEqual(live[0].args[2].required, required)
            command = live[0].args[0]
            self.assertIn("-tags=e2e", command)
            self.assertEqual(command[command.index("-run") + 1],
                             "^(TestBotsLiveLifecycle|TestBotsLiveKubernetesRetainedHome)$")
            config.unlink()
            self.assertTrue(any("KUBECONFIG" in error for error in driver.prerequisites(env)))

    def test_live_gate_requires_both_non_skipped_names(self):
        required = {"TestBotsLiveLifecycle", "TestBotsLiveKubernetesRetainedHome"}
        self.assertEqual(set(driver.LIVE_BOTS), required)
        for absent in required:
            for state in ("missing", "skip", "pass"):
                with self.subTest(absent=absent, state=state):
                    evidence = driver.GoEvidence(driver.LIVE_BOTS)
                    for name in required:
                        if name == absent and state == "missing":
                            continue
                        for action in ("run", state if name == absent else "pass"):
                            evidence.feed(json.dumps({"Package": "example/tests/e2e/bots",
                                                      "Test": name, "Action": action}))
                    self.assertEqual(bool(evidence.errors()), state != "pass")

    def test_actual_catalog_remains_missing_after_partial_lifecycle(self):
        evidence = driver.GoEvidence(driver.LIVE_BOTS)
        for name in driver.LIVE_BOTS:
            for action in ("run", "pass"):
                evidence.feed(json.dumps({"Package": "example/tests/e2e/bots", "Test": name, "Action": action}))
        errors = driver.scenario_coverage_errors([evidence])
        self.assertEqual(len(errors), 20)
        for scenario in driver.RELEASE_SCENARIOS:
            self.assertIn(scenario + ": status is not covered (missing)", errors)
            self.assertIn(scenario + ": no deployed test_refs", errors)
        readme = Path(__file__).with_name("README.bots-acceptance.md").read_text()
        for name in driver.LIVE_BOTS:
            self.assertIn(name, readme)

    def test_local_success_remains_partial(self):
        with patch.dict(os.environ, {"ASTONISH_BOT_BROWSER_PATH": "/bin/sh"}, clear=True), \
             patch.object(driver, "prerequisites", return_value=[]), \
             patch.object(driver, "run", return_value=True) as commands, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(driver.main(["--local-only"]), 3)
        self.assertEqual(commands.call_count, 9)
        self.assertIn("spec-coverage: FAIL", output.getvalue())
        self.assertIn("NOT RELEASE READY", output.getvalue())
        self.assertIn("kubernetes-home: NOT RUN", output.getvalue())

    def test_components_passing_cannot_release_missing_spec_scenarios(self):
        env = {"ASTONISH_BOT_BROWSER_PATH": "/bin/sh", "ASTONISH_BOTS_K8S_CONTEXT": "isolated",
               "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-test"}
        with patch.dict(os.environ, env, clear=True), \
             patch.object(driver, "prerequisites", return_value=[]), \
             patch.object(driver.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "isolated\n")), \
             patch.object(driver, "run", return_value=True), contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(driver.main([]), 1)
        self.assertIn("kubernetes-home: PASS", output.getvalue())
        self.assertIn("spec-coverage: FAIL", output.getvalue())
        self.assertIn("full lifecycle acceptance not implemented/evidenced", output.getvalue())
        self.assertNotIn("FULL BOT RELEASE ACCEPTANCE PASSED", output.getvalue())

    def test_catalog_requires_real_refs_and_current_exact_non_skipped_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tests/scenarios").mkdir(parents=True)
            (root / "tests/e2e/bots").mkdir(parents=True)
            (root / "go.mod").write_text("module example\n")
            source = root / "tests/e2e/bots/lifecycle_test.go"
            source.write_text("func TestLifecycle(t *testing.T) {}\n")
            catalog = root / "tests/scenarios/bots.yaml"
            content = "scenarios:\n" + "".join(
                f'  - id: {sid}\n    status: covered\n    test_refs:\n'
                '      - "tests/e2e/bots/lifecycle_test.go::TestLifecycle"\n'
                for sid in sorted(driver.RELEASE_SCENARIOS))
            catalog.write_text(content)
            def evidence(package="example/tests/e2e/bots", actions=("run", "pass"), skip=False, finished=True):
                result = driver.GoEvidence(["TestLifecycle"])
                for action in actions:
                    result.feed(json.dumps({"Package": package, "Test": "TestLifecycle", "Action": action}))
                if skip:
                    result.feed(json.dumps({"Package": package, "Test": "TestLifecycle/child", "Action": "skip"}))
                if finished:
                    result.feed(json.dumps({"Package": package, "Action": "pass"}))
                return result
            self.assertEqual(driver.scenario_coverage_errors([evidence()], root), [])
            for bad in ([], [evidence(package="example/pkg/api")], [evidence(actions=("pass",))],
                        [evidence(skip=True)], [evidence(finished=False)]):
                self.assertTrue(driver.scenario_coverage_errors(bad, root))
            catalog.write_text(content.replace("status: covered", "status: missing", 1))
            self.assertTrue(driver.scenario_coverage_errors([evidence()], root))
            catalog.write_text(content.replace("BOTS-001", "BOTS-002", 1))
            self.assertTrue(driver.scenario_coverage_errors([evidence()], root))
            catalog.write_text(content)
            source.unlink()
            self.assertTrue(driver.scenario_coverage_errors([evidence()], root))
            catalog.write_text("scenarios:\n")
            self.assertEqual(len(driver.scenario_coverage_errors([], root)), 10)

    def test_actual_makefile_filter_removes_only_production_includes(self):
        source = (driver.ROOT / "Makefile").read_text()
        expected = [line for line in source.splitlines() if line.strip() not in ("-include .env", "-include .env.local")]
        self.assertEqual(driver.sanitized_makefile(source).splitlines(), expected)
        self.assertIn("test: test-unit", expected)
        self.assertIn("lint:", expected)

    def test_preflight_diagnostics_never_print_secret_values(self):
        env = {"ASTONISH_TEST_DSN": "secret-dsn", "OPENAI_API_KEY": "secret-provider",
               "ASTONISH_BOTS_LIVE_PASSWORD": "secret-live"}
        self.assertFalse(any("secret-" in error for error in driver.prerequisites(env)))
        self.assertNotIn("secret-", driver.redact("secret-dsn secret-provider secret-live", env))

    def test_real_subprocess_output_is_redacted_and_empty_go_gate_fails(self):
        env = dict(os.environ, ASTONISH_TEST_DSN="postgres://owner:sensitive@host/db")
        with contextlib.redirect_stdout(io.StringIO()) as output:
            ok = driver.run([sys.executable, "-c", "import os; print(os.environ['ASTONISH_TEST_DSN'])"],
                            env, driver.GoEvidence(["TestRequired"]), timeout=5)
        self.assertFalse(ok)
        self.assertNotIn("sensitive", output.getvalue())
        self.assertIn("no matching tests executed", output.getvalue())

    def test_connection_string_password_is_redacted(self):
        env = {"ASTONISH_TEST_DSN": "host=localhost password='sensitive phrase' dbname=postgres"}
        self.assertNotIn("sensitive", driver.redact("error sensitive phrase", env))

    def test_lint_failure_cannot_be_suppressed(self):
        def run(command, *_):
            return command[-1] != "lint"
        with patch.dict(os.environ, {"ASTONISH_BOT_BROWSER_PATH": "/bin/sh"}, clear=True), \
             patch.object(driver, "prerequisites", return_value=[]), patch.object(driver, "run", side_effect=run), \
             contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(driver.main(["--local-only"]), 1)
        self.assertIn("make-lint: FAIL", output.getvalue())
        self.assertIn("NOT RELEASE READY", output.getvalue())


if __name__ == "__main__":
    unittest.main()
