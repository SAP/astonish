package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

// localProfileCDPURL reads only the configured profile's endpoint, never a
// machine-wide port scan or a user's unrelated browser. Chromium writes this
// file when launched with --remote-debugging-port=0. Treat stale files as hints:
// the connection and Browser.getVersion must succeed before reusing anything.
func localProfileCDPURL(profile string) string {
	if profile == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		return ""
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	path := strings.TrimSpace(lines[1])
	if err != nil || port < 1 || port > 65535 || !strings.HasPrefix(path, "/devtools/browser/") {
		return ""
	}
	id := strings.TrimPrefix(path, "/devtools/browser/")
	if id == "" || strings.ContainsAny(id, "/?#\\ \t\r\n") {
		return ""
	}
	return fmt.Sprintf("ws://127.0.0.1:%d%s", port, path)
}

// reconnectLocalProfileLocked borrows an already-running local browser without
// taking ownership of its process. Call only after sandbox/remote routing, with
// m.mu held. The socket has its own closer: rod.Browser.Close closes Chrome itself.
func (m *Manager) reconnectLocalProfileLocked() *rod.Browser {
	endpoint := localProfileCDPURL(m.config.UserDataDir)
	if endpoint == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ws := &cdp.WebSocket{}
	if err := ws.Connect(ctx, endpoint, nil); err != nil {
		return nil
	}
	b := rod.New().Client(cdp.New().Start(ws)).NoDefaultDevice()
	// Connect initializes Rod's event state on this exact browser instance.
	// Bound setup by closing the transport, not by connecting a context clone
	// whose initialized state would be lost (or canceled on return).
	timer := time.AfterFunc(2*time.Second, func() { _ = ws.Close() })
	defer timer.Stop()
	if err := b.Connect(); err != nil {
		_ = ws.Close()
		return nil
	}
	version, err := b.Version()
	if err != nil {
		_ = ws.Close()
		return nil
	}
	if !timer.Stop() {
		_ = ws.Close()
		return nil
	}
	m.browser = b
	m.localDisconnect = ws.Close
	m.cdpURL = endpoint
	m.chromeVersion = version.Product
	m.logger.Printf("Reusing browser for configured local profile")
	return b
}

func (m *Manager) detachLocalBrowserLocked() {
	if m.localDisconnect == nil {
		return
	}
	if m.incognito != nil {
		_ = m.incognito.Timeout(2 * time.Second).Close()
	}
	_ = m.localDisconnect()
	m.localDisconnect = nil
	m.browser = nil
	m.incognito = nil
}
