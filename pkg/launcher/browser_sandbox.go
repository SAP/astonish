package launcher

import (
	"log/slog"

	"github.com/SAP/astonish/pkg/browser"
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

// wireBrowserContainerCallbacks configures a browser Manager for Incus session
// containers using a lazily opened sandbox client. Used by console/flow paths
// that do not already hold a NodeClientPool.
func wireBrowserContainerCallbacks(mgr *browser.Manager) {
	client, err := sandbox.SetupSandboxRuntime()
	if err != nil {
		slog.Debug("browser container callbacks: sandbox runtime unavailable", "error", err)
		return
	}
	if !sandbox.WireIncusBrowserManager(mgr, client, nil, nil) {
		slog.Warn("browser container callbacks: failed to wire in-container Chromium")
	}
}
