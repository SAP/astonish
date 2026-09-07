package sandbox

import (
	"context"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestContainerEnsureReadyFunc_UsesContextChain(t *testing.T) {
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
