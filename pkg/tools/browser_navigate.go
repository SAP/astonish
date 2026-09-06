package tools

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/browser"
	"google.golang.org/adk/tool"
)

// BrowserNavigateArgs is the input for browser_navigate.
type BrowserNavigateArgs struct {
	URL string `json:"url" jsonschema:"The URL to navigate to (http or https)"`
}

// BrowserNavigateResult is the output of browser_navigate.
type BrowserNavigateResult struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// BrowserNavigate navigates the browser to a URL.
func BrowserNavigate(mgr *browser.Manager, guard *browser.NavigationGuard) func(tool.Context, BrowserNavigateArgs) (BrowserNavigateResult, error) {
	return func(ctx tool.Context, args BrowserNavigateArgs) (BrowserNavigateResult, error) {
		ensureBrowserSessionAndContext(mgr, ctx)

		if args.URL == "" {
			return BrowserNavigateResult{}, fmt.Errorf("url is required")
		}

		// Chromium often resolves localhost → ::1 first; IPv4-only listeners
		// (common for Vite --host 0.0.0.0) then refuse. Prefer 127.0.0.1.
		args.URL = browser.NormalizeLoopbackURL(args.URL)

		if err := guard.Check(args.URL); err != nil {
			return BrowserNavigateResult{}, err
		}

		pg, err := mgr.CurrentPage()
		if err != nil {
			err = fmt.Errorf("failed to get browser page: %w", err)
			if tip := sandboxBrowserErrorTip(mgr, err, ""); tip != "" {
				err = fmt.Errorf("%w (%s)", err, tip)
			}
			return BrowserNavigateResult{}, err
		}

		// Use navigation timeout for the Navigate() call itself to prevent
		// indefinite blocking when CDP connection dies or page hangs.
		if err := pg.Timeout(mgr.NavigationTimeout()).Navigate(args.URL); err != nil {
			err = fmt.Errorf("navigation failed: %w", err)
			if tip := sandboxBrowserErrorTip(mgr, err, args.URL); tip != "" {
				err = fmt.Errorf("%w (%s)", err, tip)
			}
			return BrowserNavigateResult{}, err
		}

		// Wait for the page to load with a timeout.
		// Many sites (NYT, CNN, etc.) have infinite network activity from ads,
		// analytics, and websockets that prevent the load event from firing.
		// A timeout ensures we don't hang forever.
		if err := pg.Timeout(mgr.NavigationTimeout()).WaitLoad(); err != nil {
			// Non-fatal — page content is likely already usable
		}

		info, _ := pg.Info()
		result := BrowserNavigateResult{URL: args.URL}
		if info != nil {
			result.URL = info.URL
			result.Title = info.Title
		}
		return result, nil
	}
}

func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "err_connection_refused") || strings.Contains(s, "connection refused")
}

func looksLikeLoopbackURL(rawURL string) bool {
	u := strings.ToLower(rawURL)
	return strings.Contains(u, "://127.0.0.1") ||
		strings.Contains(u, "://localhost") ||
		strings.Contains(u, "://[::1]")
}

// sandboxBrowserErrorTip returns a context-aware hint for navigate/page failures.
// It never claims in-container Chromium when SandboxEnabled is false or when
// CDP is bound to a container that does not match the current session.
// Concrete launch/start failures already carry their own diagnostics — do not
// append the generic "CDP not bound / restart Studio" tip on top of them.
func sandboxBrowserErrorTip(mgr *browser.Manager, err error, navURL string) string {
	if mgr == nil {
		return ""
	}
	if hasConcreteBrowserLaunchFailure(err) {
		if isBrowserLaunchSIGTERM(err) {
			return "in-container browser launch was interrupted by SIGTERM — retry browser_navigate; " +
				"if it persists check CloakBrowser/Chromium logs in the session container"
		}
		return ""
	}

	sessionID := mgr.SessionID()
	container := mgr.ContainerName()

	if !mgr.SandboxEnabled {
		if isConnectionRefused(err) && looksLikeLoopbackURL(navURL) {
			return fmt.Sprintf(
				"host Chromium / sandbox browser not wired (SandboxEnabled=false session=%q) — "+
					"cannot reach sandbox localhost; do not rewrite URLs to the container bridge IP",
				shortID(sessionID),
			)
		}
		return ""
	}

	if container == "" {
		return fmt.Sprintf(
			"sandbox browser was expected in-container but CDP is not bound to a session container "+
				"(session=%q) — restart Studio after upgrading",
			shortID(sessionID),
		)
	}

	if sessionID != "" && !containerMatchesSession(container, sessionID) {
		return fmt.Sprintf(
			"browser CDP is bound to container %q but the tool session is %q — "+
				"Manager was stuck on a previous session; retry browser_navigate after session rebound "+
				"(do not rewrite URLs to the container bridge IP)",
			container, shortID(sessionID),
		)
	}

	if isConnectionRefused(err) && looksLikeLoopbackURL(navURL) {
		return fmt.Sprintf(
			"in-container Chromium could not reach %s (session=%q container=%q) — "+
				"confirm the service listens on 0.0.0.0/127.0.0.1 inside the sandbox; "+
				"do not rewrite drills to the container bridge IP",
			navURL, shortID(sessionID), container,
		)
	}
	return ""
}

func hasConcreteBrowserLaunchFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	needles := []string{
		"failed to start browser",
		"browser launch exited",
		"failed to launch browser",
		"interrupted by sigterm",
		"cloakbrowser",
		"devtools port",
		"failed to start xvnc",
		"browser stack",
	}
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func isBrowserLaunchSIGTERM(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "exited with code 143") ||
		strings.Contains(s, "interrupted by sigterm")
}

func containerMatchesSession(containerName, sessionID string) bool {
	if containerName == "" || sessionID == "" {
		return false
	}
	return strings.Contains(containerName, sessionID)
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// BrowserNavigateBackArgs is the input for browser_navigate_back.
type BrowserNavigateBackArgs struct{}

// BrowserNavigateBack navigates the browser back in history.
func BrowserNavigateBack(mgr *browser.Manager) func(tool.Context, BrowserNavigateBackArgs) (BrowserNavigateResult, error) {
	return func(_ tool.Context, args BrowserNavigateBackArgs) (BrowserNavigateResult, error) {
		pg, err := mgr.CurrentPage()
		if err != nil {
			return BrowserNavigateResult{}, fmt.Errorf("failed to get browser page: %w", err)
		}

		if err := pg.Timeout(mgr.NavigationTimeout()).NavigateBack(); err != nil {
			return BrowserNavigateResult{}, fmt.Errorf("navigate back failed: %w", err)
		}

		if err := pg.Timeout(mgr.NavigationTimeout()).WaitLoad(); err != nil {
			// Non-fatal
		}

		info, _ := pg.Info()
		result := BrowserNavigateResult{}
		if info != nil {
			result.URL = info.URL
			result.Title = info.Title
		}
		return result, nil
	}
}
