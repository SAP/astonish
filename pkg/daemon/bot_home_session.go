package daemon

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func prepareBoundBotHomeSession(ctx context.Context, backend sandbox.Backend, binding *sandbox.BotHomeBinding, sessionID string) error {
	if prepared, ok := backend.(sandbox.BotHomeSessionPreparer); ok {
		return prepared.PrepareBotHomeSession(ctx, binding, sessionID)
	}
	binder, ok := backend.(sandbox.BotHomeSessionBinder)
	if !ok {
		return nil
	}
	if err := binder.BindBotHomeSession(binding, sessionID); err != nil {
		return err
	}
	if err := backend.StartSession(ctx, sessionID); err != nil {
		return err
	}
	return backend.WaitForSessionReady(ctx, sessionID)
}

// prepareExistingBotHomeSession is a trusted lifecycle boundary, never a codec callback.
func (w *BotWorker) prepareExistingBotHomeSession(ctx context.Context, repo store.BotStore, id store.BotID) error {
	if _, ok := w.config.Sandbox.(sandbox.BotHomeSessionBinder); !ok {
		return nil
	}
	// Get reads control metadata only. Recover the authorized recipe before
	// run/version hydration can open retained content inside a transaction.
	b, err := repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if b == nil || b.Home.ID == uuid.Nil {
		return nil
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Subject != b.OwnerID.String() {
		return store.ErrBotForbidden
	}
	owner, _, err := w.authenticateOwner(ctx, BotSchedulerScope{p.OrgSlug, p.TeamSlug, b.OwnerID.String()})
	if err != nil {
		return err
	}
	binding := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: b.OwnerID.String(), BotID: b.ID.String()}, Backend: sandbox.BackendKind(b.Home.Backend), Ref: b.Home.StorageKey}
	return prepareBoundBotHomeSession(owner, w.config.Sandbox, binding, b.HomeSessionID.String())
}

func prepareBotHomeSession(ctx context.Context, backend sandbox.Backend, pool sandbox.ToolNodePool, b store.Bot, binding *sandbox.BotHomeBinding, template string) error {
	binder, ok := backend.(sandbox.BotHomeSessionBinder)
	if !ok {
		return nil
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || b.HomeSessionID == uuid.Nil || pool == nil || binding == nil || binding.Scope.UserID != b.OwnerID.String() || p.Subject != b.OwnerID.String() || p.OrgSlug != binding.Scope.OrgID || p.TeamSlug != binding.Scope.TeamID {
		return store.ErrBotForbidden
	}
	id := b.HomeSessionID.String()
	if err := binder.BindBotHomeSession(binding, id); err != nil {
		return err
	}
	pool.SetSessionScope(id, p.OrgSlug, p.TeamSlug)
	if err := sandbox.BindBotHome(pool, id, binding); err != nil {
		return err
	}
	return sandbox.EnsureToolClientReady(ctx, pool.GetOrCreateWithTemplate(id, template), id)
}
