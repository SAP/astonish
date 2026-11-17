package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
)

// Exercise actual HTTP admission, title answer, worker, factory and provider input.
func TestBotTitleSchedulingFollowupLatestInputHTTP(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "ask_user", map[string]any{"kind": "select", "prompt": "Which AI news interests?", "allow_custom_answer": true, "options": []map[string]any{{"id": "models", "label": "Model releases"}, {"id": "policy", "label": "AI policy"}, {"id": "apps", "label": "AI applications"}}}, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	defer f.worker.Stop()
	var receipt titleHTTPReceipt
	if err := json.Unmarshal(f.request("POST", "/api/studio/bots", `{"name":"AI News","idempotency_key":"schedule-followup","start_from_title":true}`, 201), &receipt); err != nil {
		t.Fatal(err)
	}
	f.b, f.path = receipt.Bot, "/api/studio/bots/"+receipt.ID.String()
	f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"interactive-http"}`, 200)
	poll := func(id uuid.UUID) *store.BotRun {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; {
			if err := f.worker.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			run, err := f.repo.GetRun(f.ctx, f.b.ID, id)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != store.RunQueued || time.Now().After(deadline) {
				return run
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	run := poll(receipt.InitialTurn.Run.ID)
	if run.Status != store.RunWaitingApproval {
		t.Fatalf("title: %+v", run)
	}
	approvals, err := f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals %v %v", approvals, err)
	}
	const assignment = "New LLM Models and new Inference methods"
	a := approvals[0]
	body, _ := json.Marshal(map[string]any{"action_digest": a.ActionDigest, "response": map[string]any{"question_id": a.Interaction.QuestionID, "answer": assignment}})
	f.request("POST", f.path+"/approvals/"+a.ID.String()+"/approve-and-continue", string(body), 202)
	p, _ := execution.PrincipalFromContext(f.ctx)
	internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	if err := entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: f.b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: f.scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.model.called, f.model.name, f.model.args = false, "read_file", map[string]any{"path": "/isolated/receipt.txt"}
	run = poll(run.ID)
	if run.Status != store.RunSucceeded {
		t.Fatalf("initial completion: %+v", run)
	}
	const latest = "Great, scheduled to deliver it every 5 minutes"
	checked := 0
	var operationID uuid.UUID
	var currentMandate store.BotMandateRef
	var pendingOperation *store.BotOperation
	var policy store.BotMandatePolicyResolver
	workCalls := f.pool.calls
	f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
		raw, _ := json.Marshal(req.Contents)
		if !strings.Contains(string(raw), latest) {
			t.Errorf("latest owner request missing from actual provider input: %s", raw)
		}
		if !strings.Contains(string(raw), assignment) {
			t.Errorf("prior confirmed context missing: %s", raw)
		}
		if checked == 0 {
			prompt, _ := json.Marshal(req.Config.SystemInstruction)
			for _, want := range []string{"The newest owner message defines this turn's requested outcome", "Historical title confirmations and their answers are context, not a new instruction to repeat completed work"} {
				if !strings.Contains(string(prompt), want) {
					t.Errorf("missing current-turn precedence contract %q", want)
				}
			}
		}
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if response := part.FunctionResponse; response != nil && response.Name == "execute_tool" {
					if id, ok := response.Response["operation_id"].(string); ok {
						operationID = uuid.MustParse(id)
						if pendingOperation == nil {
							runtime, err := entstore.NewBotMandateRuntime(f.repo, policy)
							if err != nil {
								t.Fatal(err)
							}
							pendingOperation, err = runtime.(store.BotOperationStore).ReadBotOperation(f.ctx, currentMandate, operationID)
							if err != nil {
								t.Fatal(err)
							}
						}
						if response.Response["status"] != string(store.BotOperationPending) {
							t.Errorf("capture must report pending, not applied: %+v", response.Response)
						}
					}
				}
			}
		}
		checked++
		return nil, false
	}
	// New authenticated owner message gets a fresh bounded mandate; no prior
	// title answer or registry grant is used as schedule authorization.
	policy = func(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		return store.BotMandatePolicy{Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"schedule_job"}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig()), TTL: time.Hour}, nil
	}
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
	svc := &store.Services{TenantRouter: f.db, Bots: f.repo, PersonalBots: f.repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: policy}
	router := mux.NewRouter()
	router.HandleFunc("/api/studio/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth, svc)))
	}))
	defer server.Close()
	f.worker.Stop()
	f.model.principal = execution.Principal{Subject: p.Subject, Authentication: execution.AuthMethodBotMandate, Surface: execution.Surface("daemon")}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, MandatePolicy: policy, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return f.model, nil
	}, Prepare: func(c context.Context, r bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if r.Instructions != latest || r.Run.TitleConfirmation != nil || r.Mandate == nil || r.Mandate.Authorization.Ref().ID == uuid.Nil {
			t.Error("follow-up lost newest assignment or fresh authority")
		}
		currentMandate = *r.Run.MandateRef
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Mandate: r.Mandate, Guard: restartMandateGuard{r, f.repo}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.worker = worker
	defer worker.Stop()
	f.model.called, f.model.name, f.model.args = false, "execute_tool", map[string]any{"name": "schedule_job", "arguments": map[string]any{"name": "AI briefing", "mode": "adaptive", "schedule": "*/5 * * * *", "timezone": "UTC", "instructions": assignment}}
	var follow store.BotMessageReceipt
	body, _ = json.Marshal(map[string]any{"version": f.b.Version, "instructions": latest, "idempotency_key": "followup"})
	res, err := server.Client().Post(server.URL+f.path+"/messages?scope=personal", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusAccepted {
		t.Fatalf("followup HTTP: %d %s %v", res.StatusCode, raw, err)
	}
	if err := json.Unmarshal(raw, &follow); err != nil {
		t.Fatal(err)
	}
	input, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, f.b.ID, follow.Run.ID)
	if err != nil || input.Instructions != latest || input.Run.TitleConfirmation != nil {
		t.Fatalf("new input: %+v %v", input, err)
	}
	run = poll(follow.Run.ID)
	if checked == 0 {
		t.Fatalf("provider not called: %+v", run)
	}
	if operationID == uuid.Nil || run.MandateRef == nil {
		t.Fatalf("no pending schedule receipt or fresh mandate: %+v", run)
	}
	if f.pool.calls != workCalls {
		t.Fatal("scheduling follow-up repeated completed external work")
	}
	// Observe persistence while the source run is still active. Subsequent worker
	// application is intentionally outside this fixture's activation policy.
	op := pendingOperation
	if op == nil {
		t.Fatal("pending operation was never read from scoped storage")
	}
	if op.Status != store.BotOperationPending || op.Decision != store.BotOperationExecute || op.Payload.Operation != store.BotControlScheduleCreate || op.MandateRef != *run.MandateRef || op.Provenance == nil || op.Provenance.SourceRunID != run.ID || op.Provenance.UserEventID != run.UserEventID {
		t.Fatalf("schedule not durably bound to latest owner turn: %+v", op)
	}
	payload, _ := json.Marshal(op.Payload)
	if !strings.Contains(string(payload), assignment) || !strings.Contains(string(payload), "*/5 * * * *") {
		t.Fatalf("saved operation lost task or cadence: %s", payload)
	}
	t.Logf("checked %d provider requests; persisted pending schedule %s under fresh owner mandate", checked, operationID)
}
