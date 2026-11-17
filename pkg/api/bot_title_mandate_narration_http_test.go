package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Real HTTP title admission, durable mandate/approval and worker/factory path.
// A controlled HTTP source blocks before returning so Home delivery cannot be
// mistaken for final publication. No live daemon, provider or user data is used.
func TestBotTitleMandateAcknowledgementHomeHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) { testTitleMandateAcknowledgementHomeHTTP(t, scope) })
	}
}

func testTitleMandateAcknowledgementHomeHTTP(t *testing.T, scope store.BotScope) {
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
	svc := &store.Services{TenantRouter: f.db, Bots: f.repo, Sessions: ss, PersonalBots: f.repo, PersonalSessions: ss, BotMandatePolicy: policy}
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
			if !strings.Contains(string(raw), "Weekly roundup") {
				t.Errorf("producer lost owner answer: %s", raw)
			}
			mdl.called = true
			return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: acknowledgement}, {FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "web_fetch", Args: map[string]any{"url": upstream.URL}}}}}}, true
		}
		return nil, false
	}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, MandatePolicy: policy, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}, Prepare: func(c context.Context, r bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		reads, err := launcher.BotMandatedReadTools(r.Mandate, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }, AllowIP: func(context.Context, string, net.IP) error { return nil }})
		if err != nil {
			return nil, err
		}
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", Tools: reads, PolicyRuntime: agent.BotPolicyRuntime{Mandate: r.Mandate, Guard: restartMandateGuard{r, f.repo}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
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
	homePath := root + "/" + f.b.ID.String() + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
	initial := string(request("GET", homePath, "", 200))
	if titleAt, questionAt := strings.Index(initial, "AI news"), strings.Index(initial, "Which AI news assignment?"); titleAt < 0 || questionAt <= titleAt || strings.Contains(initial, acknowledgement) {
		t.Fatalf("initial Home must show title then question, without work narration: %s", initial)
	}
	body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": "weekly"}})
	answerPath := root + "/" + f.b.ID.String() + "/approvals/" + a.ID.String() + "/approve-and-continue"
	request("POST", answerPath, string(body), 202)
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
	for scanner.Scan() {
		if scanner.Text() == "event: replay_complete" {
			break
		}
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
		answerAt, ackAt := strings.Index(delta, "bot_owner_answer"), strings.Index(delta, acknowledgement)
		if answerAt < 0 || ackAt <= answerAt || strings.Count(delta, acknowledgement) != 1 || strings.Contains(delta, final) || strings.Contains(delta, `"type":"tool_call"`) {
			t.Fatalf("live public answer -> acknowledgement order without tool telemetry: %s", delta)
		}
	case <-streamCtx.Done():
		t.Fatal("live Home delta did not deliver before blocked source returned")
	}
	cancelStream()
	live := request("GET", homePath, "", 200)
	if !strings.Contains(string(live), acknowledgement) {
		t.Fatalf("Home missing actual producer acknowledgement while source blocked: %s", live)
	}
	if !strings.Contains(string(live), "Weekly roundup") || strings.Index(string(live), "bot_owner_answer") > strings.Index(string(live), acknowledgement) {
		t.Fatalf("owner answer absent or after acknowledgement: %s", live)
	}
	if strings.Contains(string(live), `"type":"tool_call"`) || strings.Contains(string(live), "web_fetch") || strings.Contains(string(live), final) || strings.Contains(string(live), "SOURCE_BODY_MUST_STAY_PRIVATE") {
		t.Fatalf("early final/source leaked: %s", live)
	}
	// Chronology remains diagnostic evidence, not a public tool card.
	runPath := root + "/" + f.b.ID.String() + "/sessions/" + run.SessionID.String() + "/stream"
	diagnostic := string(request("GET", runPath, "", 200))
	ackAt, toolAt := strings.Index(diagnostic, acknowledgement), strings.Index(diagnostic, `"name":"web_fetch"`)
	if ackAt < 0 || toolAt <= ackAt || !strings.Contains(diagnostic, `"type":"tool_call"`) {
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
	// Simulate Home telemetry persisted by the previous release. Presentation
	// hides it, while the source row and cursor remain available in storage.
	legacy := &session.Event{ID: "historical-private-tool", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{RunID: run.ID, SessionID: run.SessionID, TurnID: uuid.New(), Sequence: 99, Type: "tool_call", Payload: json.RawMessage(`{"name":"web_search","args":{"query":"HISTORICAL_PRIVATE_QUERY"}}`)}}}}
	if err := ss.AppendFleetEvent(auth, f.b.HomeSessionID.String(), legacy); err != nil {
		t.Fatal(err)
	}
	historical := string(request("GET", homePath, "", 200))
	if strings.Contains(historical, "HISTORICAL_PRIVATE_QUERY") || !strings.Contains(historical, legacy.ID) {
		t.Fatal("historical Home projection leaked telemetry or dropped its cursor row")
	}
	stored, err := ss.ReadTranscriptEvents(auth, "astonish_chat", p.Subject, f.b.HomeSessionID.String())
	if err != nil {
		t.Fatal(err)
	}
	rawStored, _ := json.Marshal(stored)
	if !strings.Contains(string(rawStored), "HISTORICAL_PRIVATE_QUERY") {
		t.Fatal("presentation deleted historical diagnostics")
	}
	finished := request("GET", homePath, "", 200)
	if strings.Count(string(finished), "bot_owner_answer") != 1 || strings.Count(string(finished), acknowledgement) != 1 || strings.Count(string(finished), final) != 1 || strings.Contains(string(finished), "SOURCE_BODY_MUST_STAY_PRIVATE") || calls.Load() != 1 {
		t.Fatalf("publication/replay boundary: %s calls=%d", finished, calls.Load())
	}
}
