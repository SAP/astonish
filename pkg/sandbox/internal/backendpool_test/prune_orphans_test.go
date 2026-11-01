// Package backendpool_test — unit tests for PruneOrphansForBackend using the
// mock backend. Lives here to avoid the mock-import cycle.

package backendpool_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
)

func TestPruneOrphansForBackend_RequiresAuthoritativeSnapshot(t *testing.T) {
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalSessionStore: %v", err)
	}
	registry := sandbox.NewSessionRegistryFromStore(st)
	if err := registry.Put("must-survive", "pod-survive", "@base"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	mb := mock.New()
	_, _ = mb.CreateSession(context.Background(), sandbox.SessionSpec{SessionID: "must-survive"})

	pruned, err := sandbox.PruneOrphansForBackend(context.Background(), mb, registry, nil)
	if !errors.Is(err, sandbox.ErrOrphanPruneAuthorityUnavailable) {
		t.Fatalf("PruneOrphansForBackend() error = %v, want %v", err, sandbox.ErrOrphanPruneAuthorityUnavailable)
	}
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0", pruned)
	}
	if calls := mb.DestroySessionCalls(); len(calls) != 0 {
		t.Fatalf("DestroySession calls = %v, want none", calls)
	}
}

// TestPruneOrphansForBackend_DeletesUnreferenced creates three mock sessions,
// marks only one as alive, and verifies that the other two (non-pinned) are
// destroyed. The pinned session is also absent from the alive set but must
// survive because of its pinned flag.
func TestPruneOrphansForBackend_DeletesUnreferenced(t *testing.T) {
	// Set up store-backed registry in a temp dir.
	dir := t.TempDir()
	st, err := sandbox.NewLocalSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSandboxSessionStore: %v", err)
	}
	registry := sandbox.NewSessionRegistryFromStore(st)

	// Register three sessions in the registry.
	if err := registry.Put("alive-sess", "pod-alive", "@base"); err != nil {
		t.Fatalf("Put alive: %v", err)
	}
	if err := registry.Put("orphan-sess", "pod-orphan", "@base"); err != nil {
		t.Fatalf("Put orphan: %v", err)
	}
	if err := registry.Put("pinned-sess", "pod-pinned", "@base"); err != nil {
		t.Fatalf("Put pinned: %v", err)
	}
	// Mark one as pinned.
	if err := registry.SetPinned("pod-pinned", true); err != nil {
		t.Fatalf("SetPinned: %v", err)
	}

	// Build mock backend.
	mb := mock.New()

	// Seed mock sessions so ListSessions returns them (secondary pass coverage).
	ctx := context.Background()
	mb.CreateSession(ctx, sandbox.SessionSpec{SessionID: "alive-sess"})
	mb.CreateSession(ctx, sandbox.SessionSpec{SessionID: "orphan-sess"})
	mb.CreateSession(ctx, sandbox.SessionSpec{SessionID: "pinned-sess"})

	// The alive set: only alive-sess.
	aliveIDs := map[string]bool{"alive-sess": true}

	pruned, err := sandbox.PruneOrphansForBackend(ctx, mb, registry, aliveIDs)
	if err != nil {
		t.Fatalf("PruneOrphansForBackend: %v", err)
	}

	// Expect exactly 1 pruned (orphan-sess). Pinned survives even though absent.
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	// Verify DestroySession was called for orphan-sess.
	calls := mb.DestroySessionCalls()
	found := false
	for _, c := range calls {
		if c == "orphan-sess" {
			found = true
		}
		if c == "pinned-sess" {
			t.Error("DestroySession was called for pinned-sess, should be exempt")
		}
	}
	if !found {
		t.Errorf("DestroySession was not called for orphan-sess; calls = %v", calls)
	}
}

// TestPruneOrphansForBackend_UnregisteredOldSession tests the secondary pass:
// a session exists in the backend (ListSessions) but NOT in the registry.
// It should be pruned if older than 1 hour.
func TestPruneOrphansForBackend_UnregisteredOldSession(t *testing.T) {
	dir := t.TempDir()
	st, err := sandbox.NewLocalSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSandboxSessionStore: %v", err)
	}
	registry := sandbox.NewSessionRegistryFromStore(st)

	// Empty registry — no sessions registered.
	mb := mock.New()
	ctx := context.Background()

	// Create a session in the mock that's "old" (created > 1h ago).
	// We'll use the mock's CreateSession which sets CreatedAt = now.
	// To test the age check, we need to manipulate the session's CreatedAt.
	// The mock stores sessions internally; let's use CreateSession then
	// modify the returned session's CreatedAt via the mock's SetSessionCreatedAt.
	mb.CreateSession(ctx, sandbox.SessionSpec{SessionID: "unregistered-old"})
	mb.SetSessionCreatedAt("unregistered-old", time.Now().Add(-2*time.Hour))

	// Create a recent session that should NOT be pruned (< 1h).
	mb.CreateSession(ctx, sandbox.SessionSpec{SessionID: "unregistered-new"})
	// (CreatedAt is now, which is < 1h)

	aliveIDs := map[string]bool{} // no alive sessions in chat store

	pruned, err := sandbox.PruneOrphansForBackend(ctx, mb, registry, aliveIDs)
	if err != nil {
		t.Fatalf("PruneOrphansForBackend: %v", err)
	}

	// Only unregistered-old should be pruned (older than 1h).
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}

	calls := mb.DestroySessionCalls()
	found := false
	for _, c := range calls {
		if c == "unregistered-old" {
			found = true
		}
		if c == "unregistered-new" {
			t.Error("DestroySession was called for unregistered-new (too recent)")
		}
	}
	if !found {
		t.Errorf("DestroySession not called for unregistered-old; calls = %v", calls)
	}
}

// TestPruneOrphansForBackend_CancelledContext stops early.
func TestPruneOrphansForBackend_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	st, err := sandbox.NewLocalSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSandboxSessionStore: %v", err)
	}
	registry := sandbox.NewSessionRegistryFromStore(st)
	registry.Put("orphan-1", "pod-1", "@base")
	registry.Put("orphan-2", "pod-2", "@base")

	mb := mock.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err = sandbox.PruneOrphansForBackend(ctx, mb, registry, map[string]bool{})
	if err == nil {
		t.Fatal("expected context error")
	}
}
