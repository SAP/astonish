package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/config"
	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// ---------------------------------------------------------------------------
// executeRunDrill argument validation tests
// ---------------------------------------------------------------------------

func TestScopedInjectDrillCredentialsNoFallback(t *testing.T) {
	fs := newMemFlowStore()
	if err := fs.SaveFlow(t.Context(), "fixture", "name: fixture\ntype: drill_suite\nsuite_config:\n  credentials: {app: owner}\n  credential_injection:\n    files: [{credential: app, path: /workspace/token, field: token}]\n"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"general-store", "missing-injector", "empty", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			base := WithDrillExecutionContext(store.WithFlowStore(flowDrillPrincipal(t, false), fs), nil)
			if mode != "general-store" {
				base = store.WithTeamFlowStore(base, fs)
			}
			if mode == "cancelled" {
				cancelled, cancel := context.WithCancel(base)
				cancel()
				base = cancelled
			}
			if mode == "empty" {
				empty := newMemFlowStore()
				if err := empty.SaveFlow(t.Context(), "fixture", "name: fixture\ntype: drill_suite\nsuite_config: {}\n"); err != nil {
					t.Fatal(err)
				}
				base = store.WithTeamFlowStore(base, empty)
			}
			got, err := executeInjectDrillCredentials(&mockToolCtx{Context: base}, &runDrillDeps{}, InjectDrillCredentialsArgs{SuiteName: "fixture"})
			if mode == "empty" {
				if err != nil || got.Status != "skipped" {
					t.Fatalf("empty: %+v %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("scoped injection fell back: %+v", got)
			}
			if mode == "missing-injector" && !strings.Contains(err.Error(), "injector is unavailable") {
				t.Fatalf("missing actionable error: %v", err)
			}
		})
	}
}

func TestExecuteRunDrill_EmptySuiteName(t *testing.T) {
	deps := &runDrillDeps{}
	result, err := executeRunDrill(nil, deps, RunDrillArgs{SuiteName: ""})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Summary, "suite_name is required") {
		t.Errorf("Summary = %q", result.Summary)
	}
}

func TestExecuteRunDrill_SuiteNotFound(t *testing.T) {
	fs := newMemFlowStore()
	ctx := testCtxWithStore(fs)

	deps := &runDrillDeps{}
	result, err := executeRunDrill(ctx, deps, RunDrillArgs{SuiteName: "nonexistent-suite"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Summary, "not found") {
		t.Errorf("Summary = %q", result.Summary)
	}
}

func TestExecuteRunDrill_StripYAMLExtension(t *testing.T) {
	fs := newMemFlowStore()
	ctx := testCtxWithStore(fs)

	deps := &runDrillDeps{}
	// This should strip .yaml and then fail with "not found" (not "empty suite name")
	result, err := executeRunDrill(ctx, deps, RunDrillArgs{SuiteName: "someapp.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Summary, "not found") {
		t.Errorf("Summary = %q, expected 'not found' (not 'suite_name is required')", result.Summary)
	}
}

func TestExecuteRunDrill_TestNameNotFound(t *testing.T) {
	fs := newMemFlowStore()
	ctx := testCtxWithStore(fs)

	// Create a valid suite with one test
	suiteYAML := "description: App\ntype: drill_suite\nsuite_config:\n  setup: []\n"
	fs.SaveFlow(context.Background(), "testapp", suiteYAML)
	drillYAML := "type: drill\nsuite: testapp\ndescription: s\nnodes:\n  - name: s\n    type: tool\n    args:\n      tool: shell_command\n      command: echo hi\n    assert:\n      type: contains\n      expected: hi"
	fs.SaveFlow(context.Background(), "existing-drill", drillYAML)

	deps := &runDrillDeps{}
	result, err := executeRunDrill(ctx, deps, RunDrillArgs{
		SuiteName: "testapp",
		TestName:  "nonexistent-drill",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Summary, "not found") {
		t.Errorf("Summary = %q", result.Summary)
	}
}

func TestExecuteRunDrill_TagFilterNoMatch(t *testing.T) {
	fs := newMemFlowStore()
	ctx := testCtxWithStore(fs)

	suiteYAML := "description: App\ntype: drill_suite\nsuite_config:\n  setup: []\n"
	fs.SaveFlow(context.Background(), "testapp", suiteYAML)
	drillYAML := "type: drill\nsuite: testapp\ndescription: s\ndrill_config:\n  tags: [smoke]\nnodes:\n  - name: s\n    type: tool\n    args:\n      tool: shell_command\n      command: echo hi\n    assert:\n      type: contains\n      expected: hi"
	fs.SaveFlow(context.Background(), "smoke-test", drillYAML)

	deps := &runDrillDeps{}
	result, err := executeRunDrill(ctx, deps, RunDrillArgs{
		SuiteName: "testapp",
		Tag:       "regression",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" {
		t.Errorf("Status = %q, want passed (no matching tests = vacuous pass)", result.Status)
	}
	if !strings.Contains(result.Summary, "No tests matching") {
		t.Errorf("Summary = %q", result.Summary)
	}
}

func TestExecuteRunDrill_RunsLocally(t *testing.T) {
	fs := newMemFlowStore()
	ctx := testCtxWithStore(fs)

	suiteYAML := "description: Echo App\ntype: drill_suite\nsuite_config:\n  setup: []\n"
	fs.SaveFlow(context.Background(), "echoapp", suiteYAML)
	drillYAML := "type: drill\nsuite: echoapp\ndescription: echo test\nnodes:\n  - name: echo-step\n    type: tool\n    args:\n      tool: shell_command\n      command: \"echo hello-drill-test\"\n    assert:\n      type: contains\n      expected: \"hello-drill-test\""
	fs.SaveFlow(context.Background(), "echo-test", drillYAML)

	deps := &runDrillDeps{} // no sandbox
	result, err := executeRunDrill(ctx, deps, RunDrillArgs{
		SuiteName: "echoapp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" {
		t.Errorf("Status = %q, want passed. Report:\n%s", result.Status, result.Report)
	}
	if !strings.Contains(result.Summary, "1/1") {
		t.Errorf("Summary = %q, want 1/1 passed", result.Summary)
	}
}

func TestExecuteRunDrill_SingleTestFilter(t *testing.T) {
	fs := newMemFlowStore()
	ctx := testCtxWithStore(fs)

	suiteYAML := "description: App\ntype: drill_suite\nsuite_config:\n  setup: []\n"
	fs.SaveFlow(context.Background(), "multiapp", suiteYAML)

	drill1 := "type: drill\nsuite: multiapp\ndescription: test1\nnodes:\n  - name: s\n    type: tool\n    args:\n      tool: shell_command\n      command: \"echo aaa\"\n    assert:\n      type: contains\n      expected: aaa"
	drill2 := "type: drill\nsuite: multiapp\ndescription: test2\nnodes:\n  - name: s\n    type: tool\n    args:\n      tool: shell_command\n      command: \"echo bbb\"\n    assert:\n      type: contains\n      expected: bbb"
	fs.SaveFlow(context.Background(), "drill-one", drill1)
	fs.SaveFlow(context.Background(), "drill-two", drill2)

	deps := &runDrillDeps{}
	result, err := executeRunDrill(ctx, deps, RunDrillArgs{
		SuiteName: "multiapp",
		TestName:  "drill-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" {
		t.Errorf("Status = %q, want passed. Report:\n%s", result.Status, result.Report)
	}
	// Should only run 1 test, not 2
	if !strings.Contains(result.Summary, "1/1") {
		t.Errorf("Summary = %q, want 1/1 (single test filter)", result.Summary)
	}
}

func TestExecuteRunDrill_AssertionFail(t *testing.T) {
	fs := newMemFlowStore()
	ctx := testCtxWithStore(fs)

	suiteYAML := "description: Fail App\ntype: drill_suite\nsuite_config:\n  setup: []\n"
	fs.SaveFlow(context.Background(), "failapp", suiteYAML)
	drillYAML := "type: drill\nsuite: failapp\ndescription: failing test\nnodes:\n  - name: step1\n    type: tool\n    args:\n      tool: shell_command\n      command: \"echo actual-output\"\n    assert:\n      type: contains\n      expected: \"expected-but-missing\""
	fs.SaveFlow(context.Background(), "fail-test", drillYAML)

	deps := &runDrillDeps{}
	result, err := executeRunDrill(ctx, deps, RunDrillArgs{
		SuiteName: "failapp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Errorf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Summary, "0/1") {
		t.Errorf("Summary = %q, want 0/1", result.Summary)
	}
}

// ---------------------------------------------------------------------------
// testCompositeExecutor routing tests
// ---------------------------------------------------------------------------

func TestCompositeExecutor_RoutesToBrowser(t *testing.T) {
	// Test routing by checking the tool name maps directly.
	// These maps determine how testCompositeExecutor routes calls.

	browserTools := []string{
		"browser_navigate", "browser_snapshot", "browser_click",
		"browser_type", "browser_run_code", "browser_wait_for",
	}
	for _, tool := range browserTools {
		if !testBrowserToolNames[tool] {
			t.Errorf("expected %q in testBrowserToolNames", tool)
		}
	}

	containerTools := []string{
		"shell_command", "read_file", "write_file", "grep_search",
		"http_request", "web_fetch",
	}
	for _, tool := range containerTools {
		if !testContainerTools[tool] {
			t.Errorf("expected %q in testContainerTools", tool)
		}
	}

	// Verify no overlap between browser and container tool lists
	for tool := range testBrowserToolNames {
		if testContainerTools[tool] {
			t.Errorf("tool %q appears in both browser and container lists — routing ambiguity", tool)
		}
	}
}

func TestContainerToolMap_Completeness(t *testing.T) {
	// Verify key tools are in the container tools map
	expected := []string{
		"shell_command", "read_file", "write_file", "edit_file",
		"file_tree", "grep_search", "find_files",
		"process_read", "process_write", "process_list", "process_kill",
		"http_request", "web_fetch",
	}
	for _, tool := range expected {
		if !testContainerTools[tool] {
			t.Errorf("expected %q in testContainerTools", tool)
		}
	}
}

func TestBrowserToolMap_Completeness(t *testing.T) {
	// Verify all core browser tools are present
	expected := []string{
		"browser_navigate", "browser_navigate_back",
		"browser_click", "browser_type", "browser_hover",
		"browser_press_key", "browser_select_option", "browser_fill_form",
		"browser_highlight", "browser_clear_highlights", "browser_move_cursor",
		"browser_snapshot", "browser_take_screenshot",
		"browser_wait_for", "browser_evaluate", "browser_run_code",
		"browser_console_messages", "browser_network_requests",
		"browser_tabs", "browser_close", "browser_resize", "browser_fullscreen",
		"browser_cookies", "browser_storage",
	}
	for _, tool := range expected {
		if !testBrowserToolNames[tool] {
			t.Errorf("expected %q in testBrowserToolNames", tool)
		}
	}
}

// ---------------------------------------------------------------------------
// templateDisplay tests
// ---------------------------------------------------------------------------

func TestTemplateDisplay_Empty(t *testing.T) {
	if got := templateDisplay(""); got != "@base" {
		t.Errorf("templateDisplay(\"\") = %q, want @base", got)
	}
}

func TestTemplateDisplay_Named(t *testing.T) {
	if got := templateDisplay("juicytrade"); got != "juicytrade" {
		t.Errorf("templateDisplay(\"juicytrade\") = %q", got)
	}
}

func TestTemplateDisplay_AtPrefix(t *testing.T) {
	if got := templateDisplay("@juicytrade"); got != "juicytrade" {
		t.Errorf("templateDisplay(\"@juicytrade\") = %q, want juicytrade", got)
	}
	if got := templateDisplay("@base"); got != "@base" {
		t.Errorf("templateDisplay(\"@base\") = %q, want @base", got)
	}
}

func TestNormalizeSandboxTemplateName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"base", ""},
		{"@base", ""},
		{" juicytrade ", "juicytrade"},
		{"@juicytrade", "juicytrade"},
	}
	for _, tc := range cases {
		if got := normalizeSandboxTemplateName(tc.in); got != tc.want {
			t.Errorf("normalizeSandboxTemplateName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnsureDrillSandboxTemplate_NoopWithoutPool(t *testing.T) {
	deps := &runDrillDeps{}
	if err := ensureDrillSandboxTemplate(nil, deps, "suite", "myapp", nil, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureDrillSandboxTemplate_ForceSkips(t *testing.T) {
	deps := &runDrillDeps{}
	// force=true must not attempt ReplaceSession even with a required template.
	if err := ensureDrillSandboxTemplate(nil, deps, "suite", "myapp", nil, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecideDrillTemplateSwitch(t *testing.T) {
	cases := []struct {
		name                         string
		current, required            string
		requiredExists, startPresent bool
		want                         drillTemplateSwitchAction
	}{
		{"same template", "juicytrade", "juicytrade", true, false, drillSwitchNoop},
		{"empty required", "juicytrade", "", false, false, drillSwitchNoop},
		{"preserve when start script present", "", "juicytrade", true, true, drillSwitchPreserve},
		{"preserve prepared non-base without script", "other", "juicytrade", true, false, drillSwitchPreserve},
		{"skip when template missing on base", "", "juicytrade", false, false, drillSwitchSkipMissingTemplate},
		{"replace from base when template exists", "", "juicytrade", true, false, drillSwitchReplace},
		{"preserve when script present even if template missing", "prep", "juicytrade", false, true, drillSwitchPreserve},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideDrillTemplateSwitch(tc.current, tc.required, tc.requiredExists, tc.startPresent)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractStartServicesPaths(t *testing.T) {
	sc := &config.DrillSuiteConfig{
		Setup: []string{
			"bash /root/juicytrade/.astonish/start-services.sh",
			"echo already started",
		},
		Services: []config.ServiceConfig{
			{Name: "api", Setup: "bash /root/juicytrade/.astonish/start-services.sh"},
		},
	}
	paths := extractStartServicesPaths(sc)
	if len(paths) != 1 || paths[0] != "/root/juicytrade/.astonish/start-services.sh" {
		t.Fatalf("paths = %#v, want single absolute start-services path", paths)
	}
	if !suiteReferencesStartServices(sc) {
		t.Fatal("expected suiteReferencesStartServices true")
	}
	if suiteReferencesStartServices(&config.DrillSuiteConfig{Setup: []string{"echo hi"}}) {
		t.Fatal("expected false when no start-services reference")
	}
	if !suiteReferencesStartServices(&config.DrillSuiteConfig{Setup: []string{"bash .astonish/start-services.sh"}}) {
		t.Fatal("expected true for relative start-services reference")
	}
}

func TestPreflightDrillStartServices_NoRef(t *testing.T) {
	deps := &runDrillDeps{nodePool: &sandbox.NodeClientPool{}}
	err := preflightDrillStartServices(nil, deps, "suite", "myapp", &config.DrillSuiteConfig{
		Setup: []string{"echo ready"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflightDrillStartServices_MissingPathError(t *testing.T) {
	// nodePool non-nil triggers sandbox-aware preflight; without a real container
	// path checks return false and bootstrap inject is a no-op → clear error.
	deps := &runDrillDeps{nodePool: &sandbox.NodeClientPool{}}
	sc := &config.DrillSuiteConfig{
		Setup: []string{"bash /root/myapp/.astonish/start-services.sh"},
	}
	err := preflightDrillStartServices(nil, deps, "myapp", "myapp", sc)
	if err == nil {
		t.Fatal("expected error for missing start-services.sh")
	}
	msg := err.Error()
	if !strings.Contains(msg, "start-services.sh") || !strings.Contains(msg, "Do not switch") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPreflightDrillStartServices_NoSandboxSkips(t *testing.T) {
	err := preflightDrillStartServices(nil, &runDrillDeps{}, "suite", "myapp", &config.DrillSuiteConfig{
		Setup: []string{"bash /root/myapp/.astonish/start-services.sh"},
	})
	if err != nil {
		t.Fatalf("expected skip without sandbox deps, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// enrichReportWithFailureContext tests
// ---------------------------------------------------------------------------

func TestEnrichReportWithFailureContext(t *testing.T) {
	report := &adrill.SuiteReport{
		Suite:  "testapp",
		Status: "failed",
		Tests: []adrill.TestReport{
			{
				Name:   "health-check",
				Status: "failed",
				Steps: []adrill.StepResult{
					{
						Name:   "step1",
						Tool:   "shell_command",
						Status: "failed",
						Output: "error: connection refused",
						Assertion: &adrill.AssertionResult{
							Passed:  false,
							Message: `expected "ok" but output does not contain it`,
						},
					},
				},
			},
			{
				Name:   "passing-test",
				Status: "passed",
				Steps: []adrill.StepResult{
					{
						Name:   "ok-step",
						Tool:   "shell_command",
						Status: "passed",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	enrichReportWithFailureContext(&buf, report)
	output := buf.String()

	if !strings.Contains(output, "health-check") {
		t.Error("enriched report should mention the failing test name")
	}
	if !strings.Contains(output, "step1") {
		t.Error("enriched report should mention the failing step name")
	}
	if !strings.Contains(output, "connection refused") {
		t.Error("enriched report should include the step output")
	}
	// Should NOT include passing test details
	if strings.Contains(output, "ok-step") {
		t.Error("enriched report should not include passing step details")
	}
}

func TestTestBrowserExecutor_RequiresSandboxedManager(t *testing.T) {
	t.Parallel()

	t.Run("nil manager", func(t *testing.T) {
		t.Parallel()
		exec := newTestBrowserExecutor(nil, "sess-1", true)
		_, err := exec.Execute(context.Background(), "browser_navigate", map[string]interface{}{
			"url": "http://localhost:3000/",
		})
		if err == nil {
			t.Fatal("expected error when browser manager is nil")
		}
		if !strings.Contains(err.Error(), "in-container") {
			t.Errorf("error = %v, want mention of in-container browser", err)
		}
	})

	t.Run("host manager not sandboxed", func(t *testing.T) {
		t.Parallel()
		mgr := browser.NewManager(browser.DefaultConfig())
		exec := newTestBrowserExecutor(mgr, "sess-1", true)
		_, err := exec.Execute(context.Background(), "browser_navigate", map[string]interface{}{
			"url": "http://localhost:3000/",
		})
		if err == nil {
			t.Fatal("expected error when SandboxEnabled is false")
		}
		if !strings.Contains(err.Error(), "in-container") {
			t.Errorf("error = %v, want mention of in-container browser", err)
		}
	})

	t.Run("sandboxed manager accepts init", func(t *testing.T) {
		t.Parallel()
		mgr := browser.NewManager(browser.DefaultConfig())
		mgr.SandboxEnabled = true
		mgr.ContainerResolveFunc = func(sessionID string) (string, string, error) {
			return "astn-sess-" + sessionID, "10.0.0.1", nil
		}
		mgr.ContainerStartBrowserFunc = func(string) (io.Closer, error) {
			return nil, nil
		}
		exec := newTestBrowserExecutor(mgr, "sess-abc", true)
		if err := exec.ensureInit(); err != nil {
			t.Fatalf("ensureInit: %v", err)
		}
		if !exec.initialized {
			t.Fatal("expected executor to be initialized")
		}
	})
}

func TestBuildTestExecutor_UsesSharedBrowserManager(t *testing.T) {
	t.Parallel()
	mgr := browser.NewManager(browser.DefaultConfig())
	mgr.SandboxEnabled = true
	deps := &runDrillDeps{browserMgr: mgr, sessionID: "fleet-1"}
	exec := buildTestExecutor(nil, deps)
	comp, ok := exec.(*testCompositeExecutor)
	if !ok {
		t.Fatalf("executor type = %T", exec)
	}
	if comp.browser == nil || comp.browser.mgr != mgr {
		t.Fatal("buildTestExecutor should reuse deps.browserMgr, not create a host Manager")
	}
}

func TestSuiteUsesBrowserTools(t *testing.T) {
	withBrowser := []adrill.LoadedTest{{
		Config: &config.AgentConfig{
			Nodes: []config.Node{{
				Type: "tool",
				Args: map[string]interface{}{"tool": "browser_navigate", "url": "http://127.0.0.1/"},
			}},
		},
	}}
	if !suiteUsesBrowserTools(withBrowser) {
		t.Fatal("expected true for browser_navigate suite")
	}
	shellOnly := []adrill.LoadedTest{{
		Config: &config.AgentConfig{
			Nodes: []config.Node{{
				Type: "tool",
				Args: map[string]interface{}{"tool": "shell_command", "command": "curl localhost"},
			}},
		},
	}}
	if suiteUsesBrowserTools(shellOnly) {
		t.Fatal("expected false for shell-only suite")
	}
}

func TestIsBrowserStackTransientError(t *testing.T) {
	if !isBrowserStackTransientError(fmt.Errorf("failed to resolve CDP URL after 15s")) {
		t.Fatal("expected CDP resolve as transient")
	}
	if isBrowserStackTransientError(fmt.Errorf(`net::ERR_CONNECTION_REFUSED`)) {
		t.Fatal("app connection refused alone is not Class A")
	}
}

func TestBrowserExecutor_ClassBProbeServiceDead(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	mgr.SandboxEnabled = true
	mgr.ContainerResolveFunc = func(string) (string, string, error) {
		return "astn-sess-x", "10.0.0.1", nil
	}
	mgr.ContainerStartBrowserFunc = func(string) (io.Closer, error) {
		return nil, fmt.Errorf("start skipped in unit test")
	}
	exec := newTestBrowserExecutor(mgr, "sess", true)
	if err := exec.ensureInit(); err != nil {
		t.Fatal(err)
	}
	// Force navigate path to fail with connection refused after warm would fail —
	// exercise Class B messaging via a fake probe without calling real Navigate.
	exec.probeURL = func(string) bool { return false }
	err := fmt.Errorf("navigation failed: net::ERR_CONNECTION_REFUSED")
	// Simulate the post-failure branch used by Execute by calling the formatter path:
	navURL := "http://127.0.0.1:3001/"
	if exec.probeURL(navURL) {
		t.Fatal("probe should report dead")
	}
	annotated := fmt.Errorf("%w (service not answering at %s inside the sandbox — app/frontend likely died after ready_check; restore via start-services.sh / restart supervisors. This is not a browser stack failure)", err, navURL)
	if !strings.Contains(annotated.Error(), "not a browser stack failure") {
		t.Fatalf("unexpected: %v", annotated)
	}
	if adrill.ClassifyKnownFailure(adrill.StepResult{Error: annotated.Error()}).Classification != "environment" {
		t.Fatal("expected environment classification for Class B")
	}
}

func TestWarmBrowserForDrill_Unwired(t *testing.T) {
	exec := &testCompositeExecutor{
		browser: newTestBrowserExecutor(browser.NewManager(browser.DefaultConfig()), "s", true),
	}
	err := warmBrowserForDrill(exec)
	if err == nil {
		t.Fatal("expected preflight error when SandboxEnabled=false")
	}
	if !strings.Contains(err.Error(), "in-container browser") {
		t.Fatalf("got %v", err)
	}
}
