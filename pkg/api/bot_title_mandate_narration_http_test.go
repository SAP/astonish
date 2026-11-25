package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/SAP/astonish/pkg/question"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// Real HTTP title admission, durable mandate/approval and worker/factory path.
// A controlled HTTP source blocks before returning so Home delivery cannot be
// mistaken for final publication. No live daemon, provider or user data is used.
// TestBotEnhancedChatBaseline exercises real HTTP admission, public Home replay,
// typed answers and budget enforcement with controlled model/compute dependencies.
func TestBotEnhancedChatBaseline(t *testing.T) {
	t.Run("title_question_answer_once", TestBotTitleMandateAcknowledgementHomeHTTP)
	t.Run("budget_stops_without_replay", TestBotBudgetComposedHTTP)
}

func TestBotTitleMandateAcknowledgementHomeHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, answer := range []struct{ name, wire, text string }{
			{"option", "weekly", "Weekly roundup"},
			{"custom", "Focus on open-source AI releases this week.", "Focus on open-source AI releases this week."},
		} {
			t.Run(string(scope)+"/"+answer.name, func(t *testing.T) {
				testTitleMandateAcknowledgementHomeHTTP(t, scope, answer.wire, answer.text)
			})
		}
	}
}

func testTitleMandateAcknowledgementHomeHTTP(t *testing.T, scope store.BotScope, answer, answerText string) {
	f := newInteractiveHTTPFixture(t, "ask_user", nil, []string{"chat", "tool:execute"}, scope)
	defer f.worker.Stop()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "SOURCE_BODY_MUST_STAY_PRIVATE: isolated receipt")
	}))
	defer upstream.Close()
	defer close(release)
	// Shared Chat dispatches ordinary web_fetch through the scoped node pool.
	// This controlled transport permits only this fixture's local source.
	f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
		if name != "web_fetch" || args["url"] != upstream.URL {
			return nil, fmt.Errorf("unexpected scoped node call %s: %v", name, args)
		}
		res, err := upstream.Client().Get(upstream.URL)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"content": string(body)})
	}
	origin, _ := tools.RoutineReadOrigin(upstream.URL)
	policy := func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"ask_user", "web_fetch", origin}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: time.Hour}, nil
	}
	p, _ := execution.PrincipalFromContext(f.ctx)
	p.Authentication = execution.AuthMethodPlatformJWT
	auth, err := execution.WithPrincipal(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	org, err := f.db.ForOrg(p.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	ss := org.ForUser(p.Subject).Sessions()
	if scope == store.BotScopeTeam {
		ss = org.ForTeam(p.TeamSlug).Sessions()
	}
	svc := &store.Services{TenantRouter: f.db, Bots: f.repo, Sessions: ss, PersonalBots: f.repo, PersonalSessions: ss, BotMandatePolicy: policy, BotInteractiveReady: true}
	router := mux.NewRouter()
	root := "/api/studio/bots"
	router.HandleFunc(root, api.BotCreateHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/sessions/{sessionID}/events", api.BotEventsHandler).Methods("GET")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(auth)
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		defer cancel()
		router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
	}))
	defer server.Close()
	request := func(method, path, body string, status int) []byte {
		t.Helper()
		q, err := http.NewRequest(method, server.URL+path+"?scope="+string(scope), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		q.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
		return raw
	}
	var receipt titleHTTPReceipt
	if err := json.Unmarshal(request("POST", root, `{"name":"AI news","idempotency_key":"narration","start_from_title":true}`, 201), &receipt); err != nil || receipt.InitialTurn == nil {
		t.Fatalf("title: %v", err)
	}
	f.b = receipt.Bot
	f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"interactive-http"}`, 200)
	f.registry.Close() // this journey must not borrow registry-backed authorization
	const acknowledgement = "I'll prepare a concise weekly AI roundup covering practical developments."
	const final = "Observed scoped result."
	mdl := &interactiveHTTPModel{t: t, principal: execution.Principal{Subject: p.Subject, Authentication: execution.AuthMethodBotMandate, Surface: execution.Surface("daemon")}}
	confirmed := false
	mdl.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
		if response, ok := f.model.assessment(req); ok {
			return response, true
		}
		if !confirmed {
			return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "ask_user", Args: map[string]any{"kind": "select", "prompt": "Which AI news assignment?", "allow_custom_answer": true, "options": []map[string]any{{"id": "weekly", "label": "Weekly roundup"}, {"id": "research", "label": "Research breakthroughs"}, {"id": "policy", "label": "Policy changes"}}}}}}}}, true
		}
		if !mdl.called {
			raw, _ := json.Marshal(req.Contents)
			if !strings.Contains(string(raw), answerText) {
				t.Errorf("producer lost owner answer: %s", raw)
			}
			mdl.called = true
			return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: acknowledgement}, {FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "web_fetch", Args: map[string]any{"url": upstream.URL}}}}}}, true
		}
		return nil, false
	}
	files := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: f.pool.dir}, repo: f.repo, id: f.b.ID, scope: f.scope}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: synthesisHTTPBackend{files: files.disk}, Pool: f.pool, MandatePolicy: policy, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}, Prepare: func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		runFiles := *files
		runFiles.id = r.Bot.ID
		runFiles.install(c, r, s)
		reads, err := launcher.BotMandatedReadTools(r.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
		if err != nil {
			return nil, err
		}
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, Tools: reads, PolicyRuntime: agent.BotPolicyRuntime{Mandate: r.Mandate, Guard: restartMandateGuard{r, f.repo}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()
	poll := func() {
		t.Helper()
		if err := worker.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	run, err := f.repo.GetRun(auth, f.b.ID, receipt.InitialTurn.Run.ID)
	if err != nil || run.Status != store.RunWaitingApproval || run.MandateRef == nil || calls.Load() != 0 {
		t.Fatalf("question-only mandate: %+v %v calls=%d", run, err, calls.Load())
	}
	approvals, err := f.repo.ListApprovals(auth, f.b.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approval %v %v", approvals, err)
	}
	a := approvals[0]
	var q question.Question
	if a.Interaction == nil || a.Interaction.Validate() != nil || json.Unmarshal(a.Interaction.Payload, &q) != nil || q.Kind != question.Select || q.Prompt != "Which AI news assignment?" || len(q.Options) != 3 || q.AllowCustomAnswer == nil || !*q.AllowCustomAnswer {
		t.Fatalf("actual ask_user schema was not retained: %+v", a.Interaction)
	}
	questionBot, err := f.repo.Get(auth, f.b.ID)
	if err != nil || questionBot.Home.ID != uuid.Nil || f.pool.calls != 0 {
		t.Fatalf("question-only turn provisioned compute/Home or called a node: bot=%+v calls=%d err=%v", questionBot, f.pool.calls, err)
	}
	homePath := root + "/" + f.b.ID.String() + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
	initial := string(request("GET", homePath, "", 200))
	if titleAt, questionAt := strings.Index(initial, "AI news"), strings.Index(initial, "Which AI news assignment?"); titleAt < 0 || questionAt <= titleAt || strings.Contains(initial, acknowledgement) {
		t.Fatalf("initial Home must show title then question, without work narration: %s", initial)
	}
	body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": answer}})
	answerPath := root + "/" + f.b.ID.String() + "/approvals/" + a.ID.String() + "/approve-and-continue"
	request("POST", answerPath, string(body), 202)
	// Transport retries must not enqueue a second continuation or owner answer.
	request("POST", answerPath, string(body), 202)
	internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.repo.Get(auth, f.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: current.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: f.scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	confirmed = true
	// Connect before resume: this must arrive as a live persisted delta, not only
	// as a snapshot taken after completion.
	streamCtx, cancelStream := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStream()
	eventsPath := root + "/" + f.b.ID.String() + "/sessions/" + f.b.HomeSessionID.String() + "/events?scope=" + string(scope)
	streamReq, _ := http.NewRequestWithContext(streamCtx, "GET", server.URL+eventsPath, nil)
	streamRes, err := server.Client().Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer streamRes.Body.Close()
	scanner := bufio.NewScanner(streamRes.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var acceptedReplay strings.Builder
	for scanner.Scan() {
		acceptedReplay.WriteString(scanner.Text())
		acceptedReplay.WriteByte('\n')
		if scanner.Text() == "event: replay_complete" {
			break
		}
	}
	if strings.Count(acceptedReplay.String(), `"text":"`+answerText+`"`) != 1 {
		t.Fatalf("accepted answer must already be in replay before worker resume: %s", acceptedReplay.String())
	}
	liveDelta := make(chan string, 1)
	go func() {
		var rows strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			rows.WriteString(line)
			rows.WriteByte('\n')
			if strings.Contains(line, acknowledgement) {
				liveDelta <- rows.String()
				return
			}
		}
		liveDelta <- rows.String()
	}()
	done := make(chan error, 1)
	go func() { done <- worker.Poll(context.Background()) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("worker ended before first source: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("first source never started")
	}
	select {
	case delta := <-liveDelta:
		ackAt := strings.Index(delta, acknowledgement)
		if ackAt < 0 || strings.Contains(delta, `"text":"`+answerText+`"`) || strings.Count(delta, acknowledgement) != 1 || strings.Contains(delta, final) || strings.Contains(delta, "SOURCE_BODY_MUST_STAY_PRIVATE") {
			t.Fatalf("live public answer -> acknowledgement order/private-source boundary: %s", delta)
		}
	case <-streamCtx.Done():
		t.Fatal("live Home delta did not deliver before blocked source returned")
	}
	cancelStream()
	live := request("GET", homePath, "", 200)
	if !strings.Contains(string(live), acknowledgement) {
		t.Fatalf("Home missing actual producer acknowledgement while source blocked: %s", live)
	}
	if !strings.Contains(string(live), answerText) || strings.Count(string(live), `"text":"`+answerText+`"`) != 1 || strings.Index(string(live), `"text":"`+answerText+`"`) > strings.Index(string(live), acknowledgement) {
		t.Fatalf("owner answer absent or after acknowledgement: %s", live)
	}
	if strings.Contains(string(live), final) || strings.Contains(string(live), "SOURCE_BODY_MUST_STAY_PRIVATE") {
		t.Fatalf("early final/source leaked: %s", live)
	}
	// Run diagnostics retain acknowledgement -> work-call chronology independently
	// of whether the same public tool frame has reached Home yet.
	runPath := root + "/" + f.b.ID.String() + "/sessions/" + run.SessionID.String() + "/stream"
	diagnostic := string(request("GET", runPath, "", 200))
	ackAt, toolAt := strings.Index(diagnostic, acknowledgement), strings.Index(diagnostic, `"name":"web_fetch"`)
	if ackAt < 0 || toolAt <= ackAt || !strings.Contains(diagnostic, "event: tool_call\n") {
		t.Fatalf("private acknowledgement -> work-call chronology lost: %s", diagnostic)
	}
	// A fresh HTTP read is replay of the same durable rows, not an in-memory event.
	replay := request("GET", homePath, "", 200)
	if string(live) != string(replay) || strings.Count(string(replay), acknowledgement) != 1 {
		t.Fatalf("blocked replay duplicated or lost acknowledgement: %s", replay)
	}
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not finish")
	}
	poll()
	poll()
	run, err = f.repo.GetRun(auth, f.b.ID, run.ID)
	if err != nil || run.Status != store.RunSucceeded || calls.Load() != 1 {
		t.Fatalf("final run=%+v err=%v calls=%d", run, err, calls.Load())
	}
	request("POST", answerPath, string(body), 202)
	poll()
	// Historical public tool activity remains visible through the shared Chat
	// projection; private checkpoint fields are excluded without losing its cursor.
	legacy := &session.Event{ID: "historical-public-tool", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{RunID: run.ID, SessionID: run.SessionID, TurnID: uuid.New(), Sequence: 99, Type: "tool_call", Payload: json.RawMessage(`{"name":"web_search","args":{"query":"historical public release notes"},"private_checkpoint":"HISTORICAL_PRIVATE_CHECKPOINT"}`)}}}}
	if err := ss.AppendFleetEvent(auth, f.b.HomeSessionID.String(), legacy); err != nil {
		t.Fatal(err)
	}
	historical := string(request("GET", homePath, "", 200))
	reader, ok := ss.(store.BotTranscriptReader)
	if !ok {
		t.Fatal("historical source has no durable cursor reader")
	}
	rows, err := reader.ReadBotTranscript(auth, f.b.HomeSessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var legacyCursor int64
	for _, row := range rows {
		if row.Event != nil && row.Event.ID == legacy.ID {
			legacyCursor = row.Cursor
		}
	}
	wantID := f.b.HomeSessionID.String() + ":" + fmt.Sprint(legacyCursor)
	var publicFrames int
	for _, block := range strings.Split(historical, "\n\n") {
		if !strings.Contains(block, `"name":"web_search"`) {
			continue
		}
		publicFrames++
		if !strings.HasPrefix(block, "id: "+wantID+"\nevent: tool_call\ndata: ") || !strings.Contains(block, `"args":{"query":"historical public release notes"}`) {
			t.Fatalf("historical public tool payload or durable identity changed: %s", block)
		}
	}
	if legacyCursor == 0 || publicFrames != 1 || strings.Contains(historical, "HISTORICAL_PRIVATE_CHECKPOINT") || !strings.Contains(historical, "id: "+wantID+"\nevent: done\n") {
		t.Fatalf("historical public telemetry/private-field/cursor boundary: %s", historical)
	}
	stored, err := ss.ReadTranscriptEvents(auth, "astonish_chat", p.Subject, f.b.HomeSessionID.String())
	if err != nil {
		t.Fatal(err)
	}
	rawStored, _ := json.Marshal(stored)
	if !strings.Contains(string(rawStored), "HISTORICAL_PRIVATE_CHECKPOINT") {
		t.Fatal("presentation deleted historical diagnostics")
	}
	finished := request("GET", homePath, "", 200)
	if strings.Count(string(finished), `"text":"`+answerText+`"`) != 1 || strings.Count(string(finished), acknowledgement) != 1 || strings.Count(string(finished), final) != 1 || strings.Contains(string(finished), "SOURCE_BODY_MUST_STAY_PRIVATE") || calls.Load() != 1 {
		t.Fatalf("publication/replay boundary: %s calls=%d", finished, calls.Load())
	}
}

// TestBotEmployeeObjectiveInquiryHTTP drives production admission and the shared
// configured-search executable. Only the external provider and retained transport
// are controlled; no invented request/answer handler or search-shaped read_file.
func TestBotEmployeeObjectiveInquiryHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, adequate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/adequate-first=%t", scope, adequate), func(t *testing.T) {
				f := newInteractiveHTTPFixture(t, "ask_user", nil, []string{"chat", "tool:execute"}, scope)
				defer f.worker.Stop()
				p, _ := execution.PrincipalFromContext(f.ctx)
				p.Authentication = execution.AuthMethodPlatformJWT
				auth, err := execution.WithPrincipal(f.ctx, p)
				if err != nil {
					t.Fatal(err)
				}
				auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				org, err := f.db.ForOrg(p.OrgSlug)
				if err != nil {
					t.Fatal(err)
				}
				team := org.ForTeam(p.TeamSlug)
				if err := f.db.PlatformSettings().Save(auth, &store.PlatformSettings{PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "selected-search", Model: "sonar"}}); err != nil {
					t.Fatal(err)
				}
				if err := team.Settings().Save(auth, &store.TeamSettings{WebSearchTool: "perplexity:perplexity_web_search", Providers: map[string]store.ProviderConfig{"selected-search": {"api_key": "owned-fixture-only"}}}); err != nil {
					t.Fatal(err)
				}
				ss := org.ForUser(p.Subject).Sessions()
				if scope == store.BotScopeTeam {
					ss = team.Sessions()
				}
				configuration := &store.Services{PlatformSettings: f.db.PlatformSettings(), OrgSettings: f.db.OrgSettings(p.OrgSlug), Settings: team.Settings()}
				route, err := launcher.BotEffectiveConfiguredSearchRoute(auth, configuration)
				if err != nil {
					t.Fatal(err)
				}
				policy := func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"ask_user", launcher.BotConfiguredSearchToolName}}}, PublicResearch: &execution.BotPublicResearchScope{Version: 1, Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview, execution.BotResearchRecent, execution.BotResearchPrimary, execution.BotResearchContradictions}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 12}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: time.Hour}, nil
				}
				svc := &store.Services{TenantRouter: f.db, Bots: f.repo, PersonalBots: f.repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: policy, BotInteractiveReady: true}
				router := mux.NewRouter()
				root := "/api/studio/bots"
				router.HandleFunc(root, api.BotCreateHandler).Methods("POST")
				router.HandleFunc(root+"/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
				router.HandleFunc(root+"/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler).Methods("GET")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					router.ServeHTTP(w, r.WithContext(store.WithServices(auth, svc)))
				}))
				defer server.Close()
				request := func(method, path, body string, status int) []byte {
					t.Helper()
					q, err := http.NewRequest(method, server.URL+path+"?scope="+string(scope), strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					q.Header.Set("Content-Type", "application/json")
					res, err := server.Client().Do(q)
					if err != nil {
						t.Fatal(err)
					}
					defer res.Body.Close()
					raw, _ := io.ReadAll(res.Body)
					if res.StatusCode != status {
						t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
					}
					return raw
				}
				var created titleHTTPReceipt
				if err := json.Unmarshal(request("POST", root, `{"name":"Apple News","start_from_title":true,"idempotency_key":"employee-objective"}`, 201), &created); err != nil || created.InitialTurn == nil {
					t.Fatalf("create: %v", err)
				}
				f.b = created.Bot
				f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"interactive-http"}`, 200)
				f.registry.Close()
				var queries []string
				var observations []string
				var sharedSearchConfig *config.AppConfig
				const refinement = "Apple Inc. product launches and material market developments."
				const final = "Apple Inc. launched a new device and market revenue rose 5%. Sources: https://fixture.test/product and https://fixture.test/market."
				producerCalls, reviewCalls := 0, 0
				confirmed := false
				mdl := &interactiveHTTPModel{t: t, principal: execution.Principal{Subject: p.Subject, Authentication: execution.AuthMethodBotMandate, Surface: execution.Surface("daemon")}}
				mdl.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
					if response, ok := f.model.assessment(req); ok {
						reviewCalls++
						return response, true
					}
					call := func(name string, args map[string]any) *model.LLMResponse {
						return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}}
					}
					if !confirmed {
						return call("ask_user", map[string]any{"kind": "select", "prompt": "Which Apple news?", "allow_custom_answer": true, "options": []map[string]any{{"id": "company", "label": refinement}, {"id": "fruit", "label": "Fruit harvests"}, {"id": "services", "label": "Apple services"}}}), true
					}
					producerCalls++
					raw, _ := json.Marshal(req.Contents)
					if !strings.Contains(string(raw), refinement) {
						t.Fatal("lost canonical owner refinement")
					}
					var evidence strings.Builder
					for _, c := range req.Contents {
						for _, part := range c.Parts {
							if part.FunctionResponse != nil {
								b, _ := json.Marshal(part.FunctionResponse.Response)
								evidence.Write(b)
							}
						}
					}
					seen := evidence.String()
					query := "Apple News"
					switch {
					case strings.Contains(seen, "market revenue rose 5%"):
						return &model.LLMResponse{Content: genai.NewContentFromText(final, "model")}, true
					case strings.Contains(seen, "Market developments not covered"):
						query = "Apple Inc. material market revenue developments"
					case strings.Contains(seen, "fruit harvest"):
						query = "Apple Inc. product launches"
					}
					return call("perplexity_web_search", map[string]any{"query": query, "max_results": 5, "recency": "week"}), true
				}
				search := &employeeConfiguredSearchFixture{run: func(ctx context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
					raw, _ := json.Marshal(req.Contents)
					text := string(raw)
					query := "Apple News"
					answer := "Apple fruit harvest increases. Source: https://fixture.test/fruit"
					if strings.Contains(text, "Apple Inc. material market revenue developments") {
						query = "Apple Inc. material market revenue developments"
						answer = "Apple Inc. market revenue rose 5%. Source: https://fixture.test/market"
					} else if strings.Contains(text, "Apple Inc. product launches") {
						query = "Apple Inc. product launches"
						answer = "Apple Inc. launched a new device. Market developments not covered. Source: https://fixture.test/product"
					}
					if adequate {
						answer = "Apple Inc. launched a new device and market revenue rose 5%. Sources: https://fixture.test/product https://fixture.test/market"
					}
					queries = append(queries, query)
					observations = append(observations, answer)
					return &model.LLMResponse{Content: genai.NewContentFromText(answer, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil
				}}
				files := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: f.pool.dir}, repo: f.repo, id: f.b.ID, scope: scope}
				worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: synthesisHTTPBackend{files: files.disk}, Pool: f.pool, MandatePolicy: policy, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return mdl, nil
				}, Prepare: func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					runFiles := *files
					runFiles.id = r.Bot.ID
					runFiles.install(c, r, s)
					local, err := launcher.BotScopedChatConfig(c, s)
					if err != nil {
						return nil, err
					}
					sharedSearchConfig = local
					episode, err := launcher.NewBotResearchEpisode(r.Run.ID, r.Run.SessionID.String(), launcher.BotResearchOpenWeb, nil)
					if err != nil {
						return nil, err
					}
					return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: local, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, Research: episode, ProviderFactory: func(c context.Context, p, m string, cfg *config.AppConfig) (model.LLM, error) {
						if !provider.IsConfinedProviderSecrets(c) || p != "selected-search" || m != "sonar" || cfg.Providers[p]["api_key"] != "owned-fixture-only" {
							t.Fatal("lost selected scoped search configuration")
						}
						return search, nil
					}, PolicyRuntime: agent.BotPolicyRuntime{Mandate: r.Mandate, Guard: restartMandateGuard{r, f.repo}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Stop()
				poll := func() {
					t.Helper()
					if err := worker.Poll(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				poll()
				run, err := f.repo.GetRun(auth, f.b.ID, created.InitialTurn.Run.ID)
				if err != nil || run.Status != store.RunWaitingApproval || len(queries) != 0 {
					t.Fatalf("title ran research: %+v failure=%+v %v", run, run.Report.Failure, err)
				}
				approvals, err := f.repo.ListApprovals(auth, f.b.ID, store.BotPage{})
				if err != nil || len(approvals) != 1 || approvals[0].Interaction == nil {
					t.Fatalf("actual refinement missing: %v %v", approvals, err)
				}
				a := approvals[0]
				body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": "company"}})
				answerPath := root + "/" + f.b.ID.String() + "/approvals/" + a.ID.String() + "/approve-and-continue"
				request("POST", answerPath, string(body), 202)
				request("POST", answerPath, string(body), 202)
				current, err := f.repo.Get(auth, f.b.ID)
				if err != nil {
					t.Fatal(err)
				}
				internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
				if err != nil {
					t.Fatal(err)
				}
				if err := entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: current.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
					t.Fatal(err)
				}
				confirmed = true
				poll()
				run, err = f.repo.GetRun(auth, f.b.ID, run.ID)
				want := 3
				if adequate {
					want = 1
				}
				if err != nil || run.Status != store.RunSucceeded || len(queries) != want || producerCalls != want+1 || reviewCalls != 0 || mdl.reviews != 0 || len(run.Report.Artifacts) != 0 || run.Report.EmployeeMessage == nil || run.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || run.Report.EmployeeMessage.Text != final {
					t.Fatalf("inline employee: run=%+v queries=%v model=%d review=%d/%d err=%v", run, queries, producerCalls, reviewCalls, mdl.reviews, err)
				}
				if !adequate && (queries[1] != "Apple Inc. product launches" || queries[2] != "Apple Inc. material market revenue developments") {
					t.Fatalf("adaptive queries: %v", queries)
				}
				homePath := root + "/" + f.b.ID.String() + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
				replay := string(request("GET", homePath, "", 200))
				if strings.Count(replay, `"text":"`+refinement+`"`) != 1 || strings.Count(replay, final) != 1 {
					t.Fatalf("exactly-once answer/final replay: %s", replay)
				}
				poll()
				request("GET", homePath, "", 200)
				if len(queries) != want {
					t.Fatal("GET or maintenance replayed research")
				}
				// Run a fresh ordinary Chat session through the same executable search
				// constructor, selected provider, evidence-responsive producer and source
				// service. No Bot context, work state or hooks accompany this execution.
				botQueries := append([]string(nil), queries...)
				botObservations := append([]string(nil), observations...)
				botCalls := producerCalls
				queries, observations, producerCalls = nil, nil, 0
				ordinaryCtx := auth
				ordinaryModel := &employeeConfiguredSearchFixture{run: func(c context.Context, q *model.LLMRequest) (*model.LLMResponse, error) {
					if _, ok := execution.BotRunIdentityFromContext(c); ok {
						t.Fatal("ordinary producer acquired Bot guard")
					}
					response, ok := mdl.assessment(q)
					if !ok {
						return nil, fmt.Errorf("unexpected ordinary Chat model request")
					}
					return response, nil
				}}
				ordinarySearch, err := tools.NewPerplexityWebSearchTool(sharedSearchConfig, func(c context.Context, selected, modelName string, cfg *config.AppConfig) (model.LLM, error) {
					if selected != "selected-search" || modelName != "sonar" || cfg.Providers[selected]["api_key"] != "owned-fixture-only" {
						t.Fatal("ordinary Chat substituted configured provider")
					}
					return search, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				ordinarySessions := session.InMemoryService()
				ordinaryID := uuid.NewString()
				ordinaryRuntime, err := launcher.NewScopedChatRuntime(ordinaryCtx, launcher.ChatRuntimeDependencies{
					Config: &launcher.ChatFactoryConfig{AppConfig: sharedSearchConfig}, LLM: ordinaryModel,
					Sessions: ordinarySessions, SessionID: ordinaryID,
					Catalog: &launcher.ChatCatalog{MainThreadTools: []tool.Tool{ordinarySearch}, AllTools: []tool.Tool{ordinarySearch}},
				}, launcher.ChatRuntimeHooks{})
				if err != nil {
					t.Fatal(err)
				}
				defer ordinaryRuntime.Cleanup()
				completion := api.RunEmployeeOrdinaryChatTest(t, ordinaryCtx, ordinaryRuntime.ChatAgent, ordinarySessions, ordinaryModel, ordinaryID, p.Subject, "Apple News\nOwner refinement: "+refinement)
				botEvidence, _ := json.Marshal(botObservations)
				chatEvidence, _ := json.Marshal(observations)
				if strings.Join(queries, "\n") != strings.Join(botQueries, "\n") || string(chatEvidence) != string(botEvidence) || producerCalls != botCalls || len(queries) != want || completion.FinalText != run.Report.EmployeeMessage.Text || reviewCalls != 0 {
					t.Fatalf("Chat/Bot actual observations diverged: Chat queries=%v evidence=%s calls=%d final=%q; Bot queries=%v evidence=%s calls=%d final=%q", queries, chatEvidence, producerCalls, completion.FinalText, botQueries, botEvidence, botCalls, run.Report.EmployeeMessage.Text)
				}
				t.Logf("ordinary-chat-compared=true shared-configured-provider=true exact-observations=%d Chat-model=%d Bot-model=%d final-source-body-equal=true", len(observations), producerCalls, botCalls)
				t.Logf("real title/answer routes, configured scoped search=%d, reviewer=0 artifact=0 mandatory-plan=0", want)
			})
		}
	}
}

type employeeConfiguredSearchFixture struct {
	run func(context.Context, *model.LLMRequest) (*model.LLMResponse, error)
}

func (*employeeConfiguredSearchFixture) Name() string { return "sonar" }
func (m *employeeConfiguredSearchFixture) GenerateContent(c context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) { response, err := m.run(c, q); yield(response, err) }
}
