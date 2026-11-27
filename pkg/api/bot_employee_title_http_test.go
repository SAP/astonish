package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// employeeHTTPModel follows the returned server-owned handle, never a fixture ID.
type employeeHTTPModel struct {
	calls     int
	workerID  string
	finalOnly bool
	t         *testing.T
}

func (m *employeeHTTPModel) Name() string { return "controlled-employee" }
func (m *employeeHTTPModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				if strings.Contains(p.Text, "BOT_WORK_REVIEW_V1") {
					m.calls-- // Review is not an Employee conversation turn.
					var in botWorkContentInput
					if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &in); err != nil {
						m.t.Fatal(err)
					}
					response := botWorkContentReview{BotWorkReview: store.BotWorkReview{CandidateDigest: in.Candidate.Digest}}
					for _, c := range in.Criteria {
						response.Criteria = append(response.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Controlled fixture checks the Employee's own dispatch acknowledgment."})
					}
					for _, item := range in.Items {
						response.ContentAssessments = append(response.ContentAssessments, botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentNonfactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Employee acknowledgment, not an external factual certification."})
					}
					raw, _ := json.Marshal(response)
					yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true}, nil)
					return
				}
			}
		}
		if m.finalOnly {
			if req.Config == nil || len(req.Config.Tools) != 0 {
				m.t.Error("resumed Employee final retained executable tools")
			}
		}
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "employee_chat" {
					raw, _ := json.Marshal(p.FunctionResponse.Response)
					m.t.Logf("employee_chat response: %s", raw)
					var result bot.EmployeeChatResult
					_ = json.Unmarshal(raw, &result)
					if result.WorkerID != "" {
						m.workerID = result.WorkerID
					}
				}
			}
		}
		var content *genai.Content
		switch m.calls {
		case 1:
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "employee_chat", Args: map[string]any{"message": "Read the assigned source"}}}}}
		case 2:
			if m.workerID == "" {
				m.t.Error("Employee did not receive worker handle")
			}
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "employee_chat", Args: map[string]any{"worker_id": m.workerID, "message": "Explain that same source without rereading"}}}}}
		default:
			content = genai.NewContentFromText("Employee observed the violet signal.", "model")
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}

// employeeHTTPReadClassifier models an exact-path constraint established by
// owner review. Before review there is no autonomous filesystem permission.
type employeeHTTPReadClassifier struct {
	path     string
	reviewed *atomic.Bool
}

func (c employeeHTTPReadClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if name == "prepare_private_plan" {
		return execution.ActionDescriptor{Tool: name, Effect: execution.EffectRead, Arguments: args, ConstraintsVerified: true}, nil
	}
	var input struct {
		Path string `json:"path"`
	}
	if name != "read_file" || json.Unmarshal(args, &input) != nil || input.Path != c.path {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	info, err := os.Lstat(c.path)
	if err != nil || !info.Mode().IsRegular() {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return execution.ActionDescriptor{Tool: name, Effect: execution.EffectRead, Arguments: args, ConstraintsVerified: c.reviewed.Load()}, nil
}

// Loopback production admission/history handlers, scoped SQL persistence and the
// actual ChatRunner. Scripted models establish orchestration, not provider quality.
type employeeHTTPGuard struct {
	reviewed *atomic.Bool
	calls    *atomic.Int64
}

func (g employeeHTTPGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return execution.PolicyDecision{}, err
	}
	g.calls.Add(1)
	digest, err := a.Digest()
	kind := execution.PolicyAllow
	if a.Tool != "prepare_private_plan" && !g.reviewed.Load() {
		kind = execution.PolicyRequestApproval
	}
	return execution.PolicyDecision{Kind: kind, ActionDigest: digest}, err
}

func TestEmployeePrivateChatHTTPAcceptance(t *testing.T) {
	for _, test := range []struct {
		scope    store.BotScope
		approval bool
		plan     bool
	}{{store.BotScopePersonal, false, false}, {store.BotScopeTeam, false, false}, {store.BotScopePersonal, true, false}, {store.BotScopeTeam, true, false}, {store.BotScopePersonal, true, true}, {store.BotScopeTeam, true, true}} {
		scope := test.scope
		t.Run(fmt.Sprintf("%s/approval=%v/plan=%v", scope, test.approval, test.plan), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			org := &store.Organization{ID: uuid.NewString(), Slug: "private-chat", Name: "Private", Status: "active"}
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
			principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
			ctx, err = execution.WithPrincipal(ctx, principal)
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
			repo := ods.ForTeam(team.Slug).Bots()
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner).Bots()
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			defer registry.Close()
			ctx = store.WithServices(ctx, &store.Services{Bots: repo, PersonalBots: ods.ForUser(owner).Bots(), Sessions: ods.ForTeam(team.Slug).Sessions(), PersonalSessions: ods.ForUser(owner).Sessions(), BotInteractiveReady: true, BotTurnRegistry: registry})
			router := mux.NewRouter()
			registerBotRoutes(router)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCtx, cancel := context.WithCancel(ctx)
				stop := context.AfterFunc(r.Context(), cancel)
				defer stop()
				defer cancel()
				router.ServeHTTP(w, r.WithContext(requestCtx))
			}))
			defer server.Close()
			request := func(method, path, body string, status int) []byte {
				t.Helper()
				q, _ := http.NewRequest(method, server.URL+path+"?scope="+string(scope), strings.NewReader(body))
				q.Header.Set("Content-Type", "application/json")
				r, e := server.Client().Do(q)
				if e != nil {
					t.Fatal(e)
				}
				defer r.Body.Close()
				var raw []byte
				if strings.Contains(path, "/events") && r.StatusCode == http.StatusOK {
					scanner := bufio.NewScanner(r.Body)
					for scanner.Scan() {
						line := scanner.Text()
						raw = append(raw, []byte(line+"\n")...)
						if line == "event: replay_complete" {
							break
						}
					}
					if e := scanner.Err(); e != nil {
						t.Fatal(e)
					}
				} else {
					raw, _ = io.ReadAll(r.Body)
				}
				if r.StatusCode != status {
					t.Fatalf("%s: %d: %s", path, r.StatusCode, raw)
				}
				return raw
			}
			var b store.Bot
			if err = json.Unmarshal(request("POST", "/api/studio/bots", `{"name":"Private Employee","idempotency_key":"create"}`, 201), &b); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/" + b.ID.String() + "/messages"
			body := fmt.Sprintf(`{"version":%d,"instructions":"Inspect the violet signal","idempotency_key":"private"}`, b.Version)
			var admitted, replay store.BotMessageReceipt
			if err = json.Unmarshal(request("POST", path, body, 202), &admitted); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(request("POST", path, body, 202), &replay)
			if replay.Run.ID != admitted.Run.ID {
				t.Fatal("duplicate admission")
			}
			input, err := repo.(store.BotRuntimeStore).LoadRunInput(ctx, b.ID, admitted.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			id := execution.BotRunIdentity{BotID: b.ID, RunID: input.Run.ID, Version: b.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug}
			workerCtx := execution.WithBotRunIdentity(ctx, id)
			lease, err := repo.ClaimRun(workerCtx, store.BotClaimRequest{BotID: b.ID, RunID: input.Run.ID, WorkerID: "private-http", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			auth, err := registry.Authorizer(id, *input.Run.CallerCeiling, func(c context.Context, p execution.Principal) (execution.Principal, error) { return p, c.Err() })
			if err != nil {
				t.Fatal(err)
			}
			if !input.Run.IsInteractiveChat() {
				t.Fatal("HTTP admission substituted legacy execution")
			}
			input.Run.Lease, input.Run.Status = lease, store.RunRunning
			req := bot.RunRequest{Bot: input.Bot, Version: input.Version, Run: input.Run, Principal: principal, Authorization: auth, Budget: &botChatTestBudget{}, Instructions: input.Instructions, Work: input.Work}
			workerCtx = agent.WithBotPolicyRuntime(workerCtx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
			file := filepath.Join(dir, "source.txt")
			var reviewed atomic.Bool
			var guardCalls atomic.Int64
			reviewed.Store(!test.approval)
			workerCtx = bot.WithApprovalStore(workerCtx, repo)
			workerCtx = agent.WithBotPolicyRuntime(workerCtx, agent.BotPolicyRuntime{Classifier: employeeHTTPReadClassifier{path: file, reviewed: &reviewed}, Guard: employeeHTTPGuard{reviewed: &reviewed, calls: &guardCalls}})
			ss := ods.ForTeam(team.Slug).Sessions()
			if scope == store.BotScopePersonal {
				ss = ods.ForUser(owner).Sessions()
			}
			// Close every SQL handle and rebuild the scoped repositories, while keeping
			// the original caller ceiling and process-local authorizer (not renewing it).
			reopen := func() {
				t.Helper()
				if e := db.Close(); e != nil {
					t.Fatal(e)
				}
				_, db, err = entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				ods, err = db.ForOrg(org.Slug)
				if err != nil {
					t.Fatal(err)
				}
				repo, ss = ods.ForTeam(team.Slug).Bots(), ods.ForTeam(team.Slug).Sessions()
				if scope == store.BotScopePersonal {
					repo, ss = ods.ForUser(owner).Bots(), ods.ForUser(owner).Sessions()
				}
				ctx = store.WithServices(ctx, &store.Services{Bots: repo, PersonalBots: ods.ForUser(owner).Bots(), Sessions: ods.ForTeam(team.Slug).Sessions(), PersonalSessions: ods.ForUser(owner).Sessions(), BotInteractiveReady: true, BotTurnRegistry: registry})
				workerCtx = execution.WithBotRunIdentity(ctx, id)
				workerCtx = bot.WithApprovalStore(workerCtx, repo)
				workerCtx = agent.WithBotPolicyRuntime(workerCtx, agent.BotPolicyRuntime{Classifier: employeeHTTPReadClassifier{path: file, reviewed: &reviewed}, Guard: employeeHTTPGuard{reviewed: &reviewed, calls: &guardCalls}})
			}
			if err = os.WriteFile(file, []byte("PRIVATE_SOURCE violet"), 0600); err != nil {
				t.Fatal(err)
			}
			var executions atomic.Int64
			read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read the assigned source"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
				if guardCalls.Load() == 0 {
					t.Fatal("private effect bypassed live guard")
				}
				if args["path"] != file {
					return nil, store.ErrBotForbidden
				}
				info, e := os.Lstat(file)
				if e != nil || !info.Mode().IsRegular() {
					return nil, store.ErrBotForbidden
				}
				raw, e := os.ReadFile(file)
				if e != nil {
					return nil, e
				}
				executions.Add(1)
				return map[string]any{"content": string(raw)}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			privateLLM := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": file}), TextTurn("PRIVATE_WORKER"), TextTurn("PRIVATE_FOLLOWUP"))
			employee := &employeeHTTPModel{t: t}
			var resumedWorkerID string
			factory := func(c context.Context, q bot.RunRequest) (*BotChatResources, error) {
				if q.Policy != nil || execution.RunPolicyFromContext(c) != nil {
					t.Fatal("interactive factory minted autonomy policy")
				}
				runtime, bindErr := BindBotChatPolicyRuntime(c, agent.BotPolicyRuntime{Classifier: employeeHTTPReadClassifier{path: file, reviewed: &reviewed}, Guard: employeeHTTPGuard{reviewed: &reviewed, calls: &guardCalls}})
				if bindErr != nil {
					return nil, bindErr
				}
				c = agent.WithBotPolicyRuntime(c, runtime)
				orchestrate, e := functiontool.New(functiontool.Config{Name: "employee_chat", Description: "Direct private Chat"}, func(tc tool.Context, a bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
					return bot.EmployeeChatServiceFromContext(c).Send(tc, a)
				})
				if e != nil {
					return nil, e
				}
				sealed, e := agent.NewBotEmployeeChatTool(orchestrate, func(tc tool.Context) (tool.Context, error) {
					if tc.SessionID() != q.Run.SessionID.String() || q.Run.ControlOnly() || q.Run.TitleConfirmation != nil && q.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion {
						return nil, store.ErrBotForbidden
					}
					if e := botWorkBoundary(tc, q); e != nil {
						return nil, e
					}
					return tc, nil
				})
				if e != nil {
					return nil, e
				}
				r := &BotChatResources{PolicyRuntime: &runtime, Authorization: auth, Agent: agent.NewChatAgent(employee, []tool.Tool{sealed}, nil, ss, &agent.SystemPromptBuilder{}, false, false), SessionService: ss, AppName: studioChatAppName, Transcript: repo.(store.BotTranscriptStore), Work: repo.(store.BotWorkStore), Cleanup: func() {}}
				r.WorkerFactory = func(c context.Context, w BotEmployeeWorkerRequest) (*BotChatResources, error) {
					if q.Budget != req.Budget || q.Authorization != auth {
						t.Fatal("worker reset original runtime budget/authorization")
					}
					resumedWorkerID = w.ConversationID
					workerTools := []tool.Tool{read}
					workerModel := privateLLM
					if test.plan {
						bridge := BotPreparedPlanSessionFromContext(c)
						plans := repo.(store.BotPreparedPlanStore)
						var invocation tool.Context
						var record *store.BotPreparedPlan
						engine := agent.NewPlanRuntime(nil, agent.PlanRuntimeOptions{
							PrepareOperation: func(op agent.PlanPreparedOperation) error {
								if bridge.Resume != nil {
									return nil
								}
								raw, _ := json.Marshal(map[string]any{"path": file})
								action, e := (employeeHTTPReadClassifier{path: file, reviewed: &reviewed}).DescribeAction(c, "read_file", raw)
								if e != nil {
									return e
								}
								digest, _ := action.Digest()
								encoded, _ := json.Marshal(op)
								record, e = plans.PrepareBotPlan(c, q.Run.Lease, store.BotPreparedPlan{BotID: q.Bot.ID, RunID: q.Run.ID, SessionID: q.Run.SessionID, Version: q.Run.Version, OperationID: uuid.MustParse(op.ID), LeafKind: store.BotPlanLeafMirror, Action: action, ActionDigest: digest, EngineRecord: encoded})
								if e != nil {
									return e
								}
								record, e = plans.ChangeBotPlan(c, q.Run.Lease, store.BotPreparedPlanChange{OperationID: record.OperationID, ExpectedRevision: record.Revision, Stage: store.BotPlanAwaitingApproval, ApprovalExpiresAt: time.Now().Add(time.Hour)})
								if e != nil {
									return e
								}
								if e = bridge.CaptureApproval(record); e != nil {
									return e
								}
								return store.ErrBotConflict
							},
							CommitSnapshot: func(snapshot agent.PlanSnapshot) error {
								if invocation == nil || bridge.Resume == nil {
									return store.ErrBotForbidden
								}
								if e := q.Authorization.AuthorizeTool(invocation, record.Action.Tool); e != nil {
									return e
								}
								decision, e := (employeeHTTPGuard{reviewed: &reviewed, calls: &guardCalls}).Authorize(invocation, record.Action)
								if e != nil || decision.Kind != execution.PolicyAllow || decision.ActionDigest != record.ActionDigest {
									return store.ErrBotForbidden
								}
								dispatch := uuid.New()
								record, e = plans.ChangeBotPlan(invocation, q.Run.Lease, store.BotPreparedPlanChange{OperationID: record.OperationID, ExpectedRevision: record.Revision, Stage: store.BotPlanDispatchStarted, ApprovalID: record.ApprovalID, DispatchID: dispatch})
								if e != nil {
									return e
								}
								executions.Add(1)
								encoded, _ := json.Marshal(snapshot)
								record, e = plans.ChangeBotPlan(invocation, q.Run.Lease, store.BotPreparedPlanChange{OperationID: record.OperationID, ExpectedRevision: record.Revision, Stage: store.BotPlanSettled, DispatchID: dispatch, Snapshot: encoded, TrustedResult: json.RawMessage(`{"effect":"private-plan"}`)})
								if e != nil {
									return e
								}
								record, e = plans.ChangeBotPlan(invocation, q.Run.Lease, store.BotPreparedPlanChange{OperationID: record.OperationID, ExpectedRevision: record.Revision, Stage: store.BotPlanPublished})
								return e
							},
						})
						prepare, e := functiontool.New(functiontool.Config{Name: "prepare_private_plan", Description: "Prepare private plan"}, func(tc tool.Context, args map[string]any) (map[string]any, error) {
							return nil, engine.Announce("PRIVATE_PLAN", agent.PlanDocumentInfo{Context: "Exact private plan", WhatNotToDo: "No unrelated effects", Verification: "Read saved plan"}, []agent.PlanStepInfo{{Name: "private", Description: "private work", Details: "Persist exact snapshot", Summary: "Private plan", Outcome: "Plan saved", Verify: "test -s PLAN.md", VerifyKind: "unit", Files: []agent.PlanFileChange{{Path: "PLAN.md", Kind: "modify"}}}})
						})
						if e != nil {
							return nil, e
						}
						prepared := &employeePreparedTool{employeePreparedRunnable: read.(employeePreparedRunnable), invoke: func(tc tool.Context, args any) (map[string]any, error) {
							var e error
							record, e = bridge.ClaimResume("read_file", args)
							if e != nil {
								return nil, e
							}
							invocation = tc
							var op agent.PlanPreparedOperation
							if e = json.Unmarshal(record.EngineRecord, &op); e != nil {
								return nil, e
							}
							result, e := engine.ResumePreparedOperation(op)
							return map[string]any{"result": result}, e
						}}
						workerTools = []tool.Tool{prepare, prepared}
						workerModel = NewMockLLM(ToolCallTurn("prepare_private_plan", map[string]any{}))
					}
					chat := agent.NewChatAgent(workerModel, workerTools, nil, w.Sessions, &agent.SystemPromptBuilder{}, false, false)
					chat.ToolIndex = agent.NewLexicalToolIndex()
					if e := chat.ToolIndex.PrimeTools(c, workerTools, nil); e != nil {
						return nil, e
					}
					return &BotChatResources{Agent: chat, SessionService: w.Sessions, AppName: studioChatAppName, Cleanup: func() {}}, nil
				}
				return r, nil
			}
			adapter, err := NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(workerCtx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if test.approval {
				if result.Status != store.RunWaitingApproval || executions.Load() != 0 {
					t.Fatalf("approval did not pause: %+v %v", result, err)
				}
				if err = repo.CommitRun(workerCtx, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report, Usage: result.Usage, Checkpoint: result.Checkpoint}); err != nil {
					t.Fatal(err)
				}
				checkpoint, e := bot.DecodeApprovalCheckpoint(result.Checkpoint, req.Run)
				if e != nil {
					t.Fatal(e)
				}
				if checkpoint.BotID != b.ID || checkpoint.RunID != req.Run.ID || checkpoint.SessionID != req.Run.SessionID {
					t.Fatal("approval lost original SQL-scoped execution binding")
				}
				approvals, e := repo.ListApprovals(ctx, b.ID, store.BotPage{})
				if e != nil || len(approvals) != 1 || approvals[0].ID != checkpoint.ApprovalID || approvals[0].Action.Effect != execution.EffectRead {
					t.Fatalf("exact read approval binding: %+v %v", approvals, e)
				}
				originalRun, e := repo.GetRun(ctx, b.ID, req.Run.ID)
				if e != nil {
					t.Fatal(e)
				}
				originalCeiling, _ := json.Marshal(originalRun.CallerCeiling)
				reopen()
				restoredRun, e := repo.GetRun(ctx, b.ID, req.Run.ID)
				restoredCeiling, _ := json.Marshal(restoredRun.CallerCeiling)
				if e != nil || string(originalCeiling) != string(restoredCeiling) || restoredRun.Usage != originalRun.Usage || string(restoredRun.Checkpoint) != string(originalRun.Checkpoint) {
					t.Fatalf("restart renewed caller expiry/quota or changed checkpoint: %+v %v", restoredRun, e)
				}
				base := "/api/studio/bots/" + b.ID.String()
				request("POST", base+"/runs/"+req.Run.ID.String()+"/resume", fmt.Sprintf(`{"approval_id":%q}`, uuid.New()), 409)
				request("POST", base+"/approvals/"+checkpoint.ApprovalID.String()+"/decision", fmt.Sprintf(`{"status":"approved","action_digest":%q}`, strings.Repeat("0", 64)), 409)
				request("POST", base+"/approvals/"+checkpoint.ApprovalID.String()+"/decision", fmt.Sprintf(`{"status":"approved","action_digest":%q}`, checkpoint.ActionDigest), 200)
				reviewed.Store(true) // Exact owner-reviewed path, not a wildcard permission.
				request("POST", base+"/approvals/"+checkpoint.ApprovalID.String()+"/approve-and-continue", fmt.Sprintf(`{"action_digest":%q}`, strings.Repeat("0", 64)), 409)
				request("POST", base+"/approvals/"+checkpoint.ApprovalID.String()+"/approve-and-continue", fmt.Sprintf(`{"action_digest":%q}`, checkpoint.ActionDigest), 202)
				request("POST", base+"/approvals/"+checkpoint.ApprovalID.String()+"/approve-and-continue", fmt.Sprintf(`{"action_digest":%q}`, checkpoint.ActionDigest), 202)
				// Lose SQL handles again after approval, before any private action dispatch.
				reopen()
				approvedAfterRestart, loadErr := repo.ListApprovals(ctx, b.ID, store.BotPage{})
				if loadErr != nil || len(approvedAfterRestart) != 1 || approvedAfterRestart[0].Status != store.ApprovalApproved || approvedAfterRestart[0].ID != approvals[0].ID || !approvedAfterRestart[0].ExpiresAt.Equal(approvals[0].ExpiresAt) || approvedAfterRestart[0].ActionDigest != checkpoint.ActionDigest {
					t.Fatalf("restart renewed/rebound unconsumed exact approval: %+v %v", approvedAfterRestart, loadErr)
				}
				resumedInput, loadErr := repo.(store.BotRuntimeStore).LoadRunInput(ctx, b.ID, req.Run.ID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				resumedCeiling, _ := json.Marshal(resumedInput.Run.CallerCeiling)
				if string(resumedCeiling) != string(originalCeiling) || resumedInput.Run.Usage != originalRun.Usage {
					t.Fatal("HTTP continuation reset original expiry/cumulative quota")
				}
				req.Run = resumedInput.Run
				lease, err = repo.ClaimRun(workerCtx, store.BotClaimRequest{BotID: b.ID, RunID: req.Run.ID, WorkerID: "private-http-resume", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				req.Run.Lease, req.Run.Checkpoint, req.Run.Status = lease, result.Checkpoint, store.RunRunning
				savedWorkerID := resumedWorkerID
				if savedWorkerID == "" {
					t.Fatal("paused worker has no server-owned handle")
				}
				resumedWorkerID = ""
				employee = &employeeHTTPModel{t: t, calls: 2, finalOnly: true} // fresh tools-disabled final model
				privateLLM = NewMockLLM(TextTurn("PRIVATE_WORKER"))
				result, err = adapter.Resume(workerCtx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
				if err != nil {
					for cause := err; cause != nil; cause = errors.Unwrap(cause) {
						t.Logf("resume cause: %T %v", cause, cause)
					}
					t.Fatalf("private approval resume: %v result=%+v failure=%+v", err, result, result.Failure)
				}
				if test.plan {
					prepared, loadErr := repo.(store.BotPreparedPlanStore).LoadBotPlanByApproval(workerCtx, lease, checkpoint.ApprovalID)
					if loadErr != nil || prepared.Stage != store.BotPlanPublished || len(prepared.Snapshot) == 0 || prepared.DispatchID == uuid.Nil {
						t.Fatalf("plan not durably published: %+v %v", prepared, loadErr)
					}
					var snapshot agent.PlanSnapshot
					if json.Unmarshal(prepared.Snapshot, &snapshot) != nil || snapshot.Goal != "PRIVATE_PLAN" {
						t.Fatal("saved exact private plan changed")
					}
				}
				if resumedWorkerID != savedWorkerID {
					t.Fatalf("resume selected foreign worker %q, want %q", resumedWorkerID, savedWorkerID)
				}
				if !test.plan && len(privateLLM.Calls) != 1 {
					t.Fatalf("saved action redispatched model instead of exact action: %d", len(privateLLM.Calls))
				}
				callsBeforeReplay := employee.calls
				workerCallsBeforeReplay := len(privateLLM.Calls)
				reopen() // consumed grants and resuming/settled markers survive process resources
				if _, replayErr := adapter.Resume(workerCtx, req, func(context.Context, bot.RuntimeEvent) error { return nil }); replayErr == nil {
					t.Fatal("consumed worker approval replayed")
				}
				if employee.calls != callsBeforeReplay || len(privateLLM.Calls) != workerCallsBeforeReplay {
					t.Fatal("replay invoked a model")
				}
				approvals, e = repo.ListApprovals(ctx, b.ID, store.BotPage{})
				if e != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalConsumed || executions.Load() != 1 {
					t.Fatalf("approval not consumed exactly once: %+v executions=%d err=%v", approvals, executions.Load(), e)
				}
			}
			if executions.Load() != 1 || employee.calls != 3 || (!test.approval && len(privateLLM.Calls) != 3) {
				t.Fatalf("execution=%d employee=%d worker=%d", executions.Load(), employee.calls, len(privateLLM.Calls))
			}
			if !test.approval {
				history, _ := json.Marshal(privateLLM.Calls[2])
				if !strings.Contains(string(history), "PRIVATE_SOURCE") || !strings.Contains(string(history), "PRIVATE_WORKER") {
					t.Fatal("follow-up lost private history")
				}
			}
			if !test.approval {
				reopen()
				resources, e := factory(workerCtx, req)
				if e != nil {
					t.Fatal(e)
				}
				restored := &botEmployeeChatService{req: req, resources: resources, workers: make(map[string]*botEmployeeWorker)}
				defer restored.close()
				conversation := botConversationSessions{Service: ss, transcript: repo.(store.BotTranscriptStore), lease: lease, id: req.Run.SessionID, userEventID: req.Run.UserEventID}
				saved, e := conversation.Get(workerCtx, &session.GetRequest{AppName: studioChatAppName, UserID: owner, SessionID: req.Run.SessionID.String()})
				if e != nil {
					t.Fatal(e)
				}
				if e = restored.restore(saved.Session.Events()); e != nil {
					t.Fatal(e)
				}
				w := restored.workers[employee.workerID]
				if len(restored.workers) != 1 || w == nil || w.pending != "" {
					t.Fatal("SQL restart lost settled exact worker")
				}
				privateHistory, _ := json.Marshal(w.events)
				for _, text := range []string{"PRIVATE_SOURCE", "PRIVATE_WORKER", "PRIVATE_FOLLOWUP"} {
					if !strings.Contains(string(privateHistory), text) {
						t.Fatalf("SQL restart lost private history %q", text)
					}
				}
				restoredCalls := NewMockLLM(TextTurn("PRIVATE_RESTORED_FOLLOWUP"))
				privateLLM = restoredCalls
				followupCtx := execution.WithBotTurnAuthorizer(workerCtx, auth)
				followupCtx = context.WithValue(followupCtx, botChatBudgetKey{}, req.Budget)
				followup, e := restored.Send(followupCtx, bot.EmployeeChatRequest{WorkerID: w.id, Message: "Explain the restored same source without rereading"})
				if e != nil || followup.Status != bot.EmployeeChatCompleted || followup.WorkerID != w.id || len(restoredCalls.Calls) != 1 || executions.Load() != 1 {
					t.Fatalf("restored worker follow-up: %+v calls=%d err=%v", followup, len(restoredCalls.Calls), e)
				}
				modelHistory, _ := json.Marshal(restoredCalls.Calls[0])
				for _, text := range []string{"PRIVATE_SOURCE", "PRIVATE_WORKER", "PRIVATE_FOLLOWUP"} {
					if !strings.Contains(string(modelHistory), text) {
						t.Fatalf("restored model lost private history %q", text)
					}
				}
				calls := len(privateLLM.Calls)
				if _, e = restored.Send(workerCtx, bot.EmployeeChatRequest{WorkerID: uuid.NewString(), Message: "foreign worker"}); !errors.Is(e, store.ErrBotForbidden) {
					t.Fatalf("foreign worker accepted: %v", e)
				}
				if len(privateLLM.Calls) != calls || executions.Load() != 1 {
					t.Fatal("foreign worker invoked model/tool")
				}
				if e = restored.record(workerCtx, botEmployeeWorkerTrace{WorkerID: w.id, DispatchID: "lost-start", Phase: botEmployeeWorkerStarted}); e != nil {
					t.Fatal(e)
				}
				reopen()
				resources, e = factory(workerCtx, req)
				if e != nil {
					t.Fatal(e)
				}
				unsettled := &botEmployeeChatService{req: req, resources: resources, workers: make(map[string]*botEmployeeWorker)}
				defer unsettled.close()
				conversation.Service, conversation.transcript = ss, repo.(store.BotTranscriptStore)
				saved, e = conversation.Get(workerCtx, &session.GetRequest{AppName: studioChatAppName, UserID: owner, SessionID: req.Run.SessionID.String()})
				if e != nil {
					t.Fatal(e)
				}
				if e = unsettled.restore(saved.Session.Events()); e != nil {
					t.Fatal(e)
				}
				partial, e := unsettled.Send(workerCtx, bot.EmployeeChatRequest{WorkerID: w.id, Message: "retry lost dispatch"})
				if e != nil || partial.Status != bot.EmployeeChatPartial || len(privateLLM.Calls) != calls || executions.Load() != 1 {
					t.Fatalf("unsettled SQL restart replayed: %+v %v", partial, e)
				}
			}
			if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || result.Report.Summary != "Employee observed the violet signal." {
				t.Fatalf("final was not Employee-authored: %+v", result.Report)
			}
			if err = repo.CommitRun(workerCtx, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report, Usage: result.Usage}); err != nil {
				t.Logf("employee=%+v validation=%v", result.Report.EmployeeMessage, result.Report.ValidateEmployeeMessage(result.Status, nil))
				t.Fatalf("commit: %v result=%+v report=%+v", err, result, result.Report)
			}
			if _, err = repo.(store.BotPublicationStore).PublishReport(workerCtx, store.BotPublicationRequest{BotID: b.ID, RunID: req.Run.ID}); err != nil {
				t.Fatalf("publish Employee reply: %v", err)
			}
			raw := request("GET", "/api/studio/bots/"+b.ID.String()+"/sessions/"+b.HomeSessionID.String()+"/events", "", 200)
			if strings.Contains(string(raw), "PRIVATE_SOURCE") || strings.Contains(string(raw), "PRIVATE_WORKER") || strings.Contains(string(raw), "PRIVATE_FOLLOWUP") {
				t.Fatalf("private content leaked: %s", raw)
			}
			if strings.Count(string(raw), "Employee observed the violet signal") != 1 {
				t.Fatalf("expected exactly one Employee reply: %s", raw)
			}
			listed, err := ss.List(ctx, &session.ListRequest{AppName: studioChatAppName, UserID: owner})
			if err != nil || len(listed.Sessions) != 0 {
				t.Fatalf("private sessions listed: %+v %v", listed, err)
			}
		})
	}
}

// Controlled runner, production HTTP handlers and scoped repositories. No provider or sandbox.
func TestBotTitleConfirmationHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "confirmation", Name: "Confirmation", Status: "active"}
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
			owner := uuid.NewString()
			if err := db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
			ctx, err = execution.WithPrincipal(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
			personal := ods.ForUser(owner)
			repo := ods.ForTeam(team.Slug).Bots()
			if scope == store.BotScopePersonal {
				repo = personal.Bots()
			}
			registry := execution.NewBotTurnAuthorizationRegistry(20)
			defer registry.Close()
			ctx = store.WithBotMessageAdmission(ctx, registry)
			ctx = store.WithServices(ctx, &store.Services{Bots: repo, PersonalBots: personal.Bots(), BotTurnRegistry: registry, BotInteractiveReady: true})
			router := mux.NewRouter()
			registerBotRoutes(router)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(ctx)) }))
			defer server.Close()
			request := func(method, path, body string, status int) []byte {
				t.Helper()
				r, _ := http.NewRequest(method, server.URL+path+"?scope="+string(scope), strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				resp, e := server.Client().Do(r)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != status {
					t.Fatalf("%s: %d want %d: %s", path, resp.StatusCode, status, raw)
				}
				return raw
			}
			for _, answer := range []string{"brief", "Custom private assignment"} {
				var receipt store.BotTitleStartReceipt
				raw := request("POST", "/api/studio/bots", fmt.Sprintf(`{"name":"AI News","start_from_title":true,"idempotency_key":%q}`, answer), 201)
				if err := json.Unmarshal(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				runs, err := repo.ListRuns(ctx, receipt.ID, store.BotPage{})
				if err != nil || len(runs) != 1 {
					t.Fatalf("runs: %v %v", runs, err)
				}
				run := runs[0]
				input, err := repo.(store.BotRuntimeStore).LoadRunInput(ctx, receipt.ID, run.ID)
				if err != nil || input.Run.TitleConfirmation == nil || input.Run.TitleConfirmation.State != store.BotTitleConfirmationNeedsQuestion || input.Task != nil {
					t.Fatalf("admission: %+v %v", input, err)
				}
				worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "controlled", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Premature"}}); err == nil {
					t.Fatal("unconfirmed success accepted")
				}
				payload := json.RawMessage(`{"questionId":"title-q","kind":"select","prompt":"Which AI news assignment?","allow_custom_answer":true,"options":[{"id":"brief","label":"Prepare an AI news briefing"},{"id":"compare","label":"Compare AI research announcements"},{"id":"sources","label":"Find original AI news sources"}]}`)
				action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
				digest, _ := action.Digest()
				a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "title-q", Payload: payload}, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				// Approval alone is not an answer identity: persist the question's
				// real call/response pair under the worker lease, as the runner does.
				var questionArgs map[string]any
				if err := json.Unmarshal(payload, &questionArgs); err != nil {
					t.Fatal(err)
				}
				questionResponse := map[string]any{"status": "ok"}
				for key, value := range questionArgs {
					questionResponse[key] = value
				}
				for _, event := range []*session.Event{
					{ID: "title-question-call", Author: "model", InvocationID: "title-question", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "title-question-call", Name: "ask_user", Args: questionArgs}}}}}},
					{ID: "title-question-response", Author: "user", InvocationID: "title-question", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "title-question-call", Name: "ask_user", Response: questionResponse}}}}}},
				} {
					if err := repo.(store.BotTranscriptStore).AppendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
						t.Fatal(err)
					}
				}
				checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
					t.Fatal(err)
				}
				persisted, err := repo.GetRun(ctx, run.BotID, run.ID)
				if err != nil || !persisted.RuntimeAccountingRecorded || persisted.Usage.RuntimeNanos != 0 {
					t.Fatalf("zero accounting: %+v %v", persisted, err)
				}
				path := "/api/studio/bots/" + run.BotID.String() + "/approvals/" + a.ID.String() + "/approve-and-continue"
				body := fmt.Sprintf(`{"action_digest":%q,"response":{"question_id":"title-q","answer":%q}}`, digest, answer)
				request("POST", path, fmt.Sprintf(`{"action_digest":%q,"response":{"question_id":"foreign","answer":%q}}`, digest, answer), 400)
				request("POST", path, fmt.Sprintf(`{"action_digest":"stale","response":{"question_id":"title-q","answer":%q}}`, answer), 409)
				stillWaiting, err := repo.GetRun(ctx, run.BotID, run.ID)
				if err != nil || stillWaiting.Status != store.RunWaitingApproval || stillWaiting.TitleConfirmation.State != store.BotTitleConfirmationWaiting {
					t.Fatal("rejected answer unlocked execution", err)
				}
				if answer != "brief" {
					// Simulate losing process-local authority while the owner waits.
					registry.Cancel(execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
					continuePath := "/api/studio/bots/" + run.BotID.String() + "/runs/" + run.ID.String() + "/continue-interactive"
					continueBody := fmt.Sprintf(`{"expected_revision":%d,"idempotency_key":"confirm-custom","response":{"question_id":"title-q","answer":%q}}`, receipt.Revision, answer)
					request("POST", continuePath, continueBody, 202)
					request("POST", continuePath, continueBody, 202)
				} else {
					request("POST", path, body, 202)
				}
				input, err = repo.(store.BotRuntimeStore).LoadRunInput(ctx, run.BotID, run.ID)
				want := answer
				if answer == "brief" {
					want = "Prepare an AI news briefing"
				}
				if err != nil || input.Instructions != want || input.Run.TitleConfirmation.OriginalTitle != "AI News" {
					t.Fatalf("confirmed: %+v %v", input, err)
				}
				request("POST", path, body, 202)
				if _, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "resume", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
					t.Fatal(err)
				}
				request("POST", path, body, 202)
				request("POST", path, fmt.Sprintf(`{"action_digest":%q,"response":{"question_id":"title-q","answer":"different"}}`, digest), 409)
			}
		})
	}
}

// A real loopback HTTP invocation of the production producer and event sink.
// The model is controlled, not paid: this proves transport ordering, not that a
// provider follows the prompt or that title admission supplies production tools.
// The reviewed final publication is intentionally outside this producer test.
func TestBotEmployeeTitleAcknowledgementHTTPToolBoundary(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		for _, toolBoundary := range []bool{false, true} {
			name := "historical"
			if guarded {
				name = "evidence-bound"
			}
			if toolBoundary {
				name += "/work-tool"
			} else {
				name += "/toolless"
			}
			t.Run(name, func(t *testing.T) {
				ctx, req, ss := botChatTestRequest(t)
				req.Bot.Name, req.Instructions, req.Interactive = "AI News", "AI News", true
				req.Run.Intent = store.BotIntentRegular
				if guarded {
					req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: req.Instructions})
					req.Work.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
				}
				ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
				ctx = execution.WithRunPolicy(ctx, req.Policy)
				ctx = context.WithValue(ctx, botChatBudgetKey{}, req.Budget)
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
				const acknowledgement = "I'll put together an AI news briefing and check the original reporting."
				const candidate = "PRIVATE_FINAL_CANDIDATE"
				var cleaned, executed atomic.Int64
				read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Inspect original controlled reporting"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
					if err := agent.AuthorizeBotTool(ctx, "read_file", args); err != nil {
						return nil, err
					}
					executed.Add(1)
					return map[string]any{"content": "Controlled original reporting: a new model was announced."}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				turns := []*MockTurn{TextTurn(candidate)}
				if toolBoundary {
					turns = []*MockTurn{{Parts: []*genai.Part{{Text: acknowledgement}, {FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "original-reporting"}}}}, TurnComplete: true}, TextTurn(candidate)}
				}
				llm := NewMockLLM(turns...)
				resources, err := botChatTestFactory(t, ss, llm, []tool.Tool{read}, &cleaned)(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				defer resources.Cleanup()
				var events []bot.RuntimeEvent
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
					var sequence int64
					var completion ChatCompletion
					result, runErr := runBotChatAttempt(ctx, req, resources, func(_ context.Context, event bot.RuntimeEvent) error {
						events = append(events, event)
						return nil
					}, nil, nil, nil, false, &sequence, &completion)
					if runErr != nil {
						t.Errorf("producer: %v", runErr)
						out.WriteHeader(http.StatusInternalServerError)
					}
					if !completion.Final || completion.FinalText != candidate || result.Report.Summary != candidate {
						t.Errorf("producer lost its final candidate: %+v", completion)
					}
					_ = json.NewEncoder(out).Encode(result)
				}))
				defer server.Close()
				response, err := server.Client().Post(server.URL, "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("HTTP %d", response.StatusCode)
				}
				ackIndex, toolIndex, resultIndex := -1, -1, -1
				var live strings.Builder
				for i, event := range events {
					if event.Sequence != int64(i+1) || event.RunID != req.Run.ID || event.SessionID != req.Run.SessionID || event.TurnID != events[0].TurnID {
						t.Fatalf("wrong event identity/order: %+v", event)
					}
					if event.Type == "text" {
						var text struct {
							Text string `json:"text"`
						}
						if err := json.Unmarshal(event.Payload, &text); err != nil {
							t.Fatal(err)
						}
						live.WriteString(text.Text)
						if text.Text == acknowledgement {
							ackIndex = i
						}
					}
					if event.Type == "tool_call" {
						toolIndex = i
					}
					if event.Type == "tool_result" {
						resultIndex = i
					}
					if guarded && event.Type == "done" {
						t.Fatal("unreviewed producer closed the public stream")
					}
				}
				if toolBoundary && (ackIndex < 0 || toolIndex <= ackIndex || resultIndex <= toolIndex || executed.Load() != 1) {
					t.Fatalf("acknowledgement did not precede useful work: ack=%d call=%d result=%d executed=%d events=%+v", ackIndex, toolIndex, resultIndex, executed.Load(), events)
				}
				if guarded && toolBoundary && live.String() != acknowledgement {
					t.Fatalf("guarded active text must be exactly the model acknowledgement, got %q", live.String())
				}
				if guarded && !toolBoundary && live.Len() != 0 {
					t.Fatalf("toolless guarded candidate escaped: %q", live.String())
				}
				if guarded && strings.Contains(live.String(), candidate) {
					t.Fatal("unreviewed final candidate leaked")
				}
				if !guarded && !strings.Contains(live.String(), candidate) {
					t.Fatal("historical producer publication changed")
				}
				if !toolBoundary && (ackIndex >= 0 || executed.Load() != 0) {
					t.Fatal("server fabricated acknowledgement or work")
				}
				if len(llm.Calls) != len(turns) {
					t.Fatal("acknowledgement added a model call")
				}
				prompt := llm.Calls[0].Config.SystemInstruction.Parts[0].Text
				for _, guidance := range []string{
					"Once the assignment is sufficiently specified, give a brief friendly AI-authored acknowledgement of the actual next work",
					"not a canned ready message or a claim that work is complete",
					"continue immediately into actual useful work under the current scoped authority",
					"Put the acknowledgement before the first real work tool call",
					"Do not invoke a dummy tool merely to release text",
				} {
					if !strings.Contains(prompt, guidance) {
						t.Fatalf("producer prompt lost semantic acknowledgement guidance: %q", guidance)
					}
				}
			})
		}
	}
}
