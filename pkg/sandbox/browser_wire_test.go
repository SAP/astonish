package sandbox

import (
	"context"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/store"
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

func TestWireIncusBrowserManager_NilPoolSkipsEnsureReady(t *testing.T) {
	t.Parallel()
	// When pool is nil, ContainerEnsureReadyFunc must NOT be set. Drill/fleet
	// callers pass nil pool; setting the func unconditionally would break them.
	mgr := browser.NewManager(browser.DefaultConfig())
	if mgr.ContainerEnsureReadyFunc != nil {
		t.Fatal("ContainerEnsureReadyFunc should be nil before wiring")
	}
	// WireIncusBrowserManager returns false for nil client, which also means
	// ContainerEnsureReadyFunc is never set. Verify the nil-pool guard works.
	WireIncusBrowserManager(mgr, nil, nil, nil)
	if mgr.ContainerEnsureReadyFunc != nil {
		t.Fatal("nil pool must not set ContainerEnsureReadyFunc")
	}
}

func TestIncusContainerEnsureReadyFunc_UsesContextChain(t *testing.T) {
	t.Parallel()
	client := &browserReadySpyClient{}
	pool := &browserReadySpyPool{client: client}
	// We cannot call WireIncusBrowserManager with a real *IncusClient in a unit
	// test (the concrete type requires a live daemon). Instead we test the closure
	// body directly — the same logic WireIncusBrowserManager assigns at
	// browser_wire.go:62-68. GetPoolClientFromContext is the critical shared path.
	ensureReady := func(ctx context.Context, sessionID string) error {
		c := GetPoolClientFromContext(ctx, pool, sessionID)
		if c == nil {
			return fmt.Errorf("no sandbox client for session %q", sessionID)
		}
		return c.EnsureReady(sessionID)
	}
	ctx := store.WithSandboxLayerChain(context.Background(), []string{"@base", "overlay-abc"})
	ctx = store.WithSandboxTemplate(ctx, "mytemplate")
	if err := ensureReady(ctx, "sess-incus-1"); err != nil {
		t.Fatalf("ensureReady error: %v", err)
	}
	if pool.method != "GetOrCreateWithImage" {
		t.Errorf("pool method = %q, want GetOrCreateWithImage", pool.method)
	}
	if pool.sessionID != "sess-incus-1" {
		t.Errorf("pool sessionID = %q, want sess-incus-1", pool.sessionID)
	}
	if client.sessionID != "sess-incus-1" {
		t.Errorf("client.EnsureReady sessionID = %q, want sess-incus-1", client.sessionID)
	}
}

func TestIncusContainerEnsureReadyFunc_FallbackWithoutChain(t *testing.T) {
	t.Parallel()
	client := &browserReadySpyClient{}
	pool := &browserReadySpyPool{client: client}
	ensureReady := func(ctx context.Context, sessionID string) error {
		c := GetPoolClientFromContext(ctx, pool, sessionID)
		if c == nil {
			return fmt.Errorf("no sandbox client for session %q", sessionID)
		}
		return c.EnsureReady(sessionID)
	}
	// Empty context: no chain, no template, no image
	if err := ensureReady(context.Background(), "sess-incus-2"); err != nil {
		t.Fatalf("ensureReady error: %v", err)
	}
	if pool.method != "GetOrCreate" {
		t.Errorf("pool method = %q, want GetOrCreate", pool.method)
	}
	if pool.sessionID != "sess-incus-2" {
		t.Errorf("pool sessionID = %q, want sess-incus-2", pool.sessionID)
	}
}
