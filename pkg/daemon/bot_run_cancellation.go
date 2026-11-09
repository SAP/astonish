package daemon

import (
	"context"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// botSessionDestructionVerifier must inspect physical execution, not the session
// registry: DestroySession may only have requested asynchronous deletion.
type botSessionDestructionVerifier interface {
	VerifySessionDestroyed(context.Context, string) error
}

// confirmCancelledRun verifies an exact interrupted writer after Execute joins,
// or after restart when its former lease was revoked/expired. It only observes
// physical destruction; it never stops a possibly current worker or treats
// cancellation, lease expiry, or a missing registry row as a teardown receipt.
func (w *BotWorker) confirmCancelledRun(ctx context.Context, repo store.BotStore, id store.BotID, rid store.RunID) error {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	run, err := repo.GetRun(c, id, rid)
	if err != nil {
		return err
	}
	// ConfirmCleanup rechecks the exact active-run/fence binding before and
	// after verification. Never adopt a still-leased foreign worker.
	if run.Status != store.RunInterrupted || (run.Lease.WorkerID != w.config.WorkerID && !run.Lease.ExpiresAt.IsZero() && time.Now().Before(run.Lease.ExpiresAt)) {
		return nil
	}
	if run.BotID != id || run.ID != rid || run.SessionID == (store.RunID{}) || run.Lease.Fence < 1 {
		return store.ErrBotConflict
	}
	verify, ok := w.config.Sandbox.(botSessionDestructionVerifier)
	if !ok {
		return store.ErrBotUnsupported
	}
	cleanup, ok := repo.(store.BotCleanupStore)
	if !ok {
		return store.ErrBotUnsupported
	}
	b, err := repo.Get(c, id)
	if err != nil {
		return err
	}
	p, ok := execution.PrincipalFromContext(c)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || p.Subject != b.OwnerID.String() {
		return store.ErrBotForbidden
	}
	internal, err := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, ClientID: w.config.WorkerID, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Scopes: []string{store.BotCleanupScope}, Authenticated: true})
	if err != nil {
		return err
	}
	q := store.BotCleanupRequest{Kind: store.BotCleanupRunTeardown, BotID: id, RunID: rid, RunFence: run.Lease.Fence, Fence: run.Lease.Fence + 1, ExpectedRevision: b.Revision, Home: b.Home}
	internal = store.WithBotCleanupVerifier(internal, func(c context.Context, got store.BotCleanupRequest) error {
		if got != q {
			return store.ErrBotForbidden
		}
		return verify.VerifySessionDestroyed(c, run.SessionID.String())
	})
	return cleanup.ConfirmCleanup(internal, q)
}
