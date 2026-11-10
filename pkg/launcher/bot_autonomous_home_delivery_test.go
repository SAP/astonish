package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

const homeWeatherTask = "Fetch https://wttr.in/Orlando?format=j1 and post a brief weather update to this Bot's home chat."
const homeWeatherURL = "https://wttr.in/Orlando?format=j1"
const homeWeatherAnswer = "Orlando: 24 C, clear (controlled weather observation)."

// Scripted provider checks the contract, then fetches and returns a final answer.
// It does not establish real-provider judgment or reproduce the historical model.
type homeDeliveryModel struct {
	t     *testing.T
	calls int
}

func (*homeDeliveryModel) Name() string { return "home-delivery-controlled" }
func (m *homeDeliveryModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		system := ""
		for _, p := range req.Config.SystemInstruction.Parts {
			system += p.Text
		}
		for _, want := range []string{"Your final answer is automatically delivered to this Bot's own Home chat", "Do not search for or invoke a posting tool", "exact destination and action"} {
			if !strings.Contains(system, want) {
				m.t.Errorf("actual provider system prompt missing %q", want)
			}
		}
		foundTask, foundWeather := false, false
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				foundTask = foundTask || c.Role == "user" && p.Text == homeWeatherTask
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "http_request" {
					raw, _ := json.Marshal(p.FunctionResponse.Response)
					foundWeather = len(raw) > 39000 && strings.Contains(string(raw), "24")
				}
				if strings.Contains(p.Text, "Your final answer is automatically delivered") {
					m.t.Error("system guidance leaked into transcript")
				}
			}
		}
		if !foundTask {
			m.t.Error("natural task rewritten or missing")
		}
		content := genai.NewContentFromText(homeWeatherAnswer, "model")
		input, output := int32(31241), int32(97)
		if m.calls == 1 {
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "weather-fetch", Name: "http_request", Args: map[string]any{"url": homeWeatherURL, "method": "GET"}}}}}
			input, output = 9135, 81
		} else if m.calls != 2 || !foundWeather {
			yield(nil, fmt.Errorf("unexpected continuation: calls=%d weather=%v", m.calls, foundWeather))
			return
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: input, CandidatesTokenCount: output, TotalTokenCount: input + output}}, nil)
	}
}

func TestBotAutonomousFetchFinalHomePublication(t *testing.T) {
	f, repo := scheduleFixture(t, store.BotScopePersonal)
	b, err := repo.Get(f.ctx, f.req.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := repo.GetVersion(f.ctx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.RequestRun(f.ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: homeWeatherTask, IdempotencyKey: "home-weather"})
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
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"http_request"}})
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(100000)
	toolsLimit := int64(100)
	spec := store.BudgetSpec{MaxRuntimeSeconds: 300, MaxTokens: &limit, MaxToolCalls: &toolsLimit}
	meter, err := bot.NewRunBudget(spec, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v.Budget = spec
	f.req.Bot, f.req.Version, f.req.Run, f.req.Policy, f.req.Budget, f.req.Instructions = *b, *v, *run, policy, meter, homeWeatherTask
	f.services.PersonalBots = repo
	f.services.Memory = nil
	_, err = f.sessions.Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: b.OwnerID.String(), SessionID: run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &homeDeliveryModel{t: t}
	f.cfg.ContextWindow = 200000
	f.cfg.WrapModel = func(_ context.Context, _ model.LLM, budget execution.RunBudget) (model.LLM, error) {
		return provider.NewBudgetLLM(continuityAssessmentModel{llm}, budget, provider.BudgetLLMOptions{MaxOutputTokens: 4096, SupportsOutputLimit: true})
	}
	fetches := 0
	fetch, err := functiontool.New(functiontool.Config{Name: "http_request", Description: "Read controlled weather fixture"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
		fetches++
		if args["url"] != homeWeatherURL || args["method"] != "GET" {
			return nil, fmt.Errorf("unexpected fetch: %v", args)
		}
		bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Source: homeWeatherURL, Summary: homeWeatherAnswer, Kind: store.BotEvidenceObservation, ObservedAt: time.Now().UTC(), ActorID: b.OwnerID}}})
		return map[string]any{"status_code": 200, "body": `{"current_condition":[{"temp_C":"24","weatherDesc":[{"value":"Clear"}]}],"fixture_padding":"` + strings.Repeat("x", 39417) + `"}`}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
		return &BotChatSafeResources{Tools: []tool.Tool{fetch}, WorkspaceDir: t.TempDir(), PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
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
	if result.Status != store.RunSucceeded || result.Report.Summary != homeWeatherAnswer || len(result.Report.Unverified) != 0 || len(result.Report.Unchanged) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if result.Report.Quality == nil || result.Report.Quality.Outcome != store.BotWorkAccepted {
		t.Fatalf("weather report bypassed quality review: %+v", result.Report.Quality)
	}
	usage := meter.Usage()
	if llm.calls != 2 || fetches != 1 || usage.ToolCalls != 1 || usage.InputTokens+usage.OutputTokens != 40554+20 {
		t.Fatalf("calls=%d fetches=%d usage=%+v", llm.calls, fetches, usage)
	}
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report, Usage: usage}); err != nil {
		t.Fatal(err)
	}
	publisher := &bot.Publisher{Store: repo.(store.BotPublicationStore)}
	publication, err := publisher.Publish(f.ctx, b.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if publication.HomeSessionID != b.HomeSessionID || !strings.Contains(publication.Text, homeWeatherAnswer) || len(publication.Notifications) != 0 {
		t.Fatalf("publication=%+v", publication)
	}
	replay, err := publisher.Publish(f.ctx, b.ID, run.ID)
	if err != nil || replay.EventID != publication.EventID {
		t.Fatalf("duplicate publication: %+v %v", replay, err)
	}
	homeEvents, err := f.services.PersonalSessions.ReadTranscriptEvents(f.ctx, "astonish", b.OwnerID.String(), b.HomeSessionID.String())
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	for _, event := range homeEvents {
		if event.ID == publication.EventID {
			published++
			if event.Content == nil || len(event.Content.Parts) != 1 || event.Content.Parts[0].Text != publication.Text {
				t.Fatal("Home transcript lost final report")
			}
		}
	}
	if published != 1 {
		t.Fatalf("Home report count=%d", published)
	}
	// The finite cap remains enforced after settlement; no model call may evade it.
	_, err = meter.ReserveModel(f.ctx, execution.BudgetReservation{InputTokens: limit, OutputTokens: 4096})
	if err == nil {
		t.Fatal("finite cap bypassed")
	}
}
