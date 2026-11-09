package launcher

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// This controlled provider checks the actual complete request, not a prompt
// excerpt or Agent.Tools membership. It then emits a genuine tool call.
type clarificationContractModel struct {
	t     *testing.T
	kind  string
	calls int
}

func (*clarificationContractModel) Name() string { return "clarification-contract" }
func (m *clarificationContractModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls > 1 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Waiting for your answer.", "model"), TurnComplete: true}, nil)
			return
		}
		var system strings.Builder
		for _, p := range req.Config.SystemInstruction.Parts {
			system.WriteString(p.Text)
		}
		for _, want := range []string{"When missing information genuinely blocks the current request, call the actual ask_user tool for one focused clarification instead of asking questions in plain chat text.", "Use kind=text for open-ended missing details, or kind=select with allow_custom_answer=true when meaningful choices exist.", "If the request is clear, do the useful work first; do not start an onboarding questionnaire.", "The user does not need to say to call a tool."} {
			if !strings.Contains(system.String(), want) {
				m.t.Fatalf("effective provider prompt missing %q", want)
			}
		}
		var declaration *genai.FunctionDeclaration
		for _, tools := range req.Config.Tools {
			for _, d := range tools.FunctionDeclarations {
				if d.Name == "ask_user" {
					declaration = d
				}
			}
		}
		if declaration == nil {
			m.t.Fatal("provider has no actual ask_user declaration")
		}
		raw, err := json.Marshal(declaration)
		if err != nil {
			m.t.Fatal(err)
		}
		for _, want := range []string{"text", "select", "yesno", "allow_custom_answer", "prompt", "options"} {
			if !strings.Contains(string(raw), want) {
				m.t.Fatalf("provider schema missing %s: %s", want, raw)
			}
		}
		args := map[string]any{"kind": m.kind, "prompt": "Which destination and dates should I use?"}
		if m.kind == "select" {
			args["allow_custom_answer"] = true
			args["options"] = []any{map[string]any{"id": "trip", "label": "Upcoming trip"}, map[string]any{"id": "home", "label": "Home"}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "clarification-call", Name: "ask_user", Args: args}}}}, TurnComplete: true}, nil)
	}
}

type clarificationStore struct {
	botFactoryTestRepo
	saved *store.BotApproval
}

func (s *clarificationStore) RequestApproval(_ context.Context, _ store.BotLease, a store.BotApproval) (*store.BotApproval, error) {
	a.ID = uuid.New()
	a.Status = store.ApprovalPending
	s.saved = &a
	return &a, nil
}

func TestInteractiveBotClarificationProviderContract(t *testing.T) {
	for _, kind := range []string{"text", "select"} {
		t.Run(kind, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			repo := &clarificationStore{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
			f.services.Bots, f.services.PersonalBots = repo, repo
			f.ctx = bot.WithApprovalStore(f.ctx, repo)
			f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
			f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
			f.req.Authorization = interactiveFactoryAuthorization{}
			f.req.Policy = nil
			f.req.Instructions = "Weather for my trip"
			llm := &clarificationContractModel{t: t, kind: kind}
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: &fileOutcomeNode{}}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
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
			if err != nil {
				t.Fatal(err)
			}
			if llm.calls == 0 || repo.saved == nil || repo.saved.Interaction == nil || repo.saved.Interaction.Kind != store.BotInteractionQuestion || !strings.Contains(string(repo.saved.Interaction.Payload), `"kind":"`+kind+`"`) {
				t.Fatalf("no real clarification checkpoint: status=%s checkpoint=%s report=%+v", result.Status, result.Checkpoint, result.Report)
			}
			if result.Status == store.RunSucceeded {
				t.Fatal("clarification incorrectly completed")
			}
		})
	}
}
