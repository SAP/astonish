package launcher

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Observe the exact provider-bound request, not a prompt helper. The optional
// update runs through the production tool and work service between dispatches.
type agentWorkPromptModel struct {
	t                  *testing.T
	repo               *sourceRedactionRepo
	prompts            []string
	update             bool
	employeeDispatched bool
}

func (*agentWorkPromptModel) Name() string { return "agent-work-prompt-probe" }
func (m *agentWorkPromptModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if req.Config == nil || req.Config.SystemInstruction == nil {
			m.t.Fatal("missing provider system instruction")
		}
		employee := false
		for _, group := range req.Config.Tools {
			for _, declaration := range group.FunctionDeclarations {
				if declaration.Name == "employee_chat" {
					employee = true
				}
			}
		}
		if employee && m.update && !m.employeeDispatched {
			m.employeeDispatched = true
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "delegate-prompt-update", Name: "employee_chat", Args: map[string]any{"message": "Update the private work notes to updated-private-note using the scoped work service."}}}}}, TurnComplete: true}, nil)
			return
		}
		if employee && m.update {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("A bounded answer from the supplied evidence.", "model"), TurnComplete: true}, nil)
			return
		}
		var system strings.Builder
		for _, p := range req.Config.SystemInstruction.Parts {
			system.WriteString(p.Text)
		}
		m.prompts = append(m.prompts, system.String())
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if strings.Contains(part.Text, "## Agent-owned work") || strings.Contains(part.Text, "## Current durable work") {
					m.t.Error("private system context leaked into conversation text")
				}
			}
		}
		for _, want := range []string{
			"## Agent-owned work",
			"Bare listing headlines and URL slugs are not support for details, causality or recommendations beyond their actual content",
			"Substantive search output is different: judge what its text supports and attribute secondary reporting honestly",
			"For ordinary information, a dated, substantive answer from the configured Perplexity search provider with real source links is adequate",
			"For explicit verification or a high-stakes decision or action, inspect the primary/live source",
			"including actionable numbers on which the decision or action depends",
			"For an ordinary roundup, substantive sourced search output is usable by default",
			"Primary-source fetches fill a material gap, not a routine verification step",
			"an older/different-version page or a failed fetch is not a material factual contradiction",
			"Preserve distinct relevant developments already supported by returned coverage",
			"shorten individual stories before dropping supported coverage",
			"not a verification diary",
			"do not embellish beyond the retrieved text",
			"do not imply an unread linked page was inspected",
			"preserve unrelated supported coverage",
			"The owner's requested depth and coverage govern; preserve detailed assignments",
			"## Current durable work",
			"They are not prerequisites to a final answer",
			"Stock Market News",
		} {
			if !strings.Contains(system.String(), want) {
				m.t.Errorf("dispatch %d missing %q", len(m.prompts), want)
			}
		}
		for _, forbidden := range []string{
			"## Pinned work requirements", "Assessment context unavailable", "\"criteria\"", "\"checklist\"",
			"For actionable facts inspect the primary/live source",
			"Open the decisive original article or primary source before relying on consequential claims, including actionable numbers",
			"Inspect decisive articles or primary documents selectively for consequential details such as availability, licensing, prices and performance",
		} {
			if strings.Contains(system.String(), forbidden) {
				m.t.Errorf("dispatch %d contains legacy context %q", len(m.prompts), forbidden)
			}
		}
		if len(m.prompts) == 1 && !strings.Contains(system.String(), "initial-private-note") {
			m.t.Error("initial durable note missing")
		}
		if len(m.prompts) == 2 {
			if m.repo.state.AgentWork.Notes != "updated-private-note" {
				m.t.Fatal("production optional update did not persist")
			}
			if !strings.Contains(system.String(), "updated-private-note") || strings.Contains(system.String(), "initial-private-note") {
				m.t.Error("post-tool system instruction did not replace durable notes")
			}
		}
		content := genai.NewContentFromText("A bounded answer from the supplied evidence.", "model")
		if m.update && len(m.prompts) == 1 {
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "prompt-update", Name: "execute_tool", Args: map[string]any{"name": botWorkUpdateTool, "arguments": map[string]any{"expected_revision": m.repo.state.Revision, "kind": string(bot.BotWorkReplaceNotes), "notes": "updated-private-note"}}}}}}
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}

func TestBotAgentWorkProductionFactoryPrompt(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		for _, update := range []bool{false, true} {
			name := map[bool]string{false: "background", true: "interactive"}[interactive] + "/" + map[bool]string{false: "no-optional-tools", true: "rehydrate-after-update"}[update]
			t.Run(name, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Instructions = "Stock Market News"
				w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Run.Version, Instructions: f.req.Instructions})
				w.AgentWork.Notes = "initial-private-note"
				f.req.Work = w.Clone()
				repo := &sourceRedactionRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, state: w}
				f.services.Bots, f.services.PersonalBots = repo, repo
				llm := &agentWorkPromptModel{t: t, repo: repo, update: update}
				if update {
					workerStore := workerWorkOutcomeFixtureStore{workerOutcomeFixtureStore: workerOutcomeFixtureStore{BotStore: repo, fixture: f}, BotWorkStore: repo}
					f.services.Bots, f.services.PersonalBots = workerStore, workerStore
				}
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
				var err error
				f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{botWorkUpdateTool}})
				if err != nil {
					t.Fatal(err)
				}
				if interactive {
					f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
					f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
					f.req.Authorization = interactiveFactoryAuthorization{}
					f.req.Policy = nil
					f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
						return &BotChatSafeResources{PolicyRuntime: agent.BotPolicyRuntime{Guard: workCapabilityFixtureGuard{}, Classifier: BotCatalogueClassifier{}}, ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: &fileOutcomeNode{}}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
					}
				} else {
					f.req.Run.Trigger = store.TriggerCron
					prepare := f.cfg.Prepare
					f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
						safe, err := prepare(ctx, req, scope)
						safe.PolicyRuntime = agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: BotCatalogueClassifier{}}
						return safe, err
					}
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
				if err != nil || result.Status != store.RunSucceeded {
					t.Fatalf("status=%s err=%v report=%+v", result.Status, err, result.Report)
				}
				wantCalls := 1
				if update {
					wantCalls++
				}
				if len(llm.prompts) != wantCalls || repo.state.Phase != store.BotWorkFinished || result.Report.Quality != nil {
					t.Fatalf("calls=%d work=%s quality=%+v", len(llm.prompts), repo.state.Phase, result.Report.Quality)
				}
			})
		}
	}
}
