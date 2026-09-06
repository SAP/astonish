package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
	"google.golang.org/adk/tool"
)

// DefaultKasmVNCPort is the port KasmVNC listens on for web connections.
const defaultKasmVNCPort = 6901

// --- browser_request_human (non-blocking: opens visual browser access for the user) ---

// BrowserRequestHumanArgs is the input for browser_request_human.
type BrowserRequestHumanArgs struct {
	Reason         string `json:"reason" jsonschema:"required,Why you need human help. Shown to the user. Be specific about what they should do (e.g. 'solve the CAPTCHA and click Submit')."`
	CaptureActions bool   `json:"capture_actions,omitempty" jsonschema:"When true, start DOM action capture for the handoff window so clicks/typing can be turned into drill steps via draft_drill_from_action_log. Capture stops when the user clicks Done."`
}

// BrowserRequestHumanResult is the output of browser_request_human.
type BrowserRequestHumanResult struct {
	Success       bool   `json:"success"`
	ListenAddress string `json:"listen_address"` // e.g. "127.0.0.1:9222" or VNC proxy URL
	PageURL       string `json:"page_url"`
	PageTitle     string `json:"page_title"`
	Message       string `json:"message"`                 // Full instructions for the user
	VNCProxyURL   string `json:"vnc_proxy_url,omitempty"` // Set when using KasmVNC
}

// BrowserRequestHuman opens visual browser access for the user and returns
// immediately. The chat remains fully interactive — the agent can continue
// receiving instructions and controlling the browser while the user views
// and interacts with it via the VNC iframe or CDP DevTools.
//
// In host mode, this starts a CDP proxy (chrome://inspect).
// In container mode, this starts KasmVNC and returns a web URL for visual access.
//
// The user clicks "Done" in the Studio browser panel when they no longer need
// visual access. This revokes the VNC token but does NOT destroy the browser
// or end the session.
func BrowserRequestHuman(mgr *browser.Manager) func(tool.Context, BrowserRequestHumanArgs) (BrowserRequestHumanResult, error) {
	return func(ctx tool.Context, args BrowserRequestHumanArgs) (BrowserRequestHumanResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)

		if args.Reason == "" {
			return BrowserRequestHumanResult{}, fmt.Errorf("reason is required")
		}

		// Ensure the browser is running
		cdpURL := mgr.CDPURL()
		if cdpURL == "" {
			if _, err := mgr.GetOrLaunch(); err != nil {
				return BrowserRequestHumanResult{}, fmt.Errorf("failed to launch browser: %w", err)
			}
			cdpURL = mgr.CDPURL()
		}
		if cdpURL == "" {
			return BrowserRequestHumanResult{}, fmt.Errorf("browser CDP endpoint not available")
		}

		// Capture current page state for context
		pageURL := ""
		pageTitle := ""
		page, err := mgr.CurrentPage()
		if err == nil {
			if pageInfo, infoErr := page.Info(); infoErr == nil && pageInfo != nil {
				pageURL = pageInfo.URL
				pageTitle = pageInfo.Title
			}
		}

		// Container mode: start KasmVNC for visual access
		if mgr.IsContainerMode() {
			res, err := startKasmVNCHandoff(mgr, args.Reason, pageURL, pageTitle)
			if err != nil {
				return res, err
			}
			if args.CaptureActions {
				if capErr := mgr.StartActionCapture(true); capErr != nil {
					res.Message += fmt.Sprintf("\n\nWARNING: action capture failed to start: %v", capErr)
				} else {
					res.Message += "\n\nAction capture is ON — clicks and typing will be logged for tutorial drafting. Capture stops when Done is clicked."
				}
			}
			return res, nil
		}

		// Host mode: start CDP proxy (existing behavior)
		res, err := startCDPHandoff(mgr, args.Reason, cdpURL, pageURL, pageTitle)
		if err != nil {
			return res, err
		}
		if args.CaptureActions {
			if capErr := mgr.StartActionCapture(true); capErr != nil {
				res.Message += fmt.Sprintf("\n\nWARNING: action capture failed to start: %v", capErr)
			} else {
				res.Message += "\n\nAction capture is ON — clicks and typing will be logged for tutorial drafting. Capture stops when Done is clicked."
			}
		}
		return res, nil
	}
}

// startCDPHandoff starts the traditional CDP proxy handoff (chrome://inspect).
func startCDPHandoff(mgr *browser.Manager, reason string, cdpURL, pageURL, pageTitle string) (BrowserRequestHumanResult, error) {
	handoffCfg := mgr.HandoffConfig()
	info, err := mgr.StartHandoff(browser.HandoffOpts{
		CDPURL:      cdpURL,
		Port:        handoffCfg.Port,
		BindAddress: handoffCfg.BindAddress,
		Timeout:     5 * time.Minute,
		Reason:      reason,
	})
	if err != nil {
		return BrowserRequestHumanResult{}, fmt.Errorf("failed to start handoff: %w", err)
	}

	// Build user-facing instructions that the agent MUST relay.
	connectAddr := info.ListenAddress
	addrNote := ""
	if strings.HasPrefix(connectAddr, "0.0.0.0:") || strings.HasPrefix(connectAddr, "[::]:") {
		port := connectAddr[strings.LastIndex(connectAddr, ":")+1:]
		addrNote = fmt.Sprintf(
			"\nNOTE: The proxy is listening on all interfaces (port %s). "+
				"Use this machine's IP address instead of 0.0.0.0 "+
				"(e.g. 192.168.x.x:%s or your server's IP:%s).\n", port, port, port)
		connectAddr = "<this-machine-ip>:" + port
	}
	message := fmt.Sprintf(
		"BROWSER ACCESS SHARED\n\n"+
			"Reason: %s\n\n"+
			"Current page: %s\n\n"+
			"To take over the browser:\n"+
			"1. Open Chrome and go to chrome://inspect\n"+
			"2. Click 'Configure...' and add: %s\n"+
			"3. The browser tab should appear under 'Remote Target'\n"+
			"4. Click 'inspect' to open DevTools with full control\n"+
			"%s\n"+
			"You can continue sending me instructions while the browser is shared.\n"+
			"Click 'Done' in the browser panel when you no longer need visual access.",
		reason,
		pageURL,
		connectAddr,
		addrNote,
	)

	return BrowserRequestHumanResult{
		Success:       true,
		ListenAddress: info.ListenAddress,
		PageURL:       pageURL,
		PageTitle:     pageTitle,
		Message:       message,
	}, nil
}

// startKasmVNCHandoff starts KasmVNC in the browser container for visual access.
// Returns a proxy URL that the user can open in any web browser.
//
// Two backend paths:
//   - OpenShell (ContainerDialFunc set): KasmVNC is already running inside the
//     sandbox (started as part of the browser launch script). We only need to
//     register the handoff token and return the proxy URL.
//   - Incus (ContainerDialFunc nil): connect via Incus API, start KasmVNC if
//     not already running, then register token and return proxy URL.
func startKasmVNCHandoff(mgr *browser.Manager, reason string, pageURL, pageTitle string) (BrowserRequestHumanResult, error) {
	containerName := mgr.ContainerName()
	if containerName == "" {
		return BrowserRequestHumanResult{}, fmt.Errorf("browser container not available")
	}

	// OpenShell path: KasmVNC is pre-started by the launch script.
	// No need to call into Incus.
	if mgr.ContainerDialFunc != nil {
		return buildVNCHandoffResult(mgr, containerName, reason, pageURL, pageTitle)
	}

	// Incus path: start KasmVNC via Incus exec API.
	platform := incus.DetectPlatform()
	client, err := incus.Connect(platform)
	if err != nil {
		return BrowserRequestHumanResult{}, fmt.Errorf("failed to connect to sandbox: %w", err)
	}

	cfg := mgr.Config()

	// Start KasmVNC in the container (auth disabled via -DisableBasicAuth)
	err = incus.StartKasmVNC(client, containerName, incus.BrowserContainerConfig{
		ViewportWidth:       cfg.ViewportWidth,
		ViewportHeight:      cfg.ViewportHeight,
		KasmVNCPort:         cfg.KasmVNCPort,
		ChromePath:          cfg.ChromePath,
		FingerprintSeed:     cfg.FingerprintSeed,
		FingerprintPlatform: cfg.FingerprintPlatform,
	})
	if err != nil {
		return BrowserRequestHumanResult{}, fmt.Errorf("failed to start KasmVNC: %w", err)
	}

	return buildVNCHandoffResult(mgr, containerName, reason, pageURL, pageTitle)
}

// buildVNCHandoffResult registers a handoff token and constructs the proxy URL.
// Shared between Incus and OpenShell paths.
func buildVNCHandoffResult(mgr *browser.Manager, containerName, reason, pageURL, pageTitle string) (BrowserRequestHumanResult, error) {
	// Register a handoff token that authorizes VNC proxy access for this
	// container. The auth middleware validates this token — without it,
	// the VNC proxy returns 401 even though KasmVNC itself has auth disabled.
	registry := browser.GetHandoffTokenRegistry()
	token, err := registry.Register(containerName)
	if err != nil {
		return BrowserRequestHumanResult{}, fmt.Errorf("failed to generate handoff token: %w", err)
	}

	// Determine VNC port — use config if set, else default.
	vncPort := mgr.Config().KasmVNCPort
	if vncPort == 0 {
		vncPort = defaultKasmVNCPort
	}
	_ = vncPort // Port is encoded in the proxy route, not the URL query.

	// Build the proxy URL through the Studio API.
	// The KasmVNC web client constructs its WebSocket URL from the browser's
	// current location. Since the iframe loads at /api/browser/vnc/{container}/,
	// the default WebSocket path "websockify" would resolve to the wrong URL
	// (e.g. wss://host/websockify instead of wss://host/api/browser/vnc/{container}/websockify).
	// We pass the correct path via query parameter which KasmVNC's getConfigVar() reads.
	wsPath := fmt.Sprintf("api/browser/vnc/%s/websockify", containerName)
	proxyURL := fmt.Sprintf("/api/browser/vnc/%s/?token=%s&path=%s", containerName, token, wsPath)

	message := fmt.Sprintf(
		"BROWSER ACCESS SHARED\n\n"+
			"Reason: %s\n\n"+
			"Current page: %s\n\n"+
			"A visual browser session is now available. The browser panel should appear automatically in Studio.\n\n"+
			"You can continue sending me instructions while the browser is shared — "+
			"I can navigate, click, and interact with the browser while you watch.\n\n"+
			"Click 'Done' in the browser panel when you no longer need visual access.",
		reason,
		pageURL,
	)

	return BrowserRequestHumanResult{
		Success:       true,
		ListenAddress: proxyURL,
		PageURL:       pageURL,
		PageTitle:     pageTitle,
		Message:       message,
		VNCProxyURL:   proxyURL,
	}, nil
}
