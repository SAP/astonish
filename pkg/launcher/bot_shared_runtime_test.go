package launcher

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
	"iter"
	"testing"
	"time"
)

type sharedBotDispatchPool struct {
	catalogTestPool
	client *sourceAccessCatalogueClient
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
