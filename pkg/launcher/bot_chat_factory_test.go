package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

type botFactoryTestRepo struct {
	store.BotStore
	fixture *botFactoryFixture
}

func (r botFactoryTestRepo) Get(context.Context, store.BotID) (*store.Bot, error) {
	b := r.fixture.req.Bot
	return &b, nil
}
func (r botFactoryTestRepo) GetRun(context.Context, store.BotID, store.RunID) (*store.BotRun, error) {
	run := r.fixture.req.Run
	return &run, nil
}

type botFactoryTestBudget struct{}

func (*botFactoryTestBudget) Check(ctx context.Context) error                 { return ctx.Err() }
func (*botFactoryTestBudget) ReserveTool(ctx context.Context, _ string) error { return ctx.Err() }
func (*botFactoryTestBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	return r, ctx.Err()
}
func (*botFactoryTestBudget) SettleModel(ctx context.Context, _ uuid.UUID, _ execution.BudgetUsage) error {
	return ctx.Err()
}

type botFactoryTestModel struct {
	calls    int
	contexts []context.Context
	requests []*model.LLMRequest
}

func (*botFactoryTestModel) Name() string { return "pinned-model" }
func (m *botFactoryTestModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		m.contexts = append(m.contexts, ctx)
		m.requests = append(m.requests, req)
		yield(&model.LLMResponse{Content: genai.NewContentFromText("controlled result", "model"), TurnComplete: true}, nil)
	}
}

type botFactoryTestResolver struct {
	model *botFactoryTestModel
	ids   []uuid.UUID
}

func (r *botFactoryTestResolver) Resolve(ctx context.Context, id uuid.UUID) (bot.ResolvedModel, error) {
	r.ids = append(r.ids, id)
	return bot.ResolvedModel{Provider: "owner-provider", Model: "pinned-model", LLM: r.model}, nil
}

type botFactoryTestClassifier struct{}

func (botFactoryTestClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

type botFactoryTestGuard struct{}

func (botFactoryTestGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	return execution.EvaluateRunPolicy(ctx, a, time.Now()), nil
}

type botFactoryFixture struct {
	ctx                                context.Context
	req                                bot.RunRequest
	cfg                                BotChatFactoryConfig
	services                           *store.Services
	sessions                           session.Service
	resolver                           *botFactoryTestResolver
	scopeCleaned, safeCleaned, wrapped int
}

func newBotFactoryFixture(t *testing.T) *botFactoryFixture {
	t.Helper()
	f := &botFactoryFixture{}
	owner := uuid.New()
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "owner-org", TeamSlug: "owner-team", Authenticated: true}
	var err error
	f.ctx, err = execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: owner.String(), OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	v := store.DefaultBotVersion()
	v.BotID = id.BotID
	v.Charter = "Report controlled observations."
	f.req = bot.RunRequest{Bot: store.Bot{ID: id.BotID, OwnerID: owner, Scope: store.BotScopePersonal, Status: store.BotActive, Version: 1}, Version: v, Run: store.BotRun{ID: id.RunID, BotID: id.BotID, Version: 1, SessionID: uuid.New(), Status: store.RunRunning, Lease: store.BotLease{BotID: id.BotID, RunID: id.RunID, WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}}, Principal: p, Policy: policy, Budget: &botFactoryTestBudget{}, Instructions: "Report."}
	f.sessions = session.InMemoryService()
	_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: owner.String(), SessionID: f.req.Run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
	if err != nil {
		t.Fatal(err)
	}
	f.services = &store.Services{Bots: botFactoryTestRepo{fixture: f}, PersonalBots: botFactoryTestRepo{fixture: f}}
	f.resolver = &botFactoryTestResolver{model: &botFactoryTestModel{}}
	f.cfg = BotChatFactoryConfig{
		ContextWindow: 8192,
		LoadScope: func(ctx context.Context, req bot.RunRequest) (*BotChatScope, error) {
			if store.FromContext(ctx) != nil || store.CredentialStoreFromContext(ctx) != nil || agent.LLMFromContext(ctx) != nil {
				t.Error("ambient values reached scope loader")
			}
			if store.UserIDFromContext(ctx) != owner.String() {
				t.Error("scope loader lost owner")
			}
			return &BotChatScope{Services: f.services, Sessions: f.sessions, Models: f.resolver, Cleanup: func() { f.scopeCleaned++ }}, nil
		},
		WrapModel: func(_ context.Context, llm model.LLM, b execution.RunBudget) (model.LLM, error) {
			if b != f.req.Budget {
				t.Error("wrong budget")
			}
			f.wrapped++
			return llm, nil
		},
		Prepare: func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
			return &BotChatSafeResources{WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { f.safeCleaned++ }}, nil
		},
	}
	return f
}

// Invoke through the adapter so its private budget context is installed. Stop
// before ChatRunner, retaining resources for controlled inspection and cleanup.
func (f *botFactoryFixture) construct(t *testing.T) (*api.BotChatResources, error) {
	t.Helper()
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		return nil, err
	}
	var got *api.BotChatResources
	var constructionErr error
	stop := errors.New("stop after construction")
	adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
		got, constructionErr = factory(context.WithoutCancel(ctx), req)
		return nil, stop
	})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if got == nil && constructionErr == nil {
		return nil, runErr
	}
	return got, constructionErr
}

func TestBotChatFactoryFresh(t *testing.T) {
	f := newBotFactoryFixture(t)
	a, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	b, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Cleanup()
	if a.Agent == b.Agent || a.Agent.ToolIndex == b.Agent.ToolIndex || a.Agent.SubAgentManager == b.Agent.SubAgentManager || a.Agent.Compactor == b.Agent.Compactor || a.Agent.Redactor == b.Agent.Redactor || a.Agent.PendingSecrets == b.Agent.PendingSecrets || a.Agent.LLM == b.Agent.LLM {
		t.Fatal("mutable run resources shared")
	}
	if a.Agent.SessionService != f.sessions || a.SessionService != f.sessions || a.Agent.AutoApprove {
		t.Fatal("session/approval invariant changed")
	}
	if a.Agent.SubAgentManager.LLM != a.Agent.LLM || a.Agent.SubAgentManager.Compactor == a.Agent.Compactor {
		t.Fatal("child wiring not request-local")
	}
	if f.wrapped != 2 || len(f.resolver.ids) != 2 || f.resolver.ids[0] != f.req.Run.SessionID {
		t.Fatal("model pin was not resolved per run")
	}
	if _, err := a.Agent.Compactor.LLM(f.ctx, "compact"); err != nil {
		t.Fatal(err)
	}
	if f.resolver.model.calls != 1 {
		t.Fatal("auxiliary model not wired")
	}
	a.Cleanup()
	a.Cleanup()
	b.Cleanup()
	if f.scopeCleaned != 2 || f.safeCleaned != 2 {
		t.Fatalf("cleanup scope=%d safe=%d", f.scopeCleaned, f.safeCleaned)
	}
}

func TestBotChatFactoryValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*botFactoryFixture)
	}{
		{"owner", func(f *botFactoryFixture) { f.req.Bot.OwnerID = uuid.New() }},
		{"version", func(f *botFactoryFixture) { f.req.Run.Version++ }},
		{"expired", func(f *botFactoryFixture) { f.req.Run.Lease.ExpiresAt = time.Now().Add(-time.Second) }},
		{"fence", func(f *botFactoryFixture) { f.req.Run.Lease.Fence = 0 }},
		{"scope", func(f *botFactoryFixture) { f.req.Bot.Scope = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			tc.change(f)
			res, err := f.construct(t)
			if err == nil || res != nil {
				t.Fatal("invalid request accepted")
			}
			if f.wrapped != 0 {
				t.Fatal("invalid request reached resources")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*BotChatFactoryConfig)
	}{
		{"loader", func(c *BotChatFactoryConfig) { c.LoadScope = nil }},
		{"safe", func(c *BotChatFactoryConfig) { c.Prepare = nil }},
		{"budget", func(c *BotChatFactoryConfig) { c.WrapModel = nil }},
		{"window", func(c *BotChatFactoryConfig) { c.ContextWindow = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			tc.change(&f.cfg)
			if _, err := NewBotChatAgentFactory(f.cfg); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatal(err)
			}
		})
	}
}

func TestBotChatFactoryCleanup(t *testing.T) {
	for _, stage := range []string{"scope", "wrap", "prepare", "isolation", "tool"} {
		t.Run(stage, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			sentinel := errors.New("controlled failure")
			load := f.cfg.LoadScope
			prepare := f.cfg.Prepare
			switch stage {
			case "scope":
				f.cfg.LoadScope = func(ctx context.Context, r bot.RunRequest) (*BotChatScope, error) {
					s, _ := load(ctx, r)
					return s, sentinel
				}
			case "wrap":
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return nil, sentinel }
			default:
				f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
					safe, _ := prepare(ctx, r, s)
					switch stage {
					case "prepare":
						return safe, sentinel
					case "isolation":
						safe.VerifyIsolation = func(context.Context) error { return sentinel }
					case "tool":
						safe.Tools = append(safe.Tools, nil)
					}
					return safe, nil
				}
			}
			res, err := f.construct(t)
			if err == nil || res != nil {
				t.Fatal("expected failure")
			}
			wantSafe := 1
			if stage == "scope" || stage == "wrap" {
				wantSafe = 0
			}
			if f.scopeCleaned != 1 || f.safeCleaned != wantSafe {
				t.Fatalf("cleanup scope=%d safe=%d", f.scopeCleaned, f.safeCleaned)
			}
		})
	}
}

type botFactoryCredentialMarker struct{ store.CredentialStore }

func (*botFactoryCredentialMarker) List(context.Context) map[string]store.CredentialType { return nil }
func (*botFactoryCredentialMarker) ListSecrets(context.Context) []string                 { return nil }
func TestBotChatFactoryScope(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.req.Bot.Scope = scope
			personal, team, viewer := &botFactoryCredentialMarker{}, &botFactoryCredentialMarker{}, &botFactoryCredentialMarker{}
			f.services.PersonalCredentials = personal
			f.services.Credentials = team
			f.ctx = store.WithServices(f.ctx, &store.Services{PersonalCredentials: viewer})
			f.ctx = store.WithCredentialStore(f.ctx, viewer)
			res, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Cleanup()
			for _, err := range res.Agent.LLM.GenerateContent(f.ctx, &model.LLMRequest{}, false) {
				if err != nil {
					t.Fatal(err)
				}
			}
			observed := f.resolver.model.contexts[0]
			merged, ok := store.CredentialStoreFromContext(observed).(*store.MergedCredentialStore)
			if !ok || merged.Team != team {
				t.Fatal("team credentials lost")
			}
			if scope == store.BotScopePersonal && merged.Personal != personal {
				t.Fatal("owner credentials lost")
			}
			if scope == store.BotScopeTeam && merged.Personal != nil {
				t.Fatal("team Bot inherited personal credentials")
			}
			if store.UserIDFromContext(observed) != f.req.Bot.OwnerID.String() {
				t.Fatal("owner context lost")
			}
			if f.services.PersonalCredentials != personal {
				t.Fatal("shared services mutated")
			}
		})
	}
}

func TestBotChatModelResolverPins(t *testing.T) {
	id := uuid.New()
	llm := &botFactoryTestModel{}
	for _, tc := range []struct {
		name      string
		pin       *store.SessionPin
		inherited bool
		wantErr   error
	}{
		{name: "pinned", pin: &store.SessionPin{Provider: "owner-provider", Model: "pinned-model"}},
		{name: "unpinned-denied", wantErr: store.ErrBotUnsupported},
		{name: "inherited", inherited: true},
		{name: "partial", pin: &store.SessionPin{Provider: "owner-provider"}, inherited: true, wantErr: store.ErrBotInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providerCalls, inheritedCalls := 0, 0
			r := BotChatModelResolver{
				SessionPin: func(_ context.Context, sessionID string) (*store.SessionPin, error) {
					if sessionID != id.String() {
						t.Fatal("wrong session pin")
					}
					return tc.pin, nil
				},
				ResolveProvider: func(_ context.Context, provider, name string) (model.LLM, error) {
					providerCalls++
					if provider != "owner-provider" || name != "pinned-model" {
						t.Fatal("pin changed")
					}
					return llm, nil
				},
			}
			if tc.inherited {
				r.ResolveInherited = func(_ context.Context, sessionID uuid.UUID) (bot.ResolvedModel, error) {
					inheritedCalls++
					if sessionID != id {
						t.Fatal("wrong inherited session")
					}
					return bot.ResolvedModel{Provider: "owner-provider", Model: "pinned-model", LLM: llm}, nil
				}
			}
			got, err := r.Resolve(context.Background(), id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want=%v", err, tc.wantErr)
			}
			if tc.wantErr == nil && (got.LLM != llm || got.Model != "pinned-model" || providerCalls+inheritedCalls != 1) {
				t.Fatalf("resolution=%+v", got)
			}
			if tc.wantErr != nil && providerCalls+inheritedCalls != 0 {
				t.Fatal("invalid pin fell back")
			}
		})
	}
}

func TestBotChatFactoryRevocationAndCleanup(t *testing.T) {
	f := newBotFactoryFixture(t)
	revoked := false
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		safe.VerifyIsolation = func(context.Context) error {
			if revoked {
				return store.ErrBotConflict
			}
			return nil
		}
		return safe, err
	}
	res, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()
	revoked = true
	if _, err := res.Agent.Compactor.LLM(f.ctx, "blocked"); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	revoked = false
	res.Cleanup()
	if _, err := res.Agent.Compactor.LLM(f.ctx, "closed"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.resolver.model.calls != 0 {
		t.Fatal("provider reached after revocation/cleanup")
	}
}

type botFactoryToolContext struct {
	tool.Context
	base context.Context
}

func (c botFactoryToolContext) Value(key any) any           { return c.base.Value(key) }
func (c botFactoryToolContext) Deadline() (time.Time, bool) { return c.base.Deadline() }
func (c botFactoryToolContext) Done() <-chan struct{}       { return c.base.Done() }
func (c botFactoryToolContext) Err() error                  { return c.base.Err() }

// The embedded tool.Context supplies methods that declaration registration does
// not call; context values must still be captured from the accountable owner.
func TestBotChatFactoryToolRegistrationStaysWrapped(t *testing.T) {
	f := newBotFactoryFixture(t)
	res, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()
	if res.Agent.SubAgentManager.ToolIndex == nil {
		t.Fatal("missing per-run index")
	}
	// The always-installed delegate is a function tool whose ProcessRequest
	// normally stores the underlying function-tool receiver in req.Tools.
	wired := res.Agent.LLM.(*botFactoryLLM)
	in := &botWiredInjection{scope: &BotChatScope{Sessions: f.sessions}, safe: &BotChatSafeResources{WorkspaceDir: "/isolated"}, binding: wired.binding, llm: wired, contextWindow: 8192}
	result, err := newBotWiredChatAgent(f.ctx, &ChatFactoryConfig{}, in)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Cleanup()
	request := &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
	wrapped := result.InternalTools[len(result.InternalTools)-1].(*botBoundTool)
	ctx := botFactoryToolContext{base: f.ctx}
	if err := wrapped.ProcessRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if request.Tools[wrapped.Name()] != wrapped {
		t.Fatal("registration exposed unguarded function tool")
	}
}

func TestBotChatFactoryRuntimeToolless(t *testing.T) {
	f := newBotFactoryFixture(t)
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunSucceeded || result.Report.Summary != "controlled result" || f.scopeCleaned != 1 || f.safeCleaned != 1 {
		t.Fatalf("result=%+v cleanup=%d/%d", result, f.scopeCleaned, f.safeCleaned)
	}
}
