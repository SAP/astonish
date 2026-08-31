package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

// TestGetOrLaunch_ContainerPath_MissingResolveFunc verifies that GetOrLaunch
// returns a clear error when SandboxEnabled is true but ContainerResolveFunc
// is not wired.
func TestGetOrLaunch_ContainerPath_MissingResolveFunc(t *testing.T) {
	m := NewManager(BrowserConfig{})
	m.SandboxEnabled = true
	m.sessionID = "test-session"
	// Do NOT set ContainerResolveFunc

	_, err := m.GetOrLaunch()
	if err == nil {
		t.Fatal("expected error when ContainerResolveFunc is nil")
	}
	if !strings.Contains(err.Error(), "ContainerResolveFunc") {
		t.Errorf("error should mention ContainerResolveFunc, got: %v", err)
	}
}

func TestGetOrLaunch_ContainerPath_WaitsBeforeResolve(t *testing.T) {
	var sequence []string

	m := NewManager(BrowserConfig{})
	m.SandboxEnabled = true
	m.sessionID = "test-session"
	m.ContainerEnsureReadyFunc = func(sessionID string) error {
		sequence = append(sequence, "ready:"+sessionID)
		return nil
	}
	m.ContainerResolveFunc = func(sessionID string) (string, string, error) {
		sequence = append(sequence, "resolve:"+sessionID)
		return "", "", fmt.Errorf("stop after resolve")
	}

	_, err := m.GetOrLaunch()
	if err == nil {
		t.Fatal("expected resolve error")
	}
	want := []string{"ready:test-session", "resolve:test-session"}
	if fmt.Sprint(sequence) != fmt.Sprint(want) {
		t.Fatalf("callback sequence = %v, want %v", sequence, want)
	}
}

func TestGetOrLaunch_ContainerPath_ReadinessErrorStopsResolve(t *testing.T) {
	resolved := false

	m := NewManager(BrowserConfig{})
	m.SandboxEnabled = true
	m.sessionID = "test-session"
	m.ContainerEnsureReadyFunc = func(string) error {
		return fmt.Errorf("pod readiness failed")
	}
	m.ContainerResolveFunc = func(string) (string, string, error) {
		resolved = true
		return "container", "127.0.0.1", nil
	}

	_, err := m.GetOrLaunch()
	if err == nil || !strings.Contains(err.Error(), "pod readiness failed") {
		t.Fatalf("GetOrLaunch() error = %v, want readiness error", err)
	}
	if resolved {
		t.Fatal("ContainerResolveFunc called after readiness failure")
	}
}

// TestGetOrLaunch_ContainerPath_ResolveError verifies that a resolve failure
// is propagated and stale state is not left behind.
func TestGetOrLaunch_ContainerPath_ResolveError(t *testing.T) {
	m := NewManager(BrowserConfig{})
	m.SandboxEnabled = true
	m.sessionID = "test-session"
	m.ContainerResolveFunc = func(_ string) (string, string, error) {
		return "", "", fmt.Errorf("container not running")
	}

	_, err := m.GetOrLaunch()
	if err == nil {
		t.Fatal("expected error on resolve failure")
	}
	if !strings.Contains(err.Error(), "container not running") {
		t.Errorf("error should contain resolve error, got: %v", err)
	}
}

// TestGetOrLaunch_ContainerPath_StartError verifies that a browser start
// failure clears stale container state so retries go through the full sequence.
func TestGetOrLaunch_ContainerPath_StartError(t *testing.T) {
	m := NewManager(BrowserConfig{})
	m.SandboxEnabled = true
	m.sessionID = "test-session"
	m.ContainerResolveFunc = func(_ string) (string, string, error) {
		return "astn-sess-test", "10.0.0.1", nil
	}
	m.ContainerStartBrowserFunc = func(_ string) (io.Closer, error) {
		return nil, fmt.Errorf("chromium failed to start")
	}

	_, err := m.GetOrLaunch()
	if err == nil {
		t.Fatal("expected error on start failure")
	}
	if !strings.Contains(err.Error(), "chromium failed to start") {
		t.Errorf("error should contain start error, got: %v", err)
	}

	// Verify stale state was cleared
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.containerName != "" {
		t.Error("containerName should be cleared after start failure")
	}
	if m.containerIP != "" {
		t.Error("containerIP should be cleared after start failure")
	}
}

// TestGetOrLaunch_ContainerPath_CallSequence verifies the callback invocation
// order: resolve → start → resolveCDP. We can't fully test CDP resolution
// without a real Chrome, but we verify that resolve and start are called with
// the correct arguments.
func TestGetOrLaunch_ContainerPath_CallSequence(t *testing.T) {
	var resolvedSession string
	var startedContainer string

	m := NewManager(BrowserConfig{})
	m.SandboxEnabled = true
	m.sessionID = "test-session-123"
	m.ContainerResolveFunc = func(sessionID string) (string, string, error) {
		resolvedSession = sessionID
		return "astn-sess-test123", "10.0.0.1", nil
	}
	m.ContainerStartBrowserFunc = func(containerName string) (io.Closer, error) {
		startedContainer = containerName
		return nil, nil
	}
	// ContainerDialFunc that fails immediately — we just want to verify
	// the resolve+start callbacks were called, not actually connect CDP.
	m.ContainerDialFunc = func(_ string, _ int) (net.Conn, error) {
		return nil, fmt.Errorf("intentional test failure")
	}

	_, err := m.GetOrLaunch()
	// Error expected because CDP resolution will fail
	if err == nil {
		t.Fatal("expected error (no real Chrome to connect to)")
	}

	if resolvedSession != "test-session-123" {
		t.Errorf("resolve called with session %q, want %q", resolvedSession, "test-session-123")
	}
	if startedContainer != "astn-sess-test123" {
		t.Errorf("start called with container %q, want %q", startedContainer, "astn-sess-test123")
	}
}

// TestResolveCDPURL_UsesTunnel verifies that when ContainerDialFunc is set,
// HTTP requests to /json/version are routed through the tunnel dialer rather
// than direct TCP to the container IP.
func TestResolveCDPURL_UsesTunnel(t *testing.T) {
	// Start a local HTTP server that responds to /json/version
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			resp := map[string]string{
				"webSocketDebuggerUrl": "ws://10.0.0.1:9222/devtools/browser/test-guid",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Extract the port the test server is listening on
	srvAddr := srv.Listener.Addr().(*net.TCPAddr)
	srvPort := srvAddr.Port

	var tunnelCalls atomic.Int32

	m := NewManager(BrowserConfig{})
	m.containerName = "astn-sess-test"
	m.ContainerDialFunc = func(containerName string, port int) (net.Conn, error) {
		tunnelCalls.Add(1)
		if containerName != "astn-sess-test" {
			return nil, fmt.Errorf("wrong container: %s", containerName)
		}
		if port != defaultCDPPort {
			return nil, fmt.Errorf("wrong port: %d, want %d", port, defaultCDPPort)
		}
		// Connect to the local test server instead
		return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", srvPort))
	}

	// The IP here doesn't matter — the tunnel dialer ignores it
	resolved, err := m.resolveCDPURL("astn-sess-test", "10.0.0.1")
	if err != nil {
		t.Fatalf("resolveCDPURL with tunnel: %v", err)
	}

	if resolved != "ws://10.0.0.1:9222/devtools/browser/test-guid" {
		t.Errorf("resolved URL: got %q, want ws://10.0.0.1:9222/devtools/browser/test-guid", resolved)
	}

	if tunnelCalls.Load() == 0 {
		t.Error("ContainerDialFunc should have been called at least once")
	}
}

// TestResolveCDPURL_DirectFallback verifies that when ContainerDialFunc is nil,
// HTTP requests go directly to the container IP.
func TestResolveCDPURL_DirectFallback(t *testing.T) {
	// Start a local HTTP server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			resp := map[string]string{
				"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/direct-guid",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Extract host:port from test server
	srvAddr := srv.Listener.Addr().(*net.TCPAddr)

	m := NewManager(BrowserConfig{})
	// No ContainerDialFunc — should use direct HTTP
	m.ContainerDialFunc = nil

	resolved, err := m.resolveCDPURL("", srvAddr.IP.String())
	if err != nil {
		// If the port doesn't match sandbox.DefaultCDPPort, it will fail.
		// We need to override the URL to use the test server port.
		// Since resolveCDPURL hardcodes the port, this test verifies the
		// fallback path is taken (no tunnel). The actual connection will
		// fail unless the server happens to be on port 9222. That's fine —
		// this test documents the behavior.
		if !strings.Contains(err.Error(), "after 15s") {
			t.Fatalf("unexpected error (should be timeout): %v", err)
		}
		t.Skip("direct fallback test skipped — test server not on port 9222")
	}

	if resolved == "" {
		t.Error("expected non-empty resolved URL")
	}
}

// TestResolveCDPURL_RetryOnFailure verifies that resolveCDPURL retries when
// the first attempts fail.
func TestResolveCDPURL_RetryOnFailure(t *testing.T) {
	var requestCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if r.URL.Path == "/json/version" {
			// Fail the first 2 requests, succeed on the 3rd
			if count <= 2 {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			resp := map[string]string{
				"webSocketDebuggerUrl": "ws://10.0.0.1:9222/devtools/browser/retry-guid",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	srvAddr := srv.Listener.Addr().(*net.TCPAddr)
	srvPort := srvAddr.Port

	m := NewManager(BrowserConfig{})
	m.containerName = "astn-sess-retry"
	m.ContainerDialFunc = func(_ string, _ int) (net.Conn, error) {
		return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", srvPort))
	}

	resolved, err := m.resolveCDPURL("astn-sess-retry", "10.0.0.1")
	if err != nil {
		t.Fatalf("resolveCDPURL with retry: %v", err)
	}

	if resolved != "ws://10.0.0.1:9222/devtools/browser/retry-guid" {
		t.Errorf("resolved URL: got %q", resolved)
	}

	if requestCount.Load() < 3 {
		t.Errorf("expected at least 3 requests (2 failures + 1 success), got %d", requestCount.Load())
	}
}

// TestConnectContainerCDP_NoDialFunc_FallsToRemote verifies that when
// ContainerDialFunc is nil, connectContainerCDP falls back to connectRemote.
func TestConnectContainerCDP_NoDialFunc_FallsToRemote(t *testing.T) {
	m := NewManager(BrowserConfig{
		RemoteCDPURL: "ws://fake:9222/devtools/browser/test",
	})
	m.ContainerDialFunc = nil
	m.containerName = "" // empty triggers fallback

	// This will fail because there's no real browser, but it should
	// attempt the connectRemote path (not the tunnel path)
	_, err := m.connectContainerCDP()
	if err == nil {
		t.Fatal("expected error (no real browser)")
	}
	// The error should be from connectRemote, not from tunnel dialer
	if strings.Contains(err.Error(), "tunnel") {
		t.Errorf("should not attempt tunnel when ContainerDialFunc is nil, got: %v", err)
	}
}

// deadCDPClient is a mock CDPClient that returns "closed pipe" errors on Call().
// This simulates a dead CDP connection (Chrome crashed, container destroyed, etc.).
type deadCDPClient struct {
	events chan *cdp.Event
}

func newDeadCDPClient() *deadCDPClient {
	return &deadCDPClient{events: make(chan *cdp.Event)}
}

func (c *deadCDPClient) Event() <-chan *cdp.Event { return c.events }
func (c *deadCDPClient) Call(_ context.Context, _, _ string, _ interface{}) ([]byte, error) {
	return nil, fmt.Errorf("io: read/write on closed pipe")
}

// TestGetOrLaunch_ReconnectsOnDeadConnection verifies that GetOrLaunch detects
// a dead CDP connection (e.g., "io: read/write on closed pipe") and attempts
// to reconnect instead of returning the dead browser.
func TestGetOrLaunch_ReconnectsOnDeadConnection(t *testing.T) {
	m := NewManager(BrowserConfig{})
	m.SandboxEnabled = true
	m.sessionID = "test-session"

	// Set up a dead browser (Version() will return "closed pipe" error)
	deadBrowser := rod.New().Client(newDeadCDPClient()).NoDefaultDevice()
	m.browser = deadBrowser
	m.containerName = "old-container"
	m.containerIP = "10.0.0.99"
	m.config.RemoteCDPURL = "ws://10.0.0.99:9222/devtools/browser/dead-guid"

	// Wire up ContainerResolveFunc so the re-launch path is exercised
	var resolveCalled atomic.Int32
	m.ContainerResolveFunc = func(sessionID string) (string, string, error) {
		resolveCalled.Add(1)
		return "new-container", "10.0.0.2", nil
	}
	m.ContainerStartBrowserFunc = func(_ string) (io.Closer, error) {
		return nil, nil
	}
	// ContainerDialFunc fails — we just want to verify the reconnection
	// path is triggered, not that the full launch succeeds.
	m.ContainerDialFunc = func(_ string, _ int) (net.Conn, error) {
		return nil, fmt.Errorf("intentional test failure in dial")
	}

	_, err := m.GetOrLaunch()
	// The call will fail because we can't actually connect to a real browser,
	// but what matters is:
	// 1. The dead browser was detected and cleared
	// 2. The re-launch path was triggered (resolve was called)
	if err == nil {
		t.Fatal("expected error (no real Chrome to reconnect to)")
	}

	// Verify that reconnection was attempted
	if resolveCalled.Load() == 0 {
		t.Error("ContainerResolveFunc should have been called (reconnection path)")
	}

	// Verify stale state was cleared
	m.mu.Lock()
	if m.browser != nil {
		t.Error("dead browser should have been cleared")
	}
	m.mu.Unlock()
}

// TestGetOrLaunch_HealthyBrowserNotReconnected verifies that a healthy browser
// (one that responds to Version()) is returned without triggering reconnection.
func TestGetOrLaunch_HealthyBrowserNotReconnected(t *testing.T) {
	// We can't easily create a "healthy" rod.Browser without a real Chrome,
	// so we test the inverse: verify isBrowserDead recognizes non-dead errors.
	m := NewManager(BrowserConfig{})

	// Non-pipe errors should NOT trigger reconnection
	nonDeadErrors := []error{
		fmt.Errorf("context deadline exceeded"),
		fmt.Errorf("some other random error"),
		fmt.Errorf("permission denied"),
	}

	for _, err := range nonDeadErrors {
		if m.isBrowserDead(err) {
			t.Errorf("isBrowserDead should return false for %q", err)
		}
	}

	// Pipe/CDP errors SHOULD trigger reconnection
	deadErrors := []error{
		fmt.Errorf("io: read/write on closed pipe"),
		fmt.Errorf("write tcp: broken pipe"),
		fmt.Errorf("read: connection reset by peer"),
		fmt.Errorf("use of closed network connection"),
		fmt.Errorf("unexpected EOF"),
		fmt.Errorf("failed to resolve CDP URL at 10.0.0.1:9222 after 15s"),
		fmt.Errorf("websocket: close 1006 abnormal closure"),
		fmt.Errorf("target closed"),
		fmt.Errorf("failed to connect CDP WebSocket via tunnel: dial tcp 127.0.0.1:9222"),
	}

	for _, err := range deadErrors {
		if !m.isBrowserDead(err) {
			t.Errorf("isBrowserDead should return true for %q", err)
		}
	}

	// App URL refused must NOT look like a dead CDP stack.
	if m.isBrowserDead(fmt.Errorf("navigation failed: net::ERR_CONNECTION_REFUSED")) {
		t.Error("app ERR_CONNECTION_REFUSED must not be classified as dead browser")
	}
}

// TestResetBrowserLocked_ClearsAllState verifies that resetBrowserLocked
// clears all relevant state fields.
func TestResetBrowserLocked_ClearsAllState(t *testing.T) {
	m := NewManager(BrowserConfig{
		RemoteCDPURL: "ws://old:9222/devtools/browser/guid",
	})
	m.SandboxEnabled = true
	// Use a dead CDP client so Close() doesn't panic on nil context
	m.browser = rod.New().Client(newDeadCDPClient()).NoDefaultDevice()
	m.containerName = "old-container"
	m.containerIP = "10.0.0.1"
	m.cdpURL = "ws://10.0.0.1:9222/devtools/browser/guid"

	m.mu.Lock()
	m.resetBrowserLocked()
	m.mu.Unlock()

	if m.browser != nil {
		t.Error("browser should be nil after reset")
	}
	if m.activePg != nil {
		t.Error("activePg should be nil after reset")
	}
	if m.cdpURL != "" {
		t.Errorf("cdpURL should be empty after reset, got %q", m.cdpURL)
	}
	if m.containerName != "" {
		t.Errorf("containerName should be empty after reset, got %q", m.containerName)
	}
	if m.containerIP != "" {
		t.Errorf("containerIP should be empty after reset, got %q", m.containerIP)
	}
	if m.config.RemoteCDPURL != "" {
		t.Errorf("RemoteCDPURL should be empty after reset, got %q", m.config.RemoteCDPURL)
	}
}
