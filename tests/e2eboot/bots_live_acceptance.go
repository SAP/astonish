//go:build e2e

package e2eboot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// LiveBotAcceptance composes the existing HTTP bootstrap with a real worker.
// Bootstrap itself starts Studio only; the daemon production constructor is
// private. This deliberately narrow toolless composition does not claim parity
// with the daemon's full tool catalogue, scheduler, or channel delivery.
type LiveBotAcceptance struct {
	Harness         *Harness
	Client          *Client
	Context         context.Context
	Principal       execution.Principal
	Bots            store.BotStore
	Homes           *launcher.BotHomeManager
	Backend         sandbox.Backend
	Worker          *daemon.BotWorker
	Provider, Model string
	pin             func(context.Context, string, string, string) error
}

func BootstrapLiveBotAcceptance(t *testing.T) *LiveBotAcceptance {
	t.Helper()
	h, open := BootstrapRetainedHomeKubernetes(t) // strict preflight, never skip
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	t.Cleanup(cancel)
	seed := Seed(t, h)
	user := seed.Users[UserAliceEmail]
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: user.ID, OrgSlug: user.OrgSlug, TeamSlug: user.TeamSlug, Authenticated: true}
	owner, err := execution.WithPrincipal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	org, err := h.Store.ForOrg(p.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	b, registry := open()
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, registry)
	r := &LiveBotAcceptance{Harness: h, Client: seed.Client(UserAliceEmail), Context: owner, Principal: p, Bots: org.ForUser(p.Subject).Bots(), Backend: b, pin: org.ForUser(p.Subject).SetSessionPin}
	r.Homes = &launcher.BotHomeManager{Backend: b.(sandbox.BotHomeBackend), Pool: pool, Bots: r.Bots}
	settings, err := h.Store.PlatformSettings().Get(ctx)
	if err != nil || settings == nil {
		t.Fatalf("platform provider settings: %v", err)
	}
	r.Provider, r.Model = settings.DefaultProvider, settings.DefaultModel
	// Only use this isolated bootstrap's configured real provider. Never substitute
	// a fake model if provider construction, credentials, or generation fails.
	local := &config.AppConfig{Providers: map[string]config.ProviderConfig{}}
	for name, values := range settings.Providers {
		copy := config.ProviderConfig{}
		for key, value := range values {
			copy[key] = value
		}
		local.Providers[name] = copy
	}
	w, err := daemon.NewBotWorker(daemon.BotWorkerConfig{
		Backend: h.Store, Sandbox: b, Pool: pool, WorkerID: "live-lifecycle-" + uuid.NewString(), PollInterval: 200 * time.Millisecond,
		ContextWindow: 32768, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 1024, SupportsOutputLimit: true},
		ResolveModel: func(c context.Context, got execution.Principal, scope store.BotScope, name, modelName string) (model.LLM, error) {
			if got.Subject != p.Subject || got.OrgSlug != p.OrgSlug || got.TeamSlug != p.TeamSlug || scope != store.BotScopePersonal || name != r.Provider || modelName != r.Model {
				return nil, store.ErrBotForbidden
			}
			return provider.GetProvider(c, name, modelName, local)
		},
		Prepare: func(c context.Context, q bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
			id := q.Run.SessionID.String()
			pool.SetSessionScope(id, q.Principal.OrgSlug, q.Principal.TeamSlug)
			client := pool.GetOrCreateWithTemplate(id, q.Version.SandboxTemplate)
			guard := liveBotReadOnlyGuard{repo: scope.Services.PersonalBots, request: q}
			resources := &launcher.BotChatSafeResources{WorkspaceDir: "/root", PolicyRuntime: agent.BotPolicyRuntime{Guard: guard, Classifier: launcher.BotCatalogueClassifier{}}, Cleanup: func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(c), time.Minute)
				defer stop()
				if err := destroyLiveBotSession(cleanup, b, id); err != nil {
					t.Errorf("physical Bot session teardown %s: %v", id, err)
					return
				}
				pool.Remove(id)
			}}
			if err := sandbox.EnsureToolClientReady(c, client, id); err != nil {
				return resources, err
			}
			resources.VerifyIsolation = func(check context.Context) error {
				if err := guard.check(check); err != nil {
					return err
				}
				state, err := b.SessionState(check, id)
				if err != nil {
					return err
				}
				if state != sandbox.SessionStateRunning {
					return store.ErrBotUnsupported
				}
				return nil
			}
			if err := resources.VerifyIsolation(c); err != nil {
				return resources, err
			}
			// A real retained-home fixture, not a fabricated model tool outcome. The
			// report is generated by the real provider; artifact bytes are checked offline.
			result, err := b.Exec(c, id, sandbox.ExecSpec{Command: []string{"/bin/sh", "-c", "test \"$HOME\" = /root && mountpoint -q /root && printf live-lifecycle-artifact > /root/lifecycle.txt"}})
			if err != nil || result == nil || result.ExitCode != 0 {
				return resources, fmt.Errorf("retained-home fixture: result=%+v err=%v", result, err)
			}
			return resources, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Worker = w
	// Worker context intentionally carries no HTTP viewer or ambient owner values.
	w.Start(ctx)
	t.Cleanup(w.Stop)
	return r
}

func destroyLiveBotSession(ctx context.Context, b sandbox.Backend, id string) error {
	for {
		err := b.DestroySession(ctx, id)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ctx.Err(), err)
		case <-time.After(time.Second):
		}
	}
}

// RegisterHome is the trusted administrative operation missing from public draft
// creation. Provision first, attest exactly that result, then recover it through
// the same scoped manager used by worker activation and run binding.
func (r *LiveBotAcceptance) RegisterHome(t *testing.T, draft store.Bot) store.Bot {
	t.Helper()
	scope := sandbox.BotHomeScope{OrgID: r.Principal.OrgSlug, TeamID: r.Principal.TeamSlug, UserID: r.Principal.Subject, BotID: draft.ID.String()}
	home, err := r.Homes.Backend.EnsureBotHome(r.Context, scope)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := execution.WithPrincipal(r.Context, execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceCode, ClientID: "live-lifecycle-home-registry", OrgSlug: r.Principal.OrgSlug, TeamSlug: r.Principal.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	q := entstore.BotHomeRegistration{BotID: draft.ID, ExpectedRevision: draft.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: draft.ID, Scope: draft.Scope, Backend: string(home.Backend), StorageKey: home.Ref, Revision: 1}}
	err = entstore.RegisterBotHomeBinding(internal, r.Bots, q, func(_ context.Context, got entstore.BotHomeRegistration) error {
		if got != q || home.Validate() != nil || home.Scope != scope || got.Home.StorageKey != home.Ref || got.Home.Backend != string(home.Backend) {
			return store.ErrBotConflict
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := r.Homes.Ensure(r.Context, bot.HomeRequest{BotID: draft.ID, Scope: draft.Scope})
	if err != nil || actual != q.Home {
		t.Fatalf("scoped registered home: %+v %v", actual, err)
	}
	current, err := r.Bots.Get(r.Context, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.Worker.Stop()
		cleanup, stop := context.WithTimeout(context.WithoutCancel(r.Context), time.Minute)
		defer stop()
		// Backend refuses attached homes; never force-delete PVCs/fences on failure.
		if err := r.Backend.(sandbox.BotHomeDeleteBackend).DeleteBotHome(cleanup, home); err != nil {
			t.Errorf("safe retained-home cleanup: %v", err)
		}
	})
	return *current
}

func (r *LiveBotAcceptance) PinModel(t *testing.T, b store.Bot) {
	t.Helper()
	if err := r.pin(r.Context, b.HomeSessionID.String(), r.Provider, r.Model); err != nil {
		t.Fatal(err)
	}
}

// No executable tools are exposed in this first lifecycle scenario. Recheck the
// real durable lease and budget, and deny rather than fabricate action authority.
type liveBotReadOnlyGuard struct {
	repo    store.BotStore
	request bot.RunRequest
}

func (g liveBotReadOnlyGuard) check(ctx context.Context) error {
	if err := g.request.Budget.Check(ctx); err != nil {
		return err
	}
	b, err := g.repo.Get(ctx, g.request.Bot.ID)
	if err != nil {
		return err
	}
	if b == nil || b.Status != store.BotActive || b.Version != g.request.Version.Version || b.OwnerID != g.request.Bot.OwnerID {
		return store.ErrBotForbidden
	}
	run, err := g.repo.GetRun(ctx, b.ID, g.request.Run.ID)
	if err != nil {
		return err
	}
	lease := g.request.Run.Lease
	if run == nil || run.Status != store.RunRunning || run.SessionID != g.request.Run.SessionID || run.Lease.Fence != lease.Fence || run.Lease.WorkerID != lease.WorkerID || !time.Now().Before(run.Lease.ExpiresAt) {
		return store.ErrBotConflict
	}
	return nil
}
func (g liveBotReadOnlyGuard) Authorize(ctx context.Context, _ execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if err := g.check(ctx); err != nil {
		return execution.PolicyDecision{}, err
	}
	return execution.PolicyDecision{Kind: execution.PolicyDeny, Reason: "toolless lifecycle acceptance"}, store.ErrBotForbidden
}
