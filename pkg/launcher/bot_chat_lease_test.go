package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotFactoryBindingUsesRenewedLease(t *testing.T) {
	f := newBotFactoryFixture(t)
	// Capture the initial request, then renew only the authoritative store.
	initial := f.req
	initial.Run.Lease.ExpiresAt = time.Now().Add(-time.Second)
	f.req.Run.Lease.ExpiresAt = time.Now().Add(time.Minute)
	owner := store.WithServices(f.ctx, f.services)
	checks := 0
	b := &botFactoryBinding{owner: owner, req: initial, verifyIsolation: func(context.Context) error { checks++; return nil }}
	if err := b.check(f.ctx); err != nil {
		t.Fatalf("renewed lease rejected after initial TTL: %v", err)
	}
	if checks != 1 {
		t.Fatal("isolation check skipped")
	}
	f.req.Run.Lease.Fence++
	if err := b.check(f.ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale fence accepted: %v", err)
	}
	f.req.Run.Lease.Fence--
	f.req.Run.Lease.ExpiresAt = time.Now().Add(-time.Second)
	if err := b.check(f.ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("expired authoritative lease accepted: %v", err)
	}
	f.req.Run.Lease.ExpiresAt = time.Now().Add(time.Minute)
	f.req.Bot.Status = store.BotPaused
	if err := b.check(f.ctx); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("paused bot accepted: %v", err)
	}
	b.owner = f.ctx
	if err := b.check(f.ctx); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("missing authority accepted: %v", err)
	}
}
