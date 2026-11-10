package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type composedHomeModel struct {
	t                       *testing.T
	schedule, emitted       bool
	review                  *json.RawMessage
	declarations, questions *atomic.Int64
}

func (*composedHomeModel) Name() string { return "home-composed" }
func (m *composedHomeModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(req, func(w *store.BotWorkState) bool {
		return len(w.Candidate.Observations) > 0 && (w.Candidate.Text == "Boston weather: 12 C, clear (controlled observation)." || w.Candidate.Text == "Review the disabled every-minute Boston weather schedule for this Home chat.")
	}); ok {
		return response
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		part := &genai.Part{Text: "Boston weather: 12 C, clear (controlled observation)."}
		if m.schedule {
			if !m.emitted {
				requestBytes, _ := json.Marshal(req.Contents)
				if !strings.Contains(string(requestBytes), "schedule to deliver every minute") || !strings.Contains(string(requestBytes), "Boston weather: 12 C") {
					m.t.Error("schedule model did not receive exact user instruction and preceding weather context")
				}
			}
			found := false
			for _, ts := range req.Config.Tools {
				for _, d := range ts.FunctionDeclarations {
					if d.Name == "schedule_job" {
						found = true
						raw, _ := json.Marshal(d)
						text := string(raw)
						if !strings.Contains(text, "home chat") || strings.Contains(text, "delivery_mode") || strings.Contains(text, "delivery_channels") || strings.Contains(text, "Do NOT assume owner") {
							m.questions.Add(1)
							m.t.Errorf("recipient questionnaire declaration leaked: %s", text)
						}
						m.declarations.Add(1)
					}
				}
			}
			if !m.emitted && !found {
				m.t.Error("schedule_job declaration absent from actual interactive model request")
			}
			part.Text = "Review the disabled every-minute Boston weather schedule for this Home chat."
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil && p.FunctionResponse.Name == "schedule_job" {
						raw, _ := json.Marshal(p.FunctionResponse.Response)
						*m.review = raw
					}
				}
			}
		}
		if !m.emitted {
			m.emitted = true
			name, args := "read_file", map[string]any{"path": "/isolated/boston-weather.json"}
			if m.schedule {
				name, args = "schedule_job", map[string]any{"name": "Boston weather", "mode": "adaptive", "schedule": "* * * * *", "instructions": "Report current Boston weather in this Home chat."}
			}
			part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}
}

type composedWeatherClassifier struct{}

func (composedWeatherClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

type composedWeatherPool struct {
	interactiveHTTPPool
	weather *atomic.Int64
}

func (p *composedWeatherPool) Call(_ string, name string, args map[string]interface{}) (json.RawMessage, error) {
	if name != "read_file" || args["path"] != "/isolated/boston-weather.json" {
		return nil, fmt.Errorf("unexpected controlled tool: %s %+v", name, args)
	}
	p.weather.Add(1)
	return json.RawMessage(`{"content":"Boston weather: 12 C, clear (controlled observation)."}`), nil
}
func (p *composedWeatherPool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *composedWeatherPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}

// One composed HTTP -> interactive factory -> owner review -> scheduler -> worker
// -> Home history journey. Only identity, model, weather tool and Home are controlled.
func TestBotHomeDeliveryComposedHTTPAcceptance(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "home-composed", Name: "Home composed", Status: "active"}
	if err = db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
	if err = ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	auth := func(c context.Context) context.Context {
		c, e := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{"chat", "tool:execute"}})
		if e != nil {
			t.Fatal(e)
		}
		c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: execution.AuthMethodOAuth, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		return store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	}
	ownerCtx := auth(ctx)
	personal := ods.ForUser(owner)
	repo := personal.Bots()
	jobs := personal.ScheduledJobs()
	registry := execution.NewBotTurnAuthorizationRegistry(32)
	defer registry.Close()
	svc := &store.Services{TenantRouter: db, PersonalBots: repo, PersonalSessions: personal.Sessions(), BotInteractiveReady: true, BotTurnRegistry: registry}
	router := mux.NewRouter()
	root := "/api/studio/bots"
	for _, r := range []struct {
		path, method string
		handler      http.HandlerFunc
	}{
		{root, "POST", api.BotCreateHandler}, {root + "/{botID}", "GET", api.BotGetHandler},
		{root + "/{botID}/messages", "POST", api.BotMessageHandler}, {root + "/{botID}/activate", "POST", api.BotActivateHandler},
		{root + "/{botID}/configuration", "PUT", api.BotConfigurationHandler},
		{root + "/{botID}/triggers/{triggerID}", "PUT", api.BotTriggerSaveHandler},
		{root + "/{botID}/sessions/{sessionID}/stream", "GET", api.BotHistoryStreamHandler},
		{"/api/studio/sessions/{id}/model", "PATCH", api.PatchSessionModelHandler},
	} {
		router.HandleFunc(r.path, r.handler).Methods(r.method)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth(r.Context()), svc)))
	}))
	defer server.Close()
	request := func(method, path string, body any, status int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		if !strings.Contains(path, "scope=") {
			path += "?scope=personal"
		}
		r, e := http.NewRequest(method, server.URL+path, strings.NewReader(string(raw)))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, status, data)
		}
		return data
	}
	var b store.Bot
	if err = json.Unmarshal(request("POST", root, map[string]any{"name": "Boston weather", "idempotency_key": "create"}, 201), &b); err != nil {
		t.Fatal(err)
	}
	path := root + "/" + b.ID.String()
	homeID := b.HomeSessionID
	serviceCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	if err = entstore.RegisterBotHomeBinding(serviceCtx, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: store.BotScopePersonal, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	refresh := func() {
		t.Helper()
		if e := json.Unmarshal(request("GET", path, nil, 200), &b); e != nil {
			t.Fatal(e)
		}
	}
	refresh()
	request("PATCH", "/api/studio/sessions/"+homeID.String()+"/model", map[string]any{"provider": "controlled", "model": "home-composed"}, 200)
	var review json.RawMessage
	var declarations, questions, weather, external atomic.Int64
	pool := &composedWeatherPool{weather: &weather}
	scheduleMode := false
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: journeyHomeBackend{}, Pool: pool, TurnRegistry: registry, WorkerID: uuid.NewString(), ContextWindow: 32768, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 200, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return &composedHomeModel{t: t, schedule: scheduleMode, review: &review, declarations: &declarations, questions: &questions}, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		resources := &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}
		if !q.Run.IsInteractiveChat() {
			action, e := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read controlled Boston weather observation"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
				raw, e := pool.Call("", "read_file", args)
				if e != nil {
					return nil, e
				}
				var result map[string]any
				if e = json.Unmarshal(raw, &result); e != nil {
					return nil, e
				}
				bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Source: "controlled Boston weather", Summary: result["content"].(string), Kind: store.BotEvidenceObservation, ObservedAt: time.Now().UTC(), ActorID: q.Bot.OwnerID}}})
				return result, nil
			})
			if e != nil {
				return nil, e
			}
			resources.Tools = []tool.Tool{action}
			resources.PolicyRuntime = agent.BotPolicyRuntime{Classifier: composedWeatherClassifier{}, Guard: journeyGuard{repo: s.Services.PersonalBots, req: q}}
		}
		return resources, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	turn := func(instructions, key string) *store.BotRun {
		t.Helper()
		refresh()
		var receipt store.BotMessageReceipt
		if e := json.Unmarshal(request("POST", path+"/messages", map[string]any{"version": b.Version, "instructions": instructions, "idempotency_key": key}, 202), &receipt); e != nil {
			t.Fatal(e)
		}
		if !receipt.Run.IsInteractiveChat() {
			t.Fatal("did not use interactive factory admission")
		}
		if e := worker.Poll(ctx); e != nil {
			t.Fatal(e)
		}
		run, e := repo.GetRun(ownerCtx, b.ID, receipt.Run.ID)
		if e != nil {
			t.Fatal(e)
		}
		if run.Status != store.RunSucceeded && !(instructions == "schedule to deliver every minute" && run.Status == store.RunPartial) {
			t.Fatalf("interactive %q status=%s report=%+v", instructions, run.Status, run.Report)
		}
		refresh()
		return run
	}
	turn("Weather in Boston", "weather")
	if weather.Load() != 1 {
		t.Fatalf("initial weather tool calls=%d", weather.Load())
	}
	scheduleMode = true
	scheduleRun := turn("schedule to deliver every minute", "schedule")
	if scheduleRun.Usage.ToolCalls != 1 || declarations.Load() == 0 || questions.Load() != 0 || len(review) == 0 {
		t.Fatalf("schedule calls=%d declarations=%d unexpected recipient asks=%d review=%s", scheduleRun.Usage.ToolCalls, declarations.Load(), questions.Load(), review)
	}
	var result struct {
		Review struct {
			Method, Path string
			Body         struct {
				ExpectedRevision int64             `json:"expected_revision"`
				Reason           string            `json:"reason"`
				Definition       store.TriggerSpec `json:"definition"`
			} `json:"body"`
		} `json:"review"`
	}
	if err = json.Unmarshal(review, &result); err != nil {
		t.Fatal(err)
	}
	tr := result.Review.Body.Definition
	if tr.Enabled || tr.ChannelBinding != nil || tr.Cron != "* * * * *" || result.Review.Method != "PUT" || !strings.HasPrefix(result.Review.Path, path+"/triggers/") {
		t.Fatalf("bad scoped review: %s", review)
	}
	v, err := repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Triggers) != 0 || len(v.Delivery.Bindings) != 0 || b.Status != store.BotDraft {
		t.Fatalf("schedule silently applied or activated: %+v %+v", b, v)
	}
	// Apply the exact adapter review over HTTP, then separately grant read-only
	// autonomous capability, activate, and enable recurrence with owner authorization.
	if err = json.Unmarshal(request(result.Review.Method, result.Review.Path, result.Review.Body, 200), &b); err != nil {
		t.Fatal(err)
	}
	v, err = repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Triggers) != 1 || v.Triggers[0].Enabled || len(v.Delivery.Bindings) != 0 {
		t.Fatalf("save not disabled Home default: %+v", v)
	}
	v.Autonomy.AllowedTools = []string{"read_file"}
	configuration := store.BotConfigurationRequest{ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Explicit owner read-only weather capability", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}
	if err = json.Unmarshal(request("PUT", path+"/configuration", configuration, 200), &b); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(request("POST", path+"/activate", map[string]any{"expected_revision": b.Revision, "reason": "Explicit activation after review"}, 200), &b); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.(store.BotTriggerStore).ChangeTrigger(ownerCtx, store.BotTriggerChange{BotID: b.ID, TriggerID: tr.ID, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "Explicit owner enable after saved review"})
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	v, err = repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Delivery.Bindings) != 0 {
		t.Fatal("default schedule gained an external binding")
	}
	job := jobs.Get(ownerCtx, v.Triggers[0].ScheduleID.String())
	if job == nil || !job.Enabled {
		t.Fatal("enabled schedule missing")
	}
	due := time.Now().Add(-time.Second)
	job.NextRun = &due
	if err = jobs.Update(ownerCtx, job); err != nil {
		t.Fatal(err)
	}
	var tickID uuid.UUID
	executor := &scheduler.Executor{BotTrigger: func(c context.Context, q scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
		admission, e := repo.(store.BotScheduledRunStore).AdmitScheduledRun(auth(c), store.BotScheduledOccurrence{JobID: q.JobID, BotID: q.BotID, Version: q.Version, TriggerID: q.TriggerID, OccurrenceAt: q.OccurrenceAt, IdempotencyKey: q.IdempotencyKey})
		if e != nil {
			return scheduler.BotAdmission{}, e
		}
		tickID = admission.Run.ID
		return scheduler.BotAdmission{Status: scheduler.BotAdmissionAdmitted, RunID: tickID}, nil
	}}
	scheduleMode = false
	// RunNow uses the real scheduler executeJob path (the same path as a timer
	// tick), including Executor.Execute, persisted acknowledgement and delivery.
	jobAdapter := &journeyJobs{ctx: ownerCtx, store: jobs}
	jobAdapter.drop.Store(true) // Disable the shared fixture's crash simulation.
	engine := scheduler.New(jobAdapter, executor.Execute, scheduler.WithoutBotTriggerDelivery(func(context.Context, *scheduler.Job, string, error) error { external.Add(1); return nil }), nil)
	if _, err = engine.RunNow(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if saved := jobs.Get(ownerCtx, job.ID); saved.LastRun == nil || saved.ConsecutiveFailures != 0 {
		t.Fatalf("scheduler acknowledgement missing: %+v", saved)
	}
	if tickID == uuid.Nil {
		t.Fatal("scheduler did not admit tick")
	}
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ownerCtx, b.ID, tickID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunSucceeded || weather.Load() != 2 {
		t.Fatalf("tick status=%s weather calls=%d report=%+v", run.Status, weather.Load(), run.Report)
	}
	history := string(request("GET", path+"/sessions/"+homeID.String()+"/stream", nil, 200))
	tickReport := false
	for _, event := range strings.Split(history, "\n\n") {
		if strings.Contains(event, tickID.String()) && strings.Contains(event, "Boston weather: 12 C") {
			tickReport = true
		}
	}
	if !tickReport {
		t.Fatalf("tick-correlated weather report absent from original Home: %s", history)
	}
	if strings.Contains(history, `"name":"ask_user"`) || strings.Contains(history, `"tool_name":"ask_user"`) {
		t.Fatal("unexpected ask_user in composed Home transcript")
	}
	if external.Load() != 0 || questions.Load() != 0 {
		t.Fatalf("external deliveries=%d recipient questions=%d", external.Load(), questions.Load())
	}
	t.Logf("Composed HTTP weather -> exact schedule request -> reviewed disabled trigger -> separate activation/enable -> Execute tick %s -> original Home %s; weather tool calls=%d external=%d recipient asks=%d", tickID, homeID, weather.Load(), external.Load(), questions.Load())
}
