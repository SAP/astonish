package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type employeeBrowserModel struct {
	calls          atomic.Int64
	question       atomic.Bool
	t              *testing.T
	payload        map[string]any
	captured       chan struct{}
	release        chan struct{}
	sourceReturned *atomic.Bool
}

func (*employeeBrowserModel) Name() string { return "interactive-http" }
func (m *employeeBrowserModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.question.Load() {
			m.calls.Add(1)
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "employee-question", Name: "execute_tool", Args: map[string]any{"name": "ask_user", "arguments": map[string]any{"kind": "text", "prompt": "Which employee report should I prepare next?"}}}}}}}, nil)
			return
		}
		if m.calls.Add(1) == 1 {
			call := &genai.FunctionCall{ID: "employee-control", Name: "propose_bot_control", Args: map[string]any{"payload": m.payload}}
			if m.payload["operation"] == store.BotControlScheduleCreate {
				call.Name = "execute_tool"
				call.Args = map[string]any{"name": "schedule_job", "arguments": map[string]any{"name": "Employee report", "schedule": "* * * * *", "timezone": "UTC", "instructions": "Read local status and report", "mode": "adaptive"}}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
			return
		}
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil {
					m.t.Logf("tool result: %+v", part.FunctionResponse)
				}
			}
		}
		close(m.captured)
		select {
		case <-ctx.Done():
			return
		case <-m.release:
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Your request has been recorded.", "model"), TurnComplete: true}, nil)
		m.sourceReturned.Store(true)
	}
}

func TestBotEmployeeBrowserAcceptance(t *testing.T) { runEmployeeBrowser(t, "routine") }
func TestBotEmployeeEscalationBrowserAcceptance(t *testing.T) {
	for _, decision := range []string{"approve", "deny", "serverexpiry", "stalerace"} {
		t.Run(decision, func(t *testing.T) { runEmployeeBrowser(t, decision) })
	}
}

// All Bot API responses, operations and transcript events come from production
// handlers and a scoped SQLite store. Only identity/settings, model and physical
// home provisioning are controlled; no browser fetch interception is used.
func runEmployeeBrowser(t *testing.T, scenario string) {
	revisionProbe := scenario == "revision"
	if revisionProbe {
		scenario = "routine"
	}
	probe := newComposerRevisionProbe()
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after npm run build")
	}
	scope := store.BotScopePersonal
	if scenario != "routine" {
		scope = store.BotScopeTeam
	}
	f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
	defer f.worker.Stop()
	principal, _ := execution.PrincipalFromContext(f.ctx)
	principal.Authentication = execution.AuthMethodPlatformJWT
	auth, err := execution.WithPrincipal(f.ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	op := store.BotControlGoalCreate
	payload := map[string]any{"operation": op, "reason": "Owner requested a report goal", "goal": map[string]any{"goal": map[string]any{"title": "Ship employee report", "completion_criteria": "Report delivered", "status": "active"}}}
	if scenario == "routine" {
		op = store.BotControlScheduleCreate
		payload = map[string]any{"operation": op, "reason": "Owner requested every minute", "schedule": map[string]any{"trigger": map[string]any{"kind": "cron", "cron": "* * * * *", "timezone": "UTC", "instructions": "Read local status and report"}}}
	}
	applyReached, applyRelease := make(chan struct{}), make(chan struct{})
	var applyOnce sync.Once
	var unblockOnce sync.Once
	unblockApply := func() { unblockOnce.Do(func() { close(applyRelease) }) }
	defer unblockApply()
	var sourceReturned atomic.Bool
	policy := func(ctx context.Context, _ store.BotMandateRef) (store.BotMandatePolicy, error) {
		resources := []string{"propose_bot_control", "ask_user"}
		if scenario == "routine" {
			resources = append(resources, "schedule_job")
		}
		ttl := time.Hour
		if scenario == "serverexpiry" {
			ttl = 10 * time.Second
		}
		return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: resources}}, ExceptionConstraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{entstore.RoutineControlResource(store.BotControlGoalCreate)}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: ttl}, nil
	}
	m := &employeeBrowserModel{sourceReturned: &sourceReturned, t: t, payload: payload, captured: make(chan struct{}), release: make(chan struct{})}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, MandatePolicy: policy, TurnRegistry: f.registry, Sandbox: journeyHomeBackend{}, Pool: f.pool, WorkerID: "employee-browser", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(ctx context.Context, _ execution.Principal, _ store.BotScope, _, _ string) (model.LLM, error) {
		// Candidate readiness runs after CommitRun; a model return or policy callback
		// alone is not evidence of a committed source outcome.
		if scenario == "routine" && sourceReturned.Load() {
			applyOnce.Do(func() { close(applyReached) })
			select {
			case <-applyRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return m, nil
	}, Prepare: func(_ context.Context, q bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		workflow, e := launcher.BotWorkflowTools(q, scope)
		if e != nil {
			return nil, e
		}
		return &launcher.BotChatSafeResources{Tools: workflow, PolicyRuntime: agent.BotPolicyRuntime{Guard: employeeBrowserGuard{repo: f.repo, req: q}, Classifier: launcher.BotCatalogueClassifier{}}, WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	daemon.ConfigureBotScheduler(&scheduler.Executor{}, nil, worker.SchedulerCallbacks())
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(m.release) }) }
	defer release()
	ods, err := f.db.ForOrg(principal.OrgSlug)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Services{Bots: f.repo, Sessions: ods.ForTeam(principal.TeamSlug).Sessions(), TenantRouter: f.db, PersonalBots: f.repo, PersonalSessions: ods.ForUser(principal.Subject).Sessions(), BotMandatePolicy: policy, BotTurnRegistry: f.registry, BotInteractiveReady: true}
	svc.BotAuthorizedOperationFactory = func(context.Context, *store.Services, store.BotScope) (store.BotAuthorizedOperationStore, error) {
		return worker.AuthorizedOperations(f.repo)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	root := "/api/studio/bots"
	routes := []struct {
		path, method string
		h            http.HandlerFunc
	}{
		{root, "GET", api.BotListHandler}, {root + "/{botID}", "GET", api.BotGetHandler}, {root + "/{botID}/snapshot", "GET", api.BotSnapshotHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler}, {root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler}, {root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler}, {root + "/{botID}/messages", "POST", api.BotMessageHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler}, {root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/goals", "GET", api.BotGoalsHandler}, {root + "/{botID}/operations/{operationID}/decision", "POST", api.BotOperationDecisionHandler}, {root + "/{botID}/sessions/{sessionID}/stream", "GET", api.BotHistoryStreamHandler}, {root + "/{botID}/sessions/{sessionID}/events", "GET", api.BotEventsHandler}}
	var decisions atomic.Int64
	var readOnlyIdentity atomic.Bool
	viewer := principal
	viewer.Subject = uuid.NewString()
	viewerAuth, err := execution.WithPrincipal(auth, viewer)
	if err != nil {
		t.Fatal(err)
	}
	viewerAuth = store.WithTenantContext(viewerAuth, &store.TenantContext{UserID: viewer.Subject, OrgSlug: viewer.OrgSlug, TeamSlug: viewer.TeamSlug})
	if scope == store.BotScopeTeam {
		org, e := f.db.Organizations().GetBySlug(auth, principal.OrgSlug)
		if e != nil {
			t.Fatal(e)
		}
		team, e := ods.Teams().GetTeamBySlug(auth, principal.TeamSlug)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.db.Users().Create(auth, &store.User{ID: viewer.Subject, Email: viewer.Subject + "@example.test", DisplayName: "Viewer", Status: "active"}); e != nil {
			t.Fatal(e)
		}
		if e = f.db.Organizations().AddMember(auth, viewer.Subject, org.ID, "member"); e != nil {
			t.Fatal(e)
		}
		if e = ods.Teams().AddMember(auth, &store.TeamMembership{UserID: viewer.Subject, TeamID: team.ID, Role: "viewer"}); e != nil {
			t.Fatal(e)
		}
	}
	var raceTransition func()
	var responseStatus atomic.Int64
	var decisionBody struct {
		PayloadDigest    string `json:"payload_digest"`
		ExpectedRevision int64  `json:"expected_revision"`
		Approve          bool   `json:"approve"`
	}
	for _, r := range routes {
		router.HandleFunc(r.path, r.h).Methods(r.method)
	}
	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var fixture any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			fixture = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			userID := principal.Subject
			if readOnlyIdentity.Load() {
				userID = viewer.Subject
			}
			role := "owner"
			if readOnlyIdentity.Load() {
				role = "viewer"
			}
			fixture = map[string]any{"user": map[string]any{"id": userID, "email": "employee@example.test", "display_name": "Owner", "role": role, "platform_role": "user"}, "org": map[string]any{"slug": principal.OrgSlug}, "team": principal.TeamSlug}
		case "/api/orgs":
			fixture = map[string]any{"orgs": []any{}, "active_org": principal.OrgSlug}
		case "/api/teams":
			fixture = []any{map[string]any{"slug": principal.TeamSlug, "name": "Team"}}
		case "/api/settings/status":
			fixture = map[string]any{"setupRequired": false}
		case "/api/version":
			fixture = map[string]any{"version": "dev"}
		case "/api/agents", "/api/tools":
			fixture = []any{}
		case "/api/settings/config":
			fixture = map[string]any{}
		case "/api/settings/providers/effective":
			fixture = map[string]any{"providers": map[string]any{}}
		}
		if fixture != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(fixture)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if info, e := os.Stat(path); e == nil && !info.IsDir() {
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/operations/") && r.Method == "POST" {
			decisions.Add(1)
			var body map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&body)
			if raceTransition != nil {
				raceTransition()
			}
			encoded, _ := json.Marshal(body)
			json.Unmarshal(encoded, &decisionBody)
			r.Body = http.NoBody
			r.Body = ioNopEmployee(string(encoded))
			if r.URL.Query().Get("scope") != string(scope) {
				t.Error("decision lost scope")
			}
		}
		requestAuth := auth
		if readOnlyIdentity.Load() {
			requestAuth = viewerAuth
		}
		requestCtx, cancel := context.WithCancel(requestAuth)
		defer cancel()
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		if strings.Contains(r.URL.Path, "/operations/") && r.Method == "POST" {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, r.WithContext(store.WithServices(requestCtx, svc)))
			responseStatus.Store(int64(recorder.Code))
			for key, values := range recorder.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		if revisionProbe && strings.HasSuffix(r.URL.Path, "/messages") && r.Method == http.MethodPost {
			probe.serve(t, w, r.WithContext(store.WithServices(requestCtx, svc)), router, f.repo, auth, f.b.ID)
			return
		}
		router.ServeHTTP(w, r.WithContext(store.WithServices(requestCtx, svc)))
	}))
	defer server.Close()
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		var ok bool
		bin, ok = browserlauncher.LookPath()
		if !ok {
			t.Fatal("browser prerequisite unavailable")
		}
	}
	launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(35 * time.Second)
	page = page.WithPanic(func(v any) {
		text, _ := page.CancelTimeout().Eval(`()=>document.body.innerText`)
		t.Fatalf("browser: %v\n%v", v, text)
	})
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	if page.MustEval(`()=>document.querySelectorAll('[data-operation-id]').length`).Int() != 0 {
		t.Fatal("nonempty initial operation state")
	}
	message := "Create a goal to ship employee report"
	if scenario == "routine" {
		message = "Schedule every minute to read local status and report"
	}
	page.MustElement(`[data-testid="chat-input"]`).MustInput(message)
	page.MustElement(`[data-testid="send-button"]`).MustClick()
	page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, e := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
		if e != nil {
			t.Fatal(e)
		}
		if len(runs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("UI message not admitted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	poll := make(chan error, 1)
	go func() { poll <- worker.Poll(context.Background()) }()
	select {
	case <-m.captured:
	case e := <-poll:
		t.Fatalf("worker did not capture: %v", e)
	case <-time.After(15 * time.Second):
		t.Fatal("model capture timeout")
	}
	page.MustElement(`[data-operation-id]`).MustVisible()
	if scenario == "routine" {
		page.MustElementR(`[data-operation-id] h3`, "Pending").MustVisible()
		if page.MustEval(`()=>document.querySelectorAll('[data-review-id]').length`).Int() != 0 {
			t.Fatal("routine demanded review")
		}
	}
	release()
	if scenario == "routine" {
		select {
		case <-applyReached:
		case e := <-poll:
			t.Fatalf("source did not reach terminal pending apply: %v", e)
		case <-time.After(10 * time.Second):
			t.Fatal("pending apply timeout")
		}
		committedRuns, e := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
		if e != nil || len(committedRuns) != 1 {
			t.Fatalf("source readback: %v %v", committedRuns, e)
		}
		source, e := f.repo.GetRun(auth, f.b.ID, committedRuns[0].ID)
		wantSourceStatus := store.RunPartial
		if revisionProbe {
			// Deferred schedule capture is an observed pending operation, not an
			// unverified mutation; the worker commits it separately below.
			wantSourceStatus = store.RunSucceeded
		}
		if e != nil || source.Status != wantSourceStatus || source.FinishedAt == nil {
			t.Fatalf("source must be durably terminal before apply: %+v %v", source, e)
		}
		reportJSON, _ := json.Marshal(source.Report)
		t.Logf("persisted source report: %s", reportJSON)
		if revisionProbe {
			if len(source.Report.Unverified) != 0 || len(source.Report.Changed) != 0 || len(source.Report.Unchanged) != 1 {
				t.Fatalf("pending capture must not claim an applied change: %s", reportJSON)
			}
		} else if len(source.Report.Unverified) != 1 || source.Report.Unverified[0] != "Tool execute_tool returned without independently verified outcome metadata." || len(source.Report.Changed) != 0 {
			t.Fatalf("unexpected source outcome: %s", reportJSON)
		}
		page.MustElementR(`[data-operation-id] h3`, "Pending").MustVisible()
		page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
		if page.MustEval(`()=>document.querySelectorAll('[data-operation-id]').length`).Int() != 1 {
			t.Fatal("pending operation duplicated")
		}
		t.Logf("source durably committed status=%s finished=%s while operation remains Pending and workspace remains working", source.Status, source.FinishedAt)
		unblockApply()
	}
	if err = <-poll; err != nil {
		t.Fatal(err)
	}
	runs, err := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatal("unexpected runs", err)
	}
	rt, err := worker.AuthorizedOperations(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := rt.ListAuthorizedOperations(auth, *runs[0].MandateRef)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations %+v %v", ops, err)
	}
	if scenario != "routine" {
		page.MustElementR(`[data-operation-id] h3`, "Blocked").MustVisible()
		page.MustElementR(`[data-operation-id] dd`, "Ship employee report").MustVisible()
		exceptionCard := page.MustElement(`[data-operation-id="` + ops[0].ID.String() + `"]`)
		if ops[0].Exception == nil {
			t.Fatal("missing bounded exception")
		}
		if expiry := exceptionCard.MustElement("time"); expiry.MustText() != ops[0].Exception.ExpiresAt.UTC().Format(time.RFC3339Nano) {
			t.Fatal("exact exception expiry not visible")
		}
		if !strings.Contains(exceptionCard.MustText(), "Permission requested for this saved action only.") {
			t.Fatal("exception lifetime scope missing")
		}
		if page.MustEval(`()=>document.querySelector('[data-operation-id]').innerText.includes('payload_digest')`).Bool() {
			t.Fatal("raw authority leaked")
		}
		if scenario == "approve" {
			readOnlyIdentity.Store(true)
			page.MustReload()
			page.MustElementR(`[data-operation-id] h3`, "Blocked").MustVisible()
			page.MustElementR(`[data-operation-id] dd`, "Ship employee report").MustVisible()
			if page.MustEval(`()=>document.querySelectorAll('[data-operation-id] button').length`).Int() != 0 {
				t.Fatal("viewer retained mutation controls")
			}
			body, _ := json.Marshal(map[string]any{"payload_digest": ops[0].Provenance.PayloadDigest, "expected_revision": ops[0].Revision, "approve": false})
			status := page.MustEval(`async (url, body)=>(await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body})).status`, f.path+"/operations/"+ops[0].ID.String()+"/decision?scope=team", string(body)).Int()
			if status != http.StatusForbidden {
				t.Fatalf("viewer decision status %d", status)
			}
			saved, e := rt.ReadAuthorizedOperation(auth, ops[0].MandateRef, ops[0].ID)
			if e != nil || !reflect.DeepEqual(saved, &ops[0]) {
				t.Fatalf("viewer mutated operation: %+v %v", saved, e)
			}
			readOnlyIdentity.Store(false)
			decisions.Store(0)
			page.MustReload()
			t.Log("authenticated same-tenant viewer read real blocked card without mutation controls; direct decision forbidden; durable operation unchanged")
		}
		if scenario == "serverexpiry" || scenario == "stalerace" {
			before := ops[0]
			if scenario == "serverexpiry" {
				if before.Exception == nil || !time.Now().Before(before.Exception.ExpiresAt) {
					t.Fatal("expiry fixture must expose a live exception before submission")
				}
				// Hold the submitted request until the real persisted expiry. The
				// browser submits valid coordinates while the card is still live;
				// only ordinary server time advances, not private clocks or grants.
				raceTransition = func() {
					wait := time.Until(before.Exception.ExpiresAt) + time.Millisecond
					if wait > 10*time.Second {
						t.Error("expiry wait exceeds fixture TTL")
						return
					}
					if wait > 0 {
						time.Sleep(wait)
					}
				}
			}
			if scenario == "stalerace" {
				raceTransition = func() {
					saved, e := rt.(store.BotRoutineExceptionStore).DecideRoutineException(auth, store.BotRoutineExceptionDecision{MandateRef: before.MandateRef, OperationID: before.ID, PayloadDigest: before.Provenance.PayloadDigest, ExpectedRevision: before.Revision, Approve: false})
					if e != nil {
						t.Errorf("concurrent deny: %v", e)
						return
					}
					before = *saved
				}
			}
			page.MustElementR(`[data-operation-id] button`, "^Approve create goal$").MustClick()
			page.MustElementR(`[data-operation-id] [role="alert"]`, "Decision could not be verified").MustVisible()
			// The saved exception shares the mandate expiry; verify rejects the
			// expired mandate as forbidden before reaching the grant-expiry branch.
			wantStatus := http.StatusForbidden
			if scenario == "stalerace" {
				wantStatus = http.StatusConflict
			}
			if int(responseStatus.Load()) != wantStatus || decisionBody.ExpectedRevision != ops[0].Revision || decisionBody.ExpectedRevision <= 0 || decisionBody.PayloadDigest != ops[0].Provenance.PayloadDigest {
				t.Fatalf("negative decision status=%d coordinates=%+v", responseStatus.Load(), decisionBody)
			}
			saved, e := rt.ReadAuthorizedOperation(auth, before.MandateRef, before.ID)
			if e != nil || !reflect.DeepEqual(saved, &before) {
				t.Fatalf("rejected request changed durable operation: %+v %v", saved, e)
			}
			goals, e := f.repo.(store.BotGoalStore).ListGoals(auth, f.b.ID)
			if e != nil || len(goals) != 0 {
				t.Fatalf("negative goals %v %v", goals, e)
			}
			page.MustElement(`[data-operation-id]`).MustVisible()
			if !page.MustEval(`()=>[...document.querySelectorAll('[data-operation-id] button')].every(b=>b.disabled)`).Bool() {
				t.Fatal("error allows blind retry")
			}
			t.Logf("%s: HTTP %d, valid revision %d, persisted revision %d status %s unchanged by rejected request; zero goals", scenario, wantStatus, decisionBody.ExpectedRevision, before.Revision, before.Status)
			return
		}
		button := "^Deny$"
		if scenario == "approve" {
			button = "^Approve create goal$"
		}
		page.MustElementR(`[data-operation-id] button`, button).MustClick()
		page.MustWait(`()=>!document.querySelector('[data-operation-id] button')`)
		if decisions.Load() != 1 || decisionBody.PayloadDigest != ops[0].Provenance.PayloadDigest || decisionBody.ExpectedRevision != ops[0].Revision || decisionBody.Approve != (scenario == "approve") {
			t.Fatalf("decision coordinates incorrect: %+v", decisionBody)
		}
		if err = worker.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	expected := "Applied"
	if scenario == "deny" {
		expected = "Cancelled"
	}
	page.MustElementR(`[data-operation-id] h3`, expected).MustVisible()
	if scenario == "routine" {
		page.MustElementR(`[data-operation-id] p`, "Next run:").MustVisible()
		jobs := ods.ForUser(principal.Subject).ScheduledJobs().List(auth)
		if len(jobs) != 1 || !jobs[0].Enabled || jobs[0].NextRun == nil {
			t.Fatalf("schedule readback %+v", jobs)
		}
	} else {
		goals, e := f.repo.(store.BotGoalStore).ListGoals(auth, f.b.ID)
		want := 0
		if scenario == "approve" {
			want = 1
		}
		if e != nil || len(goals) != want {
			t.Fatalf("goals %v %v", goals, e)
		}
	}
	if revisionProbe {
		probe.check(t, page, f.repo, auth, f.b)
		return
	}
	before := m.calls.Load()
	page.MustReload()
	page.MustElementR(`[data-operation-id] h3`, expected).MustVisible()
	if page.MustEval(`()=>document.querySelectorAll('[data-operation-id]').length`).Int() != 1 || m.calls.Load() != before {
		t.Fatal("reload duplicated operation or replayed model")
	}
	if page.MustEval(`()=>document.querySelector('[aria-label="Bot workspace"]').innerText.includes('Changes made')`).Bool() {
		t.Fatal("synthetic report rendered")
	}
	for _, width := range []int{1280, 390} {
		for _, dark := range []bool{false, true} {
			page.MustSetViewport(width, 844, 1, false)
			page.MustEval(`dark=>document.documentElement.classList.toggle('dark',dark)`, dark)
			page.MustElement(`[data-operation-id]`).MustVisible()
			if !page.MustEval(`()=>{const c=document.querySelector('[data-operation-id]'); const r=c.getBoundingClientRect(); const title=c.querySelector('h3'); return r.width>100 && !!title && getComputedStyle(title).fontSize !== '0px' && c.scrollWidth<=c.clientWidth+2}`).Bool() {
				t.Log("screenshot readability diagnostic: operation card geometry requires manual review")
			}
			dir := "../../.tmp/employee-browser-artifacts"
			if err = os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			shot, e := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d-dark-%t.png", scenario, width, dark)), shot, 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	if scenario == "routine" {
		m.question.Store(true)
		page.MustElement(`[data-testid="chat-input"]`).MustInput("Ask me which employee report to prepare next")
		page.MustElement(`[data-testid="send-button"]`).MustClick()
		page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
		deadline := time.Now().Add(10 * time.Second)
		for {
			runs, e := f.repo.ListRuns(auth, f.b.ID, store.BotPage{})
			if e != nil {
				t.Fatal(e)
			}
			if len(runs) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("question turn not admitted")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if e := worker.Poll(context.Background()); e != nil {
			t.Fatal(e)
		}
		page.MustElementR(`[data-review-id]`, "Which employee report should I prepare next").MustVisible()
		assertLast := func() {
			if !page.MustEval(`()=>{const cards=[...document.querySelectorAll('[data-operation-id],[data-review-id]')].filter(e=>e.checkVisibility());return cards.length===2 && cards.at(-1).hasAttribute('data-review-id')}`).Bool() {
				t.Fatalf("pending question must remain after the single applied operation: %s", page.MustEval(`()=>JSON.stringify([...document.querySelectorAll('[data-operation-id],[data-review-id]')].map(e=>({tag:e.tagName,text:e.innerText,html:e.outerHTML.slice(0,400)})))`).Str())
			}
		}
		assertLast()
		page.MustReload()
		page.MustElementR(`[data-review-id]`, "Which employee report should I prepare next").MustVisible()
		assertLast()
	}
	t.Log("built Studio + real HTTP + persisted worker operation; canonical reload and four viewport/theme screenshots")
}

type employeeBrowserGuard struct {
	repo store.BotStore
	req  bot.RunRequest
}

func (g employeeBrowserGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if g.req.Mandate == nil {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	if e := g.req.Mandate.Check(ctx); e != nil {
		return execution.PolicyDecision{}, e
	}
	if e := g.req.Budget.Check(ctx); e != nil {
		return execution.PolicyDecision{}, e
	}
	b, e := g.repo.Get(ctx, g.req.Bot.ID)
	if e != nil {
		return execution.PolicyDecision{}, e
	}
	r, e := g.repo.GetRun(ctx, g.req.Bot.ID, g.req.Run.ID)
	if e != nil {
		return execution.PolicyDecision{}, e
	}
	if !g.req.Run.Intent.AllowsStatus(b.Status) || b.Version != g.req.Version.Version || b.OwnerID != g.req.Bot.OwnerID || r.Status != store.RunRunning || r.Lease.Fence != g.req.Run.Lease.Fence || r.Lease.WorkerID != g.req.Run.Lease.WorkerID || !time.Now().Before(r.Lease.ExpiresAt) {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if d.Kind == execution.PolicyConsumeGrant {
		grant, e := g.req.Mandate.AuthorizeAction(ctx, a)
		if e != nil || grant.ActionDigest != d.ActionDigest {
			return d, store.ErrBotForbidden
		}
		d.Kind = execution.PolicyAllow
	}
	return d, nil
}

type employeeReadCloser struct{ *strings.Reader }

func (employeeReadCloser) Close() error         { return nil }
func ioNopEmployee(s string) employeeReadCloser { return employeeReadCloser{strings.NewReader(s)} }
