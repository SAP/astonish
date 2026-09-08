package tools

import (
	"github.com/SAP/astonish/pkg/browser"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// BrowserNavigateDescription is the tool schema for browser_navigate.
// CloakBrowser is not on PATH; agents must not treat `which chromium` as proof
// the sandbox browser is missing.
const BrowserNavigateDescription = "Navigate the browser to a URL. When sandbox is enabled, CloakBrowser (patched Chromium) runs inside the session container at /home/browser/.cloakbrowser/*/chrome — it is NOT the debian chromium package and is NOT on PATH. Do not use which chromium / google-chrome to check the browser; call this tool. Use http://localhost:<port> or http://127.0.0.1:<port> to reach services in the same container. Never use the container bridge IP."

// GetBrowserTools creates all browser automation tools sharing a single
// BrowserManager instance. The browser is launched lazily on first use.
// Navigation to private/localhost IPs is blocked (SSRF protection).
func GetBrowserTools(mgr *browser.Manager) ([]tool.Tool, error) {
	guard := browser.DefaultNavigationGuard()
	return getBrowserToolsWithGuard(mgr, guard)
}

// GetBrowserToolsForSandbox creates browser tools with private network
// blocking disabled. When sandbox is enabled, Chromium runs inside the
// session container (same network namespace as services), so localhost and
// private addresses are reachable.
func GetBrowserToolsForSandbox(mgr *browser.Manager) ([]tool.Tool, error) {
	guard := &browser.NavigationGuard{BlockPrivateNetworks: false}
	return getBrowserToolsWithGuard(mgr, guard)
}

func getBrowserToolsWithGuard(mgr *browser.Manager, guard *browser.NavigationGuard) ([]tool.Tool, error) {
	refs := browser.NewRefMap()

	// --- Navigation ---

	navigateTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_navigate",
		Description: BrowserNavigateDescription,
	}, safeBrowserFunc(BrowserNavigate(mgr, guard)))
	if err != nil {
		return nil, err
	}

	navigateBackTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_navigate_back",
		Description: "Go back to the previous page in the browser history.",
	}, safeBrowserFunc(BrowserNavigateBack(mgr)))
	if err != nil {
		return nil, err
	}

	// --- Interaction ---

	clickTool, err := functiontool.New(functiontool.Config{
		Name: "browser_click",
		Description: "Click an element on the page. Use a ref from browser_snapshot. " +
			"Set animate_cursor=true for tutorial recordings (moves the visible demo cursor first).",
	}, safeBrowserFunc(BrowserClick(mgr, refs)))
	if err != nil {
		return nil, err
	}

	highlightTool, err := functiontool.New(functiontool.Config{
		Name: "browser_highlight",
		Description: "Draw a visible highlight overlay around an element (ref or CSS selector). " +
			"Optional label/color/duration_ms. Use for tutorial scene focus boxes.",
	}, safeBrowserFunc(BrowserHighlight(mgr, refs)))
	if err != nil {
		return nil, err
	}

	clearHighlightsTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_clear_highlights",
		Description: "Remove all highlight overlays drawn by browser_highlight.",
	}, safeBrowserFunc(BrowserClearHighlights(mgr)))
	if err != nil {
		return nil, err
	}

	moveCursorTool, err := functiontool.New(functiontool.Config{
		Name: "browser_move_cursor",
		Description: "Move the visible demo cursor (and real mouse) to a target: element ref, CSS selector, or x/y pixel coordinates. Enables the demo cursor overlay for tutorial recordings.\n\n" +
			"IMPORTANT: For smooth, human-like cursor movement, always use a SINGLE call with `duration_ms` set. " +
			"Do NOT chain multiple browser_move_cursor calls to animate motion — inter-call latency causes visible stop-start jerking. " +
			"One call with duration_ms produces natural ease-in-out acceleration automatically.",
	}, safeBrowserFunc(BrowserMoveCursor(mgr, refs)))
	if err != nil {
		return nil, err
	}

	fullscreenTool, err := functiontool.New(functiontool.Config{
		Name: "browser_fullscreen",
		Description: "Enter or exit Chromium window fullscreen (CDP best-effort) before recording. " +
			"Minimizes browser chrome; X11grab still captures the display.",
	}, safeBrowserFunc(BrowserFullscreen(mgr)))
	if err != nil {
		return nil, err
	}

	typeTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_type",
		Description: "Type text into an editable element (input, textarea). Clears existing content first.",
	}, safeBrowserFunc(BrowserType(mgr, refs)))
	if err != nil {
		return nil, err
	}

	hoverTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_hover",
		Description: "Hover over an element on the page to trigger hover effects or tooltips.",
	}, safeBrowserFunc(BrowserHover(mgr, refs)))
	if err != nil {
		return nil, err
	}

	dragTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_drag",
		Description: "Drag an element from one position to another.",
	}, safeBrowserFunc(BrowserDrag(mgr, refs)))
	if err != nil {
		return nil, err
	}

	pressKeyTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_press_key",
		Description: "Press a keyboard key (e.g. Enter, Tab, Escape, ArrowDown). Works on the focused element.",
	}, safeBrowserFunc(BrowserPressKey(mgr)))
	if err != nil {
		return nil, err
	}

	selectOptionTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_select_option",
		Description: "Select option(s) from a dropdown select element by visible text.",
	}, safeBrowserFunc(BrowserSelectOption(mgr, refs)))
	if err != nil {
		return nil, err
	}

	fillFormTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_fill_form",
		Description: "Fill multiple form fields at once. Pass an array of {ref, value} pairs.",
	}, safeBrowserFunc(BrowserFillForm(mgr, refs)))
	if err != nil {
		return nil, err
	}

	// --- Observation ---

	snapshotTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_snapshot",
		Description: "Capture an accessibility snapshot of the current page. Returns a text tree with ref IDs for interaction. Use mode=\"efficient\" for large pages (compact, interactive-only, depth=6, maxChars=10000).",
	}, safeBrowserFunc(BrowserSnapshot(mgr, refs)))
	if err != nil {
		return nil, err
	}

	screenshotTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_take_screenshot",
		Description: "Take a screenshot of the current page. You can't perform actions based on the screenshot — use browser_snapshot for that.",
	}, safeBrowserFunc(BrowserTakeScreenshot(mgr, refs)))
	if err != nil {
		return nil, err
	}

	consoleTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_console_messages",
		Description: "Returns console messages (log, warn, error, debug) from the current page.",
	}, safeBrowserFunc(BrowserConsoleMessages(mgr)))
	if err != nil {
		return nil, err
	}

	networkTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_network_requests",
		Description: "Returns network requests made by the current page. Useful for debugging API calls.",
	}, safeBrowserFunc(BrowserNetworkRequests(mgr)))
	if err != nil {
		return nil, err
	}

	// --- Management ---

	tabsTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_tabs",
		Description: "Manage browser tabs: list, new, close, or select. Use targetId from list results for close/select.",
	}, safeBrowserFunc(BrowserTabs(mgr, guard)))
	if err != nil {
		return nil, err
	}

	closeTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_close",
		Description: "Close the current browser page/tab.",
	}, safeBrowserFunc(BrowserClose(mgr)))
	if err != nil {
		return nil, err
	}

	resizeTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_resize",
		Description: "Resize the browser viewport to the specified width and height in pixels.",
	}, safeBrowserFunc(BrowserResize(mgr)))
	if err != nil {
		return nil, err
	}

	waitForTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_wait_for",
		Description: "Wait for a condition: text to appear/disappear, CSS selector visible, URL match, page load state, or JS expression truthy. Avoid state=\"networkidle\" on SPAs (persistent WebSocket connections cause full timeout).",
	}, safeBrowserFunc(BrowserWaitFor(mgr)))
	if err != nil {
		return nil, err
	}

	pauseTool, err := functiontool.New(functiontool.Config{
		Name: "browser_pause",
		Description: "Pause for a fixed duration in milliseconds (max 120000). Use for tutorial " +
			"pacing so narration can finish before the next UI action. Prefer browser_wait_for " +
			"when waiting for page state.",
	}, safeBrowserFunc(BrowserPause(mgr)))
	if err != nil {
		return nil, err
	}

	fileUploadTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_file_upload",
		Description: "Upload files to a file input element on the page.",
	}, safeBrowserFunc(BrowserFileUpload(mgr, refs)))
	if err != nil {
		return nil, err
	}

	handleDialogTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_handle_dialog",
		Description: "Handle a native JS dialog (alert, confirm, prompt). Must be called before the triggering action. Only works for native dialogs — use browser_snapshot for custom modals.",
	}, safeBrowserFunc(BrowserHandleDialog(mgr)))
	if err != nil {
		return nil, err
	}

	// --- Advanced ---

	evaluateTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_evaluate",
		Description: "Evaluate a JavaScript expression in the page context. Returns the result. Optionally scope to an element via ref.",
	}, safeBrowserFunc(BrowserEvaluate(mgr, refs)))
	if err != nil {
		return nil, err
	}

	runCodeTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_run_code",
		Description: "Run a multi-line JavaScript code snippet in the page context. Supports async/await. Has access to the full browser DOM API.",
	}, safeBrowserFunc(BrowserRunCode(mgr)))
	if err != nil {
		return nil, err
	}

	pdfTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_pdf",
		Description: "Save the current page as a PDF file. Returns the file path.",
	}, safeBrowserFunc(BrowserPDF(mgr)))
	if err != nil {
		return nil, err
	}

	responseBodyTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_response_body",
		Description: "Intercept and read HTTP response bodies. Workflow: action=\"listen\" urlPattern=\"*api/data*\" → trigger request → action=\"read\" to get body. Use action=\"stop\" to remove interceptor.",
	}, safeBrowserFunc(BrowserResponseBody(mgr)))
	if err != nil {
		return nil, err
	}

	// --- State & Emulation ---

	cookiesTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_cookies",
		Description: "Get, set, or clear browser cookies. Use action=get to inspect, action=set to add/modify, action=clear to remove all.",
	}, safeBrowserFunc(BrowserCookies(mgr)))
	if err != nil {
		return nil, err
	}

	storageTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_storage",
		Description: "Read, write, or clear localStorage/sessionStorage. Use kind=local or kind=session with action=get/getAll/set/clear.",
	}, safeBrowserFunc(BrowserStorage(mgr)))
	if err != nil {
		return nil, err
	}

	setOfflineTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_offline",
		Description: "Enable or disable network connectivity for the browser page. Simulates offline mode.",
	}, safeBrowserFunc(BrowserSetOffline(mgr)))
	if err != nil {
		return nil, err
	}

	setHeadersTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_headers",
		Description: "Add extra HTTP headers to all requests from this page. Pass an empty map to clear.",
	}, safeBrowserFunc(BrowserSetHeaders(mgr)))
	if err != nil {
		return nil, err
	}

	setCredentialsTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_credentials",
		Description: "Set HTTP Basic Auth credentials. The browser will automatically respond to auth challenges.",
	}, safeBrowserFunc(BrowserSetCredentials(mgr)))
	if err != nil {
		return nil, err
	}

	setGeolocationTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_geolocation",
		Description: "Override the browser's geolocation (latitude, longitude, accuracy).",
	}, safeBrowserFunc(BrowserSetGeolocation(mgr)))
	if err != nil {
		return nil, err
	}

	setMediaTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_media",
		Description: "Set the preferred color scheme (dark, light, no-preference) for CSS media queries.",
	}, safeBrowserFunc(BrowserSetMedia(mgr)))
	if err != nil {
		return nil, err
	}

	setTimezoneTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_timezone",
		Description: "Override the browser's timezone (IANA ID like 'America/New_York'). Empty to clear.",
	}, safeBrowserFunc(BrowserSetTimezone(mgr)))
	if err != nil {
		return nil, err
	}

	setLocaleTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_locale",
		Description: "Override the browser's locale (BCP 47 like 'en-US', 'fr-FR'). Empty to clear.",
	}, safeBrowserFunc(BrowserSetLocale(mgr)))
	if err != nil {
		return nil, err
	}

	setDeviceTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_set_device",
		Description: "Emulate a mobile/tablet device (viewport, user agent, touch, DPR). Supports iPhone, iPad, Pixel, Galaxy, etc. Use device=\"clear\" to remove emulation.",
	}, safeBrowserFunc(BrowserSetDevice(mgr)))
	if err != nil {
		return nil, err
	}

	// --- Human-in-the-loop ---

	requestHumanTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_request_human",
		Description: "Share the browser visually with the user for human intervention (CAPTCHAs, MFA, payment forms, or demonstrating a UI flow to capture). Returns immediately — the chat stays interactive. Set capture_actions=true to record DOM clicks/typing for draft_drill_from_action_log. The user clicks Done when finished (stops capture if started for handoff).",
	}, safeBrowserFunc(BrowserRequestHuman(mgr)))
	if err != nil {
		return nil, err
	}

	startActionCaptureTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_start_action_capture",
		Description: "Inject a DOM action recorder that logs clicks, form changes, Enter/Tab, and navigations to an in-page log (for tutorial authoring).",
	}, safeBrowserFunc(BrowserStartActionCapture(mgr)))
	if err != nil {
		return nil, err
	}

	stopActionCaptureTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_stop_action_capture",
		Description: "Stop DOM action capture. The action log is retained until browser_clear_action_log.",
	}, safeBrowserFunc(BrowserStopActionCapture(mgr)))
	if err != nil {
		return nil, err
	}

	getActionLogTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_get_action_log",
		Description: "Return the captured DOM action log as JSON events. Optionally clear after reading.",
	}, safeBrowserFunc(BrowserGetActionLog(mgr)))
	if err != nil {
		return nil, err
	}

	clearActionLogTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_clear_action_log",
		Description: "Clear the in-page DOM action capture log.",
	}, safeBrowserFunc(BrowserClearActionLog(mgr)))
	if err != nil {
		return nil, err
	}

	// --- Session recording ---

	startRecordingTool, err := functiontool.New(functiontool.Config{
		Name: "browser_start_recording",
		Description: "Start recording the browser display to an MP4 file (ffmpeg x11grab of the " +
			"live X display size). Call this before a scripted demo sequence. Only one recording " +
			"at a time. Returns path plus actual capture width/height. Video is written under " +
			"recordings/ (local) or /tmp/astonish-recordings/ (sandbox).",
	}, safeBrowserFunc(BrowserStartRecording(mgr)))
	if err != nil {
		return nil, err
	}

	stopRecordingTool, err := functiontool.New(functiontool.Config{
		Name: "browser_stop_recording",
		Description: "Stop the active browser display recording and finalize the MP4. Returns path, " +
			"duration_seconds, and size_bytes. The file appears as a session artifact for download.",
	}, safeBrowserFunc(BrowserStopRecording(mgr)))
	if err != nil {
		return nil, err
	}

	recordingStatusTool, err := functiontool.New(functiontool.Config{
		Name:        "browser_recording_status",
		Description: "Check whether a browser display recording is in progress and how long it has been running.",
	}, safeBrowserFunc(BrowserRecordingStatus(mgr)))
	if err != nil {
		return nil, err
	}

	return []tool.Tool{
		// Navigation
		navigateTool, navigateBackTool,
		// Interaction
		clickTool, typeTool, hoverTool, dragTool, pressKeyTool, selectOptionTool, fillFormTool,
		highlightTool, clearHighlightsTool, moveCursorTool,
		// Observation
		snapshotTool, screenshotTool, consoleTool, networkTool,
		// Management
		tabsTool, closeTool, resizeTool, fullscreenTool, waitForTool, pauseTool, fileUploadTool, handleDialogTool,
		// Advanced
		evaluateTool, runCodeTool, pdfTool, responseBodyTool,
		// State & Emulation
		cookiesTool, storageTool,
		setOfflineTool, setHeadersTool, setCredentialsTool,
		setGeolocationTool, setMediaTool, setTimezoneTool, setLocaleTool, setDeviceTool,
		// Human-in-the-loop + action capture
		requestHumanTool,
		startActionCaptureTool, stopActionCaptureTool, getActionLogTool, clearActionLogTool,
		// Session recording
		startRecordingTool, stopRecordingTool, recordingStatusTool,
	}, nil
}
