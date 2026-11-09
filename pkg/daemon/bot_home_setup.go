package daemon

import (
	"context"
	"strconv"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// provisionHome serves explicit setup and admitted interactive preflight through
// the same trusted registration path; it never activates or claims execution.
func (w *BotWorker) provisionHome(ctx context.Context, repo store.BotStore, id store.BotID, revision int64) (*store.Bot, error) {
	if w == nil || w.activationStopped.Load() {
		return nil, store.ErrBotUnsupported
	}
	if auth, ok := execution.BotTurnAuthorizerFromContext(ctx); ok {
		if err := auth.Authorize(ctx, execution.CapabilityChat); err != nil {
			return nil, err
		}
	}
	b, err := entstore.AuthorizeBotHomeSetup(ctx, repo, id, revision)
	if err != nil {
		return nil, err
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok {
		return nil, store.ErrBotForbidden
	}
	ownerCtx, _, err := w.authenticateOwner(ctx, BotSchedulerScope{p.OrgSlug, p.TeamSlug, b.OwnerID.String()})
	if err != nil {
		return nil, err
	}
	backend, ok := w.config.Sandbox.(sandbox.BotHomeBackend)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	scope := sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: b.OwnerID.String(), BotID: id.String()}
	if err := entstore.BeginBotHomeProvisioning(ctx, repo, id, revision); err != nil {
		return nil, err
	}
	homes := launcher.BotHomeManager{Backend: backend, Pool: w.config.Pool, Bots: repo}
	actual, err := homes.Provision(ownerCtx, *b)
	if err != nil {
		return nil, err
	}
	if actual == nil || actual.Validate() != nil || actual.Scope != scope {
		return nil, store.ErrBotConflict
	}
	// Identity and membership are live state, not authority carried by the
	// captured request or the internal registry principal. Check both people.
	authorization := entstore.BotHomeRegistrationAuthorization{RequestContext: ctx, Validate: func(c context.Context, current store.Bot) error {
		if err := c.Err(); err != nil {
			return err
		}
		if auth, ok := execution.BotTurnAuthorizerFromContext(c); ok {
			if err := auth.Authorize(c, execution.CapabilityChat); err != nil {
				return err
			}
		}
		if current.ID != b.ID || current.OwnerID != b.OwnerID || current.Scope != b.Scope {
			return store.ErrBotConflict
		}
		for _, subject := range []string{p.Subject, current.OwnerID.String()} {
			if _, _, err := w.authenticateOwner(c, BotSchedulerScope{p.OrgSlug, p.TeamSlug, subject}); err != nil {
				return store.ErrBotForbidden
			}
		}
		return nil
	}}
	if err := authorization.Validate(ctx, *b); err != nil {
		return nil, err
	}
	// Deterministic server identity makes concurrent and lost-response retries
	// converge on the same retained home. No caller-supplied mount path is accepted.
	home := store.BotHomeBinding{ID: uuid.NewSHA1(id, []byte("retained-home-v1:"+strconv.FormatInt(revision, 10))), BotID: id, Scope: b.Scope, Backend: string(actual.Backend), StorageKey: actual.Ref, Revision: 1}
	if b.Home.ID != uuid.Nil {
		if b.Revision == revision {
			home.ID = b.Home.ID
		}
		if b.Home != home {
			return nil, store.ErrBotConflict
		}
		// A retry with the current revision is also a no-op.
		if b.Revision == revision {
			current, err := entstore.AuthorizeBotHomeSetup(ctx, repo, id, revision)
			if err != nil {
				return nil, err
			}
			if current.Revision != revision || current.Home != home {
				return nil, store.ErrBotConflict
			}
			// Use the original registration revision so even this no-op traverses
			// transactional caller/owner authorization below.
			revision--
		}
	}
	// Revalidate live caller authorization after slow external provisioning.
	if _, err = entstore.AuthorizeBotHomeSetup(ctx, repo, id, revision); err != nil {
		return nil, err
	}
	internal, err := execution.WithPrincipal(botWorkerContext{ctx}, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "bot-home-setup", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		return nil, err
	}
	q := entstore.BotHomeRegistration{BotID: id, ExpectedRevision: revision, Home: home}
	err = entstore.RegisterBotHomeBinding(internal, repo, q, func(_ context.Context, got entstore.BotHomeRegistration) error {
		if got != q || actual.Scope != scope || actual.Ref != got.Home.StorageKey || string(actual.Backend) != got.Home.Backend {
			return store.ErrBotConflict
		}
		return nil
	}, authorization)
	// Never delete on CAS failure: another request may already be using this exact
	// deterministic resource. Retain it for safe retry/administrative reconciliation.
	if err != nil {
		return nil, err
	}
	return repo.Get(ctx, id)
}
