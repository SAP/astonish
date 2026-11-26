package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sharedBotDispatchPool struct {
	catalogTestPool
	client sandbox.ToolNodeClient
}

func (*sharedBotDispatchPool) SetSessionScope(string, string, string)      {}
func (p *sharedBotDispatchPool) GetOrCreate(string) sandbox.ToolNodeClient { return p.client }
func (p *sharedBotDispatchPool) ResolveOwner(id string) string             { return id }

type sharedBotScriptModel struct{ calls int }

func (*sharedBotScriptModel) Name() string { return "scripted-offline" }
func (m *sharedBotScriptModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(y func(*model.LLMResponse, error) bool) {
		m.calls++
		content := genai.NewContentFromText("Read the retained file successfully.", "model")
		if m.calls == 1 {
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "ordinary-read", Name: "execute_tool", Args: map[string]any{"name": "read_file", "arguments": map[string]any{"path": "outputs/report.txt"}}}}}}
		}
		y(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}

type sharedBotToolContext struct{ proposalTestContext }

func (sharedBotToolContext) AgentName() string { return "chat" }
func TestBotSharedRuntimeOfflineModelTurn(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			if interactive {
				setBotSharedInteractive(f)
			}
			llm := &sharedBotScriptModel{}
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			client := &sourceAccessCatalogueClient{response: json.RawMessage(`{"content":"retained ordinary bytes"}`)}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(c context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(c, r, s)
				if err == nil {
					safe.Catalog.Pool = &sharedBotDispatchPool{client: client}
				}
				return safe, err
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil || result.Status != store.RunSucceeded || llm.calls != 2 || client.calls != 1 {
				t.Fatalf("turn failed: status=%s model=%d tool=%d err=%v", result.Status, llm.calls, client.calls, err)
			}
		})
	}
}

func TestBotSharedRuntimeScriptedDispatch(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			if interactive {
				setBotSharedInteractive(f)
			}
			c := &sourceAccessCatalogueClient{response: json.RawMessage(`{"content":"shared ordinary bytes","total_lines":1}`)}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				safe, e := prepare(ctx, r, s)
				if e != nil {
					return nil, e
				}
				safe.Catalog = &ChatCatalogResources{Pool: &sharedBotDispatchPool{client: c}}
				return safe, nil
			}
			got, e := f.construct(t)
			if e != nil {
				t.Fatal(e)
			}
			defer got.Cleanup()
			entry := got.Agent.ToolIndex.GetToolEntry("read_file")
			if entry == nil {
				t.Fatal("no actual file tool")
			}
			invocation := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}}
			req := &model.LLMRequest{}
			target := entry.Tool.(botRunnableTool)
			if e := target.ProcessRequest(invocation, req); e != nil {
				t.Fatal(e)
			}
			dispatched, ok := req.Tools["read_file"].(*chatRuntimeTool)
			if !ok {
				t.Fatalf("declaration bypassed shared wrapper: %T", req.Tools["read_file"])
			}
			result, e := dispatched.Run(invocation, map[string]any{"path": "outputs/report.txt"})
			if e != nil {
				t.Fatal(e)
			}
			if result["content"] != "shared ordinary bytes" || c.calls != 1 || c.session != f.req.Run.SessionID.String() {
				t.Fatalf("ordinary dispatch mismatch: %+v %+v", result, c)
			}
			f.req.Run.Lease.Fence++
			if _, e := dispatched.Run(invocation, map[string]any{"path": "outputs/report.txt"}); e == nil || c.calls != 1 {
				t.Fatalf("stale lease dispatched: %v calls=%d", e, c.calls)
			}
		})
	}
}

func TestBotSharedInteractiveConfiguredSearchClassifier(t *testing.T) {
	f := newBotFactoryFixture(t)
	setBotSharedInteractive(f)
	knowledgeSearchSettings(f)
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err != nil {
			return nil, err
		}
		safe.ChatConfig.General.WebSearchTool = "perplexity:perplexity_web_search"
		safe.ChatConfig.PerplexityWebSearch.Provider = "search"
		safe.ChatConfig.PerplexityWebSearch.Model = "sonar"
		safe.ChatConfig.Providers = map[string]config.ProviderConfig{"search": {"api_key": "team-secret"}}
		safe.ProviderFactory = func(context.Context, string, string, *config.AppConfig) (model.LLM, error) {
			return &botFactoryTestModel{}, nil
		}
		return safe, nil
	}
	got, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Cleanup()
	classifier := got.PolicyRuntime.Classifier
	name := "configured_search/perplexity_web_search"
	action, err := classifier.DescribeAction(f.ctx, name, json.RawMessage(`{"query":"Tesla pricing news","max_results":5}`))
	if err != nil || action.Tool != name || action.Effect != execution.EffectRead || !action.ConstraintsVerified || action.TargetFingerprint == "" {
		t.Fatalf("interactive configured search not bound read: %+v err=%v", action, err)
	}
}

func TestBotSharedRuntimeWriteClassificationConservative(t *testing.T) {
	c := botSharedFileClassifier{fallback: BotFileActionClassifier{}}
	for _, name := range []string{"write_file", "edit_file"} {
		a, e := c.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
		if e != nil || a.ConstraintsVerified || a.Effect != execution.EffectUnknown {
			t.Fatalf("ordinary write inherited confined grant: %+v %v", a, e)
		}
	}
	for _, name := range []string{"read_file", "find_files", "file_tree", "web_fetch"} {
		a, e := c.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
		if e != nil || !a.ConstraintsVerified || a.Effect != execution.EffectRead {
			t.Fatalf("shared read not classified: %s %+v %v", name, a, e)
		}
	}
}

func setBotSharedInteractive(f *botFactoryFixture) {
	f.req.Policy = nil
	f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
	f.req.Authorization = interactiveFactoryAuthorization{}
}

// Both execution modes must reach the shared wrapper and the actual Chat node
// tool, not a Bot filesystem implementation with a different schema.
type sharedFetchNode struct {
	catalogueClient
	response json.RawMessage
	name     string
}

func (c *sharedFetchNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	c.calls++
	c.session = id
	c.name = name
	return c.response, nil
}

func TestBotSharedRuntimePublicFetchReceipt(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			policy, e := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"web_fetch"}})
			if e != nil {
				t.Fatal(e)
			}
			f.req.Policy = policy
			if interactive {
				setBotSharedInteractive(f)
			}
			c := &sharedFetchNode{response: json.RawMessage(`{"read_executed":true,"response":{"url":"https://example.com/a","final_url":"https://example.com/a","status":429,"error":"HTTP 429"}}`)}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				safe, e := prepare(ctx, r, s)
				if e == nil {
					safe.Catalog.Pool = &sharedBotDispatchPool{client: c}
				}
				return safe, e
			}
			got, e := f.construct(t)
			if e != nil {
				t.Fatal(e)
			}
			defer got.Cleanup()
			shared := got.Agent.ToolIndex.GetToolEntry("web_fetch").Tool.(*chatRuntimeTool)
			if _, ok := shared.chatRuntimeRunnable.(*sandbox.NodeTool); !ok {
				t.Fatal("not actual shared node")
			}
			base, _ := bot.WithToolOutcomes(execution.WithBotRunIdentity(f.ctx, policy.Identity()))
			ctx := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: f.req.Run.SessionID.String()}}
			result, e := shared.Run(ctx, map[string]any{"url": "https://example.com/a"})
			if e != nil || result["error"] != "HTTP 429" || c.name != tools.BotPublicFetchOperation {
				t.Fatalf("fetch did not use first-party transport: %v %v name=%s", result, e, c.name)
			}
			outcome, ok := bot.TakeToolOutcome(botFactoryModelForTest(t, got.Agent.LLM).binding.owner, ctx.FunctionCallID())
			if !ok || len(outcome.SourceAccess) != 1 || outcome.SourceRead == nil || outcome.ReceiptConflict {
				t.Fatalf("missing genuine receipt: %+v ok=%v", outcome, ok)
			}
		})
	}
}

func TestBotSharedRuntimeOrdinaryToolIdentity(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			if interactive {
				setBotSharedInteractive(f)
			}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(c context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(c, r, s)
				if err != nil {
					return nil, err
				}
				safe.ChatConfig = &config.AppConfig{}
				safe.Catalog = &ChatCatalogResources{Pool: &catalogTestPool{}}
				safe.Tools = nil
				return safe, nil
			}
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			for _, name := range []string{"read_file", "write_file", "edit_file", "find_files", "file_tree"} {
				var found tool.Tool
				if entry := got.Agent.ToolIndex.GetToolEntry(name); entry != nil {
					found = entry.Tool
				}
				shared, ok := found.(*chatRuntimeTool)
				if !ok {
					t.Fatalf("%s did not use shared runtime: %T", name, found)
				}
				if _, ok := shared.chatRuntimeRunnable.(*sandbox.NodeTool); !ok {
					t.Fatalf("%s replaced actual Chat tool: %T", name, shared.chatRuntimeRunnable)
				}
			}
		})
	}
}

const recoveredFetchAnswer = "The alternate source reports 42 units."

type recoveredFetchModel struct {
	t     *testing.T
	calls int
}

func (*recoveredFetchModel) Name() string { return "recovered-offline" }
func (m *recoveredFetchModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(y func(*model.LLMResponse, error) bool) {
		m.calls++
		content := genai.NewContentFromText(recoveredFetchAnswer, "model")
		raw, _ := json.Marshal(req.Contents)
		if m.calls == 1 || m.calls == 2 {
			url := "https://example.com/primary"
			if m.calls == 2 {
				url = "https://example.com/alternative"
				if !strings.Contains(string(raw), "HTTP 429") {
					m.t.Fatal("failed fetch not returned to producer")
				}
			}
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("fetch-%d", m.calls), Name: "execute_tool", Args: map[string]any{"name": "web_fetch", "arguments": map[string]any{"url": url}}}}}}
		} else if m.calls != 3 || !strings.Contains(string(raw), "42 units") {
			m.t.Fatalf("unexpected producer continuation: calls=%d contents=%s", m.calls, raw)
		}
		y(&model.LLMResponse{Content: content, TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}
}
func TestBotSharedRuntimeFailedFetchAlternateCommittedAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			b, err := repo.Get(f.ctx, f.req.Bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			v, err := repo.GetVersion(f.ctx, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.RequestRun(f.ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Fetch the primary then an alternate source, and report the observed count in Home.", IdempotencyKey: "recovered-fetch"})
			if err != nil {
				t.Fatal(err)
			}
			id := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
			worker := execution.WithBotRunIdentity(f.ctx, id)
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "controlled", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			run, err = repo.GetRun(f.ctx, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"web_fetch"}})
			if err != nil {
				t.Fatal(err)
			}
			meter, err := bot.NewRunBudget(v.Budget, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.req.Bot, f.req.Version, f.req.Run, f.req.Policy, f.req.Budget, f.req.Instructions = *b, *v, *run, policy, meter, "Fetch the primary then an alternate source, and report the observed count in Home."
			f.req.Work, err = repo.(store.BotWorkStore).LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if scope == store.BotScopePersonal {
				f.services.PersonalBots = repo
				f.sessions = f.services.PersonalSessions
			} else {
				f.services.Bots = repo
				f.sessions = f.services.Sessions
			}
			f.services.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return f.files, nil }
			f.services.Memory = nil
			llm := &recoveredFetchModel{t: t}
			f.cfg.WrapModel = func(_ context.Context, _ model.LLM, budget execution.RunBudget) (model.LLM, error) {
				return provider.NewBudgetLLM(llm, budget, provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
			}
			fetches := 0
			node := &remainingModeNode{call: func(name string, args map[string]any) (json.RawMessage, error) {
				fetches++
				if name != tools.BotPublicFetchOperation {
					return nil, fmt.Errorf("not first-party operation: %s", name)
				}
				response := map[string]any{"url": args["url"], "final_url": args["url"], "status": 200, "content": "42 units"}
				if fetches == 1 {
					response["status"] = 429
					response["error"] = "HTTP 429"
					response["content"] = "rate limited"
				} else if fetches != 2 || args["url"] != "https://example.com/alternative" {
					t.Fatalf("retry or wrong alternate: %v", args)
				}
				return json.Marshal(map[string]any{"read_executed": true, "response": response})
			}}
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &remainingModePool{client: node}}, WorkspaceDir: t.TempDir(), PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(bot.WithApprovalStore(f.ctx, repo), f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != store.RunSucceeded || result.Report.Summary != recoveredFetchAnswer || fetches != 2 || llm.calls != 3 {
				t.Fatalf("recovery did not complete: %+v calls=%d fetches=%d", result, llm.calls, fetches)
			}
			assertLauncherAgentResult(t, repo, worker, lease, result)
			work, err := repo.(store.BotWorkStore).LoadWork(worker, lease)
			if err != nil || work.AgentWork == nil || len(work.AgentWork.SourceAccess) != 2 {
				t.Fatalf("missing persisted accesses: %+v %v", work, err)
			}
			if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report, Usage: meter.Usage()}); err != nil {
				t.Fatal(err)
			}
			publisher := &bot.Publisher{Store: repo.(store.BotPublicationStore)}
			publication, err := publisher.Publish(f.ctx, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(publication.Text, recoveredFetchAnswer) || publication.HomeSessionID != b.HomeSessionID {
				t.Fatalf("committed answer lost: %+v", publication)
			}
			replay, err := publisher.Publish(f.ctx, b.ID, run.ID)
			if err != nil || replay.EventID != publication.EventID {
				t.Fatal("publication duplicated")
			}
			transcript := f.services.Sessions
			if scope == store.BotScopePersonal {
				transcript = f.services.PersonalSessions
			}
			events, err := transcript.ReadTranscriptEvents(f.ctx, "astonish", b.OwnerID.String(), b.HomeSessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event.ID == publication.EventID {
					count++
					if event.Content.Parts[0].Text != publication.Text {
						t.Fatal("wrong committed transcript answer")
					}
				}
			}
			if count != 1 {
				t.Fatalf("publication count=%d", count)
			}
		})
	}
}

func TestBotSharedRuntimePublicFetchNativeFailures(t *testing.T) {
	for _, kind := range []string{"rpc", "readiness", "envelope", "cancel", "lease", "budget", "policy"} {
		t.Run(kind, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"web_fetch"}})
			if err != nil {
				t.Fatal(err)
			}
			f.req.Policy = policy
			failure := errors.New("native transport failure")
			node := &sharedFetchFailureNode{kind: kind, failure: failure}
			prepare := f.cfg.Prepare
			f.cfg.Prepare = func(c context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
				safe, err := prepare(c, r, s)
				if err == nil {
					safe.Catalog.Pool = &sharedBotDispatchPool{client: node}
				}
				return safe, err
			}
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			target := got.Agent.ToolIndex.GetToolEntry("web_fetch").Tool.(*chatRuntimeTool)
			binding := botFactoryModelForTest(t, got.Agent.LLM).binding
			expectedCalls := 1
			base, cancel := context.WithCancel(f.ctx)
			defer cancel()
			switch kind {
			case "cancel":
				cancel()
				expectedCalls = 0
			case "lease":
				f.req.Run.Lease.Fence++
				expectedCalls = 0
			case "policy":
				binding.req.Policy = fPolicy(t, policy.Identity(), []string{"read_file"})
				expectedCalls = 0
			case "budget":
				binding.req.Budget = &sharedFetchDeniedBudget{}
				expectedCalls = 0
			case "readiness":
				expectedCalls = 0
			}
			ctx := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: f.req.Run.SessionID.String()}}
			_, err = target.Run(ctx, map[string]any{"url": "https://example.com/a"})
			if err == nil || node.calls != expectedCalls {
				t.Fatalf("native failure became recoverable or dispatched: %v calls=%d", err, node.calls)
			}
			outcome, ok := bot.TakeToolOutcome(binding.owner, ctx.FunctionCallID())
			if ok && outcome.SourceRead != nil {
				t.Fatalf("native failure acquired receipt: %+v", outcome)
			}
			if (kind == "rpc" || kind == "readiness") && !errors.Is(err, failure) {
				t.Fatalf("lost native identity: %v", err)
			}
		})
	}
}
func fPolicy(t *testing.T, id execution.BotRunIdentity, names []string) *execution.RunPolicy {
	t.Helper()
	p, e := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: names})
	if e != nil {
		t.Fatal(e)
	}
	return p
}

type sharedFetchFailureNode struct {
	catalogueClient
	kind    string
	failure error
}

func (c *sharedFetchFailureNode) EnsureReady(string) error {
	if c.kind == "readiness" {
		return c.failure
	}
	return nil
}
func (c *sharedFetchFailureNode) Call(_, _ string, _ map[string]any) (json.RawMessage, error) {
	c.calls++
	if c.kind == "rpc" {
		return nil, c.failure
	}
	return json.RawMessage(`{"read_executed":false,"response":{}}`), nil
}
func TestChatRuntimeOrdinaryPublicFetchUnchanged(t *testing.T) {
	client := &sharedFetchNode{response: json.RawMessage(`{"error":"HTTP 429"}`)}
	f := newBotFactoryFixture(t)
	inputs, err := chatruntime.Build(f.ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopePersonal, AppConfig: &config.AppConfig{}, Pool: &sharedBotDispatchPool{client: client}, LLM: &botFactoryTestModel{}, FallbackPolicy: chatruntime.FallbackForbidden})
	if err != nil {
		t.Fatal(err)
	}
	defer inputs.Close()
	runtime, err := NewScopedChatRuntime(f.ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: &botFactoryTestModel{}, Sessions: session.InMemoryService(), Resources: &ChatCatalogResources{Pool: &sharedBotDispatchPool{client: client}}, ContextWindow: 8192}, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Cleanup()
	ctx := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: "ordinary-session"}}
	result, err := runtime.ChatAgent.ToolIndex.GetToolEntry("web_fetch").Tool.(botRunnableTool).Run(ctx, map[string]any{"url": "https://example.com/a"})
	if err != nil || result["error"] != "HTTP 429" || client.name != "web_fetch" {
		t.Fatalf("ordinary Chat changed: %v %v %s", result, err, client.name)
	}
}

type cachedCoreChildModel struct {
	t                       *testing.T
	parentCalls, childCalls int
}

func (*cachedCoreChildModel) Name() string { return "cached-core-child" }
func (m *cachedCoreChildModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(y func(*model.LLMResponse, error) bool) {
		raw, _ := json.Marshal(r.Contents)
		parent := strings.Contains(string(raw), "OWNER_CACHED_CORE_CHILD")
		content := genai.NewContentFromText("Core child returned retained bytes.", "model")
		var call *genai.FunctionCall
		if parent {
			m.parentCalls++
			if m.parentCalls == 1 {
				call = &genai.FunctionCall{ID: "core-delegate", Name: "delegate_tasks", Args: map[string]any{"tasks": []any{map[string]any{"name": "core-reader", "task": "Read outputs/report.txt using read_file and report retained bytes", "tools": []any{"read_file"}}}}}
			}
		} else {
			m.childCalls++
			if m.childCalls == 1 {
				call = &genai.FunctionCall{ID: "core-child-read", Name: "read_file", Args: map[string]any{"path": "outputs/report.txt"}}
			} else if !strings.Contains(string(raw), "retained bytes") {
				m.t.Fatalf("child missing read: %s", raw)
			}
		}
		if call != nil {
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}
		}
		y(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}
func TestBotSharedRuntimeCachedMCPCoreChildAcceptance(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Policy = fPolicy(t, f.req.Policy.Identity(), []string{"delegate_tasks", "read_file"})
	f.req.Instructions = "OWNER_CACHED_CORE_CHILD: delegate a core-only file read."
	llm := &cachedCoreChildModel{t: t}
	f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
	marker := filepath.Join(t.TempDir(), "mcp-started")
	lazy := agent.NewLazyMCPToolset("cached", []cache.ToolEntry{{Name: "cached_read", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}, config.MCPServerConfig{Command: "/bin/sh", Args: []string{"-c", "touch " + marker + "; exit 1"}, Env: map[string]string{"SECRET": "{{CREDENTIAL:must-not-resolve:secret}}"}}, false)
	set := agent.NewSanitizedToolset(lazy, false)
	node := &sourceAccessCatalogueClient{response: json.RawMessage(`{"content":"retained bytes"}`)}
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
		safe, e := prepare(ctx, r, s)
		if e == nil {
			safe.Catalog.Pool = &sharedBotDispatchPool{client: node}
			safe.Catalog.Groups = map[string]*agent.ToolGroup{"mcp:cached": {Name: "mcp:cached", Toolsets: []tool.Toolset{set}}}
		}
		return safe, e
	}
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
		resources, err := factory(ctx, req)
		if err != nil {
			return resources, err
		}
		group := resources.Agent.SubAgentManager.ToolGroups["mcp:cached"]
		if group == nil || len(group.Toolsets) != 1 {
			t.Fatal("cached toolset absent from actual factory child scope")
		}
		wrapped, ok := group.Toolsets[0].(*chatRuntimeToolset)
		if !ok || !agent.IsBotMetadataOnlyToolset(wrapped) {
			t.Fatalf("factory lost cached metadata contract: %T", group.Toolsets[0])
		}
		return resources, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || result.Status != store.RunSucceeded || llm.childCalls != 2 || node.calls != 1 {
		t.Fatalf("core delegation failed: status=%s parent=%d child=%d node=%d err=%v", result.Status, llm.parentCalls, llm.childCalls, node.calls, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MCP server started during core delegation: %v", err)
	}
}

type sharedFetchDeniedBudget struct{ botFactoryTestBudget }

func (*sharedFetchDeniedBudget) ReserveTool(context.Context, string) error {
	return bot.ErrBudgetExceeded
}
