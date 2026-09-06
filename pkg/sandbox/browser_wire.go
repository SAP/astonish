package sandbox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/SAP/astonish/pkg/browser"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
)

// WireIncusBrowserManager configures mgr so Chromium + KasmVNC run inside the
// session container (same path Studio chat uses).
//
// Host-only ChromePath values (macOS/Windows binaries) are treated as
// "default" for the container: the sandbox installs Linux Chromium inside the
// session. Returning false here previously left SandboxEnabled off and drills
// fell through to host Chrome, which cannot reach container localhost services.
//
// When pool is non-nil, ContainerEnsureReadyFunc is set so that browser tools
// (which bypass NodeTool.Run) wait for the pool to provision the session
// container before trying to resolve its container name/IP. This mirrors the
// K8s path (WireBackendBrowserManager). Without this, a browser-first tool
// call fails because the Incus container has not been started yet.
func WireIncusBrowserManager(mgr *browser.Manager, client *IncusClient, pool ToolNodePool, touchActivity func(sessionID string)) bool {
	if mgr == nil || client == nil {
		return false
	}
	cfg := mgr.Config()
	engine := incus.DetectBrowserEngine(incus.BrowserContainerConfig{
		ChromePath: cfg.ChromePath,
	})
	containerChromePath := cfg.ChromePath
	if !incus.IsContainerCompatibleEngine(engine) {
		slog.Info("browser ChromePath is host-only; using default Chromium in sandbox container",
			"component", "browser-wire",
			"chrome_path", cfg.ChromePath,
			"detected_engine", engine,
		)
		containerChromePath = ""
	}

	bCfg := incus.BrowserContainerConfig{
		ViewportWidth:       cfg.ViewportWidth,
		ViewportHeight:      cfg.ViewportHeight,
		KasmVNCPort:         cfg.KasmVNCPort,
		KasmVNCPassword:     cfg.KasmVNCPassword,
		Proxy:               cfg.Proxy,
		ChromePath:          containerChromePath,
		FingerprintSeed:     cfg.FingerprintSeed,
		FingerprintPlatform: cfg.FingerprintPlatform,
	}
	mgr.SandboxEnabled = true

	// ContainerEnsureReadyFunc waits for the lazy pool to provision the session
	// container before the browser tries to resolve it. Without this, a
	// browser-first tool call fails because IsRunning returns false for a
	// container that the pool hasn't started yet.
	if pool != nil {
		mgr.ContainerEnsureReadyFunc = func(ctx context.Context, sessionID string) error {
			client := GetPoolClientFromContext(ctx, pool, sessionID)
			if client == nil {
				return fmt.Errorf("no sandbox client for session %q", sessionID)
			}
			return client.EnsureReady(sessionID)
		}
	}

	mgr.ContainerResolveFunc = func(sessionID string) (string, string, error) {
		containerName := SessionContainerName(sessionID)
		if !client.IsRunning(containerName) {
			return "", "", fmt.Errorf("session container %q is not running", containerName)
		}
		ip, err := client.GetContainerIPv4(containerName)
		if err != nil {
			return "", "", fmt.Errorf("failed to get IP for session container %q: %w", containerName, err)
		}
		return containerName, ip, nil
	}
	mgr.ContainerStartBrowserFunc = func(containerName string) (io.Closer, error) {
		return nil, incus.StartChromiumInContainer(client, containerName, bCfg)
	}
	mgr.ContainerDialFunc = func(containerName string, port int) (net.Conn, error) {
		dialer := &incus.ContainerDialer{Client: client}
		return dialer.Dial(containerName, port)
	}
	mgr.ContainerStartRecordingFunc = func(containerName, display, outPath string) (func() error, int, int, error) {
		return incus.StartRecordingInContainer(client, containerName, display, outPath)
	}
	if touchActivity != nil {
		mgr.ActivityTouchFunc = touchActivity
	}
	return true
}
