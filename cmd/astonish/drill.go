package astonish

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/config"
	adrill "github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/tools"
)

func handleDrillCommand(args []string) error {
	if len(args) < 1 || args[0] == "--help" || args[0] == "-h" {
		printDrillUsage()
		return nil
	}

	switch args[0] {
	case "run":
		return handleDrillRunCommand(args[1:])
	case "list":
		return handleDrillListCommand(args[1:])
	case "report":
		return handleDrillReportCommand(args[1:])
	case "remove", "rm", "delete":
		return handleDrillRemoveCommand(args[1:])
	default:
		printDrillUsage()
		return fmt.Errorf("unknown drill command: %s", args[0])
	}
}

func printDrillUsage() {
	fmt.Println("usage: astonish drill [-h] {run,list,report,remove} ...")
	fmt.Println("")
	fmt.Println("Deterministic drill runner for AI-authored drill suites.")
	fmt.Println("")
	fmt.Println("commands:")
	fmt.Println("  run                 Run a drill suite or single drill")
	fmt.Println("  list                List all drill suites and drills")
	fmt.Println("  report              Show the last drill report")
	fmt.Println("  remove              Remove a drill suite or single drill")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  -h, --help          Show this help message")
}

// internalToolExecutor adapts pkg/tools.ExecuteTool to the ToolExecutor interface.
type internalToolExecutor struct{}

func (e *internalToolExecutor) Execute(ctx context.Context, name string, args map[string]interface{}) (any, error) {
	return tools.ExecuteTool(ctx, name, args, "")
}

// browserToolNames lists all browser tool names for detection.
var browserToolNames = map[string]bool{
	"browser_navigate": true, "browser_navigate_back": true,
	"browser_click": true, "browser_type": true, "browser_hover": true,
	"browser_drag": true, "browser_press_key": true, "browser_select_option": true,
	"browser_fill_form": true, "browser_highlight": true, "browser_clear_highlights": true,
	"browser_move_cursor": true, "browser_snapshot": true, "browser_take_screenshot": true,
	"browser_console_messages": true, "browser_network_requests": true,
	"browser_tabs": true, "browser_close": true, "browser_resize": true, "browser_fullscreen": true,
	"browser_wait_for": true, "browser_file_upload": true, "browser_handle_dialog": true,
	"browser_evaluate": true, "browser_run_code": true, "browser_pdf": true,
	"browser_response_body": true, "browser_cookies": true, "browser_storage": true,
	"browser_set_offline": true, "browser_set_headers": true, "browser_set_credentials": true,
	"browser_set_geolocation": true, "browser_set_media": true, "browser_set_timezone": true,
	"browser_set_locale": true, "browser_set_device": true,
	"browser_request_human": true,
}

// browserToolExecutor dispatches browser tool calls through a Manager.
// When sandboxed, mgr must already be wired (SandboxEnabled); host Chrome is never launched as a fallback.
type browserToolExecutor struct {
	mu             sync.Mutex
	mgr            *browser.Manager
	guard          *browser.NavigationGuard
	refs           *browser.RefMap
	headless       bool
	requireSandbox bool // when true, refuse to create an unwired host Manager
	sessionID      string
	initialized    bool
}

func newBrowserToolExecutor(headless bool) *browserToolExecutor {
	return &browserToolExecutor{headless: headless}
}

// newSandboxedBrowserToolExecutor uses a pre-wired in-container Manager.
func newSandboxedBrowserToolExecutor(mgr *browser.Manager, sessionID string) *browserToolExecutor {
	return &browserToolExecutor{
		mgr:            mgr,
		requireSandbox: true,
		sessionID:      sessionID,
		headless:       true,
	}
}

// ensureInit lazily creates the browser manager for local (non-sandbox) runs,
// or binds the session on a pre-wired sandboxed manager.
func (b *browserToolExecutor) ensureInit() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.initialized {
		if b.mgr != nil && b.sessionID != "" {
			b.mgr.EnsureSessionID(b.sessionID)
		}
		return nil
	}
	if b.mgr != nil {
		if b.requireSandbox && !b.mgr.SandboxEnabled {
			return fmt.Errorf("browser drills require an in-container browser (sandbox Chromium+KasmVNC); host Chromium is disabled")
		}
		b.guard = &browser.NavigationGuard{BlockPrivateNetworks: false}
		b.refs = browser.NewRefMap()
		if b.sessionID != "" {
			b.mgr.EnsureSessionID(b.sessionID)
		}
		b.initialized = true
		return nil
	}
	if b.requireSandbox {
		return fmt.Errorf("browser drills require an in-container browser (sandbox Chromium+KasmVNC); host Chromium is disabled")
	}
	cfg := browser.DefaultConfig()
	cfg.Headless = b.headless
	cfg.UserDataDir = "" // temp dir avoids SingletonLock conflict with other browser instances
	b.mgr = browser.NewManager(cfg)
	b.guard = browser.DefaultNavigationGuard()
	b.refs = browser.NewRefMap()
	b.initialized = true
	return nil
}

// Close shuts down the browser if it was started for a local (non-sandbox) run.
// Sandboxed managers are owned by the sandbox wiring and are not cleaned up here.
func (b *browserToolExecutor) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mgr != nil && !b.requireSandbox {
		b.mgr.Cleanup()
	}
}

// Execute dispatches a browser tool call by name.
func (b *browserToolExecutor) Execute(_ context.Context, name string, args map[string]interface{}) (any, error) {
	if err := b.ensureInit(); err != nil {
		return nil, err
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal browser tool args: %w", err)
	}

	// Each browser tool uses a different args struct. We dispatch to the factory closure
	// and unmarshal the JSON args into the appropriate struct type.
	switch name {
	case "browser_navigate":
		var a tools.BrowserNavigateArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserNavigate(b.mgr, b.guard)(nil, a)

	case "browser_navigate_back":
		var a tools.BrowserNavigateBackArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserNavigateBack(b.mgr)(nil, a)

	case "browser_click":
		var a tools.BrowserClickArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserClick(b.mgr, b.refs)(nil, a)

	case "browser_type":
		var a tools.BrowserTypeArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserType(b.mgr, b.refs)(nil, a)

	case "browser_hover":
		var a tools.BrowserHoverArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserHover(b.mgr, b.refs)(nil, a)

	case "browser_drag":
		var a tools.BrowserDragArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserDrag(b.mgr, b.refs)(nil, a)

	case "browser_highlight":
		var a tools.BrowserHighlightArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserHighlight(b.mgr, b.refs)(nil, a)

	case "browser_clear_highlights":
		var a tools.BrowserClearHighlightsArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserClearHighlights(b.mgr)(nil, a)

	case "browser_move_cursor":
		var a tools.BrowserMoveCursorArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserMoveCursor(b.mgr, b.refs)(nil, a)

	case "browser_press_key":
		var a tools.BrowserPressKeyArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserPressKey(b.mgr)(nil, a)

	case "browser_select_option":
		var a tools.BrowserSelectOptionArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSelectOption(b.mgr, b.refs)(nil, a)

	case "browser_fill_form":
		var a tools.BrowserFillFormArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserFillForm(b.mgr, b.refs)(nil, a)

	case "browser_snapshot":
		var a tools.BrowserSnapshotArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSnapshot(b.mgr, b.refs)(nil, a)

	case "browser_take_screenshot":
		var a tools.BrowserScreenshotArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserTakeScreenshot(b.mgr, b.refs)(nil, a)

	case "browser_console_messages":
		var a tools.BrowserConsoleMessagesArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserConsoleMessages(b.mgr)(nil, a)

	case "browser_network_requests":
		var a tools.BrowserNetworkRequestsArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserNetworkRequests(b.mgr)(nil, a)

	case "browser_tabs":
		var a tools.BrowserTabsArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserTabs(b.mgr, b.guard)(nil, a)

	case "browser_close":
		var a tools.BrowserCloseArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserClose(b.mgr)(nil, a)

	case "browser_resize":
		var a tools.BrowserResizeArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserResize(b.mgr)(nil, a)

	case "browser_fullscreen":
		var a tools.BrowserFullscreenArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserFullscreen(b.mgr)(nil, a)

	case "browser_wait_for":
		var a tools.BrowserWaitForArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserWaitFor(b.mgr)(nil, a)

	case "browser_file_upload":
		var a tools.BrowserFileUploadArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserFileUpload(b.mgr, b.refs)(nil, a)

	case "browser_handle_dialog":
		var a tools.BrowserHandleDialogArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserHandleDialog(b.mgr)(nil, a)

	case "browser_evaluate":
		var a tools.BrowserEvaluateArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserEvaluate(b.mgr, b.refs)(nil, a)

	case "browser_run_code":
		var a tools.BrowserRunCodeArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserRunCode(b.mgr)(nil, a)

	case "browser_pdf":
		var a tools.BrowserPDFArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserPDF(b.mgr)(nil, a)

	case "browser_response_body":
		var a tools.BrowserResponseBodyArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserResponseBody(b.mgr)(nil, a)

	case "browser_cookies":
		var a tools.BrowserCookiesArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserCookies(b.mgr)(nil, a)

	case "browser_storage":
		var a tools.BrowserStorageArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserStorage(b.mgr)(nil, a)

	case "browser_set_offline":
		var a tools.BrowserSetOfflineArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetOffline(b.mgr)(nil, a)

	case "browser_set_headers":
		var a tools.BrowserSetHeadersArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetHeaders(b.mgr)(nil, a)

	case "browser_set_credentials":
		var a tools.BrowserSetCredentialsArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetCredentials(b.mgr)(nil, a)

	case "browser_set_geolocation":
		var a tools.BrowserSetGeolocationArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetGeolocation(b.mgr)(nil, a)

	case "browser_set_media":
		var a tools.BrowserSetMediaArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetMedia(b.mgr)(nil, a)

	case "browser_set_timezone":
		var a tools.BrowserSetTimezoneArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetTimezone(b.mgr)(nil, a)

	case "browser_set_locale":
		var a tools.BrowserSetLocaleArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetLocale(b.mgr)(nil, a)

	case "browser_set_device":
		var a tools.BrowserSetDeviceArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserSetDevice(b.mgr)(nil, a)

	case "browser_request_human":
		var a tools.BrowserRequestHumanArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return nil, fmt.Errorf("invalid args for %s: %w", name, err)
		}
		return tools.BrowserRequestHuman(b.mgr)(nil, a)

	default:
		return nil, fmt.Errorf("unknown browser tool: %s", name)
	}
}

// compositeToolExecutor routes tool calls to the appropriate executor.
// When sandbox is non-nil, container tools (shell_command, file tools, etc.)
// are routed into the sandbox container. Browser tools always run on the host.
type compositeToolExecutor struct {
	internal *internalToolExecutor
	browser  *browserToolExecutor
	sandbox  *sandboxToolExecutor // nil when no sandbox
}

func (c *compositeToolExecutor) Execute(ctx context.Context, name string, args map[string]interface{}) (any, error) {
	if browserToolNames[name] {
		return c.browser.Execute(ctx, name, args)
	}
	if c.sandbox != nil && containerToolNames[name] {
		return c.sandbox.Execute(ctx, name, args)
	}
	return c.internal.Execute(ctx, name, args)
}

// sandboxToolExecutor proxies tool calls into a sandbox container via LazyNodeClient.
type sandboxToolExecutor struct {
	lazyClient *sandbox.LazyNodeClient
	sessionID  string
}

func (e *sandboxToolExecutor) Execute(_ context.Context, name string, args map[string]interface{}) (any, error) {
	raw, err := e.lazyClient.Call(e.sessionID, name, args)
	if err != nil {
		return nil, fmt.Errorf("sandbox call %s: %w", name, err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal sandbox result for %s: %w", name, err)
	}
	return result, nil
}

// containerToolNames lists tools that should route into the sandbox container.
var containerToolNames = map[string]bool{
	"shell_command":             true,
	"read_file":                 true,
	"write_file":                true,
	"edit_file":                 true,
	"file_tree":                 true,
	"grep_search":               true,
	"find_files":                true,
	"process_read":              true,
	"process_write":             true,
	"process_list":              true,
	"process_kill":              true,
	"http_request":              true,
	"web_fetch":                 true,
	"read_pdf":                  true,
	"filter_json":               true,
	"git_diff_add_line_numbers": true,
}

func handleDrillRunCommand(args []string) error {
	// Escalate to root on Linux when sandbox is enabled — drill suites
	// that use sandbox templates need overlay mount and UID shifting capabilities.
	if sandbox.NeedsEscalation() {
		if cfg, err := config.LoadAppConfig(); err == nil && cfg != nil {
			if sandbox.IsSandboxEnabled(&cfg.Sandbox) {
				return sandbox.Escalate()
			}
		}
	}

	runCmd := flag.NewFlagSet("test run", flag.ExitOnError)
	tagFlag := runCmd.String("tag", "", "Filter tests by tag (comma-separated)")
	verbose := runCmd.Bool("verbose", false, "Verbose output")
	reportDir := runCmd.String("report-dir", "", "Directory to save report (default: ~/.config/astonish/reports)")
	analyze := runCmd.Bool("analyze", false, "Enable AI triage analysis on failures (uses default LLM provider)")

	// Extract positional argument (suite or test name) before flags
	var targetName string
	var flagArgs []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else if targetName == "" {
			targetName = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	if err := runCmd.Parse(flagArgs); err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	if targetName == "" {
		fmt.Println("usage: astonish drill run <suite_or_drill> [--tag tag1,tag2] [--verbose] [--analyze] [--report-dir dir]")
		return fmt.Errorf("no suite or test name provided")
	}

	dirs := adrill.DefaultDrillDirs()

	// Determine report directory
	rdir := *reportDir
	if rdir == "" {
		if reportsBase, err := config.GetReportsDir(); err == nil {
			rdir = filepath.Join(reportsBase, targetName)
		} else {
			return fmt.Errorf("failed to determine reports directory: %w", err)
		}
	}

	// --- Discover suite and tests FIRST (before building executor) ---

	var suite *adrill.LoadedSuite
	var tests []adrill.LoadedTest

	foundSuite, sErr := adrill.FindSuite(dirs, targetName)
	if sErr == nil {
		// Found as a suite
		if err := adrill.ValidateSuite(foundSuite); err != nil {
			return fmt.Errorf("invalid suite: %w", err)
		}
		suite = foundSuite
		tests = suite.Tests
		if *tagFlag != "" {
			tags := strings.Split(*tagFlag, ",")
			for i := range tags {
				tags[i] = strings.TrimSpace(tags[i])
			}
			tests = adrill.FilterTestsByTag(tests, tags)
			if len(tests) == 0 {
				fmt.Printf("No tests matching tags: %s\n", *tagFlag)
				return nil
			}
		}
	} else {
		// Try as individual test
		test, parentSuite, tErr := adrill.FindTestAndSuite(dirs, targetName)
		if tErr != nil {
			return fmt.Errorf("not found as suite or test: %s", targetName)
		}
		if err := adrill.ValidateSuite(parentSuite); err != nil {
			return fmt.Errorf("invalid suite %q for test %q: %w", parentSuite.Name, test.Name, err)
		}
		suite = parentSuite
		tests = []adrill.LoadedTest{*test}
	}

	// --- Build executor (sandbox-aware if suite has a template) ---

	suiteTemplate := ""
	if suite.Config.SuiteConfig != nil {
		suiteTemplate = suite.Config.SuiteConfig.Template
	}

	// Determine if sandbox should be used
	var lazyNode *sandbox.LazyNodeClient
	var testSessionID string
	useSandbox := false

	if suiteTemplate != "" {
		// Suite references a sandbox template — try to initialize sandbox
		lazyNode, testSessionID, useSandbox = initSandboxForTest(suiteTemplate)
		if lazyNode != nil {
			defer lazyNode.Cleanup()
		}
	}

	if useSandbox && lazyNode != nil {
		if _, err := lazyNode.EnsureContainerReady(testSessionID); err != nil {
			lazyNode.Cleanup()
			return fmt.Errorf("sandbox container not ready: %w", err)
		}
		if err := injectCLIDrillBootstrapAndCredentials(lazyNode, testSessionID, suiteTemplate, suite.Name, suite.Config.SuiteConfig); err != nil {
			lazyNode.Cleanup()
			return fmt.Errorf("credential/bootstrap injection failed: %w", err)
		}
	}

	// Browser executor: sandboxed runs use in-container Chromium (same path as chat).
	// Local (non-sandbox) runs may use host Chrome.
	var browserExec *browserToolExecutor
	var browserMgr *browser.Manager
	if useSandbox && lazyNode != nil {
		browserMgr = browser.NewManager(browser.DefaultConfig())
		client := lazyNode.GetIncusClient()
		// TouchActivity keeps the idle watchdog from reclaiming the session
		// during long browser drills (browser tools bypass NDJSON Call()).
		touch := func(string) { lazyNode.TouchActivity() }
		if client == nil || !sandbox.WireIncusBrowserManager(browserMgr, client, nil, touch) {
			lazyNode.Cleanup()
			return fmt.Errorf("sandbox drill browser: could not wire in-container Chromium (host Chrome is disabled for sandboxed drills)")
		}
		browserExec = newSandboxedBrowserToolExecutor(browserMgr, testSessionID)
	} else {
		browserExec = newBrowserToolExecutor(true) // headless for CI
	}
	defer func() {
		browserExec.Close()
		if browserMgr != nil {
			browserMgr.Cleanup()
		}
	}()

	executor := &compositeToolExecutor{
		internal: &internalToolExecutor{},
		browser:  browserExec,
	}

	if useSandbox && lazyNode != nil {
		executor.sandbox = &sandboxToolExecutor{
			lazyClient: lazyNode,
			sessionID:  testSessionID,
		}
	}

	// --- Discover container IP and set vars ---

	vars := map[string]string{"CONTAINER_IP": "localhost"}
	if useSandbox && lazyNode != nil {
		ip, err := lazyNode.GetContainerIP(testSessionID)
		if err == nil && ip != "" {
			vars["CONTAINER_IP"] = ip
			if *verbose {
				fmt.Printf("Container IP: %s\n", ip)
			}
		} else {
			slog.Warn("could not discover container IP", "error", err)
		}
	}

	// Create artifact manager
	am, amErr := adrill.NewArtifactManager(rdir, targetName)
	if amErr != nil {
		fmt.Printf("Warning: could not create artifact manager: %v\n", amErr)
	}

	runner := adrill.NewSuiteRunner(executor, am, *verbose)
	runner.SetVars(vars)

	// --- Set up AI triage if --analyze is enabled ---

	if *analyze {
		if err := setupTriageAgent(runner, executor, am, *verbose); err != nil {
			fmt.Printf("Warning: could not enable AI triage: %v\n", err)
			fmt.Println("Continuing without triage analysis.")
		} else {
			fmt.Println("AI triage: enabled (failures will be analyzed)")
		}
	}

	// --- Run ---

	if len(tests) == 1 && sErr != nil {
		// Individual test
		fmt.Printf("Running test: %s (suite: %s)\n", tests[0].Name, suite.Name)
	} else {
		modeStr := ""
		if useSandbox {
			modeStr = fmt.Sprintf(" [sandbox: %s]", suiteTemplate)
		}
		fmt.Printf("Running suite: %s (%d tests)%s\n", suite.Name, len(tests), modeStr)
	}

	report, err := runner.RunSuite(context.Background(), suite, tests)
	if err != nil {
		return fmt.Errorf("suite run failed: %w", err)
	}

	adrill.PrintReport(report, os.Stdout)

	// Save report
	reportPath, err := adrill.SaveReport(report, rdir)
	if err != nil {
		fmt.Printf("Warning: could not save report: %v\n", err)
	} else {
		fmt.Printf("\nReport saved: %s\n", reportPath)
	}

	if report.Status != "passed" {
		return fmt.Errorf("suite %s: %s", suite.Name, report.Status)
	}
	return nil
}

// initSandboxForTest initializes sandbox infrastructure for a CLI test run.
// Returns (lazyClient, sessionID, true) on success, or (nil, "", false) if
// sandbox is not available or initialization fails.
func initSandboxForTest(template string) (*sandbox.LazyNodeClient, string, bool) {
	// Load app config to check if sandbox is enabled
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		slog.Warn("could not load app config for sandbox", "error", err)
		fmt.Println("Warning: Suite requires sandbox template but app config not available. Running locally.")
		return nil, "", false
	}

	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		fmt.Println("Warning: Suite requires sandbox template but sandbox is disabled. Running locally.")
		return nil, "", false
	}

	// Connect to Incus
	sandbox.SetSandboxConfig(&appCfg.Sandbox)
	sandboxClient, err := sandbox.SetupSandboxRuntime()
	if err != nil {
		slog.Warn("sandbox setup failed", "error", err)
		fmt.Printf("Warning: Suite requires sandbox template %q but sandbox setup failed: %v\nRunning locally.\n", template, err)
		return nil, "", false
	}

	// Create registries
	sessRegistry, err := sandbox.NewSessionRegistry()
	if err != nil {
		slog.Warn("session registry failed", "error", err)
		return nil, "", false
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		slog.Warn("template registry failed", "error", err)
		return nil, "", false
	}

	// Check that the template exists
	if err := tplRegistry.Load(); err == nil {
		if !tplRegistry.Exists(template) {
			fmt.Printf("Warning: Sandbox template %q not found. Running locally.\n", template)
			fmt.Println("Available templates can be listed with: astonish sandbox template list")
			return nil, "", false
		}
	}

	// Create LazyNodeClient with the suite's template
	limits := sandbox.EffectiveLimits(&appCfg.Sandbox)
	lazyNode := sandbox.NewLazyNodeClient(sandboxClient, sessRegistry, tplRegistry, template, &limits)

	// Generate a unique session ID for this test run
	testSessionID := "test-" + uuid.New().String()[:8]

	// Trigger container creation
	lazyNode.BindSession(testSessionID)

	fmt.Printf("Sandbox: creating container from template %q...\n", template)
	return lazyNode, testSessionID, true
}

// injectCLIDrillBootstrapAndCredentials injects template bootstrap_files and suite
// credential_injection into the CLI drill sandbox before tests.
// Caller must ensure services are already running (CLI does not run suite setup).
func injectCLIDrillBootstrapAndCredentials(lazyNode *sandbox.LazyNodeClient, sessionID, template, suiteName string, sc *config.DrillSuiteConfig) error {
	ctx := context.Background()
	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		slog.Warn("CLI drill: template registry unavailable for bootstrap inject", "error", err)
	} else {
		_ = tplRegistry.Load()
		files := sandbox.LookupBootstrapFiles(ctx, tplRegistry, nil, template)
		if len(files) > 0 {
			client := lazyNode.GetIncusClient()
			containerName := lazyNode.GetContainerName()
			if client != nil && containerName != "" {
				if err := sandbox.MaterializeBootstrapFilesIncus(ctx, func(command []string, env map[string]string) ([]byte, []byte, int, error) {
					out, execErr := sandbox.ExecSimpleWithEnv(client, containerName, command, env)
					if execErr != nil {
						return nil, nil, -1, execErr
					}
					return []byte(out), nil, 0, nil
				}, files); err != nil {
					return fmt.Errorf("bootstrap_files: %w", err)
				}
			}
		}
	}

	spec, err := adrill.ResolveInjectionSpec(ctx, suiteName, sc, nil)
	if err != nil {
		return err
	}
	if spec == nil || !spec.HasWork() {
		return nil
	}

	client := lazyNode.GetIncusClient()
	containerName := lazyNode.GetContainerName()
	target := adrill.InjectionTarget{
		SessionID:  sessionID,
		LazyClient: lazyNode,
	}
	if client != nil && containerName != "" {
		target.ExecIncus = func(command []string, env map[string]string) ([]byte, []byte, int, error) {
			out, execErr := sandbox.ExecSimpleWithEnv(client, containerName, command, env)
			if execErr != nil {
				return nil, nil, -1, execErr
			}
			return []byte(out), nil, 0, nil
		}
	}
	_, err = adrill.ApplyCredentialInjection(ctx, spec, nil, tools.GetCredentialStore(), target)
	return err
}

// setupTriageAgent initializes an AI triage agent using the user's default LLM
// provider and attaches it to the runner. Returns an error if the provider
// cannot be initialized (missing API key, etc.).
func setupTriageAgent(runner *adrill.SuiteRunner, executor adrill.ToolExecutor, am *adrill.ArtifactManager, verbose bool) error {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("load app config: %w", err)
	}

	providerName := appCfg.General.DefaultProvider
	modelName := appCfg.General.DefaultModel

	if providerName == "" {
		return fmt.Errorf("no default provider configured — set general.default_provider in astonish config")
	}

	llm, err := provider.GetProvider(context.Background(), providerName, modelName, appCfg)
	if err != nil {
		return fmt.Errorf("initialize %s provider: %w", providerName, err)
	}

	ta := adrill.NewTriageAgent(llm, executor, am, verbose)
	runner.SetTriageAgent(ta, true) // enableForAll=true because --analyze means "triage everything"

	// Also enable semantic assertions using the same LLM
	if llmProvider := adrill.NewLLMProviderFromModel(llm); llmProvider != nil {
		runner.SetLLMProvider(llmProvider)
	}

	return nil
}

func handleDrillListCommand(args []string) error {
	listCmd := flag.NewFlagSet("test list", flag.ExitOnError)
	tagFlag := listCmd.String("tag", "", "Filter by tag (comma-separated)")
	if err := listCmd.Parse(args); err != nil {
		return err
	}

	dirs := adrill.DefaultDrillDirs()
	suites, err := adrill.DiscoverSuites(dirs)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	if len(suites) == 0 {
		fmt.Println("No test suites found.")
		return nil
	}

	// Sort suites by name
	sort.Slice(suites, func(i, j int) bool {
		return suites[i].Name < suites[j].Name
	})

	var tags []string
	if *tagFlag != "" {
		tags = strings.Split(*tagFlag, ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
	}

	totalTests := 0
	for _, suite := range suites {
		tests := suite.Tests
		if len(tags) > 0 {
			tests = adrill.FilterTestsByTag(tests, tags)
		}

		fmt.Printf("Suite: %s", suite.Name)
		if suite.File != "" {
			fmt.Printf(" (%s)", suite.File)
		}
		fmt.Println()

		sc := suite.Config.SuiteConfig
		if sc != nil {
			if sc.Template != "" {
				fmt.Printf("  Template: %s\n", sc.Template)
			}
			if sc.BaseURL != "" {
				fmt.Printf("  Base URL: %s\n", sc.BaseURL)
			}
			if len(sc.Services) > 0 {
				names := make([]string, len(sc.Services))
				for i, svc := range sc.Services {
					names[i] = svc.Name
				}
				fmt.Printf("  Services: %s\n", strings.Join(names, ", "))
			}
			if len(sc.Setup) > 0 {
				fmt.Printf("  Setup: %d commands\n", len(sc.Setup))
			}
			if len(sc.Teardown) > 0 {
				fmt.Printf("  Teardown: %d commands\n", len(sc.Teardown))
			}
		}

		if len(tests) == 0 {
			fmt.Println("  (no tests)")
		} else {
			for _, test := range tests {
				desc := test.Name
				if test.Config.Description != "" {
					desc = test.Config.Description
				}
				tagStr := ""
				if test.Config.DrillConfig != nil && len(test.Config.DrillConfig.Tags) > 0 {
					tagStr = " [" + strings.Join(test.Config.DrillConfig.Tags, ", ") + "]"
				}
				fmt.Printf("  - %s%s\n", desc, tagStr)
			}
		}
		totalTests += len(tests)
		fmt.Println()
	}

	fmt.Printf("%d suites, %d tests\n", len(suites), totalTests)
	return nil
}

func handleDrillReportCommand(args []string) error {
	reportCmd := flag.NewFlagSet("test report", flag.ExitOnError)
	reportDir := reportCmd.String("dir", "", "Report directory (default: ~/.config/astonish/reports)")
	if err := reportCmd.Parse(args); err != nil {
		return err
	}

	var targetName string
	if reportCmd.NArg() > 0 {
		targetName = reportCmd.Arg(0)
	}

	rdir := *reportDir
	if rdir == "" {
		reportsBase, err := config.GetReportsDir()
		if err != nil {
			return fmt.Errorf("failed to determine reports directory: %w", err)
		}
		if targetName != "" {
			rdir = filepath.Join(reportsBase, targetName)
		} else {
			rdir = reportsBase
		}
	}

	// If a specific suite/test was given, look for its report directly
	if targetName != "" {
		reportPath := filepath.Join(rdir, "suite_report.json")
		report, err := adrill.LoadReport(reportPath)
		if err != nil {
			return fmt.Errorf("no report found for %q: %w", targetName, err)
		}
		adrill.PrintReport(report, os.Stdout)
		return nil
	}

	// Otherwise, scan the reports directory for all reports
	entries, err := os.ReadDir(rdir)
	if err != nil {
		return fmt.Errorf("no reports found in %s: %w", rdir, err)
	}

	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		reportPath := filepath.Join(rdir, entry.Name(), "suite_report.json")
		report, err := adrill.LoadReport(reportPath)
		if err != nil {
			continue
		}
		found = true
		adrill.PrintReport(report, os.Stdout)
		fmt.Println()
	}

	if !found {
		fmt.Println("No reports found.")
		fmt.Println("Run drills first with: astonish drill run <suite>")
	}

	return nil
}

func handleDrillRemoveCommand(args []string) error {
	removeCmd := flag.NewFlagSet("test remove", flag.ExitOnError)
	forceFlag := removeCmd.Bool("force", false, "Skip confirmation prompt")
	keepTests := removeCmd.Bool("keep-tests", false, "When removing a suite, keep its test files")

	// Extract positional argument before flags
	var targetName string
	var flagArgs []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
		} else if targetName == "" {
			targetName = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	if err := removeCmd.Parse(flagArgs); err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	if targetName == "" {
		fmt.Println("usage: astonish drill remove <suite_or_drill> [--force] [--keep-tests]")
		fmt.Println("")
		fmt.Println("Removes a test suite and all its test files, or a single test file.")
		fmt.Println("When removing a suite, all associated test files are also deleted")
		fmt.Println("unless --keep-tests is specified.")
		return fmt.Errorf("no suite or test name provided")
	}

	// Strip .yaml extension if provided
	targetName = strings.TrimSuffix(targetName, ".yaml")
	targetName = strings.TrimSuffix(targetName, ".yml")

	dirs := adrill.DefaultDrillDirs()

	// Try as suite first
	suite, err := adrill.FindSuite(dirs, targetName)
	if err == nil {
		return handleRemoveSuite(dirs, suite, !*keepTests, *forceFlag)
	}

	// Try as individual test
	test, parentSuite, err := adrill.FindTestAndSuite(dirs, targetName)
	if err == nil {
		return handleRemoveTest(dirs, test, parentSuite, *forceFlag)
	}

	return fmt.Errorf("not found as suite or test: %s", targetName)
}

func handleRemoveSuite(dirs []string, suite *adrill.LoadedSuite, deleteTests bool, force bool) error {
	// Find all associated tests
	tests, findErr := adrill.FindTestsForSuite(dirs, suite.Name)
	if findErr != nil {
		slog.Warn("failed to find tests for suite", "suite", suite.Name, "error", findErr)
	}

	// Show what will be deleted
	fmt.Printf("Suite: %s (%s)\n", suite.Name, suite.File)
	if len(tests) > 0 && deleteTests {
		fmt.Printf("Tests (%d):\n", len(tests))
		for _, t := range tests {
			desc := t.Name
			if t.Config.Description != "" {
				desc = t.Config.Description
			}
			fmt.Printf("  - %s (%s)\n", desc, t.File)
		}
	} else if len(tests) > 0 {
		fmt.Printf("Warning: %d test file(s) reference this suite and will become orphaned.\n", len(tests))
	}

	// Check for report artifacts
	var reportDir string
	hasReports := false
	if reportsBase, err := config.GetReportsDir(); err == nil {
		reportDir = filepath.Join(reportsBase, suite.Name)
		if info, err := os.Stat(reportDir); err == nil && info.IsDir() {
			hasReports = true
			fmt.Printf("Reports: %s (will also be removed)\n", reportDir)
		}
	}

	// Confirm
	if !force {
		totalFiles := 1 // suite file
		if deleteTests {
			totalFiles += len(tests)
		}
		fmt.Printf("\nRemove %d file(s)? [y/N]: ", totalFiles)
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Perform deletion
	deleted, err := adrill.DeleteSuite(dirs, suite.Name, deleteTests)
	if err != nil {
		// Show partial results
		for _, path := range deleted {
			fmt.Printf("  Deleted: %s\n", path)
		}
		return fmt.Errorf("deletion failed: %w", err)
	}

	for _, path := range deleted {
		fmt.Printf("  Deleted: %s\n", path)
	}

	// Clean up report artifacts
	if hasReports {
		if err := os.RemoveAll(reportDir); err == nil {
			fmt.Printf("  Deleted: %s\n", reportDir)
		}
	}

	// Clean up knowledge docs (best-effort)
	if memDir, err := config.GetMemoryDir(nil); err == nil {
		docPath := filepath.Join(memDir, "flows", suite.Name+".md")
		os.Remove(docPath)
		for _, t := range tests {
			docPath = filepath.Join(memDir, "flows", t.Name+".md")
			os.Remove(docPath)
		}
	}

	testCount := len(deleted) - 1
	if testCount > 0 {
		fmt.Printf("\nRemoved suite %q and %d test(s).\n", suite.Name, testCount)
	} else {
		fmt.Printf("\nRemoved suite %q.\n", suite.Name)
	}
	return nil
}

func handleRemoveTest(dirs []string, test *adrill.LoadedTest, suite *adrill.LoadedSuite, force bool) error {
	fmt.Printf("Test: %s (%s)\n", test.Name, test.File)
	fmt.Printf("Suite: %s\n", suite.Name)

	if !force {
		fmt.Printf("\nRemove this test? [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	deletedPath, _, err := adrill.DeleteTest(dirs, test.Name)
	if err != nil {
		return fmt.Errorf("deletion failed: %w", err)
	}

	fmt.Printf("  Deleted: %s\n", deletedPath)

	// Clean up knowledge doc (best-effort)
	if memDir, err := config.GetMemoryDir(nil); err == nil {
		docPath := filepath.Join(memDir, "flows", test.Name+".md")
		os.Remove(docPath)
	}

	// Check for report artifacts
	if reportsBase, err := config.GetReportsDir(); err == nil {
		reportDir := filepath.Join(reportsBase, test.Name)
		if info, err := os.Stat(reportDir); err == nil && info.IsDir() {
			if err := os.RemoveAll(reportDir); err == nil {
				fmt.Printf("  Deleted: %s\n", reportDir)
			}
		}
	}

	// Warn about remaining tests in suite
	remaining, findErr := adrill.FindTestsForSuite(dirs, suite.Name)
	if findErr != nil {
		slog.Warn("failed to find remaining tests for suite", "suite", suite.Name, "error", findErr)
	}
	fmt.Printf("\nRemoved test %q. Suite %q has %d remaining test(s).\n", test.Name, suite.Name, len(remaining))
	return nil
}
