package daemon

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

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Only the model, isolated backend and harmless tool callback are controlled.
// HTTP handlers, worker, Chat runtime, live guard, approval and transcript stores
// are production implementations; no approval/checkpoint is fabricated.
type draftApprovalHTTPModel struct {
	workerTestModel
	attempt bool
}

func (m *draftApprovalHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if !m.attempt {
		return m.workerTestModel.GenerateContent(ctx, r, stream)
	}
	m.attempt = false
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "read_file", Args: map[string]any{"body": "exact"}}}}}}, nil)
	}
}

type draftApprovalHTTPClassifier struct{}

func (draftApprovalHTTPClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Effect: execution.EffectSend, Arguments: args, ConstraintsVerified: true}, nil
}

func TestBotIntentHTTPWorkerDraftApprovalExactOnce(t *testing.T) {
	testBotHTTPWorkerApprovalExactOnce(t, false)
}

func TestBotApproveContinueHTTPWorkerExactOnce(t *testing.T) {
	testBotHTTPWorkerApprovalExactOnce(t, true)
}

func testBotHTTPWorkerApprovalExactOnce(t *testing.T, combined bool) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "draft-approval", Name: "Draft approval", Status: "active"}
			if err := db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err := db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			repo := ods.ForTeam(team.Slug).Bots()
			ss := ods.ForTeam(team.Slug).Sessions()
			schedules := ods.ForTeam(team.Slug).ScheduledJobs()
			pin := ods.ForTeam(team.Slug).SetSessionPin
			svc := &store.Services{Bots: repo, Sessions: ss}
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				ss = ods.ForUser(owner.String()).Sessions()
				schedules = ods.ForUser(owner.String()).ScheduledJobs()
				pin = ods.ForUser(owner.String()).SetSessionPin
				svc.PersonalBots = repo
				svc.PersonalSessions = ss
			}
			llm := &draftApprovalHTTPModel{attempt: true}
			var effects atomic.Int64
			target, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Harmless counted exact action"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
				if args["body"] != "exact" {
					return nil, fmt.Errorf("changed exact arguments: %v", args)
				}
				effects.Add(1)
				return map[string]any{"receipt": "controlled exact action"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			worker, err := NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: activationHomeBackend{}, Pool: workerTestPool{}, WorkerID: "draft-approval-worker", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(_ context.Context, q bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				return &launcher.BotChatSafeResources{Tools: []tool.Tool{target}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: draftApprovalHTTPClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			auth, _, err := worker.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			version := store.DefaultBotVersion()
			version.Autonomy.AllowedTools = []string{"read_file"}
			version.Triggers = []store.TriggerSpec{{ID: uuid.New(), Kind: store.TriggerCron, Cron: "0 * * * *", Timezone: "UTC", Instructions: "Remain disabled during one-time test", Enabled: false}}
			version.Charter = "Perform one owner-approved exact action"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Draft approval", OwnerID: owner, Version: version, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err := entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "registered-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/test-run", api.BotTestRunHandler)
			router.HandleFunc("/bots/{botID}/runs", api.BotRequestRunHandler)
			router.HandleFunc("/bots/{botID}/runs/{runID}", api.BotRunHandler)
			router.HandleFunc("/bots/{botID}/approvals", api.BotApprovalsHandler)
			router.HandleFunc("/bots/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler)
			router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler)
			router.HandleFunc("/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			defer server.Close()
			request := func(method, path, payload string, want int) []byte {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+"/bots/"+b.ID.String()+"/"+path+"?scope="+string(scope), strings.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != want {
					t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, raw)
				}
				return raw
			}
			assertState := func(want store.BotStatus) {
				t.Helper()
				saved, err := repo.Get(auth, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Status != want || saved.Version != b.Version {
					t.Fatalf("lifecycle changed: %+v", saved)
				}
				v, err := repo.GetVersion(auth, b.ID, b.Version)
				if err != nil {
					t.Fatal(err)
				}
				for _, trigger := range v.Triggers {
					if trigger.Enabled {
						t.Fatal("one-time run enabled recurrence")
					}
				}
				for _, job := range schedules.List(auth) {
					if job.Enabled && store.BotOwnedSchedule(job) {
						t.Fatal("one-time run enabled scheduler job")
					}
				}
			}
			poll := func() {
				t.Helper()
				if err := worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
			}
			getRun := func(id uuid.UUID) store.BotRun {
				t.Helper()
				var r store.BotRun
				if err := json.Unmarshal(request("GET", "runs/"+id.String(), "", 200), &r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			approvals := func() []store.BotApproval {
				t.Helper()
				var result struct {
					Approvals []store.BotApproval `json:"approvals"`
				}
				if err := json.Unmarshal(request("GET", "approvals", "", 200), &result); err != nil {
					t.Fatal(err)
				}
				return result.Approvals
			}
			assertState(store.BotDraft)
			payload := fmt.Sprintf(`{"version":%d,"idempotency_key":"draft-exact","instructions":"Test exact action once"}`, b.Version)
			request("POST", "runs", payload, 403)
			var run store.BotRun
			if err := json.Unmarshal(request("POST", "test-run", payload, 202), &run); err != nil {
				t.Fatal(err)
			}
			assertState(store.BotDraft)
			poll()
			assertState(store.BotDraft)
			saved := getRun(run.ID)
			if saved.Status != store.RunWaitingApproval || saved.Intent != store.BotIntentDraftTest || effects.Load() != 0 {
				t.Fatalf("checkpoint run=%+v effects=%d", saved, effects.Load())
			}
			all := approvals()
			if len(all) != 1 || all[0].Status != store.ApprovalPending {
				t.Fatalf("approvals: %+v", all)
			}
			approval := all[0]
			checkpoint, err := bot.DecodeApprovalCheckpoint(saved.Checkpoint, saved)
			if err != nil || checkpoint.Format != bot.ApprovalCheckpointFormat || !checkpoint.Matches(approval) {
				t.Fatalf("exact checkpoint %+v: %v", checkpoint, err)
			}
			var args map[string]any
			if err := json.Unmarshal(approval.Action.Arguments, &args); err != nil || args["body"] != "exact" || approval.Action.Tool != "read_file" {
				t.Fatalf("saved action %+v %v", approval.Action, err)
			}
			decision := fmt.Sprintf(`{"action_digest":%q,"status":"approved"}`, approval.ActionDigest)
			decisionPath := "approvals/" + approval.ID.String() + "/decision"
			resume := fmt.Sprintf(`{"approval_id":%q}`, approval.ID)
			resumePath := "runs/" + run.ID.String() + "/resume"
			continuePath := "approvals/" + approval.ID.String() + "/approve-and-continue"
			continueBody := fmt.Sprintf(`{"action_digest":%q}`, approval.ActionDigest)
			if combined {
				request("POST", continuePath, `{"action_digest":"changed"}`, 409)
				request("POST", continuePath, fmt.Sprintf(`{"action_digest":%q,"instructions":"replacement"}`, approval.ActionDigest), 400)
				for i := 0; i < 2; i++ {
					var result store.BotApproveContinueResult
					if err := json.Unmarshal(request("POST", continuePath, continueBody, 202), &result); err != nil || result.RunID != run.ID || result.Continuation != store.BotContinuationQueued || result.Approval.ActionDigest != approval.ActionDigest {
						t.Fatalf("combined response %+v: %v", result, err)
					}
					assertState(store.BotDraft)
				}
			} else {
				for i := 0; i < 2; i++ {
					request("POST", decisionPath, decision, 200)
					poll()
					assertState(store.BotDraft)
				}
				if getRun(run.ID).Status != store.RunWaitingApproval || effects.Load() != 0 || llm.calls != 1 {
					t.Fatal("decision alone resumed execution")
				}
				for i := 0; i < 2; i++ {
					request("POST", resumePath, resume, 202)
					assertState(store.BotDraft)
				}
			}
			poll()
			assertState(store.BotDraft)
			saved = getRun(run.ID)
			// A plain callback receipt is deliberately not verified outcome metadata;
			// production reports partial rather than upgrading it to proven success.
			if saved.Status != store.RunPartial || len(saved.Report.Unverified) != 1 || saved.Report.Summary != "durable controlled result" || saved.Intent != store.BotIntentDraftTest || effects.Load() != 1 || llm.calls != 2 {
				t.Fatalf("resume run=%+v effects=%d calls=%d", saved, effects.Load(), llm.calls)
			}
			all = approvals()
			if len(all) != 1 || all[0].Status != store.ApprovalConsumed {
				t.Fatalf("not consumed exactly once: %+v", all)
			}
			request("POST", decisionPath, decision, 409)
			if combined {
				request("POST", continuePath, continueBody, 409)
			}
			request("POST", resumePath, resume, 409)
			request("POST", "test-run", payload, 202)
			poll()
			poll()
			assertState(store.BotDraft)
			if effects.Load() != 1 || llm.calls != 2 {
				t.Fatal("repeated approval/admission/poll executed twice")
			}
			t.Log("HTTP draft-test -> production worker exact checkpoint -> repeated approval (no execution) -> explicit resume -> exact effect once; draft and recurrence unchanged")

			// A different, ordinary run approved while active must not become resumable
			// merely because inactive draft-test approval continuation is supported.
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			// End the draft acceptance above before configuring the separate
			// ordinary-run scenario: activation validates trigger capabilities.
			b, err = repo.(store.BotTriggerStore).ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: version.Triggers[0].ID, ExpectedRevision: b.Revision, Action: store.BotTriggerRemove, Reason: "Separate ordinary-run scenario after draft acceptance"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "separate ordinary run"})
			if err != nil {
				t.Fatal(err)
			}
			llm.attempt = true
			payload = fmt.Sprintf(`{"version":%d,"idempotency_key":"ordinary-exact","instructions":"Ordinary exact action"}`, b.Version)
			var ordinary store.BotRun
			if err := json.Unmarshal(request("POST", "runs", payload, 202), &ordinary); err != nil {
				t.Fatal(err)
			}
			poll()
			saved = getRun(ordinary.ID)
			if saved.Status != store.RunWaitingApproval || saved.Intent != store.BotIntentRegular {
				t.Fatalf("ordinary checkpoint %+v", saved)
			}
			var old store.BotApproval
			for _, a := range approvals() {
				if a.RunID == ordinary.ID {
					old = a
				}
			}
			if old.ID == uuid.Nil {
				t.Fatal("ordinary approval missing")
			}
			if !combined {
				request("POST", "approvals/"+old.ID.String()+"/decision", fmt.Sprintf(`{"action_digest":%q,"status":"approved"}`, old.ActionDigest), 200)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "must block old approval"})
			if err != nil {
				t.Fatal(err)
			}
			if combined {
				continuePath = "approvals/" + old.ID.String() + "/approve-and-continue"
				continueBody = fmt.Sprintf(`{"action_digest":%q}`, old.ActionDigest)
				for i := 0; i < 2; i++ {
					var result store.BotApproveContinueResult
					if err := json.Unmarshal(request("POST", continuePath, continueBody, 200), &result); err != nil || result.Continuation != store.BotContinuationBlockedByPause || result.Approval.Status != store.ApprovalApproved {
						t.Fatalf("paused response %+v: %v", result, err)
					}
				}
			}
			request("POST", "runs/"+ordinary.ID.String()+"/resume", fmt.Sprintf(`{"approval_id":%q}`, old.ID), 409)
			calls := llm.calls
			poll()
			assertState(store.BotPaused)
			if effects.Load() != 1 || llm.calls != calls {
				t.Fatal("old regular approval executed while paused")
			}
			t.Log("ordinary approved checkpoint remains blocked by paused lifecycle")
			if combined {
				// Only an independent lifecycle action may remove the pause.
				b, err = repo.(interface {
					Resume(context.Context, store.BotLifecycleRequest) (*store.Bot, error)
				}).Resume(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "explicit owner resume"})
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					request("POST", continuePath, continueBody, 202)
				}
				poll()
				assertState(store.BotActive)
				if getRun(ordinary.ID).Status != store.RunPartial || effects.Load() != 2 {
					t.Fatal("regular exact continuation did not execute once")
				}
				request("POST", continuePath, continueBody, 409)
				poll()
				if effects.Load() != 2 {
					t.Fatal("regular action replayed")
				}
				t.Log("combined HTTP decision queues draft-test and regular continuations, paused decision stays blocked, consumed replay has no duplicate effect")
			}
		})
	}
}
