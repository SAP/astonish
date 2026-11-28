package daemon

import (
	"context"
	"errors"
	"strconv"
	"strings"

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
	if _, ok := w.config.Sandbox.(sandbox.BotHomeFilesBackend); !ok {
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
	// Prepare outside SQL before registration selects file-backed content.
	if _, ok := w.config.Sandbox.(sandbox.BotHomeSessionBinder); ok {
		version, err := repo.GetVersion(ownerCtx, id, b.Version)
		if err != nil {
			return nil, err
		}
		if version == nil {
			return nil, store.ErrBotNotFound
		}
		if err := prepareBotHomeSession(ownerCtx, w.config.Sandbox, w.config.Pool, *b, actual, version.SandboxTemplate); err != nil {
			return nil, err
		}
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
	internal = entstore.WithBotContentReadOperationFromContext(internal, ctx)
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
	if installed, ok := w.config.Backend.(interface{ BotContentHomeInstalled() bool }); ok && installed.BotContentHomeInstalled() {
		if err := entstore.SelectBotHomeConfiguration(ownerCtx, repo, *actual, w.config.Sandbox.(sandbox.BotHomeFilesBackend)); err != nil {
			return nil, err
		}
	}
	current, err := repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, store.ErrBotNotFound
	}
	version, err := repo.GetVersion(ownerCtx, id, current.Version)
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, store.ErrBotNotFound
	}
	if err := initializeBotHomeDocuments(ownerCtx, w.config.Sandbox.(sandbox.BotHomeFilesBackend), actual, *version, func(commit func() error) error {
		live, err := entstore.AuthorizeBotHomeSetup(ctx, repo, id, current.Revision)
		if err != nil {
			return err
		}
		if live.Home != current.Home || live.Version != current.Version || live.Revision != current.Revision {
			return store.ErrBotConflict
		}
		if err := authorization.Validate(ctx, *live); err != nil {
			return err
		}
		return commit()
	}); err != nil {
		return nil, err
	}
	return current, nil
}

// initializeBotHomeDocuments runs only during authorized setup. Retrying never
// replaces an existing owner's file, even when reviewed configuration changed.
// File contents remain separate from the protected version/policy authority.
func initializeBotHomeDocuments(ctx context.Context, files sandbox.BotHomeFilesBackend, home *sandbox.BotHomeBinding, version store.BotVersion, authorize func(func() error) error) error {
	if files == nil || home == nil || authorize == nil {
		return store.ErrBotUnsupported
	}
	documents := []struct{ name, content string }{
		{"SOUL.md", version.Persona.Soul},
		{"IDENTITY.md", version.Persona.Identity},
		{"USER.md", version.Persona.User},
		{"CHARTER.md", version.Charter},
		{"MEMORY.md", "# Memory\n\n## Current goals\n\n## Knowledge\n\n## Completed work\n\n## Unfinished work\n"},
	}
	for _, doc := range documents {
		entry, err := files.StatBotHomeFile(ctx, home, doc.name)
		if err == nil {
			if entry == nil || entry.Kind != store.BotFileRegular {
				return store.ErrBotConflict
			}
			continue
		}
		if !errors.Is(err, store.ErrBotNotFound) {
			return err
		}
		_, err = files.WriteBotHomeFile(ctx, home, store.BotFileWrite{Path: doc.name, Body: strings.NewReader(doc.content), SizeBytes: int64(len(doc.content)), CreateOnly: true, BeforeCommit: authorize})
		if err != nil {
			return err
		}
	}
	return nil
}
