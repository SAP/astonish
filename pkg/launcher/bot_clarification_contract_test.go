package launcher

import (
	"context"
	"encoding/json"
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
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
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
		if m.calls > 2 {
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
		if req.Tools["ask_user"] == nil || req.Tools["employee_chat"] == nil || req.Tools["execute_tool"] != nil || req.Tools["describe_tools"] != nil {
			m.t.Fatal("Employee must expose direct clarification and private orchestration only")
		}
		var raw []byte
		for _, group := range req.Config.Tools {
			for _, declaration := range group.FunctionDeclarations {
				if declaration.Name == "ask_user" {
					raw, _ = json.Marshal(declaration)
				}
			}
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

// The title journey exercises the production factory and Chat runner, not a
// synthesized card. Persistence/decision fencing is separately covered by the
// entstore lifecycle and HTTP acceptance fixtures.
type titleJourneyStore struct {
	clarificationStore
	consumed int
}

func (s *titleJourneyStore) AppendBotRunEvent(context.Context, store.BotLease, uuid.UUID, *session.Event) error {
	return nil
}
func (s *titleJourneyStore) RequeueApprovedRun(context.Context, store.BotID, store.RunID, uuid.UUID) error {
	return nil
}
func (s *titleJourneyStore) LoadResumeApproval(context.Context, store.BotLease) (*store.BotApproval, error) {
	return s.saved, nil
}
func (s *titleJourneyStore) ConsumeApproval(context.Context, store.BotApprovalConsumption) error {
	s.consumed++
	return nil
}

type titleJourneyModel struct {
	t             *testing.T
	mode          string
	calls         int
	confirmed     bool
	afterQuestion func()
}

func (*titleJourneyModel) Name() string { return "title-journey" }
func (m *titleJourneyModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.confirmed {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Confirmed assignment completed.", "model"), TurnComplete: true}, nil)
			return
		}
		count := 0
		for _, group := range req.Config.Tools {
			for _, d := range group.FunctionDeclarations {
				count++
				if d.Name != "ask_user" {
					m.t.Fatalf("pending title exposed %s", d.Name)
				}
			}
		}
		if count != 1 {
			m.t.Fatalf("question declarations = %d", count)
		}
		if m.mode == "plain" || (m.mode == "corrected" && m.calls == 1) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("I will research now.", "model"), TurnComplete: true}, nil)
			return
		}
		args := map[string]any{"kind": "select", "prompt": "What should I do with AI news?", "allow_custom_answer": true, "options": []any{map[string]any{"id": "brief", "label": "Prepare an AI news briefing"}, map[string]any{"id": "compare", "label": "Compare implications of recent AI news"}, map[string]any{"id": "explain", "label": "Explain one AI news story in depth"}}}
		if m.mode == "nil-args" {
			args = nil
		}
		name := "ask_user"
		if m.mode == "wrong-tool" {
			name = "web_fetch"
		}
		if m.mode == "malformed" {
			args["allow_custom_answer"] = false
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}, TurnComplete: true}, nil)
		if m.afterQuestion != nil {
			m.afterQuestion()
		}
	}
}

func TestBotTitleConfirmationFactoryJourney(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"valid", "custom", "autonomous", "mandated", "revoked", "stale", "corrected", "plain", "malformed", "nil-args", "wrong-tool"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				f.req.Bot.Scope = scope
				// Fresh title turns use interactive admission on an Active Bot;
				// the tool-free paused conversation intent is a separate contract.
				f.req.Run.Intent = store.BotIntentRegular
				f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
				f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
				f.req.Authorization = interactiveFactoryAuthorization{}
				f.req.Policy = nil
				if mode == "autonomous" || mode == "mandated" || mode == "revoked" {
					f.req.Run.ExecutionMode = execution.BotExecutionMode("")
					f.req.Authorization = nil
					id := execution.BotRunIdentity{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: 1, ActorID: f.req.Bot.OwnerID.String(), OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
					var err error
					f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"ask_user"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				var mandateRuntime *factoryMandateRuntime
				if mode == "mandated" || mode == "revoked" {
					id := f.req.Policy.Identity()
					mandateRuntime = &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: []string{"ask_user"}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}
					auth, err := execution.VerifyBotMandateAuthorization(f.ctx, mandateRuntime, mandateRuntime.saved.Ref, execution.BotMandatePurposeTask)
					if err != nil {
						t.Fatal(err)
					}
					f.req.Principal = auth.Principal(execution.SurfaceStudio)
					f.ctx, err = execution.WithPrincipal(context.Background(), f.req.Principal)
					if err != nil {
						t.Fatal(err)
					}
					f.req.Run.CallerCeiling = nil
					f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: mandateRuntime, Identity: id}
					f.req.Run.MandateRef = &mandateRuntime.saved.Ref
					f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
				}
				f.req.Run.UserEventID = "title-event"
				f.req.Run.TitleConfirmation = &store.BotTitleConfirmation{Version: store.BotTitleConfirmationVersion, State: store.BotTitleConfirmationNeedsQuestion, BotID: f.req.Bot.ID, RunID: f.req.Run.ID, OwnerID: f.req.Bot.OwnerID, OriginalTitle: "AI news", UserEventID: "title-event"}
				f.req.Instructions = "AI news"
				repo := &titleJourneyStore{clarificationStore: clarificationStore{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}}
				f.services.Bots, f.services.PersonalBots = repo, repo
				f.ctx = bot.WithApprovalStore(f.ctx, repo)
				llm := &titleJourneyModel{t: t, mode: mode}
				if mode == "revoked" {
					llm.afterQuestion = func() { mandateRuntime.revoked = true }
				}
				if mode == "stale" {
					llm.afterQuestion = func() { f.req.Run.Lease.Fence++ }
				}
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
				prepared := 0
				f.cfg.Prepare = func(_ context.Context, request bot.RunRequest, _ *BotChatScope) (*BotChatSafeResources, error) {
					prepared++
					if request.Run.TitleConfirmation.State != store.BotTitleConfirmationConfirmed || request.Instructions != request.Run.TitleConfirmation.ConfirmedAssignment {
						t.Fatal("Prepare did not receive the bound confirmed assignment")
					}
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
				if prepared != 0 {
					t.Fatal("physical preparation before owner answer")
				}
				if mode == "revoked" || mode == "stale" {
					if err == nil || repo.saved != nil || llm.calls != 1 {
						t.Fatalf("stale authority executed: calls=%d saved=%v err=%v", llm.calls, repo.saved, err)
					}
					return
				}
				if mode == "plain" || mode == "malformed" || mode == "nil-args" || mode == "wrong-tool" {
					if err == nil || repo.saved != nil || llm.calls != 2 {
						t.Fatalf("invalid generation: calls=%d saved=%v err=%v", llm.calls, repo.saved, err)
					}
					return
				}
				if err != nil || result.Status != store.RunWaitingApproval || len(result.Checkpoint) == 0 || repo.saved == nil {
					t.Fatalf("question failed: %+v %v", result, err)
				}
				if err := store.ValidateBotTitleQuestion(repo.saved.Interaction); err != nil {
					t.Fatal(err)
				}
				existing, err := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Run.SessionID.String()})
				if err != nil {
					t.Fatal(err)
				}
				calls, responses := 0, 0
				for i := 0; i < existing.Session.Events().Len(); i++ {
					e := existing.Session.Events().At(i)
					if e.Content == nil {
						continue
					}
					for _, p := range e.Content.Parts {
						if p.FunctionCall != nil && p.FunctionCall.Name == "ask_user" {
							calls++
						}
						if p.FunctionResponse != nil && p.FunctionResponse.Name == "ask_user" {
							responses++
						}
					}
				}
				if calls != 1 || responses != 1 {
					t.Fatalf("real transcript call/result=%d/%d", calls, responses)
				}
				waiting, err := f.req.Run.TitleConfirmation.WithQuestion(*repo.saved)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now()
				repo.saved.Status = store.ApprovalApproved
				repo.saved.DecidedBy = &f.req.Bot.OwnerID
				repo.saved.DecidedAt = &now
				repo.saved.Response = &store.BotInteractionResponse{QuestionID: repo.saved.Interaction.QuestionID, Answer: "brief"}
				if mode == "custom" {
					repo.saved.Response = &store.BotInteractionResponse{QuestionID: repo.saved.Interaction.QuestionID, Answer: "Focus the briefing on robotics"}
				}
				confirmed, err := waiting.WithAnswer(*repo.saved)
				if err != nil {
					t.Fatal(err)
				}
				f.req.Run.TitleConfirmation = confirmed
				f.req.Run.Checkpoint = result.Checkpoint
				f.req.Run.Lease.Fence++
				llm.confirmed = true
				if mode == "autonomous" || mode == "mandated" || mode == "revoked" {
					f.cfg.Prepare = func(_ context.Context, request bot.RunRequest, _ *BotChatScope) (*BotChatSafeResources, error) {
						prepared++
						if request.Instructions != request.Run.TitleConfirmation.ConfirmedAssignment {
							t.Fatal("mandated Prepare did not receive confirmed assignment")
						}
						return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: &fileOutcomeNode{}}}, WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
					}
					factory, err = NewBotChatAgentFactory(f.cfg)
					if err != nil {
						t.Fatal(err)
					}
					adapter, err = api.NewBotChatRuntimeAdapter(factory)
					if err != nil {
						t.Fatal(err)
					}
				}
				result, err = adapter.Resume(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
				if err != nil || result.Status != store.RunSucceeded || prepared != 1 || repo.consumed != 1 {
					t.Fatalf("resume: result=%+v prepared=%d consumed=%d err=%v", result, prepared, repo.consumed, err)
				}
			})
		}
	}
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
