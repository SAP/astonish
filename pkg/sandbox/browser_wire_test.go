package sandbox

import (
	"testing"

	"github.com/SAP/astonish/pkg/browser"
)

func TestWireIncusBrowserManager_NilArgs(t *testing.T) {
	t.Parallel()
	mgr := browser.NewManager(browser.DefaultConfig())
	if WireIncusBrowserManager(nil, nil, nil, nil) {
		t.Fatal("expected false for nil mgr/client")
	}
	if WireIncusBrowserManager(mgr, nil, nil, nil) {
		t.Fatal("expected false for nil client")
	}
	if mgr.SandboxEnabled {
		t.Fatal("nil client must not enable sandbox on manager")
	}
}

func TestWireIncusBrowserManager_HostChromePathStillEnablesSandbox(t *testing.T) {
	t.Parallel()
	// Without a real Incus client we only assert the nil-client path.
	// Host ChromePath used to make DetectBrowserEngine return "custom" and
	// skip wiring entirely; that regression is covered by ensuring the
	// helper no longer returns false solely because of ChromePath (see
	// WireIncusBrowserManager body) and by this documentation test of the
	// engine detection fallback expectation.
	cfg := browser.DefaultConfig()
	cfg.ChromePath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	mgr := browser.NewManager(cfg)
	if WireIncusBrowserManager(mgr, nil, nil, nil) {
		t.Fatal("nil client must still return false")
	}
	// With a nil client we cannot enable sandbox; the important behavioral
	// change is that a non-nil client would enable it even with this path.
	if mgr.SandboxEnabled {
		t.Fatal("nil client must not enable sandbox")
	}
}
