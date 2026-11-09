package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// botRetentionManager installs only a service-authorized, durable reservation.
// It never captures an owner context and promotes it inside the raw callback.
func (w *BotWorker) botRetentionManager(repo store.BotStore, b store.Bot, claim *store.BotHomeCleanupClaim) *launcher.BotHomeManager {
	backend, ok := w.config.Sandbox.(sandbox.BotHomeBackend)
	if !ok {
		return nil
	}
	if _, ok := w.config.Sandbox.(sandbox.BotHomeDeleteBackend); !ok {
		return nil
	}
	reservations, ok := repo.(store.BotHomeCleanupStore)
	if !ok {
		return nil
	}
	return &launcher.BotHomeManager{Backend: backend, Pool: w.config.Pool, Bots: repo, AuthorizeDelete: func(ctx context.Context, home store.BotHomeBinding) (*sandbox.BotHomeBinding, error) {
		// Recheck the caller even when a custom store implements the optional seam.
		p, ok := execution.PrincipalFromContext(ctx)
		if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindService || p.Subject != "" || p.Surface != execution.SurfaceCode || !p.HasScope(store.BotCleanupScope) {
			return nil, store.ErrBotForbidden
		}
		if home != b.Home {
			return nil, store.ErrBotConflict
		}
		reserved, err := reservations.ReserveHomeCleanup(ctx, home, b.Revision)
		if err != nil {
			return nil, err
		}
		if reserved == nil || reserved.Request.Home != home || reserved.Request.BotID != b.ID || reserved.Request.ExpectedRevision != b.Revision || reserved.OwnerID != b.OwnerID.String() || reserved.OrgSlug != p.OrgSlug || reserved.TeamSlug != p.TeamSlug {
			return nil, store.ErrBotConflict
		}
		*claim = *reserved // Value copy: post-delete receipt never rereads mutable Bot state.
		return &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: reserved.OrgSlug, TeamID: reserved.TeamSlug, UserID: reserved.OwnerID, BotID: b.ID.String()}, Backend: sandbox.BackendKind(home.Backend), Ref: home.StorageKey}, nil
	}}
}

func (w *BotWorker) reconcileRetention(ctx context.Context, repo store.BotStore, b store.Bot) error {
	if b.Status != store.BotDeleted || b.RestoreUntil == nil || b.RestoreUntil.After(time.Now()) {
		return nil
	}
	// Resume verified purge (including drafts interrupted before the HTTP purge)
	// without requiring another request or repeating physical deletion.
	if b.PermanentDelete {
		if exact, ok := repo.(store.BotPermanentDeleteStore); ok {
			err := exact.PurgeDeleted(ctx, b.ID, b.Revision)
			if err == nil || errors.Is(err, store.ErrBotNotFound) {
				return nil
			}
			if !errors.Is(err, store.ErrBotConflict) {
				return err
			}
		}
	}
	cleanup, ok := repo.(store.BotCleanupStore)
	if !ok {
		return nil
	}
	var claim store.BotHomeCleanupClaim
	homes := w.botRetentionManager(repo, b, &claim)
	if homes == nil {
		return nil
	} // Unsupported backends retain, never use the host.
	owner, ok := execution.PrincipalFromContext(ctx)
	if !ok || owner.Validate() != nil || owner.Kind != execution.PrincipalKindUser || owner.Subject != b.OwnerID.String() {
		return store.ErrBotForbidden
	}
	runtime, err := execution.WithPrincipal(botWorkerContext{ctx}, execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, ClientID: w.config.WorkerID, OrgSlug: owner.OrgSlug, TeamSlug: owner.TeamSlug, Scopes: []string{store.BotCleanupScope}, Authenticated: true})
	if err != nil {
		return err
	}
	if err := homes.Delete(runtime, b.Home); err != nil {
		return err
	}
	receipt := claim.Request
	verified := store.WithBotCleanupVerifier(runtime, func(_ context.Context, q store.BotCleanupRequest) error {
		if q != receipt {
			return store.ErrBotConflict
		}
		return nil
	})
	if err := cleanup.ConfirmCleanup(verified, receipt); err != nil {
		return err
	}
	// The durable intent is consumed by the worker, not by an open browser.
	if exact, ok := repo.(store.BotPermanentDeleteStore); ok {
		return exact.PurgeDeleted(ctx, b.ID, b.Revision)
	}
	// Compatibility for older store implementations.
	_, err = repo.PurgeDue(ctx, time.Now(), 200)
	return err
}
