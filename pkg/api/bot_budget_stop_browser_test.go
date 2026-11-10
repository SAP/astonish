package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
)

// Keep the historical Studio/terminal fixture tied to the diagnostic formatter.
// New employee messages use RenderReport; saved historical transcripts are not rewritten.
func TestBotBudgetStopFixtureMatchesFormatter(t *testing.T) {
	raw, err := os.ReadFile("testdata/bot_budget_stop.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		RunID uuid.UUID `json:"run_id"`
		Event struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	input, output, requested := int64(80), int64(10), int64(90)
	failure := &bot.BudgetLimitError{Dimension: bot.BudgetTokens, Scope: bot.BudgetRun, Reason: bot.BudgetInputDoesNotFit, Limit: 71, Remaining: 71, Requested: &requested, InputEstimate: &input, OutputEstimate: &output}
	summary, ok := bot.BudgetFailureMessage(failure)
	if !ok {
		t.Fatal("native budget failure not recognized")
	}
	run := store.BotRun{ID: fixture.RunID, Status: store.RunFailed}
	run.Report.Summary = summary
	run.Report.Failure = &store.BotReportFailure{Kind: store.BotReportFailureBudget, TechnicalDetails: bot.BudgetFailureDetails(failure)}
	if len(fixture.Event.Content.Parts) != 1 || fixture.Event.Content.Parts[0].Text != bot.RenderDiagnosticReport(run) {
		t.Fatalf("historical client fixture drifted from diagnostic formatter:\n%s", bot.RenderDiagnosticReport(run))
	}
}

// Real built Studio, isolated HTTP/SSE and SQLite, native BudgetLLM/ChatRunner,
// fenced source persistence, CommitRun and Publisher. Only authentication,
// request estimates and the never-called provider are controlled. No daemon,
// external model, user Bot, proxy or well-known port is involved.
func TestBotBudgetStopBrowserAcceptance(t *testing.T) {
	dist, bin := chatBrowserPrerequisites(t)
	env := setupMemoryHTTPAcceptance(t, &MockLLM{}, nil)
	registry := execution.NewBotTurnAuthorizationRegistry(8)
	defer registry.Close()
	routes := mux.NewRouter()
	routes.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			svc := *store.FromContext(r.Context())
			svc.BotInteractiveReady, svc.BotTurnRegistry = true, registry
			ctx, err := execution.WithAuthenticationEvidence(r.Context(), execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Error(err)
				http.Error(w, "fixture authentication", 500)
				return
			}
			next.ServeHTTP(w, r.WithContext(store.WithServices(ctx, &svc)))
		})
	})
	registerBotRoutes(routes)
	env.Router.PathPrefix("/api/studio/bots").Handler(entstore.TenantMiddleware(env.Store)(routes))
	// The current workspace reads its retained Home model. Register the native
	// route omitted by the older memory harness, rather than hiding its alert.
	env.Router.Handle("/api/studio/sessions/{id}/model-status", entstore.TenantMiddleware(env.Store)(http.HandlerFunc(GetSessionModelStatusHandler))).Methods(http.MethodGet)
	page := newStreamBrowserPage(t, env, bin, dist).CancelTimeout().Timeout(60 * time.Second)
	t.Logf("isolated Studio URL=%s browser_target=%s browser_binary=%s", env.URL, page.TargetID, bin)
	page.MustElementR("button", "^New Bot$").MustClick()
	page.MustElement(`[aria-label="Bot name"]`).MustInput("Budget stop acceptance")
	page.MustElementR("button", "^Start conversation$").MustClick()
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	page.MustElementR("p", "Bot transcript: live").MustVisible()
	id := uuid.MustParse(strings.Split(page.MustEval(`() => location.hash`).Str(), "/")[4])
	org, err := env.Store.ForOrg(env.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	repo := org.ForUser(env.UserID).Bots()
	owner, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := repo.ListRuns(owner, id, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	input, err := repo.(store.BotRuntimeStore).LoadRunInput(owner, id, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	identity := execution.BotRunIdentity{BotID: id, RunID: runs[0].ID, Version: input.Version.Version, ActorID: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug}
	authority, err := registry.Resolve(identity)
	if err != nil {
		t.Fatal(err)
	}
	owner, err = execution.WithPrincipal(context.Background(), authority.Principal)
	if err != nil {
		t.Fatal(err)
	}
	worker := execution.WithBotRunIdentity(owner, identity)
	lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: id, RunID: runs[0].ID, WorkerID: "budget-browser", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(owner, id, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Presentation != store.BotRunPresentationHomeInteractive {
		t.Fatal("not the Home mirror path")
	}
	authorization, err := registry.Authorizer(identity, *run.CallerCeiling, func(_ context.Context, p execution.Principal) (execution.Principal, error) { return p, nil })
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately tiny execution cap, matching the cross-client fixture. The
	// estimate exceeds it before provider I/O; it is not claimed as consumed usage.
	limit := int64(71)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	llm := NewMockLLM(TextTurn("must not reach provider"))
	budgeted, err := provider.NewBudgetLLM(llm, meter, provider.BudgetLLMOptions{MaxOutputTokens: 10, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 80, nil }})
	if err != nil {
		t.Fatal(err)
	}
	transcript := repo.(store.BotTranscriptStore)
	adapter, err := NewBotChatRuntimeAdapter(func(context.Context, bot.RunRequest) (*BotChatResources, error) {
		ss := env.Services.PersonalSessions
		chat := agent.NewChatAgent(budgeted, nil, nil, ss, &agent.SystemPromptBuilder{}, false, false)
		return &BotChatResources{Agent: chat, SessionService: ss, Transcript: transcript, Authorization: authorization, AppName: studioChatAppName, Cleanup: func() {}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := bot.RunRequest{Bot: input.Bot, Run: *run, Version: input.Version, Principal: authority.Principal, Budget: meter, Authorization: authorization, Interactive: true, Instructions: input.Instructions}
	var observed []bot.RuntimeEvent
	result, failure := adapter.Start(worker, request, func(ctx context.Context, e bot.RuntimeEvent) error {
		observed = append(observed, e)
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		row := &session.Event{ID: uuid.NewSHA1(e.TurnID, []byte(fmt.Sprint(e.Sequence))).String(), InvocationID: e.TurnID.String(), Author: "bot_runtime", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": json.RawMessage(raw)}}}
		return transcript.AppendBotRunEvent(ctx, lease, e.SessionID, row)
	})
	if !errors.Is(failure, bot.ErrBudgetExceeded) || result.Status != store.RunFailed || result.Failure.Class != bot.ErrorBudget {
		t.Fatalf("native denial: %+v %v", result, failure)
	}
	if len(llm.Calls) != 0 || meter.Usage().InputTokens != 0 || meter.Usage().OutputTokens != 0 {
		t.Fatal("denied request called provider or claimed consumption")
	}
	if len(observed) == 0 || observed[len(observed)-1].Type != "done" {
		t.Fatal("missing completion")
	}
	for _, e := range observed {
		if e.Type == "error" {
			t.Fatalf("raw error event: %+v", e)
		}
	}
	if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report}); err != nil {
		t.Fatal(err)
	}
	publisher := &bot.Publisher{Store: repo.(store.BotPublicationStore)}
	for range 2 {
		if _, err = publisher.Publish(owner, id, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := repo.GetRun(owner, id, run.ID)
	if err != nil || restored.Report.Failure == nil || restored.Report.Failure.Kind != store.BotReportFailureBudget {
		t.Fatalf("lost typed failure: %+v %v", restored, err)
	}
	// Read both source and Home, not a manually fabricated UI paragraph.
	reader := env.Services.PersonalSessions.(store.BotTranscriptReader)
	for _, sid := range []uuid.UUID{run.SessionID, input.Bot.HomeSessionID} {
		rows, err := reader.ReadBotTranscript(owner, sid, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		reports := 0
		for _, row := range rows {
			if row.Event.InvocationID == "bot-report:"+run.ID.String() {
				reports++
			}
			raw, err := json.Marshal(row.Event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "[Error:") || strings.Contains(string(raw), "Run did not complete; execution requires review") {
				t.Fatalf("generic error persisted: %s", raw)
			}
		}
		t.Logf("transcript session=%s rows=%d reports=%d home=%t", sid, len(rows), reports, sid == input.Bot.HomeSessionID)
		if sid == input.Bot.HomeSessionID && reports != 1 {
			t.Fatalf("Home reports=%d", reports)
		}
	}
	check := func() {
		page.MustElementR(`[data-testid="agent-message-bubble"] h3`, "^Budget limit reached$").MustVisible()
		page.MustWait(`() => !document.querySelector('[data-testid="thinking-indicator"]')`)
		if count := page.MustEval(`() => document.querySelectorAll('[aria-label="Bot workspace"] [data-testid="agent-message-bubble"]').length`).Int(); count != 1 {
			t.Fatalf("visible report bubbles=%d", count)
		}
		text := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
		for _, fact := range []string{"run budget: 71 tokens", "Used or reserved: 0 tokens", "Remaining: 71 tokens", "Estimated next input: 80 tokens", "increase this Bot’s token budget"} {
			if !strings.Contains(text, fact) {
				t.Fatalf("missing %q: %s", fact, text)
			}
		}
		for _, unwanted := range []string{"Run report", "Technical details", "limit=71", "accounted=0", "requested=90", "[Error:", "Run did not complete; execution requires review"} {
			if strings.Contains(text, unwanted) {
				t.Fatalf("employee message leaked %q: %s", unwanted, text)
			}
		}
		if page.MustEval(`() => !!document.querySelector('[role="alert"], [data-sonner-toast], [data-testid="error-message"]')`).Bool() {
			t.Fatalf("budget denial became a toast/raw error: %s", page.MustEval(`() => Array.from(document.querySelectorAll('[role="alert"], [data-sonner-toast], [data-testid="error-message"]')).map(e => e.outerHTML).join('\n')`).Str())
		}
	}
	check()
	t.Log("live: report_bubbles=1 raw_errors=0 alerts=0 thinking_indicators=0")
	page.MustReload()
	check()
	t.Log("reload: report_bubbles=1 raw_errors=0 alerts=0 thinking_indicators=0")
	t.Logf("bot=%s run=%s source=%s home=%s provider_calls=%d input_tokens=%d output_tokens=%d runtime_events=%d publish_attempts=2", id, run.ID, run.SessionID, input.Bot.HomeSessionID, len(llm.Calls), meter.Usage().InputTokens, meter.Usage().OutputTokens, len(observed))
	if len(llm.Calls) != 0 {
		t.Fatal("reload invoked model")
	}
	if err = os.MkdirAll("../../.tmp/bot-budget-followup", 0755); err != nil {
		t.Fatal(err)
	}
	shot, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("../../.tmp/bot-budget-followup/budget-stop-restored.png", shot, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("PASS native first reservation denied before provider; fenced home_interactive source persisted; failed report committed and published twice; one normal budget report live and reload; no raw error/toast; usage zero")
}
