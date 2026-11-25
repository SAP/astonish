package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type sharedFeedbackWorkRepo struct {
	store.BotStore
	*retainedResultWork
}

// Exercise the genuine shared catalog and bridge, including an invalid call
// before a corrected configured provider invocation and a captured final answer.
func TestBotSharedResearchToolErrorCorrection(t *testing.T) {
	f, quota, episode := researchFixture(t)
	f.req.Run.Intent = store.BotIntentRegular
	f.req.Run.IdempotencyKey = "owner-confirmed-correction"
	ref := quota.m.Ref
	f.req.Run.MandateRef = &ref
	f.req.Principal = f.req.Mandate.Authorization.Principal(f.req.Principal.Surface)
	f.ctx, _ = execution.WithPrincipal(context.Background(), f.req.Principal)
	var err error
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"perplexity_web_search", "web_fetch"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := BotScopedChatConfig(f.ctx, knowledgeScope(f))
	if err != nil {
		t.Fatal(err)
	}
	search := &knowledgeModel{text: "Controlled sourced answer https://evidence.example/source"}
	factoryCalls := 0
	prepare := f.cfg.Prepare
	f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
		safe, err := prepare(ctx, req, scope)
		if err == nil {
			safe.ChatConfig = cfg
			safe.Catalog = &ChatCatalogResources{Pool: &sharedBotDispatchPool{client: &sourceAccessCatalogueClient{response: []byte(`{"content":"fixture"}`)}}}
			safe.Research = episode
			safe.PolicyRuntime.Classifier = BotCatalogueClassifier{}
			safe.ProviderFactory = func(context.Context, string, string, *config.AppConfig) (model.LLM, error) {
				factoryCalls++
				return search, nil
			}
		}
		return safe, err
	}
	const answer = "Controlled sourced answer [source](https://evidence.example/source)."
	turn := func(name string, args map[string]any) *genai.Content {
		return &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
	}
	llm := &retainedResultModel{turns: []*genai.Content{
		turn("search_tools", map[string]any{"query": "perplexity_web_search"}),
		turn("describe_tools", map[string]any{"names": []any{"perplexity_web_search"}}),
		turn("execute_tool", map[string]any{"name": "perplexity_web_search", "arguments": map[string]any{"query": "Stock Market News overview", "max_results": -1}}),
		turn("execute_tool", map[string]any{"name": "perplexity_web_search", "arguments": map[string]any{"query": "Stock Market News overview", "max_results": 8, "detail_level": "detailed", "recency": "week", "search_mode": "news"}}),
		genai.NewContentFromText(answer, "model"),
	}}
	f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
	work := &retainedResultWork{state: store.NewBotAgentWorkState(store.BotWorkContract{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Run.Version, Instructions: f.req.Instructions})}
	f.req.Work = work.state.Clone()
	f.services.PersonalBots = sharedFeedbackWorkRepo{BotStore: f.services.PersonalBots, retainedResultWork: work}
	f.services.Bots = f.services.PersonalBots
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
		resources, err := factory(ctx, req)
		if err == nil {
			resources.Work = work
		}
		return resources, err
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || result.Status != store.RunSucceeded || result.Report.Summary != answer || len(result.Report.Unverified) != 0 || result.Report.Failure != nil {
		t.Fatalf("correction lost final: result=%+v err=%v", result, err)
	}
	if factoryCalls != 1 || search.calls != 1 || quota.calls != 1 || result.Usage.ToolCalls != 4 || work.state.EmployeeReviewAttempts != 0 {
		t.Fatalf("denial dispatched, replayed or added review: factory=%d search=%d quota=%d usage=%+v", factoryCalls, search.calls, quota.calls, result.Usage)
	}
	if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Text != answer || work.state.Candidate == nil || work.state.Candidate.Text != answer || work.state.EmployeeReviewAttempts != 0 {
		t.Fatalf("employee final not captured: %+v", result.Report)
	}
}
