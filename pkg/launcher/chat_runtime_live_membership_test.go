package launcher

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// Lifecycle snapshots remain the existing factory fixture's responsibility. Every
// Get/GetRun first crosses the REAL scoped repository's authorization boundary;
// revocation is a deleted SQLite membership, not a guard returning a chosen error.
// A draft suffices for the authority probe: these tests do not claim activation,
// lease acquisition, or an interactive admission round trip.
type liveMembershipRepository struct {
	store.BotStore
	fixture *botFactoryFixture
	probe   store.BotID
	reads   int
}

func (r *liveMembershipRepository) Get(ctx context.Context, id store.BotID) (*store.Bot, error) {
	r.reads++
	if id != r.fixture.req.Bot.ID {
		return nil, store.ErrBotForbidden
	}
	if _, err := r.BotStore.Get(ctx, r.probe); err != nil {
		return nil, err
	}
	b := r.fixture.req.Bot
	return &b, nil
}
func (r *liveMembershipRepository) GetRun(ctx context.Context, id store.BotID, run store.RunID) (*store.BotRun, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return nil, err
	}
	if run != r.fixture.req.Run.ID {
		return nil, store.ErrBotForbidden
	}
	b := r.fixture.req.Run
	return &b, nil
}

type liveMembershipFixture struct {
	repo            *liveMembershipRepository
	revoke, restore func()
	current         execution.BotCurrentPrincipal
}

func newLiveMembershipFixture(t *testing.T, r *servicesParityRuntime, scope chatruntime.ResourceScope) liveMembershipFixture {
	t.Helper()
	f, p := r.f, r.f.req.Principal
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(f.ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	must(err)
	t.Cleanup(func() { _ = db.Close() })
	org := &store.Organization{ID: uuid.NewString(), Slug: p.OrgSlug, Name: "Membership acceptance", Status: "active"}
	must(db.Organizations().Create(f.ctx, org))
	must(db.ProvisionOrg(f.ctx, org.ID, org.Slug))
	ods, err := db.ForOrg(org.Slug)
	must(err)
	team := &store.Team{ID: uuid.NewString(), Slug: p.TeamSlug, Name: "Membership", SchemaName: "team_membership"}
	must(ods.Teams().CreateTeam(f.ctx, team))
	must(db.Users().Create(f.ctx, &store.User{ID: p.Subject, Email: "membership@example.test", DisplayName: "Membership", Status: "active"}))
	must(db.Organizations().AddMember(f.ctx, p.Subject, org.ID, "member"))
	must(ods.Teams().AddMember(f.ctx, &store.TeamMembership{UserID: p.Subject, TeamID: team.ID, Role: "member"}))
	scoped, err := BotChatServicesForOwner(ods, p, store.BotScope(scope))
	must(err)
	repo := scoped.PersonalBots
	if scope == chatruntime.ResourceScopeTeam {
		repo = scoped.Bots
	}
	draft, err := repo.Create(f.ctx, store.BotCreateRequest{Name: "Live membership probe", OwnerID: f.req.Bot.OwnerID, Version: f.req.Version, IdempotencyKey: "membership"})
	must(err)
	probe := &liveMembershipRepository{BotStore: repo, fixture: f, probe: draft.ID}
	f.services.PersonalBots, f.services.Bots = probe, probe
	// Mirror production TenantMiddleware: ordinary Chat receives live platform
	// identity and tenant routing dependencies, not a Bot-specific authority hook.
	f.services.Mode = store.ModePlatform
	f.services.Platform, f.services.TenantRouter = db, db
	return liveMembershipFixture{
		repo: probe,
		revoke: func() {
			if scope == chatruntime.ResourceScopePersonal {
				must(db.Organizations().RemoveMember(f.ctx, p.Subject, org.ID))
			} else {
				must(ods.Teams().RemoveMember(f.ctx, p.Subject, team.ID))
			}
		},
		restore: func() {
			if scope == chatruntime.ResourceScopePersonal {
				must(db.Organizations().AddMember(f.ctx, p.Subject, org.ID, "member"))
			} else {
				must(ods.Teams().AddMember(f.ctx, &store.TeamMembership{UserID: p.Subject, TeamID: team.ID, Role: "member"}))
			}
		},
		current: func(ctx context.Context, original execution.Principal) (execution.Principal, error) {
			if original.Subject != p.Subject || original.OrgSlug != p.OrgSlug || original.TeamSlug != p.TeamSlug {
				return execution.Principal{}, store.ErrBotForbidden
			}
			role, err := db.Organizations().GetMemberRole(ctx, original.Subject, org.ID)
			if err != nil {
				return execution.Principal{}, err
			}
			if role == "" {
				return execution.Principal{}, store.ErrBotForbidden
			}
			if scope == chatruntime.ResourceScopeTeam {
				role, err = ods.Teams().GetMemberRole(ctx, original.Subject, team.ID)
				if err != nil {
					return execution.Principal{}, err
				}
				if role == "" {
					return execution.Principal{}, store.ErrBotForbidden
				}
			}
			original.Roles = []string{role}
			return original, nil
		},
	}
}

func liveMembershipReconstruct(t *testing.T, r *servicesParityRuntime, m liveMembershipFixture, scope chatruntime.ResourceScope, mode string) {
	t.Helper()
	f := r.f
	if mode == "interactive-bot" {
		registry := execution.NewBotTurnAuthorizationRegistry(1)
		t.Cleanup(registry.Close)
		ctx, err := execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		id := execution.BotRunIdentity{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Version.Version, ActorID: f.req.Principal.Subject, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
		original, err := registry.Register(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		ceiling := original.CallerCeiling()
		f.req.Run.CallerCeiling = &ceiling
		f.req.Authorization, err = registry.Authorizer(id, ceiling, m.current)
		if err != nil {
			t.Fatal(err)
		}
	}
	if mode != "ordinary-chat" {
		runtime, err := f.construct(t)
		if err != nil {
			t.Fatal(err)
		}
		r.chat = runtime.Agent
		t.Cleanup(runtime.Cleanup)
		return
	}
	// No Bot identity, policy, lease, repository, or invented authorization hook
	// in Chat. The separate probe remains available only to assert fixture state.
	f.services.Bots, f.services.PersonalBots = nil, nil
	cfg := &config.AppConfig{}
	pool := productionCapabilityPool{backend: r.node.backend, client: r.node}
	resources := ChatCatalogResources{Pool: pool, Backend: r.node.backend}
	inputs, err := chatruntime.Build(f.ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: scope, AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputs.Close() })
	if scope == chatruntime.ResourceScopePersonal {
		factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: r.node.root}, resources, nil)
		runtime, err := factory(f.ctx, api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: f.resolver.model, SessionService: f.sessions, SessionID: r.node.session, UserID: f.req.Principal.Subject, AppName: "astonish"})
		if err != nil {
			t.Fatal(err)
		}
		r.chat = runtime.ChatAgent
		t.Cleanup(runtime.Cleanup)
	} else {
		// Explicit team composition, not a claim of team Studio HTTP support.
		resources.EmailTools, err = ScopedChatEmailTools(ScopedChatEmailOptions{Inputs: inputs, Scope: scope, Backend: r.node.backend, Client: r.node, SessionID: r.node.session})
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := NewScopedChatRuntime(f.ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: r.node.root}, Resources: &resources, LLM: f.resolver.model, Sessions: f.sessions, SessionID: r.node.session, UseScopedEngines: true}, ChatRuntimeHooks{})
		if err != nil {
			t.Fatal(err)
		}
		r.chat = runtime.ChatAgent
		t.Cleanup(runtime.Cleanup)
	}
}

func TestChatRuntimeLiveMembershipModelBoundary(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			t.Run(fmt.Sprintf("%s/%s", scope, mode), func(t *testing.T) {
				r := newServicesParityRuntime(t, scope, mode)
				m := newLiveMembershipFixture(t, r, scope)
				liveMembershipReconstruct(t, r, m, scope, mode)
				call := func() error {
					for _, err := range r.chat.LLM.GenerateContent(r.f.ctx, &model.LLMRequest{}, false) {
						if err != nil {
							return err
						}
					}
					return nil
				}
				m.revoke()
				if _, err := m.repo.Get(r.f.ctx, r.f.req.Bot.ID); err == nil {
					t.Fatal("membership fixture failed to revoke")
				}
				if err := call(); err == nil {
					t.Error("revoked member reached model")
				}
				if r.f.resolver.model.calls != 0 || len(r.node.calls) != 0 || len(r.node.backend.vault) != 0 || len(r.guard.consumed) != 0 {
					t.Error("revoked model call had effects")
				}
				m.restore()
				before := r.f.resolver.model.calls
				if err := call(); err != nil || r.f.resolver.model.calls != before+1 {
					t.Fatalf("restored model positive control: calls=%d err=%v", r.f.resolver.model.calls, err)
				}
			})
		}
	}
}
