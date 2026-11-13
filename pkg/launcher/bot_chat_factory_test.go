package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/config"
	"iter"
	"slices"
	"strings"
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

type workCapabilityProbe struct {
	bot.BotWorkServices
	calls  int
	change bot.BotWorkChangeRequest
	err    error
}

func (p *workCapabilityProbe) Read(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) {
	p.calls++
	return &bot.BotWorkReadResult{Notes: "captured work"}, p.err
}
func (p *workCapabilityProbe) Update(_ context.Context, a bot.BotWorkChangeRequest) (*store.BotWorkProjection, error) {
	p.calls++
	p.change = a
	return &store.BotWorkProjection{}, p.err
}
func (p *workCapabilityProbe) Evidence(context.Context, bot.BotEvidenceReadRequest) (*bot.BotEvidenceReadResult, error) {
	p.calls++
	return &bot.BotEvidenceReadResult{Content: "captured source"}, p.err
}
func (p *workCapabilityProbe) Check(context.Context, bot.BotWorkCheckRequest) (*store.BotWorkCheckReceipt, error) {
	p.calls++
	return &store.BotWorkCheckReceipt{Status: store.BotWorkCheckReturned}, p.err
}

func TestBotAgentWorkGuardedFactoryAndDiscovery(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			names := []string{botWorkReadTool, botWorkUpdateTool, botEvidenceReadTool, botCheckWorkTool}
			var err error
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: names})
			if err != nil {
				t.Fatal(err)
			}
			var registry *execution.BotTurnAuthorizationRegistry
			var turnIdentity execution.BotRunIdentity
			if interactive {
				f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
				turnIdentity = f.req.Policy.Identity()
				f.req.Principal.Authentication = execution.AuthMethodOAuth
				f.req.Principal.Scopes = []string{"chat", "tool:execute"}
				f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
				if err != nil {
					t.Fatal(err)
				}
				f.ctx, err = execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				registry = execution.NewBotTurnAuthorizationRegistry(2)
				admission, err := registry.Register(f.ctx, turnIdentity)
				if err != nil {
					t.Fatal(err)
				}
				ceiling := admission.CallerCeiling()
				f.req.Run.CallerCeiling = &ceiling
				f.req.Authorization, err = registry.Authorizer(turnIdentity, ceiling, func(_ context.Context, p execution.Principal) (execution.Principal, error) { return p, nil })
				if err != nil {
					t.Fatal(err)
				}
				f.req.Policy = nil
				f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
					return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}
			} else {
				prepare := f.cfg.Prepare
				f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
					safe, err := prepare(ctx, req, scope)
					safe.PolicyRuntime.Classifier = BotCatalogueClassifier{}
					return safe, err
				}
			}
			probe := &workCapabilityProbe{}
			f.ctx = bot.WithBotWorkServices(f.ctx, probe)
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			args := map[string]map[string]any{
				botWorkReadTool: {}, botWorkUpdateTool: {"expected_revision": float64(0), "kind": "set_remaining", "remaining": []any{map[string]any{"summary": "pending", "reason": "source unavailable", "continuation": "none"}}},
				botEvidenceReadTool: {"call_id": "earlier"}, botCheckWorkTool: {"question": "supported?", "draft_text": "draft"},
			}
			ctx := botFactoryHandoffContext{base: context.Background(), session: f.req.Run.SessionID.String(), functionCallID: "trusted-call"}
			byName := map[string]botRunnableTool{}
			for _, candidate := range got.Agent.Tools {
				if run, ok := candidate.(botRunnableTool); ok {
					byName[candidate.Name()] = run
				}
			}
			for _, name := range names {
				target := byName[name]
				if target == nil {
					t.Fatalf("missing %s", name)
				}
				declaration, _ := json.Marshal(target.Declaration())
				for _, forbidden := range []string{"source_run_id", "actor_id", "lease", "observed_at"} {
					if strings.Contains(string(declaration), forbidden) {
						t.Fatalf("public identity input: %s", declaration)
					}
				}
				if out, err := target.Run(ctx, args[name]); err != nil || out["status"] != "ok" {
					t.Fatalf("guarded %s: %v %v", name, out, err)
				}
			}
			if probe.change.CallID != "trusted-call" || probe.change.Remaining[0].ID != uuid.Nil || probe.change.Remaining[0].SourceRunID != uuid.Nil {
				t.Fatalf("untrusted identity %+v", probe.change)
			}
			if out, err := byName[botWorkUpdateTool].Run(ctx, map[string]any{"expected_revision": float64(0), "kind": "upsert_item", "item": map[string]any{"summary": "inspect sources", "status": "pending"}}); err != nil || out["status"] != "ok" || probe.change.Item == nil || probe.change.Item.ID != uuid.Nil {
				t.Fatalf("new item requires model ID: %v %v", out, err)
			}
			if got.Agent.ToolIndex == got.Agent.SubAgentManager.ToolIndex {
				t.Fatal("child shares writable discovery index")
			}
			for _, target := range got.Agent.SubAgentManager.AllTools() {
				if target.Name() == botWorkUpdateTool {
					t.Fatal("child group exposes work update")
				}
			}
			for _, index := range []*agent.ToolIndex{got.Agent.ToolIndex, got.Agent.SubAgentManager.ToolIndex} {
				bridge, err := fixedProviderTools(index)
				if err != nil {
					t.Fatal(err)
				}
				var execute botRunnableTool
				for _, b := range bridge {
					if b.Name() == "execute_tool" {
						execute = b.(botRunnableTool)
					}
				}
				child := index == got.Agent.SubAgentManager.ToolIndex
				callCtx := ctx
				if child {
					callCtx.session = "child-session"
				}
				for _, name := range names {
					before := probe.calls
					out, err := execute.Run(callCtx, map[string]any{"name": name, "arguments": args[name]})
					if child && name == botWorkUpdateTool {
						if probe.calls != before {
							t.Fatal("child discovery mutated work")
						}
						continue
					}
					if err != nil || probe.calls != before+1 {
						t.Fatalf("discovery child=%v %s: %v %v", child, name, out, err)
					}
				}
			}
			child := ctx
			child.session = "child-session"
			before := probe.calls
			if _, err := byName[botWorkUpdateTool].Run(child, args[botWorkUpdateTool]); err == nil || probe.calls != before {
				t.Fatal("retained parent tool allowed child update")
			}
			probe.err = store.ErrBotInvalid
			if out, err := byName[botCheckWorkTool].Run(ctx, args[botCheckWorkTool]); err != nil || out["status"] != "invalid_request" {
				t.Fatalf("optional failure escaped: %v %v", out, err)
			}
			f.req.Run.Lease.Fence++
			before = probe.calls
			if _, err := byName[botWorkReadTool].Run(ctx, args[botWorkReadTool]); err == nil || probe.calls != before {
				t.Fatal("stale lease reached work service")
			}
			f.req.Run.Lease.Fence--
			if interactive {
				registry.Cancel(turnIdentity)
			} else {
				denied, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
				if err != nil {
					t.Fatal(err)
				}
				ctx.base = execution.WithRunPolicy(ctx.base, denied)
			}
			if _, err := byName[botWorkReadTool].Run(ctx, args[botWorkReadTool]); err == nil || probe.calls != before {
				t.Fatal("work tool bypassed authority")
			}
		})
	}
}

func TestBotAgentWorkNoCapability(t *testing.T) {
	for _, binding := range []*botFactoryBinding{nil, {}, {owner: context.Background()}} {
		if tools, err := newBotWorkTools(binding); err != nil || len(tools) != 0 {
			t.Fatalf("nil capability: %v %v", tools, err)
		}
	}
}

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

// botFactoryCallbackRepo adds only the read capabilities needed by the callbacks;
// the existing fixture remains responsible for authoritative Bot/run snapshots.
type botFactoryCallbackRepo struct {
	botFactoryTestRepo
	t             *testing.T
	scopeReq      bot.RunRequest
	runs          []store.BotRun
	approvals     []store.BotApproval
	getErr        error
	readErr       error
	runPages      []store.BotPage
	approvalPages []store.BotPage
	versionReads  int
	beforeRead    func(context.Context)
	rawPages      bool
}

func (r *botFactoryCallbackRepo) assertScope(ctx context.Context, id store.BotID) {
	r.t.Helper()
	req := r.scopeReq
	p, ok := execution.PrincipalFromContext(ctx)
	if id != req.Bot.ID || !ok || p.Subject != req.Principal.Subject || p.OrgSlug != req.Principal.OrgSlug || p.TeamSlug != req.Principal.TeamSlug || store.UserIDFromContext(ctx) != req.Bot.OwnerID.String() || store.SessionIDFromContext(ctx) != req.Run.SessionID.String() {
		r.t.Fatalf("callback escaped captured owner scope: bot=%s principal=%+v", id, p)
	}
}

func (r *botFactoryCallbackRepo) Get(ctx context.Context, id store.BotID) (*store.Bot, error) {
	r.assertScope(ctx, id)
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.botFactoryTestRepo.Get(ctx, id)
}

func (r *botFactoryCallbackRepo) GetVersion(ctx context.Context, id store.BotID, version int64) (*store.BotVersion, error) {
	r.assertScope(ctx, id)
	r.versionReads++
	if version != r.fixture.req.Bot.Version {
		r.t.Fatalf("version=%d, want current %d", version, r.fixture.req.Bot.Version)
	}
	v := r.fixture.req.Version
	return &v, r.readErr
}

func (r *botFactoryCallbackRepo) ListRuns(ctx context.Context, id store.BotID, page store.BotPage) ([]store.BotRun, error) {
	r.assertScope(ctx, id)
	r.runPages = append(r.runPages, page)
	if r.beforeRead != nil {
		r.beforeRead(ctx)
	}
	if r.rawPages {
		return r.runs, nil
	}
	slices.SortFunc(r.runs, func(a, b store.BotRun) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	if r.readErr != nil {
		return nil, r.readErr
	}
	if page.Limit != 100 {
		r.t.Fatalf("unbounded run page: %+v", page)
	}
	var rows []store.BotRun
	after := page.After == uuid.Nil.String()
	for _, row := range r.runs {
		if row.BotID != id {
			continue
		}
		if !after {
			after = row.ID.String() == page.After
			continue
		}
		rows = append(rows, row)
		if len(rows) == page.Limit {
			break
		}
	}
	return rows, nil
}

func (r *botFactoryCallbackRepo) ListApprovals(ctx context.Context, id store.BotID, page store.BotPage) ([]store.BotApproval, error) {
	r.assertScope(ctx, id)
	r.approvalPages = append(r.approvalPages, page)
	if r.beforeRead != nil {
		r.beforeRead(ctx)
	}
	if r.rawPages {
		return r.approvals, nil
	}
	slices.SortFunc(r.approvals, func(a, b store.BotApproval) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	if r.readErr != nil {
		return nil, r.readErr
	}
	if page.Limit != 100 {
		r.t.Fatalf("unbounded approval page: %+v", page)
	}
	var rows []store.BotApproval
	after := page.After == uuid.Nil.String()
	for _, row := range r.approvals {
		if row.BotID != id {
			continue
		}
		if !after {
			after = row.ID.String() == page.After
			continue
		}
		rows = append(rows, row)
		if len(rows) == page.Limit {
			break
		}
	}
	return rows, nil
}

func newBotFactoryCallbackFixture(t *testing.T, scope store.BotScope) (*botFactoryFixture, *botFactoryCallbackRepo) {
	t.Helper()
	f := newBotFactoryFixture(t)
	f.req.Bot.Scope = scope
	f.req.Run.CreatedAt = time.Now().Add(-time.Hour)
	r := &botFactoryCallbackRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, t: t, scopeReq: f.req}
	// Only the repository corresponding to the captured Bot scope has these
	// capabilities; accidentally selecting the other repository fails the test.
	if scope == store.BotScopePersonal {
		f.services.PersonalBots = r
	} else {
		f.services.Bots = r
	}
	return f, r
}

func TestBotAgentFactoryPreviousWork(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := newBotFactoryCallbackFixture(t, scope)
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			if resources.PreviousWork == nil {
				t.Fatal("previous-work callback missing")
			}
			// Later viewer context must not replace the captured owner or store.
			viewer := f.req.Principal
			viewer.Subject = uuid.New().String()
			viewer.OrgSlug, viewer.TeamSlug = "foreign-org", "foreign-team"
			ctx, err := execution.WithPrincipal(context.Background(), viewer)
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithUserID(ctx, viewer.Subject)
			if got, err := resources.PreviousWork(ctx); err != nil || got != nil {
				t.Fatalf("empty history: %+v %v", got, err)
			}
			makeRun := func(finished time.Time) store.BotRun {
				id := uuid.New()
				return store.BotRun{ID: id, BotID: f.req.Bot.ID, FinishedAt: &finished, Report: store.BotReport{Work: &store.BotWorkProjection{BotID: f.req.Bot.ID, RunID: id}}}
			}
			admitted := f.req.Run.CreatedAt
			older := makeRun(admitted.Add(-2 * time.Hour))
			latest := makeRun(admitted.Add(-time.Minute))
			foreign := makeRun(admitted.Add(-time.Second))
			foreign.BotID = uuid.New()
			foreign.ID = uuid.Nil // filtered out before the first page
			current := makeRun(admitted.Add(-time.Second))
			current.ID = f.req.Run.ID
			unfinished := makeRun(admitted.Add(-time.Second))
			unfinished.FinishedAt = nil
			legacy := makeRun(admitted.Add(-time.Second))
			legacy.Report.Work = nil
			// Deliberately unordered: choose latest finish, not list position;
			// a report finishing at or after admission is not previous context.
			repo.runs = []store.BotRun{foreign, current, unfinished, legacy, makeRun(admitted), makeRun(admitted.Add(time.Second)), older}
			for len(repo.runs) < 101 { // foreign Bot row is filtered by the scoped store
				repo.runs = append(repo.runs, makeRun(admitted.Add(-3*time.Hour)))
			}
			latest.ID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
			repo.runs = append(repo.runs, latest)
			repo.runPages = nil
			got, err := resources.PreviousWork(ctx)
			if err != nil || got != latest.Report.Work {
				t.Fatalf("latest pre-admission outcome: %+v %v, want %+v", got, err, latest.Report.Work)
			}
			if len(repo.runPages) != 2 || repo.runPages[1].After != repo.runs[100].ID.String() {
				t.Fatalf("history pagination: %+v", repo.runPages)
			}
			sentinel := errors.New("history store unavailable")
			repo.readErr = sentinel
			if got, err := resources.PreviousWork(ctx); got != nil || !errors.Is(err, sentinel) {
				t.Fatalf("history error swallowed: %+v %v", got, err)
			}
		})
	}
}

func TestBotAgentFactoryWorkContinuation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := newBotFactoryCallbackFixture(t, scope)
			scheduleID, disabledID := uuid.New(), uuid.New()
			f.req.Version.Triggers = []store.TriggerSpec{{ID: scheduleID, Enabled: true}, {ID: disabledID}}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			validate := resources.ValidateWorkContinuation
			if validate == nil {
				t.Fatal("continuation callback missing")
			}
			ctx := context.Background()
			for _, tc := range []struct {
				name string
				kind store.BotWorkContinuationKind
				id   uuid.UUID
				want error
			}{
				{"enabled schedule", store.BotWorkContinueExistingSchedule, scheduleID, nil},
				{"disabled schedule", store.BotWorkContinueExistingSchedule, disabledID, store.ErrBotForbidden},
				{"foreign or absent schedule", store.BotWorkContinueExistingSchedule, uuid.New(), store.ErrBotForbidden},
				{"nil schedule", store.BotWorkContinueExistingSchedule, uuid.Nil, store.ErrBotForbidden},
				{"unknown continuation", store.BotWorkContinuationKind("invented"), scheduleID, store.ErrBotForbidden},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := validate(ctx, tc.kind, tc.id); !errors.Is(err, tc.want) {
						t.Fatalf("continuation error=%v, want %v", err, tc.want)
					}
				})
			}
			// Re-read persisted triggers; the construction snapshot is not authority.
			f.req.Version.Triggers = nil
			if err := validate(ctx, store.BotWorkContinueExistingSchedule, scheduleID); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("removed schedule retained authority: %v", err)
			}
			for _, status := range []store.ApprovalStatus{store.ApprovalPending, store.ApprovalApproved, store.ApprovalDenied, store.ApprovalExpired, store.ApprovalRevoked, store.ApprovalConsumed} {
				t.Run(string(status), func(t *testing.T) {
					approval := store.BotApproval{ID: uuid.New(), BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Status: status, ExpiresAt: time.Now().Add(time.Hour)}
					repo.approvals = []store.BotApproval{approval}
					want := error(store.ErrBotForbidden)
					if status == store.ApprovalPending || status == store.ApprovalApproved {
						want = nil
					}
					if err := validate(ctx, store.BotWorkContinueExistingApproval, approval.ID); !errors.Is(err, want) {
						t.Fatalf("approval error=%v, want %v", err, want)
					}
				})
			}
			approval := store.BotApproval{ID: uuid.New(), BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour)}
			for _, tc := range []struct {
				name   string
				change func(*store.BotApproval)
			}{
				{"foreign Bot", func(a *store.BotApproval) { a.BotID = uuid.New() }},
				{"other run", func(a *store.BotApproval) { a.RunID = uuid.New() }},
				{"expired deadline", func(a *store.BotApproval) { a.ExpiresAt = time.Now().Add(-time.Second) }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					row := approval
					tc.change(&row)
					repo.approvals = []store.BotApproval{row}
					if err := validate(ctx, store.BotWorkContinueExistingApproval, row.ID); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("invalid approval allowed: %v", err)
					}
				})
			}
			repo.approvals = nil
			for i := 0; i < 100; i++ {
				row := approval
				row.ID = uuid.New()
				repo.approvals = append(repo.approvals, row)
			}
			approval.ID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
			repo.approvals = append(repo.approvals, approval)
			repo.approvalPages = nil
			if err := validate(ctx, store.BotWorkContinueExistingApproval, approval.ID); err != nil {
				t.Fatal(err)
			}
			if len(repo.approvalPages) != 2 || repo.approvalPages[1].After != repo.approvals[99].ID.String() {
				t.Fatalf("approval pagination: %+v", repo.approvalPages)
			}
			if err := validate(ctx, store.BotWorkContinueExistingApproval, uuid.New()); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("missing approval: %v", err)
			}
			sentinel := errors.New("continuation store unavailable")
			repo.readErr = sentinel
			for _, kind := range []store.BotWorkContinuationKind{store.BotWorkContinueExistingSchedule, store.BotWorkContinueExistingApproval} {
				if err := validate(ctx, kind, scheduleID); !errors.Is(err, sentinel) {
					t.Fatalf("%s swallowed read error: %v", kind, err)
				}
			}
		})
	}
}

func TestBotAgentFactoryWorkHistoryBounds(t *testing.T) {
	for _, previous := range []bool{true, false} {
		name := "approvals"
		if previous {
			name = "previous work"
		}
		t.Run(name, func(t *testing.T) {
			for _, count := range []int{botWorkHistoryMaxPages*botWorkHistoryPageSize - 1, botWorkHistoryMaxPages * botWorkHistoryPageSize, botWorkHistoryMaxPages*botWorkHistoryPageSize + 1} {
				f, repo := newBotFactoryCallbackFixture(t, store.BotScopePersonal)
				resources, err := f.construct(t)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(resources.Cleanup)
				finished := f.req.Run.CreatedAt.Add(-time.Minute)
				for i := 0; i < count; i++ {
					repo.runs = append(repo.runs, store.BotRun{ID: uuid.New(), BotID: f.req.Bot.ID, FinishedAt: &finished, Report: store.BotReport{Work: &store.BotWorkProjection{}}})
					repo.approvals = append(repo.approvals, store.BotApproval{ID: uuid.New(), BotID: f.req.Bot.ID})
				}
				var got *store.BotWorkProjection
				var pages []store.BotPage
				if previous {
					got, err = resources.PreviousWork(context.Background())
					pages = repo.runPages
				} else {
					err = resources.ValidateWorkContinuation(context.Background(), store.BotWorkContinueExistingApproval, uuid.New())
					pages = repo.approvalPages
				}
				if len(pages) != botWorkHistoryMaxPages {
					t.Fatalf("count %d: pages=%d", count, len(pages))
				}
				if count >= botWorkHistoryMaxPages*botWorkHistoryPageSize {
					if !errors.Is(err, errBotWorkHistoryExhausted) || got != nil {
						t.Fatalf("incomplete lookup returned absence or partial result: %v %v", got, err)
					}
				} else if previous {
					if err != nil || got == nil {
						t.Fatalf("complete history: %v %v", got, err)
					}
				} else if !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("complete absence: %v", err)
				}
			}
		})
	}
}

func TestBotAgentFactoryWorkHistoryCancellation(t *testing.T) {
	for _, previous := range []bool{true, false} {
		for _, mode := range []string{"cancelled", "cancel during read", "caller deadline", "lookup timeout"} {
			name := "approval/" + mode
			if previous {
				name = "previous/" + mode
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f, repo := newBotFactoryCallbackFixture(t, store.BotScopePersonal)
				resources, err := f.construct(t)
				if err != nil {
					t.Fatal(err)
				}
				defer resources.Cleanup()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := error(context.Canceled)
				if mode == "cancelled" {
					cancel()
				}
				if mode == "caller deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 25*time.Millisecond)
					defer stop()
				}
				if mode == "caller deadline" || mode == "lookup timeout" {
					want = context.DeadlineExceeded
				}
				repo.beforeRead = func(bound context.Context) {
					deadline, ok := bound.Deadline()
					if !ok || time.Until(deadline) > botWorkHistoryTimeout {
						t.Fatalf("missing lookup deadline: %v %v", deadline, ok)
					}
					if callerDeadline, ok := ctx.Deadline(); ok && deadline.After(callerDeadline) {
						t.Fatal("caller deadline extended")
					}
					if mode == "cancel during read" {
						cancel()
					}
					<-bound.Done()
					// Deliberately return a successful empty page after cancellation;
					// the callback must not mistake it for complete absence.
				}
				if previous {
					var got *store.BotWorkProjection
					got, err = resources.PreviousWork(ctx)
					if got != nil {
						t.Fatal("cancelled lookup returned work")
					}
				} else {
					err = resources.ValidateWorkContinuation(ctx, store.BotWorkContinueExistingApproval, uuid.New())
				}
				if !errors.Is(err, want) {
					t.Fatalf("error=%v, want %v", err, want)
				}
				if mode == "cancelled" && (len(repo.runPages) != 0 || len(repo.approvalPages) != 0) {
					t.Fatal("cancelled callback read history")
				}
			})
		}
	}
}

func TestBotAgentFactoryWorkHistoryInvalidPages(t *testing.T) {
	for _, mode := range []string{"nil cursor", "duplicate", "backward", "repeated page", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			f, repo := newBotFactoryCallbackFixture(t, store.BotScopePersonal)
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			repo.rawPages = true
			low := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			high := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
			ids := []uuid.UUID{uuid.Nil}
			switch mode {
			case "duplicate":
				ids = []uuid.UUID{low, low}
			case "backward":
				ids = []uuid.UUID{high, low}
			case "repeated page", "oversized":
				ids = nil
				for i := 0; i < botWorkHistoryPageSize; i++ {
					id := low
					id[15] = byte(i + 1)
					ids = append(ids, id)
				}
				if mode == "oversized" {
					ids = append(ids, high)
				}
			}
			for _, id := range ids {
				repo.runs = append(repo.runs, store.BotRun{ID: id, BotID: f.req.Bot.ID})
				repo.approvals = append(repo.approvals, store.BotApproval{ID: id, BotID: f.req.Bot.ID})
			}
			if got, err := resources.PreviousWork(context.Background()); got != nil || !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("invalid run page: %v %v", got, err)
			}
			if err := resources.ValidateWorkContinuation(context.Background(), store.BotWorkContinueExistingApproval, uuid.New()); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("invalid approval page: %v", err)
			}
			wantPages := 1
			if mode == "repeated page" {
				wantPages = 2
			}
			if len(repo.runPages) != wantPages || len(repo.approvalPages) != wantPages {
				t.Fatalf("invalid cursor was retried: %d %d", len(repo.runPages), len(repo.approvalPages))
			}
		})
	}
}

func TestBotAgentFactoryWorkCallbacksRecheckAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*botFactoryFixture, *botFactoryCallbackRepo)
		want   error
	}{
		{"permission revoked", func(_ *botFactoryFixture, r *botFactoryCallbackRepo) { r.getErr = store.ErrBotForbidden }, store.ErrBotForbidden},
		{"foreign Bot", func(f *botFactoryFixture, _ *botFactoryCallbackRepo) { f.req.Bot.ID = uuid.New() }, store.ErrBotConflict},
		{"owner changed", func(f *botFactoryFixture, _ *botFactoryCallbackRepo) { f.req.Bot.OwnerID = uuid.New() }, store.ErrBotConflict},
		{"lease fenced", func(f *botFactoryFixture, _ *botFactoryCallbackRepo) { f.req.Run.Lease.Fence++ }, store.ErrBotConflict},
		{"lease expired", func(f *botFactoryFixture, _ *botFactoryCallbackRepo) {
			f.req.Run.Lease.ExpiresAt = time.Now().Add(-time.Second)
		}, store.ErrBotConflict},
		{"run stopped", func(f *botFactoryFixture, _ *botFactoryCallbackRepo) { f.req.Run.Status = store.RunCancelled }, store.ErrBotConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, repo := newBotFactoryCallbackFixture(t, store.BotScopePersonal)
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			tc.change(f, repo)
			if got, err := resources.PreviousWork(context.Background()); got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("history bypassed authority: %+v %v", got, err)
			}
			for _, kind := range []store.BotWorkContinuationKind{store.BotWorkContinueExistingSchedule, store.BotWorkContinueExistingApproval} {
				if err := resources.ValidateWorkContinuation(context.Background(), kind, uuid.New()); !errors.Is(err, tc.want) {
					t.Fatalf("%s bypassed authority: %v", kind, err)
				}
			}
			if len(repo.runPages) != 0 || len(repo.approvalPages) != 0 || repo.versionReads != 0 {
				t.Fatal("unauthorized callback reached scoped work data")
			}
		})
	}
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
	in := &botWiredInjection{scope: &BotChatScope{Services: f.services, Sessions: f.sessions}, safe: &BotChatSafeResources{WorkspaceDir: "/isolated"}, binding: wired.binding, llm: wired, contextWindow: 8192}
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
