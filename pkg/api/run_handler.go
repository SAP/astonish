package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// ChatRequest represents the request body for /api/chat
type ChatRequest struct {
	AgentID     string `json:"agentId"`
	Message     string `json:"message"` // User input
	SessionID   string `json:"sessionId"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	AutoApprove bool   `json:"autoApprove,omitempty"` // Global auto-approve flag
	CLIMode     bool   `json:"cliMode,omitempty"`     // When true, renders ANSI output (tool boxes etc.)
	Debug       bool   `json:"debug,omitempty"`       // When true, stream debug events (tool args/responses)
}

// SessionManager manages active sessions
type SessionManager struct {
	service         session.Service
	sessions        map[string]session.Session
	mcpManagers     map[string]*mcp.Manager // MCP manager per session
	lastActivity    map[string]time.Time    // Last activity time per session
	sandboxCleanups map[string]func()       // Per-session sandbox cleanup (flow containers)
	sandboxTools    map[string][]tool.Tool  // Per-session sandbox-wrapped tools (reused across resumes)
	mu              sync.RWMutex
}

// Session timeout - cleanup sessions with no activity for this duration
const sessionTimeout = 2 * time.Minute

var globalSessionManager *SessionManager
var sessionOnce sync.Once

// globalBrowserMgr is a shared browser manager for all Studio sessions.
// The browser is lazily launched on first use and cleaned up on session timeout.
var globalBrowserMgr *browser.Manager
var browserOnce sync.Once

// pdfBrowserMgr is a dedicated browser for PDF rendering only. Kept separate
// from globalBrowserMgr so PDF generation doesn't interfere with the user's
// active browsing session (EnsureSessionID is destructive — it closes the
// existing browser when switching sessions).
//
// When sandbox is enabled, this manager runs Chrome inside the session container
// (which has Chromium + emoji fonts pre-installed). When sandbox is disabled,
// it falls back to a local headless Chrome.
var pdfBrowserMgr *browser.Manager
var pdfBrowserOnce sync.Once

// localPDFBrowserMgr is a dedicated, always-local (non-sandbox) headless Chrome
// used for session-less PDF exports such as the slides deck export, which is
// triggered from the docs UI with no chat session to bind a container to. The
// exported document is fully self-contained and trusted (identical to the HTML
// export), so it does not need the session sandbox.
var localPDFBrowserMgr *browser.Manager
var localPDFBrowserOnce sync.Once

// slidesPDFBrowserMgr is a dedicated, sandbox-aware PDF browser manager for the
// slides deck export. Unlike localPDFBrowserMgr (host-only) it wires the same
// container browser callbacks as GetPDFBrowserManager, and unlike the chat PDF
// manager it is keyed separately so slides exports never thrash the chat PDF
// manager's container/session state (EnsureSessionID is destructive). When the
// sandbox is enabled it runs Chrome inside a dedicated per-user session
// container; when the sandbox is disabled it is unused (the handler selects
// GetLocalPDFBrowserManager instead).
var slidesPDFBrowserMgr *browser.Manager
var slidesPDFBrowserOnce sync.Once

// Registered PDF browser callbacks (set by chat_factory for OpenShell/K8s path).
// These mirror the pattern used by SetVNCContainerDialFunc: the launcher wires
// them at startup, and the PDF handler picks them up on first use.
var (
	registeredPDFResolve      func(sessionID string) (string, string, error)
	registeredPDFStartBrowser func(podName string) (io.Closer, error)
	registeredPDFDial         func(podName string, port int) (net.Conn, error)
	registeredPDFBackend      string
	registeredPDFMu           sync.RWMutex
)

// SetPDFBrowserCallbacks registers container callbacks for the dedicated PDF
// browser manager. Called by the launcher (chat_factory) after wiring the
// OpenShell/K8s browser to ensure PDF export uses the same sandbox path.
func SetPDFBrowserCallbacks(
	resolve func(sessionID string) (string, string, error),
	startBrowser func(podName string) (io.Closer, error),
	dial func(podName string, port int) (net.Conn, error),
) {
	SetPDFBrowserCallbacksForBackend("registered", resolve, startBrowser, dial)
}

// SetPDFBrowserCallbacksForBackend is SetPDFBrowserCallbacks with an explicit
// backend label used only for diagnostics.
func SetPDFBrowserCallbacksForBackend(
	backend string,
	resolve func(sessionID string) (string, string, error),
	startBrowser func(podName string) (io.Closer, error),
	dial func(podName string, port int) (net.Conn, error),
) {
	registeredPDFMu.Lock()
	registeredPDFResolve = resolve
	registeredPDFStartBrowser = startBrowser
	registeredPDFDial = dial
	registeredPDFBackend = backend
	registeredPDFMu.Unlock()
	slog.Info("PDF browser: registered sandbox callbacks", "backend", backend)
}

// GetBrowserManager returns the shared browser manager for all sessions.
func GetBrowserManager() *browser.Manager {
	browserOnce.Do(func() {
		cfg := browser.DefaultConfig()
		appCfg, cfgErr := config.LoadAppConfig()
		if cfgErr == nil {
			b := &appCfg.Browser
			cfg = browser.OverrideConfig(browser.ConfigOverrides{
				Headless:            b.Headless,
				ViewportWidth:       b.ViewportWidth,
				ViewportHeight:      b.ViewportHeight,
				NoSandbox:           b.NoSandbox,
				ChromePath:          b.ChromePath,
				UserDataDir:         b.UserDataDir,
				NavigationTimeout:   b.NavigationTimeout,
				Proxy:               b.Proxy,
				RemoteCDPURL:        b.RemoteCDPURL,
				FingerprintSeed:     b.FingerprintSeed,
				FingerprintPlatform: b.FingerprintPlatform,
				HandoffBindAddress:  b.HandoffBindAddress,
				HandoffPort:         b.HandoffPort,
				KasmVNCPort:         b.KasmVNCPort,
				KasmVNCPassword:     b.KasmVNCPassword,
			})
		}
		globalBrowserMgr = browser.NewManager(cfg)

		// Wire browser container callbacks when sandbox is available.
		wireSandboxBrowserCallbacks(globalBrowserMgr, cfg, appCfg, cfgErr)
	})
	return globalBrowserMgr
}

// GetPDFBrowserManager returns a dedicated browser manager for PDF rendering.
// When sandbox is enabled, Chrome runs inside the session container (which has
// emoji fonts + Chromium pre-installed). The sessionID identifies which
// container to use. When sandbox is disabled, a local headless Chrome is used.
//
// This is separate from GetBrowserManager() because EnsureSessionID is
// destructive — it closes the existing browser when switching sessions.
// Using a dedicated manager prevents PDF generation from killing the user's
// active browsing session.
func GetPDFBrowserManager(sessionID string) *browser.Manager {
	pdfBrowserOnce.Do(func() {
		slog.Info("PDF browser: initializing dedicated PDF browser manager")
		cfg := browser.DefaultConfig()
		cfg.Headless = true
		// Use a temp profile directory to avoid Chrome lock conflicts
		// with the main browser manager's profile.
		cfg.UserDataDir = ""

		appCfg, cfgErr := config.LoadAppConfig()
		if cfgErr == nil {
			// Apply Chrome path override from config if set.
			if appCfg.Browser.ChromePath != "" {
				cfg.ChromePath = appCfg.Browser.ChromePath
			}
		}
		pdfBrowserMgr = browser.NewManager(cfg)

		// Wire sandbox callbacks so Chrome runs inside the session container.
		// This helper is Incus-only; OpenShell/K8s register callbacks below via
		// SetPDFBrowserCallbacksForBackend from chat_factory.
		wireSandboxBrowserCallbacks(pdfBrowserMgr, cfg, appCfg, cfgErr)

		// Override ContainerResolveFunc: the PDF manager must ensure the
		// container is running (it may have been stopped by the idle watchdog
		// or a template snapshot). The shared wireSandboxBrowserCallbacks only
		// checks IsRunning and fails — we need EnsureSessionContainer which
		// re-mounts the overlay and starts a stopped container.
		if pdfBrowserMgr.SandboxEnabled && cfgErr == nil && appCfg != nil && sandbox.BackendKind(appCfg.Sandbox.BackendKind()) == sandbox.BackendKindIncus {
			sandboxLimits := &appCfg.Sandbox.Limits
			pdfBrowserMgr.ContainerResolveFunc = func(sessID string) (string, string, error) {
				client, err := sandbox.SetupSandboxRuntime()
				if err != nil {
					return "", "", fmt.Errorf("sandbox runtime not available: %w", err)
				}
				sessRegistry, err := sandbox.NewSessionRegistry()
				if err != nil {
					return "", "", fmt.Errorf("failed to open session registry: %w", err)
				}
				tplRegistry, err := sandbox.NewTemplateRegistry()
				if err != nil {
					return "", "", fmt.Errorf("failed to open template registry: %w", err)
				}
				// Look up the template name from the registry entry.
				templateName := "@base"
				if entry := sessRegistry.Get(sessID); entry != nil && entry.TemplateName != "" {
					templateName = entry.TemplateName
				}
				containerName, err := sandbox.EnsureSessionContainer(client, sessRegistry, tplRegistry, sessID, templateName, sandboxLimits)
				if err != nil {
					return "", "", fmt.Errorf("failed to ensure session container: %w", err)
				}
				ip, err := client.GetContainerIPv4(containerName)
				if err != nil {
					return "", "", fmt.Errorf("failed to get IP for session container %q: %w", containerName, err)
				}
				return containerName, ip, nil
			}
		}

		slog.Info("PDF browser: initialized (sync.Once)", "sandboxEnabled", pdfBrowserMgr.SandboxEnabled)
	})

	// After sync.Once, always check if OpenShell/K8s callbacks were registered
	// by chat_factory. These override the Incus callbacks set by
	// wireSandboxBrowserCallbacks (which incorrectly enables sandbox on platforms
	// where Incus isn't installed but sandbox config is enabled for OpenShell).
	registeredPDFMu.RLock()
	resolve := registeredPDFResolve
	startBrowser := registeredPDFStartBrowser
	dial := registeredPDFDial
	backendLabel := registeredPDFBackend
	registeredPDFMu.RUnlock()

	if resolve != nil && dial != nil {
		pdfBrowserMgr.SandboxEnabled = true
		pdfBrowserMgr.ContainerResolveFunc = resolve
		pdfBrowserMgr.ContainerStartBrowserFunc = startBrowser
		pdfBrowserMgr.ContainerDialFunc = dial
		if backendLabel == "" {
			backendLabel = "registered"
		}
		slog.Info("PDF browser: using registered sandbox callbacks", "backend", backendLabel)
	}

	// Set the session ID so the manager knows which container to use.
	// Safe to call on every request — it's a no-op if the session hasn't changed.
	if sessionID != "" {
		slog.Info("PDF browser: setting session ID", "sessionID", sessionID)
		pdfBrowserMgr.EnsureSessionID(sessionID)
		slog.Info("PDF browser: session ID set")
	}

	return pdfBrowserMgr
}

// GetLocalPDFBrowserManager returns a dedicated, always-local headless Chrome
// manager for session-less PDF exports (e.g. the slides deck export). Unlike
// GetPDFBrowserManager, it never enables the container sandbox, so it does not
// require a chat session / running container to resolve. It honors the
// Browser.ChromePath config override when set. The exported document is fully
// self-contained and trusted, so a local Chrome is appropriate here.
func GetLocalPDFBrowserManager() *browser.Manager {
	localPDFBrowserOnce.Do(func() {
		slog.Info("PDF browser (local): initializing dedicated local PDF browser manager")
		cfg := browser.DefaultConfig()
		cfg.Headless = true
		// Temp profile dir to avoid Chrome lock conflicts with other managers.
		cfg.UserDataDir = ""
		if appCfg, cfgErr := config.LoadAppConfig(); cfgErr == nil && appCfg != nil {
			if appCfg.Browser.ChromePath != "" {
				cfg.ChromePath = appCfg.Browser.ChromePath
			}
		}
		localPDFBrowserMgr = browser.NewManager(cfg)
		// Intentionally NOT sandbox-wired: SandboxEnabled stays false so
		// GetOrLaunch() uses the local launcher path, no session required.
		slog.Info("PDF browser (local): initialized (sync.Once)")
	})
	return localPDFBrowserMgr
}

// sandboxBrowserRequiredFn is the seam the slides PDF handler uses to decide
// sandbox-vs-host. It is a package var so tests can override the decision
// without depending on on-disk app config. Production points at
// sandboxBrowserRequired.
var sandboxBrowserRequiredFn = sandboxBrowserRequired

// sandboxBrowserRequired reports whether PDF rendering must happen inside a
// sandbox container rather than on the host. It returns true when the app
// config has the sandbox enabled (any backend). The slides PDF handler uses
// this to (a) choose the sandbox-aware manager over the host manager, and
// (b) enforce the no-fallback contract: when the sandbox is required but no
// in-container browser is available, the export fails rather than silently
// rendering on the host. backendLabel is the configured backend kind, used
// only for diagnostics.
func sandboxBrowserRequired() (required bool, backendLabel string) {
	appCfg, err := config.LoadAppConfig()
	if err != nil || appCfg == nil {
		return false, ""
	}
	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return false, string(sandbox.BackendKind(appCfg.Sandbox.BackendKind()))
	}
	return true, string(sandbox.BackendKind(appCfg.Sandbox.BackendKind()))
}

// slidesPDFBrowserManagerFn is the seam the slides PDF handler uses to obtain
// the sandbox-aware PDF manager. It is a package var so tests can inject a
// manager without triggering the sync.Once / real sandbox wiring. Production
// points at GetSlidesPDFBrowserManager.
var slidesPDFBrowserManagerFn = GetSlidesPDFBrowserManager

// GetSlidesPDFBrowserManager returns the dedicated, sandbox-aware PDF browser
// manager for slides deck export. It mirrors GetPDFBrowserManager's sandbox
// wiring (Incus via wireSandboxBrowserCallbacks + an EnsureSessionContainer
// ContainerResolveFunc that restarts a stopped container; K8s/OpenShell via the
// process-global registeredPDF* callbacks set by chat_factory) but uses a
// separate manager instance keyed for slides so it never thrashes the chat PDF
// manager's container/session state. The sessionID identifies the dedicated
// per-user slides container ("slides-pdf-<user>").
//
// This accessor never decides sandbox-vs-host; that decision and the
// no-fallback rule live in the slides handler via sandboxBrowserRequired().
// When the sandbox is disabled the manager's SandboxEnabled stays false, but
// the handler will not use this manager in that case.
func GetSlidesPDFBrowserManager(sessionID string) (*browser.Manager, error) {
	slidesPDFBrowserOnce.Do(func() {
		slog.Info("slides PDF browser: initializing dedicated slides PDF browser manager")
		cfg := browser.DefaultConfig()
		cfg.Headless = true
		// Temp profile dir to avoid Chrome lock conflicts with other managers.
		cfg.UserDataDir = ""

		appCfg, cfgErr := config.LoadAppConfig()
		if cfgErr == nil && appCfg != nil {
			if appCfg.Browser.ChromePath != "" {
				cfg.ChromePath = appCfg.Browser.ChromePath
			}
		}
		slidesPDFBrowserMgr = browser.NewManager(cfg)

		// Wire Incus sandbox callbacks (no-op for non-Incus backends).
		wireSandboxBrowserCallbacks(slidesPDFBrowserMgr, cfg, appCfg, cfgErr)

		// Incus: override ContainerResolveFunc so a stopped/evicted slides
		// container is re-started via EnsureSessionContainer (the shared helper
		// only checks IsRunning and fails otherwise). Mirrors the override in
		// GetPDFBrowserManager.
		if slidesPDFBrowserMgr.SandboxEnabled && cfgErr == nil && appCfg != nil && sandbox.BackendKind(appCfg.Sandbox.BackendKind()) == sandbox.BackendKindIncus {
			sandboxLimits := &appCfg.Sandbox.Limits
			slidesPDFBrowserMgr.ContainerResolveFunc = func(sessID string) (string, string, error) {
				client, err := sandbox.SetupSandboxRuntime()
				if err != nil {
					return "", "", fmt.Errorf("sandbox runtime not available: %w", err)
				}
				sessRegistry, err := sandbox.NewSessionRegistry()
				if err != nil {
					return "", "", fmt.Errorf("failed to open session registry: %w", err)
				}
				tplRegistry, err := sandbox.NewTemplateRegistry()
				if err != nil {
					return "", "", fmt.Errorf("failed to open template registry: %w", err)
				}
				templateName := "@base"
				if entry := sessRegistry.Get(sessID); entry != nil && entry.TemplateName != "" {
					templateName = entry.TemplateName
				}
				containerName, err := sandbox.EnsureSessionContainer(client, sessRegistry, tplRegistry, sessID, templateName, sandboxLimits)
				if err != nil {
					return "", "", fmt.Errorf("failed to ensure session container: %w", err)
				}
				ip, err := client.GetContainerIPv4(containerName)
				if err != nil {
					return "", "", fmt.Errorf("failed to get IP for session container %q: %w", containerName, err)
				}
				return containerName, ip, nil
			}
		}

		slog.Info("slides PDF browser: initialized (sync.Once)", "sandboxEnabled", slidesPDFBrowserMgr.SandboxEnabled)
	})

	if slidesPDFBrowserMgr == nil {
		return nil, fmt.Errorf("slides PDF browser manager unavailable")
	}

	// After the once, always apply K8s/OpenShell callbacks if chat_factory has
	// registered them (process-global). These take precedence over the Incus
	// callbacks (which wireSandboxBrowserCallbacks may have set on a platform
	// where Incus isn't the active backend).
	registeredPDFMu.RLock()
	resolve := registeredPDFResolve
	startBrowser := registeredPDFStartBrowser
	dial := registeredPDFDial
	backendLabel := registeredPDFBackend
	registeredPDFMu.RUnlock()

	if resolve != nil && dial != nil {
		slidesPDFBrowserMgr.SandboxEnabled = true
		slidesPDFBrowserMgr.ContainerResolveFunc = resolve
		slidesPDFBrowserMgr.ContainerStartBrowserFunc = startBrowser
		slidesPDFBrowserMgr.ContainerDialFunc = dial
		if backendLabel == "" {
			backendLabel = "registered"
		}
		slog.Info("slides PDF browser: using registered sandbox callbacks", "backend", backendLabel)
	}

	// Bind the dedicated per-user slides session container. Safe on every call:
	// a no-op if the session hasn't changed.
	if sessionID != "" {
		slidesPDFBrowserMgr.EnsureSessionID(sessionID)
	}

	return slidesPDFBrowserMgr, nil
}

// wireSandboxBrowserCallbacks wires Incus container callbacks onto a browser.Manager
// when sandbox mode is enabled with the Incus backend. OpenShell and K8s must
// register backend-specific callbacks through SetPDFBrowserCallbacksForBackend
// or their launcher wiring; this helper must not silently select Incus for
// non-Incus backends.
func wireSandboxBrowserCallbacks(mgr *browser.Manager, cfg browser.BrowserConfig, appCfg *config.AppConfig, cfgErr error) {
	if mgr == nil || cfgErr != nil || appCfg == nil || !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return
	}
	if sandbox.BackendKind(appCfg.Sandbox.BackendKind()) != sandbox.BackendKindIncus {
		return
	}
	engine := incus.DetectBrowserEngine(incus.BrowserContainerConfig{
		ChromePath: cfg.ChromePath,
	})
	if !incus.IsContainerCompatibleEngine(engine) {
		return
	}

	mgr.SandboxEnabled = true
	slog.Info("browser: wired Incus sandbox callbacks", "backend", sandbox.BackendKindIncus)

	mgr.ContainerResolveFunc = func(sessionID string) (string, string, error) {
		client, err := sandbox.SetupSandboxRuntime()
		if err != nil {
			return "", "", fmt.Errorf("sandbox runtime not available: %w", err)
		}
		containerName := incus.SessionContainerName(sessionID)
		if !client.IsRunning(containerName) {
			return "", "", fmt.Errorf("session container %q is not running", containerName)
		}
		ip, err := client.GetContainerIPv4(containerName)
		if err != nil {
			return "", "", fmt.Errorf("failed to get IP for session container %q: %w", containerName, err)
		}
		return containerName, ip, nil
	}

	bCfg := incus.BrowserContainerConfig{
		ViewportWidth:       cfg.ViewportWidth,
		ViewportHeight:      cfg.ViewportHeight,
		KasmVNCPort:         cfg.KasmVNCPort,
		KasmVNCPassword:     cfg.KasmVNCPassword,
		Proxy:               cfg.Proxy,
		ChromePath:          cfg.ChromePath,
		FingerprintSeed:     cfg.FingerprintSeed,
		FingerprintPlatform: cfg.FingerprintPlatform,
	}
	mgr.ContainerStartBrowserFunc = func(containerName string) (io.Closer, error) {
		client, err := sandbox.SetupSandboxRuntime()
		if err != nil {
			return nil, fmt.Errorf("sandbox runtime not available: %w", err)
		}
		return nil, incus.StartChromiumInContainer(client, containerName, bCfg)
	}

	mgr.ContainerDialFunc = func(containerName string, port int) (net.Conn, error) {
		client, err := sandbox.SetupSandboxRuntime()
		if err != nil {
			return nil, fmt.Errorf("sandbox runtime not available: %w", err)
		}
		dialer := &incus.ContainerDialer{Client: client}
		return dialer.Dial(containerName, port)
	}
}

// GetSessionManager returns the singleton session manager
func GetSessionManager() *SessionManager {
	sessionOnce.Do(func() {
		baseService := session.InMemoryService()
		globalSessionManager = &SessionManager{
			service:         common.NewAutoInitService(baseService),
			sessions:        make(map[string]session.Session),
			mcpManagers:     make(map[string]*mcp.Manager),
			lastActivity:    make(map[string]time.Time),
			sandboxCleanups: make(map[string]func()),
			sandboxTools:    make(map[string][]tool.Tool),
		}
		// Start background cleanup goroutine
		go globalSessionManager.cleanupStaleSessionsLoop()
	})
	return globalSessionManager
}

// cleanupStaleSessionsLoop periodically cleans up sessions with no recent activity
func (sm *SessionManager) cleanupStaleSessionsLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		sm.cleanupStaleSessions()
	}
}

// cleanupStaleSessions removes sessions that have been inactive for too long
func (sm *SessionManager) cleanupStaleSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for sessionID, lastActive := range sm.lastActivity {
		if now.Sub(lastActive) > sessionTimeout {
			// Cleanup MCP manager
			if mgr, exists := sm.mcpManagers[sessionID]; exists {
				mgr.Cleanup()
				delete(sm.mcpManagers, sessionID)
			}
			// Cleanup sandbox container
			if cleanup, exists := sm.sandboxCleanups[sessionID]; exists {
				cleanup()
				delete(sm.sandboxCleanups, sessionID)
			}
			delete(sm.sandboxTools, sessionID)
			delete(sm.sessions, sessionID)
			delete(sm.lastActivity, sessionID)
			slog.Info("cleaned up stale session", "session_id", sessionID, "inactive_for", now.Sub(lastActive))
		}
	}
}

// TouchSession updates the last activity time for a session
func (sm *SessionManager) TouchSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.lastActivity[sessionID] = time.Now()
}

// GetOrCreateMCPManager returns the MCP manager for a session, creating if needed
func (sm *SessionManager) GetOrCreateMCPManager(ctx context.Context, sessionID string, requiredServers []string, mcpStores ...store.MCPServerStore) (*mcp.Manager, []tool.Toolset) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Update activity timestamp
	sm.lastActivity[sessionID] = time.Now()

	// Check if we already have an MCP manager for this session
	if mgr, exists := sm.mcpManagers[sessionID]; exists {
		return mgr, mgr.GetToolsets()
	}

	// No servers needed
	if len(requiredServers) == 0 {
		return nil, nil
	}

	var mgr *mcp.Manager

	// Platform mode: build config from DB stores
	var validStores []store.MCPServerStore
	for _, s := range mcpStores {
		if s != nil {
			validStores = append(validStores, s)
		}
	}
	if len(validStores) > 0 {
		mcpCfg := buildMCPConfigFromStores(validStores, requiredServers, EffectiveAppConfigFromContext(ctx, true))
		mgr = mcp.NewManagerFromConfig(mcpCfg)
		if resolver := credentials.ResolverFromContext(ctx); resolver != nil {
			mgr.SetCredentialResolver(resolver)
		}
	} else {
		// No platform MCP stores available — return empty
		return nil, nil
	}

	if err := mgr.InitializeSelectiveToolsets(ctx, requiredServers); err != nil {
		slog.Warn("failed to initialize mcp toolsets", "error", err)
		return nil, nil
	}

	sm.mcpManagers[sessionID] = mgr
	return mgr, mgr.GetToolsets()
}

// buildMCPConfigFromStores creates an MCPConfig from multiple platform DB stores
// (typically team + org) for the specified server names. Team store is queried first
// so that team-level server configs override org-level ones of the same name.
// appCfg is used by MergeStandardServersWithConfig to resolve team-level WebSearchTool;
// pass nil to fall back to the on-disk config.yaml.
func buildMCPConfigFromStores(mcpStores []store.MCPServerStore, requiredServers []string, appCfg *config.AppConfig) *config.MCPConfig {
	cfg := &config.MCPConfig{
		MCPServers: make(map[string]config.MCPServerConfig),
	}

	needed := make(map[string]bool, len(requiredServers))
	for _, name := range requiredServers {
		needed[name] = true
	}

	// Query each store in order (team first, org second).
	// First match wins — team servers override org servers of the same name.
	for _, mcpStore := range mcpStores {
		allServers, err := mcpStore.List(context.TODO())
		if err != nil {
			slog.Warn("failed to list MCP servers from store for config build", "error", err)
			continue
		}

		for _, srv := range allServers {
			if !needed[srv.Name] {
				continue
			}
			// Skip if already found in a higher-priority store
			if _, exists := cfg.MCPServers[srv.Name]; exists {
				continue
			}
			serverCfg := config.MCPServerConfig{
				Command:   srv.Command,
				Args:      srv.Args,
				Env:       srv.Env,
				Transport: srv.Transport,
				URL:       srv.URL,
				Enabled:   srv.Enabled,
			}
			cfg.MCPServers[srv.Name] = serverCfg
		}
	}

	// Merge installed standard servers (Tavily, Brave, Firecrawl, etc.)
	// This injects their full config (command, args, env with API key) from
	// the credential store, so they can be launched even if they
	// are not in the team DB store.
	// Use the passed appCfg so team-level WebSearchTool is honored.
	config.MergeStandardServersWithConfig(cfg, appCfg)

	return cfg
}

// CleanupSession removes a session and its MCP manager
func (sm *SessionManager) CleanupSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if mgr, exists := sm.mcpManagers[sessionID]; exists {
		mgr.Cleanup()
		delete(sm.mcpManagers, sessionID)
	}
	if cleanup, exists := sm.sandboxCleanups[sessionID]; exists {
		cleanup()
		delete(sm.sandboxCleanups, sessionID)
	}
	delete(sm.sandboxTools, sessionID)
	delete(sm.sessions, sessionID)
}

// HandleStopSession handles POST /api/session/{id}/stop - cleans up session and MCP
func HandleStopSession(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from URL
	path := r.URL.Path
	// Expected format: /api/session/{sessionId}/stop
	parts := strings.Split(path, "/")
	if len(parts) < 4 {
		respondError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	sessionID := parts[3] // /api/session/{id}/stop

	sm := GetSessionManager()
	sm.CleanupSession(sessionID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"stopped": true})
}

// HandleSessionKeepalive handles POST /api/session/{id}/keepalive - extends session lifetime
// The UI should call this every 30 seconds while a flow is active to prevent timeout
func HandleSessionKeepalive(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from URL
	path := r.URL.Path
	// Expected format: /api/session/{sessionId}/keepalive
	parts := strings.Split(path, "/")
	if len(parts) < 4 {
		respondError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	sessionID := parts[3] // /api/session/{id}/keepalive

	sm := GetSessionManager()
	sm.TouchSession(sessionID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// getRequiredMCPServers extracts the list of MCP server names needed for this flow
// It maps tool names from nodes' ToolsSelection to their source MCP server names
func getRequiredMCPServers(cfg *config.AgentConfig, mcpStores ...store.MCPServerStore) []string {
	// Collect all required tools from the flow
	toolsNeeded := make(map[string]bool)
	for _, node := range cfg.Nodes {
		for _, toolName := range node.ToolsSelection {
			toolsNeeded[toolName] = true
		}
	}

	if len(toolsNeeded) == 0 {
		return nil
	}

	// Platform mode: query the DB stores for tool→server mappings
	// Filter out nil stores
	var validStores []store.MCPServerStore
	for _, s := range mcpStores {
		if s != nil {
			validStores = append(validStores, s)
		}
	}
	if len(validStores) > 0 {
		return getRequiredMCPServersFromStore(validStores, toolsNeeded)
	}

	// No platform stores available — cannot resolve MCP servers
	return nil
}

// getRequiredMCPServersFromStore queries multiple platform MCP stores' cached_tools
// to find which servers provide the needed tools. Stores are queried in order
// (typically team first, then org) so that team-level servers take precedence.
func getRequiredMCPServersFromStore(mcpStores []store.MCPServerStore, toolsNeeded map[string]bool) []string {
	serversNeeded := make(map[string]bool)

	for _, mcpStore := range mcpStores {
		allServers, err := mcpStore.List(context.TODO())
		if err != nil {
			slog.Warn("failed to list MCP servers from store", "error", err)
			continue
		}

		for _, srv := range allServers {
			if !srv.IsEnabled() {
				continue
			}
			// Check cached_tools to see if this server provides any needed tools
			if len(srv.CachedTools) > 0 {
				var tools []struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(srv.CachedTools, &tools); err == nil {
					for _, t := range tools {
						if toolsNeeded[t.Name] {
							serversNeeded[srv.Name] = true
							break
						}
					}
				}
			}
			// Also check if the server name itself is in the tool list
			// (some flows reference server names directly)
			if toolsNeeded[srv.Name] {
				serversNeeded[srv.Name] = true
			}
		}
	}

	// Also check installed standard servers (Tavily, Brave, Firecrawl, etc.)
	// that are NOT already in the DB stores. These are configured via the
	// filesystem credential store and may not have been explicitly installed
	// into the team DB.
	for _, srv := range config.GetStandardServers() {
		if serversNeeded[srv.ID] {
			continue // Already found in DB stores
		}
		if !config.IsStandardServerInstalled(srv.ID) {
			continue // Not installed
		}
		// Check if this standard server provides any needed tools
		// First try the persistent file-based cache
		cachedEntries := cache.GetToolsForServer(srv.ID)
		if len(cachedEntries) > 0 {
			for _, e := range cachedEntries {
				if toolsNeeded[e.Name] {
					serversNeeded[srv.ID] = true
					break
				}
			}
		} else {
			// Fallback: check the known web tool names from the definition
			if srv.WebSearchTool != "" {
				if parts := strings.SplitN(srv.WebSearchTool, ":", 2); len(parts) == 2 {
					if toolsNeeded[parts[1]] {
						serversNeeded[srv.ID] = true
					}
				}
			}
			if srv.WebExtractTool != "" && !serversNeeded[srv.ID] {
				if parts := strings.SplitN(srv.WebExtractTool, ":", 2); len(parts) == 2 {
					if toolsNeeded[parts[1]] {
						serversNeeded[srv.ID] = true
					}
				}
			}
		}
	}

	var servers []string
	for server := range serversNeeded {
		servers = append(servers, server)
	}
	return servers
}

// SendSSE sends a Server-Sent Event
func SendSSE(w io.Writer, flusher http.Flusher, eventType string, data interface{}) {
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("failed to marshal SSE data", "error", err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
	if flusher != nil {
		flusher.Flush()
	}
}

// SendErrorSSE sends an error event
func SendErrorSSE(w io.Writer, flusher http.Flusher, msg string) {
	SendSSE(w, flusher, "error", map[string]string{"error": msg})
}

// HandleChat handles the /api/chat endpoint with SSE streaming
func HandleChat(w http.ResponseWriter, r *http.Request) {
	// Parse request (could be POST body)
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Error("failed to decode chat request", "error", err)
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.AgentID == "" {
		respondError(w, http.StatusBadRequest, "AgentID is required")
		return
	}

	if req.SessionID == "" {
		req.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "Streaming unsupported")
		return
	}

	// Send ping
	SendSSE(w, flusher, "ping", map[string]string{"status": "connected"})

	ctx := r.Context()
	sm := GetSessionManager()

	// Update session activity timestamp
	sm.TouchSession(req.SessionID)

	// 1. Load Agent Config
	// "team:" prefix signals the user explicitly selected the team version.
	// When present, skip personal resolution and go directly to team store.
	forceTeam := strings.HasPrefix(req.AgentID, "team:")
	if forceTeam {
		req.AgentID = strings.TrimPrefix(req.AgentID, "team:")
	}

	var cfg *config.AgentConfig
	var cfgErr error

	// Platform mode: load from flow store (personal-first, team fallback).
	// When forceTeam is set, skip personal and resolve from team only.
	if svc := store.FromRequest(r); svc != nil && (svc.PersonalFlows != nil || svc.Flows != nil) {
		var yamlContent string
		var found bool
		if !forceTeam && svc.PersonalFlows != nil {
			if y, err := svc.PersonalFlows.GetFlow(r.Context(), req.AgentID); err == nil && y != "" {
				yamlContent = y
				found = true
			}
		}
		if !found && svc.Flows != nil {
			if y, err := svc.Flows.GetFlow(r.Context(), req.AgentID); err == nil {
				yamlContent = y
				found = true
			}
		}
		if !found {
			SendErrorSSE(w, flusher, fmt.Sprintf("Agent not found: %s", req.AgentID))
			return
		}
		cfg, cfgErr = config.LoadAgentFromBytes([]byte(yamlContent))
		if cfgErr != nil {
			SendErrorSSE(w, flusher, fmt.Sprintf("Failed to parse agent config: %v", cfgErr))
			return
		}
	} else {
		// Personal mode: load from filesystem.
		agentPath, _, findErr := findAgentPath(req.AgentID)
		if findErr != nil {
			SendErrorSSE(w, flusher, fmt.Sprintf("Agent not found: %s", req.AgentID))
			return
		}
		cfg, cfgErr = config.LoadAgent(agentPath)
		if cfgErr != nil {
			SendErrorSSE(w, flusher, fmt.Sprintf("Failed to load configuration: %v", cfgErr))
			return
		}
	}

	// 2. Determine Provider/Model
	appCfg := effectiveAppConfig(r)
	injectProviderSecrets(appCfg)
	// Same PreSeed path as FlowRunHandler / Studio Chat: DB allow rules
	// (e.g. **.cloud.sap) + OpenShell gateway must be on ctx before first egress.
	ctx = withRuntimeNetworkPolicyContext(ctx, r, appCfg)
	ctx = withRuntimeSandboxContext(ctx, r)

	providerName := req.Provider
	if providerName == "" {
		providerName = appCfg.General.DefaultProvider
	}
	// Normalize provider name (handle Display Name -> ID mapping)
	// Frontend might send "SAP AI Core" instead of "sap_ai_core"
	normalizedProvider := providerName
	for id, displayName := range provider.ProviderDisplayNames {
		if displayName == providerName {
			normalizedProvider = id
			break
		}
	}
	providerName = normalizedProvider

	if providerName == "" {
		providerName = "gemini"
	}

	modelName := req.Model
	if modelName == "" {
		modelName = appCfg.General.DefaultModel
	}

	// 3. Initialize Provider/LLM
	llm, err := provider.GetProvider(ctx, providerName, modelName, appCfg)
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("Failed to initialize provider: %v", err))
		return
	}

	// 4. Initialize Tools
	internalTools, err := tools.GetInternalTools()
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("Failed to initialize tools: %v", err))
		return
	}

	// Register credential tools (resolve_credential, etc.)
	if credTools, credErr := tools.GetCredentialTools(); credErr == nil {
		internalTools = append(internalTools, credTools...)
	}

	// Register process management tools (process_start, process_write, etc.)
	if processTools, procErr := tools.GetProcessTools(); procErr == nil {
		internalTools = append(internalTools, processTools...)
	}

	// Register browser automation tools (shared manager across sessions)
	if browserTools, browserErr := tools.GetBrowserTools(GetBrowserManager()); browserErr == nil {
		internalTools = append(internalTools, browserTools...)
	}

	// Wrap tools with sandbox proxies (reuse across resumes of the same flow session)
	sm.mu.RLock()
	cachedTools, hasCached := sm.sandboxTools[req.SessionID]
	sm.mu.RUnlock()
	if hasCached {
		internalTools = cachedTools
	} else {
		result, sbErr := sandbox.SetupFlowSandbox(appCfg, internalTools)
		if sbErr != nil {
			SendErrorSSE(w, flusher, fmt.Sprintf("Sandbox setup failed: %v", sbErr))
			return
		}
		internalTools = result.Tools
		if result.Cleanup != nil {
			sm.mu.Lock()
			sm.sandboxCleanups[req.SessionID] = result.Cleanup
			sm.sandboxTools[req.SessionID] = result.Tools
			sm.mu.Unlock()
		}
	}

	// Initialize MCP - per-session, only servers needed for this flow
	// In platform mode, query team, org, and platform MCP stores for server configs
	var teamMCPStore, orgMCPStore, platformMCPStore store.MCPServerStore
	if svc := store.FromRequest(r); svc != nil && svc.Mode == store.ModePlatform {
		teamMCPStore = svc.TeamMCPServers
		orgMCPStore = svc.MCPServers
		platformMCPStore = svc.PlatformMCPServers
	}
	requiredServers := getRequiredMCPServers(cfg, teamMCPStore, orgMCPStore, platformMCPStore)
	_, mcpToolsets := sm.GetOrCreateMCPManager(ctx, req.SessionID, requiredServers, teamMCPStore, orgMCPStore, platformMCPStore)

	// 5. Create Astonish Agent & ADK Agent
	astonishAgent := agent.NewAstonishAgentWithToolsets(cfg, llm, internalTools, mcpToolsets)
	astonishAgent.DebugMode = req.Debug    // Enable verbose debug output when requested
	astonishAgent.IsWebMode = !req.CLIMode // CLI mode renders ANSI tool boxes; web mode uses markdown
	astonishAgent.SessionService = sm.service
	astonishAgent.AutoApprove = req.AutoApprove

	// Wire credential redactor so secrets are masked in SSE output
	if cs := tools.GetCredentialStore(); cs != nil {
		astonishAgent.Redactor = cs.Redactor()
		astonishAgent.CredentialStore = cs
		astonishAgent.PendingSecrets = credentials.NewPendingVault(cs.Redactor())
		// Inject Redactor into context so memory_save can Placeholderize()
		ctx = credentials.WithRedactor(ctx, cs.Redactor())
	}
	// Platform mode: inject PG-backed credential store into runner context
	// so BeforeToolCallback can resolve {{CREDENTIAL:...}} placeholders.
	if svc := store.FromRequest(r); svc != nil && (svc.PersonalCredentials != nil || svc.Credentials != nil) {
		merged := store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials)
		ctx = store.WithCredentialStore(ctx, merged)
		// Wire agent-level resolver as fallback (in case context propagation fails)
		astonishAgent.CredentialStore = credentials.NewStoreAdapter(merged)
		if astonishAgent.PendingSecrets == nil {
			astonishAgent.PendingSecrets = credentials.NewPendingVault(nil)
		}
		// Hydrate redactor from PG store so credential values are masked in SSE
		if astonishAgent.Redactor == nil {
			astonishAgent.Redactor = credentials.NewRedactor()
			ctx = credentials.WithRedactor(ctx, astonishAgent.Redactor)
		}
		astonishAgent.Redactor.HydrateFromStore(merged)
	}

	adkAgent, err := adkagent.New(adkagent.Config{
		Name:        "astonish_agent",
		Description: cfg.Description,
		Run:         astonishAgent.Run,
	})
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("Failed to create agent: %v", err))
		return
	}

	// 6. Manage Session
	sm.mu.Lock()
	sess, exists := sm.sessions[req.SessionID]
	if !exists {
		// Create new session
		resp, err := sm.service.Create(ctx, &session.CreateRequest{
			AppName: "astonish",
			UserID:  req.SessionID,
		})
		if err != nil {
			sm.mu.Unlock()
			SendErrorSSE(w, flusher, fmt.Sprintf("Failed to create session: %v", err))
			return
		}
		sess = resp.Session
		sm.sessions[req.SessionID] = sess
	}
	sm.mu.Unlock()

	// Overlap sandbox cold start with the first LLM/tool work in this run.
	sandbox.WarmFlowSession(ctx, internalTools, sess.ID())

	// 7. Create Runner
	rnr, err := runner.New(runner.Config{
		AppName:        "astonish",
		Agent:          adkAgent,
		SessionService: sm.service,
	})
	if err != nil {
		SendErrorSSE(w, flusher, fmt.Sprintf("Failed to create runner: %v", err))
		return
	}

	// Inject sandbox layer chain into context so that sandbox tool calls
	// (NodeTool.getClientFromContext) create pods with the configured @base
	// layer chain. Mirrors the Studio chat handler's resolution logic.
	if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
		if settings, err := svc.Settings.Get(r.Context()); err == nil && settings.TemplateName != "" {
			ctx = store.WithSandboxTemplate(ctx, settings.TemplateName)
			if chain := resolveTemplateLayerChain(r.Context(), settings.TemplateName); len(chain) > 0 {
				ctx = store.WithSandboxLayerChain(ctx, chain)
			}
			// On OpenShell, resolve per-template container image.
			if img := resolveTemplateImage(r.Context(), settings.TemplateName); img != "" {
				ctx = store.WithSandboxImage(ctx, img)
			}
		}
	}
	if store.SandboxLayerChainFromContext(ctx) == nil {
		if chain := resolveBaseLayerChain(r.Context()); len(chain) > 0 {
			ctx = store.WithSandboxLayerChain(ctx, chain)
		}
	}
	// On OpenShell, resolve @base's custom image if no team-specific image set.
	if store.SandboxImageFromContext(ctx) == "" {
		if img := resolveBaseImage(r.Context()); img != "" {
			ctx = store.WithSandboxImage(ctx, img)
		}
	}

	// 8. Run & Stream
	var userMsg *genai.Content
	if req.Message != "" {
		userMsg = agent.NewTimestampedUserContent(req.Message)
	}

	SendSSE(w, flusher, "status", map[string]string{"status": "running"})

	// Emit debug initialization info
	if req.Debug {
		SendSSE(w, flusher, "debug", map[string]any{
			"type":     "init",
			"provider": req.Provider,
			"model":    req.Model,
			"agent":    req.AgentID,
			"tools":    len(internalTools),
		})
	}
	var lastNodeName string
	var currentNodeType string // Track node type for conditional streaming
	var hasOutputModel bool    // Track if current node has output_model
	var toolCallCount int      // Track tool calls for text suppression

	for event, err := range rnr.Run(ctx, req.SessionID, sess.ID(), userMsg, adkagent.RunConfig{}) {
		// Break early if the SSE client disconnected.
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			SendErrorSSE(w, flusher, err.Error())
			return
		}

		// Check for _user_message_display marker - this event has proper display content
		isUserMessageDisplay := event.Actions.StateDelta != nil && event.Actions.StateDelta["_user_message_display"] != nil

		// Stream LLM Text chunks (only for appropriate node types)
		// Suppress for: update_state (internal state changes), tool (internal processing)
		// Allow for: llm, output, input (prompts should be visible to users)
		// EXCEPTION: Always stream tool approval requests (they contain approval_options)
		// EXCEPTION: Always stream _user_message_display events (properly formatted output)
		// SUPPRESS: Text after tool calls for output_model nodes (raw JSON)
		isApprovalRequest := event.Actions.StateDelta != nil && event.Actions.StateDelta["approval_options"] != nil
		shouldStream := currentNodeType == "" || currentNodeType == "llm" || currentNodeType == "output" || currentNodeType == "input" || isApprovalRequest || isUserMessageDisplay

		// For output_model nodes, suppress ALL text (raw JSON will be parsed and displayed via user_message)
		// Only allow _user_message_display events, approval requests, and input prompts
		isInputRequest := event.Actions.StateDelta != nil && event.Actions.StateDelta["input_options"] != nil
		if hasOutputModel && !isUserMessageDisplay && !isApprovalRequest && !isInputRequest {
			shouldStream = false
		}

		// Check for _output_node marker (from handleOutputNode)
		isOutputNode := event.Actions.StateDelta != nil && event.Actions.StateDelta["_output_node"] != nil

		if shouldStream && event.LLMResponse.Content != nil {
			for _, part := range event.LLMResponse.Content.Parts {
				if part.Text != "" {
					payload := map[string]interface{}{
						"text": part.Text,
					}
					if isOutputNode || isUserMessageDisplay {
						payload["preserveWhitespace"] = true
					}
					SendSSE(w, flusher, "text", payload)
				}
			}
		}

		// Track tool calls AFTER processing text for this event
		// This ensures greeting text in the same event as tool call is sent first
		if event.LLMResponse.Content != nil {
			for _, part := range event.LLMResponse.Content.Parts {
				if part.FunctionCall != nil {
					toolCallCount++
					// Emit debug event for tool call args
					if req.Debug {
						argsJSON, _ := json.Marshal(part.FunctionCall.Args)
						SendSSE(w, flusher, "debug", map[string]any{
							"type": "tool_call",
							"tool": part.FunctionCall.Name,
							"args": string(argsJSON),
						})
					}
				}
				if part.FunctionResponse != nil && req.Debug {
					respJSON, _ := json.Marshal(part.FunctionResponse.Response)
					SendSSE(w, flusher, "debug", map[string]any{
						"type":   "tool_response",
						"tool":   part.FunctionResponse.Name,
						"result": string(respJSON),
					})
				}
			}
		}

		// Stream State Updates / Node Transitions
		if event.Actions.StateDelta != nil {
			delta := event.Actions.StateDelta

			// Detect node transition
			if nodeName, ok := delta["current_node"].(string); ok {
				// Only send if node actually changed
				if nodeName != lastNodeName {
					lastNodeName = nodeName
					// Also check node_type if available (sometimes implicit)
					nodeType, _ := delta["node_type"].(string)
					currentNodeType = nodeType // Update for streaming filter

					// Reset tool call count for new node
					toolCallCount = 0

					// Check if this node has output_model (from config)
					hasOutputModel = false
					for _, node := range cfg.Nodes {
						if node.Name == nodeName && len(node.OutputModel) > 0 {
							hasOutputModel = true
							break
						}
					}

					// Always send node event, include silent flag for frontend filtering
					isSilent, _ := delta["silent"].(bool)
					SendSSE(w, flusher, "node", map[string]any{
						"node":   nodeName,
						"type":   nodeType,
						"silent": isSilent,
					})
				}
			}

			// Check for Retry Info (smart error handling)
			if retryInfoVal, ok := delta["_retry_info"]; ok {
				if retryInfo, ok := retryInfoVal.(map[string]interface{}); ok {
					attempt := 0
					maxRetries := 3
					reason := ""

					if a, ok := retryInfo["attempt"].(int); ok {
						attempt = a
					} else if a, ok := retryInfo["attempt"].(float64); ok {
						attempt = int(a)
					}

					if m, ok := retryInfo["max_retries"].(int); ok {
						maxRetries = m
					} else if m, ok := retryInfo["max_retries"].(float64); ok {
						maxRetries = int(m)
					}

					if r, ok := retryInfo["reason"].(string); ok {
						reason = r
					}

					SendSSE(w, flusher, "retry", map[string]interface{}{
						"attempt":    attempt,
						"maxRetries": maxRetries,
						"reason":     reason,
					})
				}
			}

			// Check for Failure Info (smart error handling)
			if failureInfoVal, ok := delta["_failure_info"]; ok {
				if failureInfo, ok := failureInfoVal.(map[string]interface{}); ok {
					title, _ := failureInfo["title"].(string)
					reason, _ := failureInfo["reason"].(string)
					originalError, _ := failureInfo["original_error"].(string)
					suggestion, _ := failureInfo["suggestion"].(string)

					SendSSE(w, flusher, "error_info", map[string]interface{}{
						"title":         title,
						"reason":        reason,
						"suggestion":    suggestion,
						"originalError": originalError,
					})
				}
			}

			// Capture input request from approval_options (tool approval)
			if options, ok := delta["approval_options"].([]string); ok {
				SendSSE(w, flusher, "input_request", map[string]interface{}{
					"options": options,
				})
			} else if optionsRaw, ok := delta["approval_options"].([]interface{}); ok {
				SendSSE(w, flusher, "input_request", map[string]interface{}{
					"options": optionsRaw,
				})
			}

			// Capture input request from input_options (input node)
			if options, ok := delta["input_options"].([]string); ok && len(options) > 0 {
				// Input node with predefined options
				SendSSE(w, flusher, "input_request", map[string]interface{}{
					"options": options,
				})
			} else if optionsRaw, ok := delta["input_options"].([]interface{}); ok && len(optionsRaw) > 0 {
				// Handle []interface{} case
				SendSSE(w, flusher, "input_request", map[string]interface{}{
					"options": optionsRaw,
				})
			} else if waiting, ok := delta["waiting_for_input"].(bool); ok && waiting {
				// Free-text input (no options) - send empty options to enable input
				SendSSE(w, flusher, "input_request", map[string]interface{}{
					"options": []string{},
				})
			}

			// Send full state delta for UI variables view
			SendSSE(w, flusher, "state", delta)
		}
	}

	// Check if flow reached END node - cleanup MCP for this session
	if lastNodeName == "END" {
		sm.CleanupSession(req.SessionID)
	}

	SendSSE(w, flusher, "done", map[string]bool{"done": true})
}
