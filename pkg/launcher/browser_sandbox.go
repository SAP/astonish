package launcher

import (
	"log/slog"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
)

// WireIncusBrowserManager configures mgr for in-container Chromium (Incus).
// When pool is non-nil, ContainerEnsureReadyFunc is set so browser tools wait
// for the pool to provision the container before resolving it.
func WireIncusBrowserManager(mgr *browser.Manager, client *sandbox.IncusClient, pool sandbox.ToolNodePool, touchActivity func(sessionID string)) bool {
	return sandbox.WireIncusBrowserManager(mgr, client, pool, touchActivity)
}

// WireOpenShellBrowserManager configures mgr for in-container CloakBrowser (OpenShell).
func WireOpenShellBrowserManager(
	mgr *browser.Manager,
	gw openshell.GatewayClient,
	sessReg *sandbox.SessionRegistry,
	touchActivity func(sessionID string),
) bool {
	return openshell.WireBrowserManager(mgr, gw, sessReg, touchActivity)
}

// wireBrowserContainerCallbacks configures a browser Manager for OverlayFS
// session containers using the configured sandbox backend.
func wireBrowserContainerCallbacks(mgr *browser.Manager) {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		slog.Debug("browser container callbacks: app config unavailable", "error", err)
		return
	}
	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return
	}
	sessReg, err := sandbox.NewSessionRegistry()
	if err != nil {
		slog.Debug("browser container callbacks: session registry unavailable", "error", err)
		return
	}
	b, cleanup, err := sandbox.BackendFromAppConfigWithSessions(appCfg, sessReg)
	if err != nil {
		slog.Debug("browser container callbacks: sandbox backend unavailable", "error", err)
		return
	}
	_ = cleanup
	if !sandbox.WireBackendBrowserManager(mgr, b, sessReg, nil, nil) {
		slog.Warn("browser container callbacks: failed to wire in-container Chromium")
	}
}
