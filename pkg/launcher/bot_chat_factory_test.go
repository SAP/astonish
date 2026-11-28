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
	"github.com/SAP/astonish/pkg/sandbox"
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

type presentationFactoryClaimStore struct {
	store.BotEmployeePresentationStore
	p *store.BotEmployeePresentation
}

func (s *presentationFactoryClaimStore) ReadEmployeePresentation(_ context.Context, q store.BotEmployeePresentationRequest) (*store.BotEmployeePresentation, error) {
	if s.p.ClaimToken != q.ClaimToken {
		return nil, store.ErrBotForbidden
	}
	return s.p, nil
}

type presentationFactoryModel struct {
	t      *testing.T
	digest string
	fail   bool
	calls  int
}

func (m *presentationFactoryModel) Name() string { return "original" }
func (m *presentationFactoryModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if len(req.Tools) != 0 || len(req.Config.Tools) != 0 || len(req.Contents) != 1 {
			m.t.Fatal("execution context or tools leaked")
		}
		if _, ok := execution.BotTurnAuthorizerFromContext(ctx); ok {
			m.t.Fatal("turn authorizer leaked")
		}
		if m.fail {
			yield(nil, errors.New("unavailable"))
			return
		}
		b, _ := json.Marshal(map[string]string{"source_digest": m.digest, "text": "The requested change is unavailable."})
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(b), "model")}, nil)
	}
}

func TestEmployeePresentationFactory(t *testing.T) {
	for _, mode := range []string{"ok", "historical-caps", "unlimited", "model-error", "foreign", "expired", "wrong-claim"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			run := f.req.Run
			run.Status = store.RunFailed
			run.UserEventID = "request"
			run.StateSequence = 1
			run.Report = store.BotReport{Summary: "immutable failure"}
			p := &store.BotEmployeePresentation{SchemaVersion: store.BotEmployeePresentationVersion, State: store.BotEmployeePresentationClaimed, BotID: run.BotID, RunID: run.ID, BotVersion: run.Version, OwnerID: f.req.Bot.OwnerID, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug, HomeSessionID: uuid.New(), SourceSessionID: run.SessionID, UserEventID: run.UserEventID, EffectiveLimits: store.BudgetSpec{MaxRuntimeSeconds: 60}, Deadline: time.Now().Add(time.Minute), AuthorityExpiresAt: time.Now().Add(time.Minute), Provider: "original-provider", Model: "original-model", ReportSequence: run.StateSequence, ReportDigest: run.Report.EmployeeMessageSourceDigest(run.Status), ClaimToken: "claim"}
			claimStore := &presentationFactoryClaimStore{p: p}
			q := BotEmployeePresentationRequest{Bot: f.req.Bot, Source: run, Claim: store.BotEmployeePresentationRequest{BotID: run.BotID, RunID: run.ID, ClaimToken: "claim"}, Store: claimStore, DispatchToken: "dispatch", Voice: "concise"}
			m := &presentationFactoryModel{t: t, digest: p.ReportDigest, fail: mode == "model-error"}
			factory, err := NewBotEmployeePresentationFactory(BotEmployeePresentationFactoryConfig{LoadScope: f.cfg.LoadScope, ResolvePinned: func(ctx context.Context, scope *BotChatScope, provider, name string) (model.LLM, error) {
				if provider != "original-provider" || name != "original-model" {
					t.Fatal("changed pin")
				}
				return m, nil
			}, WrapModel: func(ctx context.Context, llm model.LLM, b execution.RunBudget) (model.LLM, error) {
				if _, ok := b.(*bot.PresentationBudget); !ok {
					t.Fatal("fresh execution budget")
				}
				if !errors.Is(b.ReserveTool(ctx, "read_file"), store.ErrBotForbidden) {
					t.Fatal("tools permitted")
				}
				return llm, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "foreign":
				q.Bot.OwnerID = uuid.New()
			case "historical-caps":
				zero := int64(0)
				p.EffectiveLimits = store.BudgetSpec{MaxRuntimeSeconds: 1, MaxTokens: &zero, MaxModelCostMicros: &zero}
				p.Deadline = time.Now().Add(-time.Hour)
			case "unlimited":
				p.EffectiveLimits = store.BudgetSpec{}
				p.Deadline = time.Time{}
			case "expired":
				p.AuthorityExpiresAt = time.Now().Add(-time.Second)
			case "wrong-claim":
				q.Claim.ClaimToken = "foreign"
			}
			text, err := factory(f.ctx, q)
			wantSuccess := mode == "ok" || mode == "historical-caps" || mode == "unlimited"
			if (err == nil) != wantSuccess || err != nil && text != "" {
				t.Fatalf("text=%q err=%v", text, err)
			}
			if f.safeCleaned != 0 || len(f.resolver.ids) != 0 {
				t.Fatal("sandbox or current session resolver called")
			}
			if !wantSuccess && mode != "model-error" && m.calls != 0 {
				t.Fatal("denied claim called model")
			}
		})
	}
}

func TestEmployeeWorkerFactoryTools(t *testing.T) {
	f := newBotFactoryFixture(t)
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	if got.WorkerFactory == nil || got.Agent.ToolIndex.GetToolEntry("employee_chat") == nil {
		t.Fatal("missing Employee orchestration")
	}
	if got.Agent.SubAgentManager != nil || len(got.Agent.Toolsets) != 0 {
		t.Fatal("Employee retains execution manager/toolsets")
	}
	for _, name := range []string{"execute_tool", "search_tools", "describe_tools", "shell_command", "read_file", "delegate_tasks", "run_flow", "run_drill"} {
		if got.Agent.ToolIndex.GetToolEntry(name) != nil {
			t.Fatalf("Employee execution leak %s", name)
		}
	}
	ss := session.InMemoryService()
	id := uuid.NewString()
	if _, err := ss.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: id}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*api.BotEmployeeWorkerRequest){
		func(q *api.BotEmployeeWorkerRequest) { q.Request.Run.ID = uuid.New() },
		func(q *api.BotEmployeeWorkerRequest) { q.Request.Run.SessionID = uuid.New() },
		func(q *api.BotEmployeeWorkerRequest) { q.Request.Budget = nil },
		func(q *api.BotEmployeeWorkerRequest) { q.Sessions = nil },
		func(q *api.BotEmployeeWorkerRequest) { q.ConversationID = "" },
	} {
		q := api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: id, Sessions: ss}
		mutate(&q)
		if _, err := got.WorkerFactory(f.ctx, q); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("foreign worker request accepted: %v", err)
		}
	}
	worker, err := got.WorkerFactory(f.ctx, api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: id, Sessions: ss})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Cleanup()
	for _, name := range []string{"ask_user", "employee_chat"} {
		if worker.Agent.ToolIndex.GetToolEntry(name) != nil {
			t.Fatalf("private worker exposes %s", name)
		}
	}
	if worker.Agent.ToolIndex.GetToolEntry("read_file") == nil {
		t.Fatal("worker has no real shared file tool")
	}
	if worker.SessionService != ss || worker.Agent.SessionService != f.sessions || f.req.Run.SessionID.String() == id {
		t.Fatal("conversation/execution identities conflated")
	}
}

func TestEmployeeWorkerConfiguredLazyMCPIndex(t *testing.T) {
	f := newBotFactoryFixture(t)
	setBotSharedInteractive(f)
	f.req.Principal.Scopes = []string{"chat", "tool:execute"}
	f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
	f.req.Run.CallerCeiling.Principal = f.req.Principal
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(c context.Context, q bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(c, q, s)
		if safe != nil {
			safe.Catalog.Pool = sandbox.NewSingleClientPool(&sourceAccessCatalogueClient{}, nil)
		}
		return safe, err
	}
	f.services.MCPServers = newMemMCPStore(store.MCPServer{Name: "cached-private", Transport: "streamableHTTP", URL: "https://mcp.example.test/", CachedTools: json.RawMessage(`[{"name":"lookup_signal","description":"Look up violet","inputSchema":{"type":"object"}}]`)})
	got, err := f.construct(t)
	if err != nil {
		var failure *bot.RunError
		if errors.As(err, &failure) {
			t.Fatalf("construction cause: %v", failure.Cause)
		}
		t.Fatal(err)
	}
	defer got.Cleanup()
	ss := session.InMemoryService()
	id := uuid.NewString()
	if _, err := ss.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: id}); err != nil {
		t.Fatal(err)
	}
	worker, err := got.WorkerFactory(f.ctx, api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: id, Sessions: ss})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Cleanup()
	entry := worker.Agent.ToolIndex.GetToolEntry("lookup_signal")
	if entry == nil {
		t.Fatal("configured lazy MCP lost from private worker index")
	}
	shared, ok := entry.Tool.(*chatRuntimeTool)
	if !ok || shared.source == nil || entry.GroupName != "mcp:cached-private" {
		t.Fatalf("private MCP lost guarded executable/group: %+v", entry)
	}
	for _, name := range []string{"ask_user", "employee_chat", botProposalToolName} {
		if worker.Agent.ToolIndex.GetToolEntry(name) != nil {
			t.Fatalf("private index reintroduced %s", name)
		}
	}
}

func TestEmployeeWorkerDiscoveryPreservesMetadataReceipts(t *testing.T) {
	f := newBotFactoryFixture(t)
	setBotSharedInteractive(f)
	f.ctx, _ = bot.WithToolOutcomes(f.ctx)
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	ss := session.InMemoryService()
	id := uuid.NewString()
	if _, err := ss.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: id}); err != nil {
		t.Fatal(err)
	}
	worker, err := got.WorkerFactory(f.ctx, api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: id, Sessions: ss})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Cleanup()
	ledger := botFactoryModelForTest(t, worker.Agent.LLM).binding.owner
	helpers := 0
	for _, helper := range worker.Agent.Tools {
		if helper.Name() != "search_tools" && helper.Name() != "describe_tools" {
			continue
		}
		helpers++
		ctx := employeeInteractiveToolContext{botFactoryHandoffContext{base: f.ctx, session: id, functionCallID: helper.Name()}}
		request := &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
		if err := helper.(botRunnableTool).ProcessRequest(ctx, request); err != nil || request.Tools[helper.Name()] != helper {
			t.Fatalf("private %s registration lost sealed executable: %v", helper.Name(), err)
		}
		args := map[string]any{"query": "read_file"}
		if helper.Name() == "describe_tools" {
			args = map[string]any{"names": []string{"read_file"}}
		}
		out, err := helper.(botRunnableTool).Run(ctx, args)
		if err != nil {
			t.Fatalf("private %s failed: %v", helper.Name(), err)
		}
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), "read_file") {
			t.Fatalf("private %s lost shared read tool: %s", helper.Name(), raw)
		}
		outcome, observed := bot.TakeToolOutcome(ledger, ctx.FunctionCallID())
		if !observed || !outcome.Verified || len(outcome.Unchanged) != 1 || len(outcome.Changed) != 0 {
			t.Fatalf("private %s lost sealed metadata receipt: observed=%v outcome=%+v", helper.Name(), observed, outcome)
		}
		cancelled, cancel := context.WithCancel(f.ctx)
		cancel()
		ctx.base = cancelled
		if _, err := helper.(botRunnableTool).Run(ctx, args); err == nil {
			t.Fatalf("private %s ignored cancellation", helper.Name())
		}
		if _, observed := bot.TakeToolOutcome(ledger, ctx.FunctionCallID()); observed {
			t.Fatalf("cancelled %s minted metadata receipt", helper.Name())
		}
	}
	if helpers != 2 {
		t.Fatalf("expected both private discovery helpers, got %d", helpers)
	}
}

func TestEmployeeWorkerInteractiveApprovalBoundary(t *testing.T) {
	f := newBotFactoryFixture(t)
	setBotSharedInteractive(f)
	guard := &employeeInteractiveApprovalGuard{}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if safe != nil {
			safe.PolicyRuntime.Guard = guard
		}
		return safe, err
	}
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	if f.req.Policy != nil || got.Authorization != f.req.Authorization {
		t.Fatal("interactive authority replaced with autonomy policy")
	}
	if got.Agent.ToolIndex.GetToolEntry("employee_chat") == nil || got.Agent.ToolIndex.GetToolEntry("read_file") != nil {
		t.Fatal("production Employee capability restriction lost")
	}
	ss := session.InMemoryService()
	id := uuid.NewString()
	if _, err := ss.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: id}); err != nil {
		t.Fatal(err)
	}
	worker, err := got.WorkerFactory(f.ctx, api.BotEmployeeWorkerRequest{Request: f.req, ConversationID: id, Sessions: ss})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Cleanup()
	if worker.Agent.ToolIndex.GetToolEntry("employee_chat") != nil || worker.Agent.ToolIndex.GetToolEntry("ask_user") != nil {
		t.Fatal("worker can impersonate Employee or ask owner directly")
	}
	entry := worker.Agent.ToolIndex.GetToolEntry("read_file")
	if entry == nil {
		t.Fatal("missing production worker file tool")
	}
	target, ok := entry.Tool.(botRunnableTool)
	if !ok {
		t.Fatal("production worker file tool is not runnable")
	}
	out, err := target.Run(employeeInteractiveToolContext{botFactoryHandoffContext{base: f.ctx, session: id, functionCallID: "private-exact-read"}}, map[string]any{"path": "/isolated/source.txt"})
	var approval *agent.BotPolicyError
	if !errors.As(err, &approval) || !approval.ApprovalRequired() || guard.calls != 1 {
		t.Fatalf("interactive worker bypassed genuine exact-action approval: output=%v error=%v guard_calls=%d", out, err, guard.calls)
	}
}

type employeeInteractiveToolContext struct{ botFactoryHandoffContext }

func (employeeInteractiveToolContext) AgentName() string { return "private-worker" }

type employeeInteractiveApprovalGuard struct{ calls int }

func (g *employeeInteractiveApprovalGuard) Authorize(_ context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	g.calls++
	digest, err := action.Digest()
	return execution.PolicyDecision{Kind: execution.PolicyRequestApproval, ActionDigest: digest, Reason: "exact private source requires owner review"}, err
}

type workCapabilityFixtureGuard struct{}

func (workCapabilityFixtureGuard) Authorize(_ context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	digest, err := action.Digest()
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, err
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
					return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}, PolicyRuntime: agent.BotPolicyRuntime{Guard: workCapabilityFixtureGuard{}, Classifier: BotCatalogueClassifier{}}}, nil
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
			got, err := f.constructWorker(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			args := map[string]map[string]any{
				botWorkReadTool: {}, botWorkUpdateTool: {"expected_revision": float64(0), "kind": "set_remaining", "remaining": []any{map[string]any{"summary": "pending", "reason": "source unavailable", "continuation": "none"}}},
				botEvidenceReadTool: {"call_id": "earlier"}, botCheckWorkTool: {"question": "supported?", "draft_text": "draft"},
			}
			ctx := botFactoryHandoffContext{base: context.Background(), session: uuid.NewSHA1(f.req.Run.ID, []byte("private-fixture-worker")).String(), functionCallID: "trusted-call"}
			byName := map[string]botRunnableTool{}
			for _, name := range names {
				if entry := got.Agent.ToolIndex.GetToolEntry(name); entry != nil {
					byName[name], _ = entry.Tool.(botRunnableTool)
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
			if out, err := byName[botWorkUpdateTool].Run(ctx, map[string]any{"expected_revision": float64(0), "kind": "set_delivery", "delivery_intent": "file_deliverable", "deliverable_format": "markdown"}); err != nil || out["status"] != "ok" || probe.change.DeliveryIntent != store.BotDeliveryFileDeliverable || probe.change.DeliverableFormat != store.BotDeliverableMarkdown {
				t.Fatalf("lost explicit delivery intent: %v %v %+v", out, err, probe.change)
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
					if child {
						if err == nil || probe.calls != before {
							t.Fatal("child discovery escaped private worker conversation")
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

func TestBotTitleUsesSharedChatRuntime(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.ctx = bot.WithInteractionOwner(f.ctx)
	f.req.Instructions = "AI news"
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
	f.req.Authorization = interactiveFactoryAuthorization{}
	f.req.Policy = nil
	f.req.Run.UserEventID = "title-event"
	f.req.Run.TitleConfirmation = &store.BotTitleConfirmation{
		Version: store.BotTitleConfirmationVersion, State: store.BotTitleConfirmationNeedsQuestion,
		BotID: f.req.Bot.ID, RunID: f.req.Run.ID, OwnerID: f.req.Bot.OwnerID,
		OriginalTitle: "AI news", UserEventID: "title-event",
	}
	repo := &titleJourneyStore{clarificationStore: clarificationStore{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}}
	f.services.Bots, f.services.PersonalBots = repo, repo
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		t.Fatal("title clarification provisioned execution resources")
		return nil, nil
	}
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	if got.Agent.FlowDistiller == nil || got.Agent.ToolIndex == nil || got.Agent.PendingSecrets == nil {
		t.Fatal("title skipped shared Chat composition")
	}
	if len(got.Agent.Tools) != 1 || got.Agent.Tools[0].Name() != "ask_user" || len(got.Agent.Toolsets) != 0 {
		t.Fatal("title exposed more than the question capability")
	}
	got.Cleanup()
	if f.scopeCleaned != 1 || f.safeCleaned != 0 || f.resolver.model.calls != 0 {
		t.Fatalf("title lifetime leaked or started work: scope=%d compute=%d model=%d", f.scopeCleaned, f.safeCleaned, f.resolver.model.calls)
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
	files                              *confinedTestFiles
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
	f.files = &confinedTestFiles{root: t.TempDir(), snapshots: map[string][]byte{}}
	for name, content := range map[string]string{"SOUL.md": v.Persona.Soul, "IDENTITY.md": v.Persona.Identity, "USER.md": v.Persona.User, "CHARTER.md": v.Charter, "MEMORY.md": "# Memory\n"} {
		if _, err := f.files.Write(f.ctx, f.req.Bot.ID, store.BotFileWrite{Path: name, Body: strings.NewReader(content), SizeBytes: int64(len(content)), CreateOnly: true}); err != nil {
			t.Fatal(err)
		}
	}
	f.services.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return f.files, nil }
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
			return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &sharedBotDispatchPool{client: &sourceAccessCatalogueClient{response: json.RawMessage(`{"content":"fixture"}`)}}}, WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { f.safeCleaned++ }}, nil
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
			if resources.PreviousWork != nil || len(repo.runPages) != 0 {
				t.Fatal("historical work must be retrieved explicitly, not bootstrapped")
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
	for _, count := range []int{botWorkHistoryMaxPages*botWorkHistoryPageSize - 1, botWorkHistoryMaxPages * botWorkHistoryPageSize, botWorkHistoryMaxPages*botWorkHistoryPageSize + 1} {
		f, repo := newBotFactoryCallbackFixture(t, store.BotScopePersonal)
		resources, err := f.construct(t)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(resources.Cleanup)
		for i := 0; i < count; i++ {
			repo.approvals = append(repo.approvals, store.BotApproval{ID: uuid.New(), BotID: f.req.Bot.ID})
		}
		err = resources.ValidateWorkContinuation(context.Background(), store.BotWorkContinueExistingApproval, uuid.New())
		if len(repo.approvalPages) != botWorkHistoryMaxPages {
			t.Fatalf("count %d: pages=%d", count, len(repo.approvalPages))
		}
		want := error(store.ErrBotForbidden)
		if count >= botWorkHistoryMaxPages*botWorkHistoryPageSize {
			want = errBotWorkHistoryExhausted
		}
		if !errors.Is(err, want) {
			t.Fatalf("count %d: %v, want %v", count, err, want)
		}
	}
}

func TestBotAgentFactoryWorkHistoryCancellation(t *testing.T) {
	for _, mode := range []string{"cancelled", "cancel during read", "caller deadline", "lookup timeout"} {
		t.Run(mode, func(t *testing.T) {
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
			}
			err = resources.ValidateWorkContinuation(ctx, store.BotWorkContinueExistingApproval, uuid.New())
			if !errors.Is(err, want) {
				t.Fatalf("error=%v, want %v", err, want)
			}
			if mode == "cancelled" && len(repo.approvalPages) != 0 {
				t.Fatal("cancelled callback read history")
			}
		})
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
			if err := resources.ValidateWorkContinuation(context.Background(), store.BotWorkContinueExistingApproval, uuid.New()); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("invalid approval page: %v", err)
			}
			wantPages := 1
			if mode == "repeated page" {
				wantPages = 2
			}
			if len(repo.runPages) != 0 || len(repo.approvalPages) != wantPages {
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
	if a.Agent == b.Agent || a.Agent.ToolIndex == b.Agent.ToolIndex || a.Agent.Compactor == b.Agent.Compactor || a.Agent.Redactor == b.Agent.Redactor || a.Agent.PendingSecrets == b.Agent.PendingSecrets || a.Agent.LLM == b.Agent.LLM {
		t.Fatal("mutable run resources shared")
	}
	if a.Agent.SessionService != f.sessions || a.SessionService != f.sessions || a.Agent.AutoApprove {
		t.Fatal("session/approval invariant changed")
	}
	if a.Agent.SubAgentManager != nil || b.Agent.SubAgentManager != nil || a.WorkerFactory == nil || b.WorkerFactory == nil {
		t.Fatal("Employee must delegate only through the private worker factory")
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
			if scope == store.BotScopeTeam {
				if store.CredentialStoreFromContext(observed) != team {
					t.Fatal("team credentials lost or personal scope inherited")
				}
			} else {
				merged, ok := store.CredentialStoreFromContext(observed).(*store.MergedCredentialStore)
				if !ok || merged.Team != team || merged.Personal != personal {
					t.Fatal("owner credentials lost")
				}
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
	res, err := f.constructWorker(t)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Cleanup()
	if res.Agent.SubAgentManager.ToolIndex == nil {
		t.Fatal("missing per-run index")
	}
	// The always-installed delegate is a function tool whose ProcessRequest
	// normally stores the underlying function-tool receiver in req.Tools.
	wired := botFactoryModelForTest(t, res.Agent.LLM)
	in := &botChatDependencies{scope: &BotChatScope{Services: f.services, Sessions: f.sessions}, safe: &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated"}, binding: wired.binding, llm: wired, contextWindow: 8192}
	result, err := newBotScopedChatRuntime(f.ctx, &ChatFactoryConfig{}, in)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Cleanup()
	request := &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
	wrapped := result.ChatAgent.ToolIndex.GetToolEntry("delegate_tasks").Tool.(*chatRuntimeTool)
	ctx := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}}
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
