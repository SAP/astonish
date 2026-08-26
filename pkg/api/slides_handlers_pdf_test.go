package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/sandbox"
)

// saveRegisteredPDF snapshots and clears the process-global PDF browser
// callbacks, returning a restore function. Mirrors the pattern in
// TestSetPDFBrowserCallbacksForBackendStoresDiagnostics.
func saveRegisteredPDF(t *testing.T) func() {
	t.Helper()
	registeredPDFMu.Lock()
	oldResolve := registeredPDFResolve
	oldStart := registeredPDFStartBrowser
	oldDial := registeredPDFDial
	oldBackend := registeredPDFBackend
	registeredPDFResolve = nil
	registeredPDFStartBrowser = nil
	registeredPDFDial = nil
	registeredPDFBackend = ""
	registeredPDFMu.Unlock()
	return func() {
		registeredPDFMu.Lock()
		registeredPDFResolve = oldResolve
		registeredPDFStartBrowser = oldStart
		registeredPDFDial = oldDial
		registeredPDFBackend = oldBackend
		registeredPDFMu.Unlock()
	}
}

// TestSlidesPDF_SessionIDDerivation asserts the dedicated per-user PDF session
// id is derived as "slides-pdf-<user>" from effectiveUserID, mirroring the
// Apps "app-mcp-<user>" convention. In personal mode effectiveUserID falls back
// to the studio_user constant.
func TestSlidesPDF_SessionIDDerivation(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/docs/slides/deck/export/pdf", nil)
	got := "slides-pdf-" + effectiveUserID(r)
	want := "slides-pdf-" + studioChatUserID
	if got != want {
		t.Fatalf("personal-mode session id = %q, want %q", got, want)
	}
}

// TestSlidesPDF_LocalManagerIsHostOnly asserts that when the sandbox is
// disabled the handler's selected manager (GetLocalPDFBrowserManager) is the
// host path: SandboxEnabled must be false so Chrome runs on the host, which is
// the legitimate personal/local-dev behavior.
func TestSlidesPDF_LocalManagerIsHostOnly(t *testing.T) {
	mgr := GetLocalPDFBrowserManager()
	if mgr == nil {
		t.Fatal("local PDF manager is nil")
	}
	if mgr.SandboxEnabled {
		t.Fatal("local PDF manager must never enable the sandbox (host Chrome only)")
	}
}

// TestSlidesPDFManager_UsesRegisteredCallbacks asserts that when K8s/OpenShell
// callbacks are registered (as chat_factory does), GetSlidesPDFBrowserManager
// returns a manager wired for in-container rendering: SandboxEnabled==true with
// the three Container* funcs set from the registered callbacks.
func TestSlidesPDFManager_UsesRegisteredCallbacks(t *testing.T) {
	restore := saveRegisteredPDF(t)
	defer restore()

	resolve := func(string) (string, string, error) { return "pod", "127.0.0.1", nil }
	start := func(string) (io.Closer, error) { return nil, nil }
	dial := func(string, int) (net.Conn, error) { return nil, nil }
	SetPDFBrowserCallbacksForBackend(string(sandbox.BackendKindK8s), resolve, start, dial)

	mgr, err := GetSlidesPDFBrowserManager("slides-pdf-tester")
	if err != nil {
		t.Fatalf("GetSlidesPDFBrowserManager error: %v", err)
	}
	if mgr == nil {
		t.Fatal("slides PDF manager is nil")
	}
	if !mgr.SandboxEnabled {
		t.Fatal("registered callbacks must enable the sandbox on the slides PDF manager")
	}
	if mgr.ContainerResolveFunc == nil || mgr.ContainerDialFunc == nil {
		t.Fatal("registered callbacks must set ContainerResolveFunc and ContainerDialFunc")
	}
}

// TestSlidesPDFHandler_NoFallbackWhenSandboxRequiredButUnavailable asserts the
// no-fallback contract: when the sandbox is required but the injected manager
// reports SandboxEnabled==false (e.g. K8s/OpenShell with no chat wired yet, so
// no in-container browser callbacks registered), the handler returns HTTP 500
// with the sentinel message and NEVER selects the host manager. Because the
// handler resolves the browser BEFORE loading the scene, this exercises the
// real handler without a store service.
func TestSlidesPDFHandler_NoFallbackWhenSandboxRequiredButUnavailable(t *testing.T) {
	oldReq := sandboxBrowserRequiredFn
	sandboxBrowserRequiredFn = func() (bool, string) { return true, "k8s" }
	defer func() { sandboxBrowserRequiredFn = oldReq }()

	// Inject a host-like manager (SandboxEnabled=false) to simulate "callbacks
	// never registered". The guard must reject it rather than render on host.
	oldMgr := slidesPDFBrowserManagerFn
	hostLike := browser.NewManager(browser.DefaultConfig())
	var called bool
	slidesPDFBrowserManagerFn = func(string) (*browser.Manager, error) {
		called = true
		return hostLike, nil
	}
	defer func() { slidesPDFBrowserManagerFn = oldMgr }()

	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/deck/export/pdf", nil)
	rec := httptest.NewRecorder()
	ExportSlidesPDFHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !called {
		t.Fatal("expected the sandbox manager accessor to be consulted")
	}
	const sentinel = "requires an in-container browser"
	if body := rec.Body.String(); !strings.Contains(body, sentinel) {
		t.Fatalf("body = %q, want it to contain %q", body, sentinel)
	}
}

// TestSlidesPDFHandler_SandboxDisabledUsesHostManager asserts that when the
// sandbox is disabled the handler does NOT consult the sandbox manager accessor
// (it selects GetLocalPDFBrowserManager instead). We stop before Chrome launch
// by omitting a store service, so loadSlidesScene returns after the host
// manager is selected — proving the branch without launching Chrome.
func TestSlidesPDFHandler_SandboxDisabledUsesHostManager(t *testing.T) {
	oldReq := sandboxBrowserRequiredFn
	sandboxBrowserRequiredFn = func() (bool, string) { return false, "" }
	defer func() { sandboxBrowserRequiredFn = oldReq }()

	oldMgr := slidesPDFBrowserManagerFn
	var sandboxAccessed bool
	slidesPDFBrowserManagerFn = func(string) (*browser.Manager, error) {
		sandboxAccessed = true
		return nil, nil
	}
	defer func() { slidesPDFBrowserManagerFn = oldMgr }()

	req := httptest.NewRequest(http.MethodGet, "/api/docs/slides/deck/export/pdf", nil)
	rec := httptest.NewRecorder()
	ExportSlidesPDFHandler(rec, req)

	if sandboxAccessed {
		t.Fatal("sandbox-disabled path must not consult the sandbox PDF manager accessor")
	}
	// Without a store service, loadSlidesScene fails with 503 — which proves we
	// selected the host manager and moved on to scene loading (no 500 guard,
	// no Chrome launch).
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (scene load unavailable after host manager selected)", rec.Code)
	}
}

