package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// defaultPlatform reported to websites via navigator.platform / CDP.
const defaultPlatform = "Win32"

// defaultCDPPort is the CDP DevTools port exposed by the socat bridge inside
// sandbox containers. Chromium listens on localhost:9223; socat bridges
// 0.0.0.0:9222 → 127.0.0.1:9223 making it reachable from outside.
const defaultCDPPort = 9222

// timestampToTime converts a CDP timestamp (seconds since epoch as float64)
// to a Go time.Time.
func timestampToTime(ts float64) time.Time {
	if ts == 0 {
		return time.Now()
	}
	sec := int64(ts)
	nsec := int64((ts - float64(sec)) * 1e9)
	return time.Unix(sec, nsec)
}

// BrowserConfig configures the managed browser instance.
type BrowserConfig struct {
	Headless          bool          // Default: false (headed with Xvfb for stealth)
	ChromePath        string        // Empty = auto-download via rod launcher
	ViewportWidth     int           // Default: 1920
	ViewportHeight    int           // Default: 1080
	NoSandbox         bool          // For running as root in containers
	UserDataDir       string        // Browser profile dir. Default: ~/.config/astonish/browser/
	NavigationTimeout time.Duration // Max time to wait for page load (default: 30s)
	Proxy             string        // HTTP/SOCKS proxy URL. Empty = direct connection.
	RemoteCDPURL      string        // External CDP endpoint. When set, skip local launch.

	// CloakBrowser fingerprint control (only effective with CloakBrowser binary)
	FingerprintSeed     string // Deterministic fingerprint seed (e.g. "42000")
	FingerprintPlatform string // OS to spoof: "windows", "macos", "linux"

	// Handoff (human-in-the-loop)
	HandoffBindAddress string // Network bind address for CDP handoff. Default: "127.0.0.1"
	HandoffPort        int    // TCP port for CDP handoff proxy. Default: 9222

	// KasmVNCPort is the port KasmVNC listens on inside the container. Default: 6901.
	KasmVNCPort     int
	KasmVNCPassword string // VNC password for handoff. Empty = auto-generated.
}

// HandoffCfg holds resolved handoff configuration for external use.
type HandoffCfg struct {
	BindAddress string
	Port        int
}

// DefaultConfig returns a BrowserConfig with sensible defaults.
// Headless defaults to false for better anti-detection (headed mode with Xvfb).
// NoSandbox is enabled automatically when running as root (uid 0),
// because Chrome refuses to use its sandbox when running as root.
// UserDataDir defaults to ~/.config/astonish/browser/ so that login
// sessions, cookies, and site data persist across restarts.
func DefaultConfig() BrowserConfig {
	return BrowserConfig{
		Headless:          false,
		ViewportWidth:     1920,
		ViewportHeight:    1080,
		NoSandbox:         os.Getuid() == 0,
		UserDataDir:       defaultProfileDir(),
		NavigationTimeout: 30 * time.Second,
	}
}

// ConfigOverrides holds optional user settings from config.yaml that override
// the browser defaults. Pointer fields (nil = use default) and zero-value
// fields (empty string / 0 = use default) follow the same convention as
// config.BrowserAppConfig.
type ConfigOverrides struct {
	Headless            *bool
	ViewportWidth       int
	ViewportHeight      int
	NoSandbox           *bool
	ChromePath          string
	UserDataDir         string
	NavigationTimeout   int // seconds; 0 = use default
	Proxy               string
	RemoteCDPURL        string
	HandoffBindAddress  string
	HandoffPort         int
	FingerprintSeed     string
	FingerprintPlatform string
	KasmVNCPort         int
	KasmVNCPassword     string
}

// OverrideConfig applies optional overrides to the default config.
// Zero/nil values are ignored (the default is preserved). This is used by the
// launcher to merge user config from config.yaml with sensible defaults.
func OverrideConfig(o ConfigOverrides) BrowserConfig {
	cfg := DefaultConfig()
	if o.Headless != nil {
		cfg.Headless = *o.Headless
	}
	if o.ViewportWidth > 0 {
		cfg.ViewportWidth = o.ViewportWidth
	}
	if o.ViewportHeight > 0 {
		cfg.ViewportHeight = o.ViewportHeight
	}
	if o.NoSandbox != nil {
		cfg.NoSandbox = *o.NoSandbox
	}
	if o.ChromePath != "" {
		cfg.ChromePath = o.ChromePath
	}
	if o.UserDataDir != "" {
		cfg.UserDataDir = o.UserDataDir
	}
	if o.NavigationTimeout > 0 {
		cfg.NavigationTimeout = time.Duration(o.NavigationTimeout) * time.Second
	}
	if o.Proxy != "" {
		cfg.Proxy = o.Proxy
	}
	if o.RemoteCDPURL != "" {
		cfg.RemoteCDPURL = o.RemoteCDPURL
	}
	if o.HandoffBindAddress != "" {
		cfg.HandoffBindAddress = o.HandoffBindAddress
	}
	if o.HandoffPort > 0 {
		cfg.HandoffPort = o.HandoffPort
	}
	if o.FingerprintSeed != "" {
		cfg.FingerprintSeed = o.FingerprintSeed
	}
	if o.FingerprintPlatform != "" {
		cfg.FingerprintPlatform = o.FingerprintPlatform
	}
	if o.KasmVNCPort > 0 {
		cfg.KasmVNCPort = o.KasmVNCPort
	}
	if o.KasmVNCPassword != "" {
		cfg.KasmVNCPassword = o.KasmVNCPassword
	}
	return cfg
}

// defaultProfileDir returns the default persistent browser profile directory.
// Falls back to empty string (temp dir) if the config directory can't be resolved.
func defaultProfileDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "astonish", "browser")
}

// Manager manages a singleton browser instance. The browser is
// launched lazily on first tool invocation and cleaned up when the session ends.
// By default, Chrome runs in headed mode (with Xvfb on Linux) for maximum
// stealth. Falls back to headless if Xvfb is unavailable.
//
// When SandboxEnabled is true, the browser runs inside the session container
// (the same container used by shell_command, file tools, etc.). The Manager
// connects to Chromium via a remote CDP URL through the container's bridge IP.
type Manager struct {
	mu        sync.Mutex
	browser   *rod.Browser
	incognito *rod.Browser // ephemeral context with isolated cookies/storage (nil until requested)
	launch    *launcher.Launcher
	config    BrowserConfig
	pages     map[proto.TargetTargetID]*PageState
	pagesMu   sync.RWMutex
	activePg  *rod.Page // most recently active page
	cdpURL    string    // CDP WebSocket endpoint URL (set after launch)
	xvfb      *Xvfb     // virtual display for headed mode on Linux (nil if not used)
	logger    *log.Logger

	// Detected Chrome version (set after launch).
	chromeVersion string

	// Handoff state (human-in-the-loop). The handoff server provides a CDP
	// proxy for host-mode browser sharing. In container mode, KasmVNC is used
	// instead, but the handoff server can still be started for auto-done
	// detection via DevTools disconnect.
	handoff       *HandoffServer
	handoffReason string

	// Container state (set when SandboxEnabled is true).
	// containerName is the session container the browser runs in.
	containerName string
	// containerIP is the container's bridge IPv4 address.
	containerIP string
	// browserProcess keeps the browser exec session alive. When non-nil, closing
	// it terminates KasmVNC + Chrome + socat inside the container.
	browserProcess io.Closer
	// sessionID is the agent session this Manager is bound to.
	sessionID string
	// sessionAliases maps delegate sub-agent session IDs to their parent
	// session ID, so EnsureSessionID resolves them to the same container.
	sessionAliases map[string]string

	// ContainerEnsureReadyFunc waits for background sandbox provisioning before
	// browser launch resolves the session container. It is optional for backends
	// whose resolver already performs synchronous provisioning.
	ContainerEnsureReadyFunc func(sessionID string) error

	// ContainerResolveFunc resolves the session container for browser execution.
	// Called lazily by launchInContainer() to get the container name and IP.
	// The browser runs in the same session container as other tools.
	// Returns (containerName, containerIP, error).
	ContainerResolveFunc func(sessionID string) (string, string, error)

	// ContainerStartBrowserFunc starts Chromium + KasmVNC + socat inside the
	// session container. Called after ContainerResolveFunc succeeds. The browser
	// package doesn't import the sandbox package, so this callback bridges the gap.
	// Returns an io.Closer that keeps the browser processes alive; the Manager
	// stores it and closes it during cleanup/reset to allow the OS to reap them.
	ContainerStartBrowserFunc func(containerName string) (io.Closer, error)

	// ContainerDialFunc creates a tunneled net.Conn to a port inside the named
	// container. This is wired by the launcher to sandbox.ContainerDialer.Dial,
	// routing connections through the Incus exec API (socat STDIO) rather than
	// direct TCP to the container's bridge IP. This makes CDP work on both
	// Linux native and Docker+Incus (macOS/Windows) where bridge IPs are not
	// routable from the host.
	ContainerDialFunc func(containerName string, port int) (net.Conn, error)

	// ActivityTouchFunc, when set, is called on every browser tool execution
	// to signal that the session's container is actively being used. This
	// prevents the sandbox idle watchdog from killing containers that are
	// busy with browser automation (which bypasses the node process and thus
	// never triggers Call()-based activity tracking).
	ActivityTouchFunc func(sessionID string)

	// ContainerStartRecordingFunc starts ffmpeg x11grab inside the session
	// container after probing the live X display size. Wired by the sandbox
	// package when SandboxEnabled is true.
	ContainerStartRecordingFunc ContainerStartRecordingFunc

	// SandboxEnabled is set to true by the launcher when sandbox is available.
	// When true, the browser runs inside the session container instead of locally.
	SandboxEnabled bool

	// recording tracks an in-progress session capture (nil when idle).
	recording *activeRecording

	// actionCapture tracks DOM action recording for tutorial authoring.
	actionCapture *actionCaptureState

	// demoOverlay tracks tutorial highlight boxes and the visible demo cursor.
	demoOverlay *demoOverlayState
}

// NewManager creates a Manager with the given config. The browser is NOT
// launched until GetOrLaunch is called.
func NewManager(cfg BrowserConfig) *Manager {
	if cfg.ViewportWidth == 0 {
		cfg.ViewportWidth = 1920
	}
	if cfg.ViewportHeight == 0 {
		cfg.ViewportHeight = 1080
	}
	if cfg.NavigationTimeout == 0 {
		cfg.NavigationTimeout = 30 * time.Second
	}
	if cfg.KasmVNCPort == 0 {
		cfg.KasmVNCPort = 6901
	}
	return &Manager{
		config: cfg,
		pages:  make(map[proto.TargetTargetID]*PageState),
		logger: log.New(os.Stderr, "[browser] ", log.LstdFlags),
	}
}

// SetSessionID binds this Manager to a specific agent session. For dedicated
// container mode, this determines the container name. Must be called before
// GetOrLaunch when using container modes.
func (m *Manager) SetSessionID(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionID = id
}

// EnsureSessionID sets the session ID, resolving delegate sub-agent aliases
// to the parent session. Called from browser tools on every invocation.
// When the session changes (e.g. new Studio chat), the browser connection
// is reset so GetOrLaunch re-resolves the container for the new session.
// Also touches the sandbox idle timer to prevent container eviction during
// long browser automation sequences.
//
// Sub-agents must be registered via AliasSession (OnChildSession runs
// synchronously before the child agent starts) so their IDs resolve to the
// parent and do not trigger a reset. Unaliased IDs are treated as real
// session switches — a previous soft-keep for "unaliased child race"
// incorrectly left Chromium bound to a prior chat while shell tools used
// the current session container.
func (m *Manager) EnsureSessionID(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" {
		return
	}
	// Resolve alias: delegate sub-agents map to their parent session.
	resolved := id
	if alias, ok := m.sessionAliases[id]; ok {
		resolved = alias
	}

	if m.sessionID == resolved {
		// Touch sandbox activity on every browser tool call to prevent idle eviction.
		if m.ActivityTouchFunc != nil && m.sessionID != "" {
			m.ActivityTouchFunc(m.sessionID)
		}
		return // no change — most common path for parallel tool calls
	}

	// Session genuinely changed (or first bind) — reset browser connection so
	// GetOrLaunch re-resolves the container for the new session. Do this even
	// when a browser is already connected: that connection belongs to the
	// previous chat/session and must not be reused.
	prev := m.sessionID
	m.clearBrowserConnectionLocked()
	m.sessionID = resolved
	if prev != "" {
		m.logger.Printf("Browser session rebound %s → %s (CDP reset)", shortSessionID(prev), shortSessionID(resolved))
	}

	if m.ActivityTouchFunc != nil && resolved != "" {
		m.ActivityTouchFunc(resolved)
	}
}

// clearBrowserConnectionLocked drops CDP/browser state. Must hold m.mu.
// Close is best-effort and panic-safe: a dead or never-connected rod Browser
// can panic on Close, and we must still finish the session rebound.
func (m *Manager) clearBrowserConnectionLocked() {
	if m.browser != nil {
		b := m.browser
		m.browser = nil
		func() {
			defer func() { _ = recover() }()
			_ = b.Close()
		}()
	}
	if m.browserProcess != nil {
		_ = m.browserProcess.Close()
		m.browserProcess = nil
	}
	m.containerName = ""
	m.containerIP = ""
	m.config.RemoteCDPURL = ""
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// SessionID returns the session currently bound for container resolution.
func (m *Manager) SessionID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionID
}

// SetContainerForTest sets container name/IP without launching Chromium.
// Tests use this to assert wrong-session vs matching-session error tips.
func (m *Manager) SetContainerForTest(name, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containerName = name
	m.containerIP = ip
}

// AliasSession maps a delegate sub-agent's session ID to its parent session ID.
// This ensures that when a sub-agent calls browser tools, EnsureSessionID
// resolves to the parent's container instead of trying to find a non-existent
// container for the child session.
func (m *Manager) AliasSession(childID, parentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionAliases == nil {
		m.sessionAliases = make(map[string]string)
	}
	m.sessionAliases[childID] = parentID
}

// ContainerName returns the LXC container name, if the browser is running in
// a container. Returns empty string for host mode.
func (m *Manager) ContainerName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.containerName
}

// ContainerIP returns the container's bridge IPv4 address, or empty string
// if the browser is running on the host.
func (m *Manager) ContainerIP() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.containerIP
}

// Config returns a copy of the browser configuration.
func (m *Manager) Config() BrowserConfig {
	return m.config
}

// IsContainerMode returns true if the browser should run inside a container.
// This is determined at runtime by whether sandbox is enabled, not by a static
// config field. When SandboxEnabled is true, the ContainerMode field ("dedicated"
// or "shared") determines the container lifecycle.
func (m *Manager) IsContainerMode() bool {
	return m.SandboxEnabled
}

// isBrowserDead returns true if the error indicates the CDP connection to the
// browser is no longer functional (pipe closed, connection reset, EOF, etc.).
// This mirrors the isContainerGone() pattern in pkg/sandbox/node.go.
//
// App-level net::ERR_CONNECTION_REFUSED (navigate to a service port) is NOT
// treated as a dead browser — that is a Class B service failure.
func (m *Manager) isBrowserDead(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "closed pipe") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "websocket: close") ||
		strings.Contains(msg, "target closed") ||
		strings.Contains(msg, "session closed") ||
		strings.Contains(msg, "failed to resolve cdp") ||
		strings.Contains(msg, "devtools port") {
		return true
	}
	// CDP dial/connect failures (not app URL refused)
	if (strings.Contains(msg, "failed to connect") || strings.Contains(msg, "failed to dial")) &&
		(strings.Contains(msg, "cdp") || strings.Contains(msg, "devtools") ||
			strings.Contains(msg, "9222") || strings.Contains(msg, "9223") ||
			strings.Contains(msg, "tunnel")) {
		return true
	}
	return false
}

// ResetForReconnect clears the live browser/CDP state so the next GetOrLaunch
// re-resolves the session container and starts Chromium again. Safe for drill
// executors to call after a dead CDP or flaky first navigate.
func (m *Manager) ResetForReconnect() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetBrowserLocked()
}

// EnsureCDPReady launches (or reuses) the sandboxed browser and verifies CDP
// with Browser.Version(). Used by drill preflight so the first browser_* step
// does not pay an unguarded cold start.
func (m *Manager) EnsureCDPReady() error {
	if m == nil {
		return fmt.Errorf("browser Manager is nil")
	}
	b, err := m.GetOrLaunch()
	if err != nil {
		return err
	}
	if _, err := b.Version(); err != nil {
		if m.isBrowserDead(err) {
			m.ResetForReconnect()
			b, err = m.GetOrLaunch()
			if err != nil {
				return err
			}
			if _, err := b.Version(); err != nil {
				return err
			}
			return nil
		}
		return err
	}
	return nil
}

// resetBrowserLocked tears down the dead browser state so the next launch
// attempt performs a fresh connection. This handles the case where Chrome
// crashed, the container was destroyed, or the CDP tunnel pipe broke.
// Must be called with m.mu held.
func (m *Manager) resetBrowserLocked() {
	m.stopRecordingLocked()

	m.activePg = nil
	m.cdpURL = ""

	// For container mode: clear container state to force full re-resolve
	// on the next GetOrLaunch() call (resolve → start → connect sequence).
	// Host mode only drops the browser handle (container fields are unused).
	if m.SandboxEnabled {
		m.clearBrowserConnectionLocked()
	} else if m.browser != nil {
		b := m.browser
		m.browser = nil
		func() {
			defer func() { _ = recover() }()
			_ = b.Close()
		}()
	}

	m.pagesMu.Lock()
	m.pages = make(map[proto.TargetTargetID]*PageState)
	m.pagesMu.Unlock()
}

// GetOrLaunch returns the current browser, launching it if necessary.
// On first launch, the browser is configured with anti-detection measures:
//   - Headed mode by default (with Xvfb on Linux) for realistic fingerprint
//   - go-rod/stealth JS evasions on every page
//   - Realistic User-Agent and Client Hints matched to actual Chrome version
//   - Automation flags stripped
//
// If headed mode fails (no display, no Xvfb), falls back to headless with a warning.
//
// When SandboxEnabled is true, the browser is launched inside the session
// container and connected via a remote CDP URL.
//
// Health check: if a browser is already connected, a lightweight Version() call
// verifies the CDP connection is still alive. If the connection is dead (pipe
// closed, container destroyed, Chrome crashed), the stale state is cleared and
// a fresh launch is attempted. This prevents the "closed pipe" errors that
// previously required a daemon restart to recover from.
func (m *Manager) GetOrLaunch() (*rod.Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.browser != nil {
		// Sandbox mode must never reuse a host-launched browser (e.g. Chrome
		// started before SandboxEnabled was wired, or before container callbacks
		// were attached). Force a container relaunch.
		if m.SandboxEnabled && m.containerName == "" {
			m.logger.Printf("Sandbox browser enabled but connected browser is host-side; relaunching in container")
			m.resetBrowserLocked()
			// Fall through to launchInContainer below.
		} else {
			// Health check: verify the CDP connection is still alive.
			// Browser.Version() is a lightweight CDP call (Browser.getVersion)
			// that exercises the full pipe/WebSocket without side effects.
			if _, err := m.browser.Version(); err != nil {
				if m.isBrowserDead(err) {
					m.logger.Printf("Browser CDP connection is dead (%v), reconnecting...", err)
					m.resetBrowserLocked()
					// Fall through to launch a new browser below.
				} else {
					// Non-pipe error (unlikely) — still return the browser.
					return m.browser, nil
				}
			} else {
				return m.browser, nil
			}
		}
	}

	// Container mode: launch browser in an LXC container.
	// Checked before RemoteCDPURL because container mode sets RemoteCDPURL
	// lazily when the container is provisioned.
	if m.SandboxEnabled {
		return m.launchInContainer()
	}

	// Remote CDP mode: connect to an external browser instead of launching locally.
	// This is used with anti-detect browsers (AdsPower, Browserless, etc.) that
	// handle fingerprint spoofing at the binary level.
	if m.config.RemoteCDPURL != "" {
		return m.connectRemote()
	}

	headless := m.config.Headless

	// If headed mode is requested (default), ensure a display is available.
	if !headless {
		headless = m.ensureDisplay()
	}

	l := launcher.New().
		Headless(headless).
		NoSandbox(m.config.NoSandbox).
		// Remove the default --enable-automation flag that rod sets.
		// This flag adds "Chrome is being controlled by automated test software"
		// and sets navigator.webdriver = true.
		Delete("enable-automation").
		// Remove flags that signal automation. Normal Chrome doesn't set these;
		// their presence is detectable by anti-bot systems.
		Delete("disable-popup-blocking").
		Delete("disable-default-apps").
		// Prevent Blink from exposing AutomationControlled feature, which
		// websites check via navigator.webdriver and other signals.
		Set(flags.Flag("disable-blink-features"), "AutomationControlled").
		// Disable Cross-Origin-Opener-Policy enforcement. Google OAuth
		// popups set COOP: same-origin, which prevents the parent page
		// (e.g. Reddit) from accessing window.closed on the popup. This
		// breaks OAuth callback detection. Appending to the existing
		// disable-features list (which rod sets to "site-per-process,TranslateUI").
		Append(flags.Flag("disable-features"), "CrossOriginOpenerPolicy").
		// Prevent Chrome from throttling timers and rendering in background
		// tabs/windows. Important for consistent behavior in headed Xvfb mode.
		Set(flags.Flag("disable-background-timer-throttling")).
		Set(flags.Flag("disable-backgrounding-occluded-windows")).
		Set(flags.Flag("disable-renderer-backgrounding"))

	if m.config.ChromePath != "" {
		l = l.Bin(m.config.ChromePath)
	}
	if m.config.UserDataDir != "" {
		// Ensure the profile directory exists with restricted permissions.
		// Chrome writes cookies, session tokens, and localStorage here.
		if err := os.MkdirAll(m.config.UserDataDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create browser profile dir: %w", err)
		}
		l = l.UserDataDir(m.config.UserDataDir)
	}
	if m.config.Proxy != "" {
		l = l.Set(flags.Flag("proxy-server"), m.config.Proxy)
	}

	// CloakBrowser fingerprint flags. These are silently ignored by stock
	// Chromium, so it's safe to set them unconditionally when configured.
	if m.config.FingerprintSeed != "" {
		l = l.Set(flags.Flag("fingerprint"), m.config.FingerprintSeed)
	}
	if m.config.FingerprintPlatform != "" {
		l = l.Set(flags.Flag("fingerprint-platform"), m.config.FingerprintPlatform)
	}

	u, err := l.Launch()
	if err != nil {
		// If headed launch failed, try headless as fallback.
		if !headless {
			m.logger.Printf("Headed launch failed (%v), falling back to headless", err)
			m.stopXvfb()
			l = l.Headless(true)
			u, err = l.Launch()
		}
		if err != nil {
			return nil, fmt.Errorf("failed to launch browser: %w", err)
		}
	}

	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		l.Kill()
		m.stopXvfb()
		return nil, fmt.Errorf("failed to connect to browser: %w", err)
	}

	m.browser = b
	m.launch = l
	m.cdpURL = u

	// Detect Chrome version for dynamic UA matching.
	m.detectChromeVersion()

	return b, nil
}

// connectRemote connects to an external browser via CDP WebSocket endpoint.
// Used for anti-detect browsers that handle fingerprinting at the binary level.
// No local Chrome is launched; the external browser is fully managed externally.
// Stealth JS evasions and WebGL patches are still applied to pages since they
// don't hurt and provide an extra layer of protection.
// Must be called with m.mu held.
func (m *Manager) connectRemote() (*rod.Browser, error) {
	m.logger.Printf("Connecting to remote browser at %s", m.config.RemoteCDPURL)

	b := rod.New().ControlURL(m.config.RemoteCDPURL).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to remote browser at %s: %w", m.config.RemoteCDPURL, err)
	}

	m.browser = b
	m.cdpURL = m.config.RemoteCDPURL

	// Detect version from the remote browser for UA matching.
	m.detectChromeVersion()

	m.logger.Printf("Connected to remote browser (version: %s)", m.chromeVersion)
	return b, nil
}

// resolveCDPURL queries the Chromium DevTools /json/version endpoint inside the
// container to obtain the full WebSocket debugger URL (which includes the
// browser-specific GUID path). A bare ws://host:port URL will be rejected by
// Chromium with HTTP 404 because it doesn't route WebSocket upgrades at the
// root path.
//
// When ContainerDialFunc is set (exec tunnel mode), the HTTP request is routed
// through the tunnel instead of direct TCP to the container IP. This makes it
// work on Docker+Incus (macOS) where container bridge IPs are not routable.
//
// The function retries for up to 15 seconds to allow Chromium and socat time
// to become ready after container launch.
func (m *Manager) resolveCDPURL(containerName, ip string) (string, error) {
	// Build an HTTP client that routes through the exec tunnel when available.
	var httpClient *http.Client
	if m.ContainerDialFunc != nil && containerName != "" {
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return m.ContainerDialFunc(containerName, defaultCDPPort)
				},
			},
			Timeout: 5 * time.Second,
		}
	} else {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}

	versionURL := fmt.Sprintf("http://%s:%d/json/version", ip, defaultCDPPort)

	var lastErr error
	for range 30 { // 30 × 500ms = 15s
		resp, err := httpClient.Get(versionURL)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Parse the JSON response to extract webSocketDebuggerUrl.
		var result struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			lastErr = fmt.Errorf("failed to parse /json/version response: %w", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if result.WebSocketDebuggerURL == "" {
			lastErr = fmt.Errorf("/json/version returned empty webSocketDebuggerUrl")
			time.Sleep(500 * time.Millisecond)
			continue
		}

		m.logger.Printf("Resolved CDP URL: %s", result.WebSocketDebuggerURL)
		return result.WebSocketDebuggerURL, nil
	}
	return "", fmt.Errorf("failed to resolve CDP URL at %s after 15s: %w", versionURL, lastErr)
}

// launchInContainer launches the browser inside the session container and
// connects via remote CDP. The container is managed by the sandbox package's
// NodeClientPool — this method resolves the container info, starts Chromium
// inside it, and connects via CDP.
//
// When ContainerDialFunc is set, the CDP WebSocket connection is tunneled
// through the Incus exec API (socat), making it work on all platforms.
//
// Must be called with m.mu held.
func (m *Manager) launchInContainer() (*rod.Browser, error) {
	b, err := m.launchInContainerInner()
	if err != nil {
		return nil, err
	}

	// Close extra tabs that Chromium may have opened on startup (e.g.
	// first-run welcome page, session restore, default homepage). We want
	// to start with a single about:blank tab so the AI agent controls
	// exactly what is open. This is only done for container-launched
	// browsers — not for connectRemote() with external anti-detect browsers
	// whose tab state we should not touch.
	m.closeExtraTabs(b)

	return b, nil
}

func (m *Manager) launchInContainerInner() (*rod.Browser, error) {
	// Touch sandbox activity before a potentially multi-second Chromium/Xvnc
	// launch so the idle watchdog cannot StopForIdle mid-start.
	if m.ActivityTouchFunc != nil && m.sessionID != "" {
		m.ActivityTouchFunc(m.sessionID)
	}

	// If RemoteCDPURL is already set (by a previous call or external setup),
	// resolve it to the full debugger URL if it's a bare host:port.
	if m.config.RemoteCDPURL != "" {
		if err := m.ensureResolvedCDPURL(); err != nil {
			return nil, err
		}
		return m.connectContainerCDP()
	}

	// If container info was set directly, resolve the CDP URL from its IP.
	if m.containerIP != "" {
		resolved, err := m.resolveCDPURL(m.containerName, m.containerIP)
		if err != nil {
			return nil, err
		}
		m.config.RemoteCDPURL = resolved
		return m.connectContainerCDP()
	}

	// Browser tools bypass NodeTool.Run, so wait on the same lazy sandbox
	// provisioning barrier before resolving the session record.
	if m.ContainerEnsureReadyFunc != nil {
		if err := m.ContainerEnsureReadyFunc(m.sessionID); err != nil {
			return nil, fmt.Errorf(
				"failed to prepare session container for session %s: %w",
				shortSessionID(m.sessionID), err,
			)
		}
	}

	// Resolve the session container — get its name and IP from the sandbox.
	if m.ContainerResolveFunc == nil {
		return nil, fmt.Errorf(
			"browser sandbox mode is enabled but no ContainerResolveFunc is set; " +
				"set ContainerResolveFunc on the Manager before GetOrLaunch()",
		)
	}

	name, ip, err := m.ContainerResolveFunc(m.sessionID)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to resolve session container for session %s: %w",
			shortSessionID(m.sessionID), err,
		)
	}
	m.containerName = name
	m.containerIP = ip

	// Start Chromium + KasmVNC + socat inside the session container.
	if m.ContainerStartBrowserFunc != nil {
		closer, err := m.ContainerStartBrowserFunc(name)
		if err != nil {
			// Clear stale state so the next GetOrLaunch() retries the full
			// resolve → start → connect sequence instead of short-circuiting
			// at the containerIP check above.
			m.containerName = ""
			m.containerIP = ""
			return nil, fmt.Errorf("failed to start browser in container %q: %w", name, err)
		}
		m.browserProcess = closer
	}

	// Resolve the CDP WebSocket URL from the container IP.
	resolved, err := m.resolveCDPURL(name, ip)
	if err != nil {
		// Clear stale state so retries go through the full sequence.
		m.containerName = ""
		m.containerIP = ""
		return nil, err
	}
	m.config.RemoteCDPURL = resolved
	return m.connectContainerCDP()
}

// connectContainerCDP connects to Chromium inside a container via CDP.
//
// When ContainerDialFunc is set, the CDP WebSocket is tunneled through the
// Incus exec API, making container services reachable from any host. The
// tunnel dialer is injected into go-rod's cdp.WebSocket.Dialer field, and
// the pre-connected cdp.Client is passed to rod via Browser.Client().
//
// When ContainerDialFunc is nil (legacy fallback), it falls back to the
// standard connectRemote() path using direct TCP.
//
// Must be called with m.mu held.
func (m *Manager) connectContainerCDP() (*rod.Browser, error) {
	cdpURL := m.config.RemoteCDPURL

	// If no tunnel dialer is available, fall back to direct connection.
	if m.ContainerDialFunc == nil || m.containerName == "" {
		m.logger.Printf("Connecting to container browser (direct) at %s", cdpURL)
		return m.connectRemote()
	}

	m.logger.Printf("Connecting to container browser (exec tunnel) at %s", cdpURL)

	containerName := m.containerName

	// Create a cdp.WebSocket with our tunnel dialer injected.
	// The Dialer field routes the TCP dial through socat inside the container.
	ws := &cdp.WebSocket{
		Dialer: tunnelDialer{
			dialFunc:      m.ContainerDialFunc,
			containerName: containerName,
			port:          defaultCDPPort,
		},
	}

	if err := ws.Connect(context.Background(), cdpURL, nil); err != nil {
		return nil, fmt.Errorf("failed to connect CDP WebSocket via tunnel to %s: %w", cdpURL, err)
	}

	// Create a cdp.Client from the connected WebSocket.
	client := cdp.New().Start(ws)

	// Create the rod Browser with the pre-connected client (no ControlURL).
	b := rod.New().Client(client).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		_ = ws.Close()
		return nil, fmt.Errorf("failed to connect rod browser via tunnel: %w", err)
	}

	m.browser = b
	m.cdpURL = cdpURL

	// Detect version from the remote browser for UA matching.
	m.detectChromeVersion()

	m.logger.Printf("Connected to container browser via exec tunnel (version: %s)", m.chromeVersion)
	return b, nil
}

// tunnelDialer implements cdp.Dialer by routing connections through a
// ContainerDialFunc (exec tunnel via socat).
type tunnelDialer struct {
	dialFunc      func(containerName string, port int) (net.Conn, error)
	containerName string
	port          int
}

func (d tunnelDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	return d.dialFunc(d.containerName, d.port)
}

// ensureResolvedCDPURL checks whether m.config.RemoteCDPURL already contains
// the /devtools/browser/ path. If not, it resolves it via /json/version. This
// handles the case where a caller set a bare ws://host:port URL.
func (m *Manager) ensureResolvedCDPURL() error {
	u := m.config.RemoteCDPURL
	// If it already has a /devtools/ path, it's already resolved.
	if strings.Contains(u, "/devtools/") {
		return nil
	}
	// Extract host from the URL for resolution.
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("invalid RemoteCDPURL %q: %w", u, err)
	}
	resolved, err := m.resolveCDPURL(m.containerName, parsed.Hostname())
	if err != nil {
		return err
	}
	m.config.RemoteCDPURL = resolved
	return nil
}

// SetContainerInfo sets the container details after the sandbox package has
// launched a browser container. This allows the Manager to connect via CDP
// without creating a circular dependency between browser and sandbox packages.
// The actual CDP URL resolution (which requires querying /json/version) happens
// lazily in launchInContainer().
func (m *Manager) SetContainerInfo(containerName, containerIP string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containerName = containerName
	m.containerIP = containerIP
}

// ensureDisplay ensures a display is available for headed mode.
// On Linux without $DISPLAY, it attempts to start Xvfb.
// Returns true if we should fall back to headless, false if display is ready.
func (m *Manager) ensureDisplay() bool {
	// macOS and Windows have native displays (or the user is running with a GUI).
	if runtime.GOOS != "linux" {
		return false
	}

	// If DISPLAY is already set, a display server is running.
	if os.Getenv("DISPLAY") != "" {
		return false
	}

	// No display available. Try to start Xvfb.
	xvfb := NewXvfb(m.logger)
	if err := xvfb.Start(m.config.ViewportWidth, m.config.ViewportHeight); err != nil {
		m.logger.Printf("WARNING: Cannot start Xvfb (%v). Falling back to headless mode.", err)
		m.logger.Printf("For better stealth against strict sites, install xvfb: apt install xvfb")
		return true // fall back to headless
	}

	m.xvfb = xvfb
	return false // display is ready
}

// detectChromeVersion queries the browser for its actual version and constructs
// matching UA string and Client Hints. This prevents mismatches between the
// reported UA and the actual Chrome binary version, which is a detection signal.
func (m *Manager) detectChromeVersion() {
	if m.browser == nil {
		return
	}

	version, err := m.browser.Version()
	if err != nil {
		return
	}

	// version.Product is like "Chrome/131.0.6778.204"
	m.chromeVersion = version.Product
}

// stopXvfb stops the Xvfb process if one was started by us.
func (m *Manager) stopXvfb() {
	if m.xvfb != nil {
		m.xvfb.Stop()
		m.xvfb = nil
	}
}

// CDPURL returns the Chrome DevTools Protocol WebSocket endpoint URL.
// Returns empty string if the browser has not been launched yet.
func (m *Manager) CDPURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cdpURL
}

// HandoffConfig returns the resolved handoff configuration with defaults applied.
func (m *Manager) HandoffConfig() HandoffCfg {
	bind := m.config.HandoffBindAddress
	if bind == "" {
		bind = "127.0.0.1"
	}
	port := m.config.HandoffPort
	if port == 0 {
		port = 9222
	}
	return HandoffCfg{BindAddress: bind, Port: port}
}

// StartHandoff creates and starts a handoff server, storing it on the Manager
// so it persists across tool calls. Returns the HandoffInfo (with listen address)
// and an error. If a handoff is already active, returns an error.
func (m *Manager) StartHandoff(opts HandoffOpts) (*HandoffInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.handoff != nil && m.handoff.IsActive() {
		return nil, fmt.Errorf("a browser handoff session is already active")
	}

	h := NewHandoffServer(nil)
	info, err := h.Start(opts)
	if err != nil {
		return nil, err
	}

	m.handoff = h
	m.handoffReason = opts.Reason
	return info, nil
}

// WaitHandoff blocks until the active handoff session completes or the context
// is cancelled. Returns an error if no handoff is active. Stops and cleans up
// the handoff server after the wait completes.
func (m *Manager) WaitHandoff(ctx context.Context) error {
	m.mu.Lock()
	h := m.handoff
	m.mu.Unlock()

	if h == nil || !h.IsActive() {
		return fmt.Errorf("no active handoff session")
	}

	err := h.WaitForCompletion(ctx)

	// Clean up the handoff server after completion (or timeout)
	m.mu.Lock()
	if m.handoff == h {
		_ = m.handoff.Stop()
		m.handoff = nil
		m.handoffReason = ""
	}
	m.mu.Unlock()

	return err
}

// StopHandoff stops any active handoff session. Safe to call when no handoff
// is running. Also stops action capture that was started for the handoff.
func (m *Manager) StopHandoff() {
	m.stopActionCaptureIfHandoff()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.handoff != nil {
		_ = m.handoff.Stop()
		m.handoff = nil
		m.handoffReason = ""
	}
}

// HandoffActive returns true if a handoff session is currently running.
func (m *Manager) HandoffActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handoff != nil && m.handoff.IsActive()
}

// SignalHandoffDone signals that the human has finished interacting with the
// browser during a handoff session. This unblocks WaitHandoff. Safe to call
// when no handoff is active.
//
// Deprecated: The Done button now calls StopHandoff() directly via the API
// handler. This method is kept for backward compatibility with any code that
// still references it.
func (m *Manager) SignalHandoffDone() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handoff != nil {
		m.handoff.SignalDone()
	}
}

// CurrentPage returns the most recently active page, creating one if none exists.
func (m *Manager) CurrentPage() (*rod.Page, error) {
	// Touch sandbox activity on every browser tool call to prevent the idle
	// watchdog from killing the container during browser automation.
	if m.ActivityTouchFunc != nil && m.sessionID != "" {
		m.ActivityTouchFunc(m.sessionID)
	}

	b, err := m.GetOrLaunch()
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activePg != nil {
		// Verify the page is still open
		_, infoErr := m.activePg.Info()
		if infoErr == nil {
			return m.activePg, nil
		}
		// Page was closed, fall through
		m.activePg = nil
	}

	// Try to get an existing page
	pages, err := b.Pages()
	if err == nil && !pages.Empty() {
		pg := pages.Last()
		m.activePg = pg
		m.ensurePageState(pg)
		return pg, nil
	}

	// Create a new blank page with configured viewport
	pg, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("failed to create page: %w", err)
	}

	if err := pg.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:  m.config.ViewportWidth,
		Height: m.config.ViewportHeight,
	}); err != nil {
		return nil, fmt.Errorf("failed to set viewport: %w", err)
	}

	m.activePg = pg
	m.ensurePageState(pg)
	return pg, nil
}

// closeExtraTabs closes all but one tab in the browser. Chromium may open
// extra tabs on startup (first-run welcome, session restore, default homepage).
// We keep the first tab (typically about:blank) and close the rest so the
// agent starts with a clean, single-tab browser.
// Must be called with m.mu held.
func (m *Manager) closeExtraTabs(b *rod.Browser) {
	pages, err := b.Pages()
	if err != nil || len(pages) <= 1 {
		return
	}
	// Keep the first page, close the rest.
	for _, pg := range pages[1:] {
		_ = pg.Close()
	}
	m.logger.Printf("Closed %d extra startup tab(s)", len(pages)-1)
}

// SetActivePage sets a specific page as the active page.
func (m *Manager) SetActivePage(pg *rod.Page) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activePg = pg
	m.ensurePageState(pg)
}

// NewIncognitoPage creates a new page in an isolated (incognito) browser context.
// The page has its own cookie jar and storage — it cannot see the persistent
// profile's login sessions. Use this for testing login flows, verifying
// unauthenticated behavior, or browsing without leaking personal cookies.
// The incognito page becomes the active page.
func (m *Manager) NewIncognitoPage() (*rod.Page, error) {
	b, err := m.GetOrLaunch()
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Lazily create the incognito context (one per session is enough)
	if m.incognito == nil {
		inc, err := b.Incognito()
		if err != nil {
			return nil, fmt.Errorf("failed to create incognito context: %w", err)
		}
		m.incognito = inc
	}

	pg, err := m.incognito.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("failed to create incognito page: %w", err)
	}

	if err := pg.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:  m.config.ViewportWidth,
		Height: m.config.ViewportHeight,
	}); err != nil {
		return nil, fmt.Errorf("failed to set viewport: %w", err)
	}

	m.activePg = pg
	m.ensurePageState(pg)
	return pg, nil
}

// GetPage returns a page by its CDP target ID.
func (m *Manager) GetPage(targetID proto.TargetTargetID) (*rod.Page, error) {
	b, err := m.GetOrLaunch()
	if err != nil {
		return nil, err
	}

	pg, err := b.PageFromTarget(targetID)
	if err != nil {
		return nil, fmt.Errorf("page not found for target %s: %w", targetID, err)
	}
	return pg, nil
}

// applyStealthToPage configures anti-detection measures on a page so that
// websites see a normal browser instead of a headless automation tool.
// Uses go-rod/stealth for comprehensive JS evasions and sets a realistic
// User-Agent with Client Hints matched to the actual Chrome version.
func (m *Manager) applyStealthToPage(pg *rod.Page) {
	// 1. Inject go-rod/stealth JS evasions (runs before any page script).
	// Covers: navigator.webdriver, plugins (realistic PluginArray), languages,
	// window.chrome (csi, loadTimes, runtime), media codecs,
	// hardwareConcurrency, permissions, WebGL vendor/renderer,
	// outerWidth/outerHeight, iframe contentWindow, and more.
	if _, err := pg.EvalOnNewDocument(stealth.JS); err != nil {
		m.logger.Printf("WARNING: failed to inject stealth JS: %v", err)
	}

	// 2. Override User-Agent at the CDP level using actual Chrome version.
	// This controls both the HTTP User-Agent header and the navigator.userAgent
	// JS property, plus Client Hints (Sec-CH-UA, Sec-CH-UA-Platform, etc.).
	ua, majorVersion := m.buildUserAgent()
	if err := (proto.NetworkSetUserAgentOverride{
		UserAgent:      ua,
		AcceptLanguage: "en-US,en;q=0.9",
		Platform:       defaultPlatform,
		UserAgentMetadata: &proto.EmulationUserAgentMetadata{
			Brands: []*proto.EmulationUserAgentBrandVersion{
				{Brand: "Not_A Brand", Version: "8"},
				{Brand: "Chromium", Version: majorVersion},
				{Brand: "Google Chrome", Version: majorVersion},
			},
			FullVersionList: []*proto.EmulationUserAgentBrandVersion{
				{Brand: "Not_A Brand", Version: "8.0.0.0"},
				{Brand: "Chromium", Version: majorVersion + ".0.0.0"},
				{Brand: "Google Chrome", Version: majorVersion + ".0.0.0"},
			},
			Platform:        "Windows",
			PlatformVersion: "10.0.0",
			Architecture:    "x86",
			Model:           "",
			Mobile:          false,
		},
	}).Call(pg); err != nil {
		m.logger.Printf("WARNING: failed to set User-Agent override: %v", err)
	}

	// 3. Inject WebGL consistency patches to align capability values and
	// extension lists with the "Intel Iris OpenGL Engine" identity claimed
	// by stealth.JS. Without this, fingerprinters detect SwiftShader behavior
	// (fewer extensions, different MAX_VIEWPORT_DIMS, etc.) contradicting the
	// spoofed renderer string.
	if _, err := pg.EvalOnNewDocument(webglConsistencyJS); err != nil {
		m.logger.Printf("WARNING: failed to inject WebGL consistency JS: %v", err)
	}
}

// buildUserAgent returns a UA string and major version number based on the
// actual Chrome version detected at launch. Falls back to a reasonable default
// if the version is unknown.
func (m *Manager) buildUserAgent() (ua string, majorVersion string) {
	majorVersion = "131"
	fullVersion := "131.0.0.0"

	if m.chromeVersion != "" {
		// chromeVersion is like "Chrome/131.0.6778.204" or "HeadlessChrome/131.0.6778.204"
		parts := m.chromeVersion
		if idx := len("Chrome/"); len(parts) > idx {
			for i, c := range parts {
				if c == '/' {
					parts = parts[i+1:]
					break
				}
			}
			fullVersion = parts
			// Extract major version (everything before first dot).
			for i, c := range parts {
				if c == '.' {
					majorVersion = parts[:i]
					break
				}
			}
		}
	}

	ua = fmt.Sprintf(
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36",
		fullVersion,
	)
	return ua, majorVersion
}

// ensurePageState creates a PageState for the page if one doesn't exist.
// Applies stealth (user-agent, webdriver override) so that every page presents
// as a normal browser. Must be called with m.mu held.
//
// Event listeners (console, network, errors) are NOT attached here to avoid
// triggering Runtime.enable, which is a primary bot-detection signal. Listeners
// are attached lazily via PageState.AttachListeners() when explicitly requested.
func (m *Manager) ensurePageState(pg *rod.Page) {
	info, err := pg.Info()
	if err != nil {
		return
	}
	targetID := info.TargetID

	m.pagesMu.Lock()
	defer m.pagesMu.Unlock()
	if _, ok := m.pages[targetID]; ok {
		return
	}

	// Apply anti-detection overrides before any navigation happens on this page.
	m.applyStealthToPage(pg)

	ps := NewPageState()
	m.pages[targetID] = ps
}

// PageStateFor returns the PageState for a given page.
func (m *Manager) PageStateFor(pg *rod.Page) *PageState {
	info, err := pg.Info()
	if err != nil {
		return nil
	}
	m.pagesMu.RLock()
	defer m.pagesMu.RUnlock()
	return m.pages[info.TargetID]
}

// IsRunning returns true if a browser is currently connected.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.browser != nil
}

// NavigationTimeout returns the configured navigation timeout.
func (m *Manager) NavigationTimeout() time.Duration {
	return m.config.NavigationTimeout
}

// Cleanup closes the browser and kills the process. The persistent profile
// directory (cookies, localStorage, etc.) is preserved for future sessions.
// For container mode, this disconnects the CDP connection and destroys the
// container if it's a dedicated (per-session) container.
func (m *Manager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Finalize any in-progress recording before tearing down the display.
	m.stopRecordingLocked()

	// Stop any active handoff session
	if m.handoff != nil {
		_ = m.handoff.Stop()
		m.handoff = nil
		m.handoffReason = ""
	}

	// Close incognito context first (it's a child of the main browser)
	if m.incognito != nil {
		_ = m.incognito.Close()
		m.incognito = nil
	}
	if m.browser != nil {
		_ = m.browser.Close()
		m.browser = nil
	}
	if m.launch != nil {
		m.launch.Kill()
		// Only clean up the user data dir if we're using a temp profile.
		// Persistent profiles must survive across restarts.
		if m.config.UserDataDir == "" {
			m.launch.Cleanup()
		}
		m.launch = nil
	}

	// Stop Xvfb virtual display if we started one.
	m.stopXvfb()

	// Revoke any VNC handoff tokens for this container.
	if m.containerName != "" && m.SandboxEnabled {
		GetHandoffTokenRegistry().Revoke(m.containerName)
		// Note: we do NOT destroy the session container here — its lifecycle
		// is managed by the sandbox's NodeClientPool. The browser was just a
		// guest process in the session container.
		m.containerName = ""
		m.containerIP = ""
	}

	// Close the browser process keep-alive stream (kills KasmVNC + Chrome + socat).
	if m.browserProcess != nil {
		_ = m.browserProcess.Close()
		m.browserProcess = nil
	}

	m.activePg = nil

	m.pagesMu.Lock()
	m.pages = make(map[proto.TargetTargetID]*PageState)
	m.pagesMu.Unlock()
}
