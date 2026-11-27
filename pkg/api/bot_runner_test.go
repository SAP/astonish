package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type botChatTestBudget struct{ checks atomic.Int64 }

func (b *botChatTestBudget) Check(ctx context.Context) error                 { b.checks.Add(1); return ctx.Err() }
func (b *botChatTestBudget) ReserveTool(ctx context.Context, _ string) error { return ctx.Err() }
func (b *botChatTestBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	return r, ctx.Err()
}
func (b *botChatTestBudget) SettleModel(ctx context.Context, _ uuid.UUID, _ execution.BudgetUsage) error {
	return ctx.Err()
}

func botChatTestRequest(t *testing.T) (context.Context, bot.RunRequest, session.Service) {
	t.Helper()
	owner := uuid.New()
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: owner.String(), OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	version := store.DefaultBotVersion()
	version.BotID, version.Charter = id.BotID, "Read the assigned information and report."
	req := bot.RunRequest{
		Bot:     store.Bot{ID: id.BotID, OwnerID: owner, Status: store.BotActive, Version: 1, Scope: store.BotScopePersonal},
		Run:     store.BotRun{ID: id.RunID, BotID: id.BotID, Version: 1, SessionID: uuid.New(), Status: store.RunRunning, Lease: store.BotLease{BotID: id.BotID, RunID: id.RunID, WorkerID: "test", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}},
		Version: version, Principal: p, Policy: policy, Budget: &botChatTestBudget{}, Instructions: "Report the observation.",
	}
	ss := session.InMemoryService()
	_, err = ss.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: owner.String(), SessionID: req.Run.SessionID.String(), State: map[string]any{"execution_owner": "bot", "preserved": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, req, ss
}

func botChatTestFactory(t *testing.T, ss session.Service, llm model.LLM, tools []tool.Tool, cleaned *atomic.Int64) BotChatAgentFactory {
	t.Helper()
	return func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
		id, ok := execution.BotRunIdentityFromContext(ctx)
		if !ok || id != req.Policy.Identity() || execution.RunPolicyFromContext(ctx) != req.Policy || BotChatRunBudgetFromContext(ctx) != req.Budget {
			t.Error("factory lost trusted policy/budget context")
		}
		chat := agent.NewChatAgent(llm, tools, nil, ss, &agent.SystemPromptBuilder{}, false, false)
		return &BotChatResources{Agent: chat, SessionService: ss, AppName: studioChatAppName, Cleanup: func() { cleaned.Add(1) }}, nil
	}
}

type employeeWorkerTranscript struct {
	events []*session.Event
	err    error
}

func (s *employeeWorkerTranscript) AppendBotRunEvent(_ context.Context, _ store.BotLease, _ uuid.UUID, e *session.Event) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, e)
	return nil
}

func employeeInteractiveTestRequest(t *testing.T, ctx context.Context, req bot.RunRequest) (context.Context, bot.RunRequest) {
	t.Helper()
	id := req.Policy.Identity()
	req.Principal.Scopes = []string{"chat", "tool:execute"}
	ctx, err := execution.WithPrincipal(ctx, req.Principal)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	registry := execution.NewBotTurnAuthorizationRegistry(8)
	t.Cleanup(registry.Close)
	a, err := registry.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	ceiling := a.CallerCeiling()
	req.Run.ExecutionMode, req.Run.CallerCeiling, req.Policy = execution.BotExecutionModeChatInteractive, &ceiling, nil
	req.Authorization, err = registry.Authorizer(id, ceiling, func(c context.Context, p execution.Principal) (execution.Principal, error) { return p, c.Err() })
	if err != nil {
		t.Fatal(err)
	}
	ctx = execution.WithRunPolicy(ctx, nil)
	ctx = execution.WithBotTurnAuthorizer(ctx, req.Authorization)
	return ctx, req
}

type employeeInteractiveAllowGuard struct{}

func (employeeInteractiveAllowGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	digest, err := a.Digest()
	if ctx.Err() != nil {
		return execution.PolicyDecision{}, ctx.Err()
	}
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, err
}

type employeeInteractiveUnitGuard struct{}

func (employeeInteractiveUnitGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	digest, err := a.Digest()
	kind := execution.PolicyRequestApproval
	if _, ok := agent.BotPolicyRuntimeFromContext(ctx).Guard.(*botResumeGuard); ok {
		kind = execution.PolicyAllow
	}
	return execution.PolicyDecision{Kind: kind, ActionDigest: digest}, err
}

// Exercise the real Employee hold/release sink, fenced SQLite writer and scoped
// Home reader together; no fixture is allowed to mint speech provenance.
func TestBotEmployeeHeldTextReleasePersistedProjection(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			env := setupMemoryHTTPAcceptance(t, &MockLLM{}, nil)
			org, err := env.Store.ForOrg(env.OrgSlug)
			if err != nil {
				t.Fatal(err)
			}
			repo, sessions := org.ForUser(env.UserID).Bots(), env.Services.PersonalSessions
			if scope == store.BotScopeTeam {
				repo, sessions = org.ForTeam(env.TeamSlug).Bots(), env.Services.Sessions
			}
			principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug, Authenticated: true}
			owner, err := execution.WithPrincipal(context.Background(), principal)
			if err != nil {
				t.Fatal(err)
			}
			version := store.DefaultBotVersion()
			version.Charter = "Inspect the assigned source."
			b, err := repo.Create(owner, store.BotCreateRequest{Name: "Employee release regression", OwnerID: uuid.MustParse(env.UserID), Version: version, IdempotencyKey: "employee-release-bot"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "regression conversation"})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := repo.(store.BotMessageStore).AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Inspect the source", IdempotencyKey: "employee-release"})
			if err != nil {
				t.Fatal(err)
			}
			identity := execution.BotRunIdentity{BotID: b.ID, RunID: receipt.Run.ID, Version: b.Version, ActorID: env.UserID, OrgSlug: env.OrgSlug, TeamSlug: env.TeamSlug}
			ctx := execution.WithBotRunIdentity(owner, identity)
			lease, err := repo.ClaimRun(ctx, store.BotClaimRequest{BotID: b.ID, RunID: receipt.Run.ID, WorkerID: "release-regression", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.GetRun(owner, b.ID, receipt.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			work, err := repo.(store.BotWorkStore).LoadWork(ctx, lease)
			if err != nil {
				t.Fatal(err)
			}
			if work == nil || work.Protocol != store.BotWorkProtocolAgentLedV1 || work.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 {
				t.Fatal("fixture did not enter the guarded Employee hold/release path")
			}
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
			if err != nil {
				t.Fatal(err)
			}
			ctx = execution.WithRunPolicy(ctx, policy)
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
			read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read assigned source"}, func(tc tool.Context, args map[string]any) (map[string]any, error) {
				if err := agent.AuthorizeBotTool(tc, "read_file", args); err != nil {
					return nil, err
				}
				return map[string]any{"content": "PRIVATE_SOURCE"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := NewMockLLM(&MockTurn{Parts: []*genai.Part{{Text: "I am checking the assigned source."}, {FunctionCall: &genai.FunctionCall{ID: "inspect", Name: "read_file", Args: map[string]any{"path": "source"}}}}}, TextTurn("Held final must not become interim."))
			resources := &BotChatResources{Agent: agent.NewChatAgent(llm, []tool.Tool{read}, nil, sessions, &agent.SystemPromptBuilder{}, false, false), SessionService: sessions, AppName: studioChatAppName}
			req := bot.RunRequest{Bot: *b, Run: *run, Version: version, Principal: principal, Policy: policy, Budget: &botChatTestBudget{}, Work: work, Instructions: "Inspect the source."}
			var released *session.Event
			var envelope bot.RuntimeEvent
			var releaseCtx context.Context
			var sequence int64
			var completed ChatCompletion
			transcript := repo.(store.BotTranscriptStore)
			_, err = runBotChatAttempt(ctx, req, resources, func(c context.Context, wire bot.RuntimeEvent) error {
				e := &session.Event{ID: uuid.NewString(), Author: "bot_runtime", InvocationID: wire.TurnID.String(), Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": wire}}}
				if wire.Type == "text" {
					if released != nil {
						t.Fatal("held final released before report commit")
					}
					released, envelope, releaseCtx = e, wire, c
					if !store.BotEmployeeInterimReleased(c, wire) {
						t.Fatal("actual Employee release lacks exact trusted envelope")
					}
				} else if store.BotEmployeeInterimReleased(c, wire) {
					t.Fatalf("runtime %s acquired Employee release", wire.Type)
				}
				return transcript.AppendBotRunEvent(c, lease, wire.SessionID, e)
			}, nil, nil, nil, false, &sequence, &completed)
			if err != nil {
				t.Fatal(err)
			}
			if released == nil {
				t.Fatal("Employee interim never reached sink")
			}
			binding, err := repo.(store.BotEmployeeInterimReader).ReadBotEmployeeInterim(owner, b.ID, run.ID, released.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"worker", "runtime", "copy", "forged", "changed-envelope"} {
				wire := envelope
				e := &session.Event{ID: kind, Author: "bot_runtime", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{store.BotEmployeeInterimKey: binding}}}
				c := ctx
				if kind == "worker" {
					e.Author = "chat"
					e.LLMResponse.Content = genai.NewContentFromText("PRIVATE_WORKER", "model")
				}
				if kind == "forged" {
					e.Actions.StateDelta[store.BotEmployeeInterimKey] = true
				}
				if kind == "changed-envelope" {
					wire.Sequence++
					wire.Payload = json.RawMessage(`{"text":"Forged Employee outcome"}`)
					c = context.WithoutCancel(releaseCtx) // retain only the exact-envelope authority after runner cleanup
				}
				e.Actions.StateDelta["bot_runtime_event"] = wire
				if err := transcript.AppendBotRunEvent(c, lease, run.SessionID, e); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.(store.BotEmployeeInterimReader).ReadBotEmployeeInterim(owner, b.ID, run.ID, kind); err == nil {
					t.Fatalf("%s acquired persisted Employee speech", kind)
				}
			}
			sourceRows, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(owner, run.SessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			privateRows := 0
			for _, row := range sourceRows {
				switch row.Event.ID {
				case "worker", "runtime", "copy", "forged", "changed-envelope":
					privateRows++
					p := &SessionEventProjector{BotID: b.ID, HomeSessionID: b.HomeSessionID, SessionID: b.HomeSessionID, Repository: repo}
					projection, err := p.ProjectBot(owner, row, nil)
					if err != nil || len(projection.Events) != 0 {
						t.Fatalf("persisted %s projected: %+v err=%v", row.Event.ID, projection.Events, err)
					}
				}
			}
			if privateRows != 5 {
				t.Fatalf("private diagnostic rows lost: %d", privateRows)
			}
			rows, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(owner, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"live", "restore", "reconnect"} {
				p := &SessionEventProjector{BotID: b.ID, HomeSessionID: b.HomeSessionID, SessionID: b.HomeSessionID, Repository: repo}
				texts := 0
				for _, row := range rows {
					projection, err := p.ProjectBot(owner, row, nil)
					if err != nil {
						t.Fatal(err)
					}
					for _, event := range projection.Events {
						if event.Type == "text" {
							texts++
							if event.Data["text"] != "I am checking the assigned source." {
								t.Fatalf("%s private speech: %+v", mode, event)
							}
						}
					}
				}
				if texts != 1 {
					t.Fatalf("%s Employee texts=%d", mode, texts)
				}
			}
		})
	}
}

func TestEmployeeWorkerApprovalOwnership(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("interactive=%v", interactive), func(t *testing.T) { testEmployeeWorkerApprovalOwnership(t, interactive) })
	}
}
func testEmployeeWorkerApprovalOwnership(t *testing.T, interactive bool) {
	ctx, req, _ := botChatTestRequest(t)
	id := req.Policy.Identity()
	if interactive {
		ctx, req = employeeInteractiveTestRequest(t, ctx, req)
	}
	ctx = execution.WithBotRunIdentity(ctx, id)
	ctx = execution.WithRunPolicy(ctx, req.Policy)
	repo := &resumeTestStore{}
	capture := &botApprovalCapture{store: repo, req: req}
	var live execution.PolicyGuard = resumeTestGuard{}
	if interactive {
		live = employeeInteractiveUnitGuard{}
	}
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: live, Approvals: capture})
	var calls atomic.Int64
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Exact effect"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"result": "exact saved action"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	trace := &employeeWorkerTranscript{}
	resources := &BotChatResources{AppName: studioChatAppName, Transcript: trace}
	resources.WorkerFactory = func(c context.Context, r BotEmployeeWorkerRequest) (*BotChatResources, error) {
		llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{"body": "exact"}), TextTurn("private done"))
		if _, resumed := agent.BotPolicyRuntimeFromContext(c).Guard.(*botResumeGuard); resumed {
			llm = NewMockLLM(TextTurn("private done"))
		}
		return &BotChatResources{Agent: agent.NewChatAgent(llm, []tool.Tool{read}, nil, r.Sessions, &agent.SystemPromptBuilder{}, false, false), AppName: studioChatAppName, Cleanup: func() {}}, nil
	}
	s := &botEmployeeChatService{req: req, resources: resources, capture: capture, workers: make(map[string]*botEmployeeWorker)}
	defer s.close()
	paused, err := s.Send(ctx, bot.EmployeeChatRequest{Message: "send exact"})
	if err != nil || paused.Status != bot.EmployeeChatNeedsInput || calls.Load() != 0 {
		t.Fatalf("pause %+v %v calls=%d", paused, err, calls.Load())
	}
	if capture.invocation == nil || capture.invocation.Name != "read_file" {
		t.Fatalf("captured parent rather than worker: %+v", capture.invocation)
	}
	repo.a.Status = store.ApprovalApproved
	if interactive {
		ss := session.InMemoryService()
		created, err := ss.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range trace.events {
			if err := ss.AppendEvent(ctx, created.Session, e); err != nil {
				t.Fatal(err)
			}
		}
		waiting, ok := capture.checkpoint()
		if !ok {
			t.Fatal("missing exact private checkpoint")
		}
		req.Run.Checkpoint = waiting.Checkpoint
		req.Run.Lease.Fence++
		ctx = bot.WithApprovalStore(ctx, repo)
		ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: live})
		final := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
			if len(q.Tools) != 0 {
				t.Fatal("Employee final retained tools")
			}
			return NewMockLLM(TextTurn("Employee exact outcome.")).GenerateContent(ctx, q, false)
		}}
		adapter, err := NewBotChatRuntimeAdapter(func(c context.Context, r bot.RunRequest) (*BotChatResources, error) {
			if r.Policy != nil || execution.RunPolicyFromContext(c) != nil {
				t.Fatal("nil-policy resume minted policy")
			}
			runtime, err := BindBotChatPolicyRuntime(c, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: live})
			return &BotChatResources{Agent: agent.NewChatAgent(final, nil, nil, ss, &agent.SystemPromptBuilder{}, false, false), SessionService: ss, AppName: studioChatAppName, Authorization: r.Authorization, PolicyRuntime: &runtime, Transcript: trace, WorkerFactory: resources.WorkerFactory, Cleanup: func() {}}, err
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := adapter.Resume(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
		if err != nil || calls.Load() != 1 || repo.consumed != 1 || result.Report.Summary != "Employee exact outcome." {
			t.Fatalf("interactive exact resume: %+v %v calls=%d consumed=%d", result, err, calls.Load(), repo.consumed)
		}
		if _, err := adapter.Resume(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil }); err == nil {
			t.Fatal("consumed exact action replayed")
		}
		return
	}
	if req.Policy != nil {
		policy, err := req.Policy.WithGrant(bot.ApprovalGrant(req.Policy.Identity(), *repo.a))
		if err != nil {
			t.Fatal(err)
		}
		req.Policy = policy
		ctx = execution.WithRunPolicy(ctx, policy)
	}
	guard := &botResumeGuard{live: live, store: repo, req: req, approval: *repo.a}
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: guard})
	restored := &botEmployeeChatService{req: req, resources: resources, workers: make(map[string]*botEmployeeWorker)}
	defer restored.close()
	if err := restored.restore(botConversationEvents{current: botConversationAppendEvents{appended: trace.events}}); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.resumeWorker(ctx, &botEmployeeWorker{id: "foreign", pending: "foreign"}, repo.a.Action, nil); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("foreign worker: %v", err)
	}
	w := restored.workers[paused.WorkerID]
	result, err := restored.resumeWorker(ctx, w, repo.a.Action, nil)
	if err != nil || result.Status != store.RunSucceeded || calls.Load() != 1 || repo.consumed != 1 {
		t.Fatalf("resume %+v %v calls=%d consumed=%d", result, err, calls.Load(), repo.consumed)
	}
	if _, err := restored.resumeWorker(ctx, w, repo.a.Action, nil); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("repeated resume %v", err)
	}
	crashed := &botEmployeeChatService{req: req, resources: resources, workers: make(map[string]*botEmployeeWorker)}
	if err := crashed.restore(botConversationEvents{current: botConversationAppendEvents{appended: trace.events}}); err != nil {
		t.Fatal(err)
	}
	if crashed.workers[w.id].approvalID != uuid.Nil || calls.Load() != 1 {
		t.Fatal("resume authority survived settlement")
	}
}

// employeePreparedTool exercises the real prepared engine and adapter entry point.
// Only storage and the confined mirror effect are local fixtures; no successful
// runtime result is injected into resumeWorker or executePreparedPlan.
type employeePreparedTool struct {
	employeePreparedRunnable
	invoke func(tool.Context, any) (map[string]any, error)
}

type employeePreparedRunnable interface {
	tool.Tool
	Run(tool.Context, any) (map[string]any, error)
	ProcessRequest(tool.Context, *model.LLMRequest) error
}

func (t *employeePreparedTool) RunPreparedPlan(ctx tool.Context, args any) (map[string]any, error) {
	return t.invoke(ctx, args)
}

type employeePreparedStore struct {
	*resumeTestStore
	store.BotPreparedPlanStore
	record *store.BotPreparedPlan
}

func (s *employeePreparedStore) LoadBotPlan(_ context.Context, lease store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	if s.record == nil || id != s.record.OperationID || lease.RunID != s.record.RunID || lease.BotID != s.record.BotID {
		return nil, store.ErrBotForbidden
	}
	return s.record, nil
}
func (s *employeePreparedStore) LoadBotPlanByApproval(ctx context.Context, lease store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	if s.record == nil || id != s.record.ApprovalID {
		return nil, store.ErrBotNotFound
	}
	return s.LoadBotPlan(ctx, lease, s.record.OperationID)
}

type employeePreparedClassifier struct{}

func (employeePreparedClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if name == "prepare_private_plan" {
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	}
	return (resumeTestClassifier{}).DescribeAction(ctx, name, args)
}

func TestEmployeeWorkerPreparedPlanApprovalResume(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("interactive=%v", interactive), func(t *testing.T) { testEmployeeWorkerPreparedPlanApprovalResume(t, interactive) })
	}
}
func testEmployeeWorkerPreparedPlanApprovalResume(t *testing.T, interactive bool) {
	ctx, req, ss := botChatTestRequest(t)
	var policyErr error
	req.Policy, policyErr = execution.NewRunPolicy(execution.RunPolicySpec{Identity: req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"read_file", "prepare_private_plan"}})
	if policyErr != nil {
		t.Fatal(policyErr)
	}
	id := req.Policy.Identity()
	if interactive {
		ctx, req = employeeInteractiveTestRequest(t, ctx, req)
	}
	ctx = execution.WithBotRunIdentity(ctx, id)
	ctx = execution.WithRunPolicy(ctx, req.Policy)
	repo := &employeePreparedStore{resumeTestStore: &resumeTestStore{}}
	ctx = bot.WithApprovalStore(ctx, repo)
	var live execution.PolicyGuard = resumeTestGuard{}
	if interactive {
		live = employeeInteractiveAllowGuard{}
	}
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: employeePreparedClassifier{}, Guard: live})
	trace := &employeeWorkerTranscript{}
	var effects atomic.Int64
	var privateID string
	var mirrored agent.PlanSnapshot
	workerFactory := func(c context.Context, r BotEmployeeWorkerRequest) (*BotChatResources, error) {
		if r.Request.Budget != req.Budget || r.Request.Run.SessionID != req.Run.SessionID {
			t.Fatal("prepared worker replaced original execution or budget")
		}
		if privateID != "" && privateID != r.ConversationID {
			t.Fatal("prepared continuation changed private conversation")
		}
		privateID = r.ConversationID
		bridge := BotPreparedPlanSessionFromContext(c)
		if bridge == nil || bridge.Store != repo {
			t.Fatal("prepared worker lost trusted store bridge")
		}
		var invocation tool.Context
		engine := agent.NewPlanRuntime(nil, agent.PlanRuntimeOptions{
			PrepareOperation: func(op agent.PlanPreparedOperation) error {
				if bridge.Resume != nil {
					return nil
				}
				raw, _ := json.Marshal(map[string]any{"path": "/workspace/PLAN.md", "content": "PRIVATE_PLAN"})
				action, _ := (resumeTestClassifier{}).DescribeAction(c, "read_file", raw)
				digest, _ := action.Digest()
				saved, err := repo.RequestApproval(c, req.Run.Lease, store.BotApproval{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					return err
				}
				encoded, _ := json.Marshal(op)
				repo.record = &store.BotPreparedPlan{BotID: req.Bot.ID, RunID: req.Run.ID, SessionID: req.Run.SessionID, Version: req.Run.Version, OperationID: uuid.MustParse(op.ID), Revision: 1, LeafKind: store.BotPlanLeafMirror, Stage: store.BotPlanAwaitingApproval, Action: action, ActionDigest: digest, ApprovalID: saved.ID, EngineRecord: encoded}
				if err := bridge.CaptureApproval(repo.record); err != nil {
					return err
				}
				return store.ErrBotConflict
			},
			CommitSnapshot: func(snapshot agent.PlanSnapshot) error {
				if invocation == nil || bridge.Resume == nil || repo.record.Stage != store.BotPlanAwaitingApproval {
					return store.ErrBotForbidden
				}
				if interactive {
					if err := req.Authorization.AuthorizeTool(invocation, repo.record.Action.Tool); err != nil {
						return err
					}
				} else {
					decision := execution.EvaluateRunPolicy(invocation, repo.record.Action, time.Now())
					if decision.Kind != execution.PolicyConsumeGrant && decision.Kind != execution.PolicyAllow {
						return store.ErrBotForbidden
					}
				}
				if err := repo.ConsumeApproval(invocation, store.BotApprovalConsumption{ActionDigest: repo.record.ActionDigest}); err != nil {
					return err
				}
				repo.record.Stage = store.BotPlanDispatchStarted
				effects.Add(1)
				mirrored = snapshot.Clone()
				repo.record.Snapshot, _ = json.Marshal(snapshot)
				repo.record.Stage = store.BotPlanPublished
				return nil
			},
		})
		prepare, err := functiontool.New(functiontool.Config{Name: "prepare_private_plan", Description: "Prepare a private plan"}, func(tc tool.Context, args map[string]any) (map[string]any, error) {
			err := engine.Announce("PRIVATE_PLAN", agent.PlanDocumentInfo{Context: "Prepare the assigned private plan under exact consent.", WhatNotToDo: "Do not modify unrelated work.", Verification: "Read the persisted plan."}, []agent.PlanStepInfo{{Name: "private", Description: "private work", Details: "Persist the exact private snapshot.", Summary: "Publish the private plan.", Outcome: "The saved plan can be read.", Verify: "test -s PLAN.md", VerifyKind: "unit", Files: []agent.PlanFileChange{{Path: "PLAN.md", Kind: "modify"}}}})
			return nil, err
		})
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Confined prepared mirror"}, func(tool.Context, map[string]any) (map[string]any, error) {
			t.Fatal("generic leaf bypassed prepared engine")
			return nil, store.ErrBotForbidden
		})
		if err != nil {
			t.Fatal(err)
		}
		prepared := &employeePreparedTool{employeePreparedRunnable: leaf.(employeePreparedRunnable), invoke: func(tc tool.Context, args any) (map[string]any, error) {
			record, err := bridge.ClaimResume("read_file", args)
			if err != nil {
				return nil, err
			}
			if tc.State() == nil || tc.UserID() != req.Bot.OwnerID.String() {
				return nil, store.ErrBotForbidden
			}
			invocation = tc
			var op agent.PlanPreparedOperation
			if err := json.Unmarshal(record.EngineRecord, &op); err != nil {
				return nil, err
			}
			result, err := engine.ResumePreparedOperation(op)
			return map[string]any{"result": result}, err
		}}
		llm := NewMockLLM(ToolCallTurn("prepare_private_plan", map[string]any{}))
		chat := agent.NewChatAgent(llm, []tool.Tool{prepare, prepared}, nil, r.Sessions, &agent.SystemPromptBuilder{}, false, false)
		chat.ToolIndex = agent.NewLexicalToolIndex()
		if err := chat.ToolIndex.PrimeTools(c, []tool.Tool{prepare, prepared}, nil); err != nil {
			t.Fatal(err)
		}
		return &BotChatResources{Agent: chat, AppName: studioChatAppName, Cleanup: func() {}}, nil
	}
	// The initial worker really runs Chat and suspends inside PlanRuntime's
	// prepare-before-effect callback, capturing the exact private leaf approval.
	capture := &botApprovalCapture{store: repo, req: req}
	bridge := &BotPreparedPlanSession{Request: req, Store: repo, capture: capture}
	initial := context.WithValue(ctx, botPreparedPlanSessionKey{}, bridge)
	resources := &BotChatResources{AppName: studioChatAppName, Transcript: trace, WorkerFactory: workerFactory}
	service := &botEmployeeChatService{req: req, resources: resources, capture: capture, workers: make(map[string]*botEmployeeWorker)}
	paused, err := service.Send(initial, bot.EmployeeChatRequest{Message: "Prepare the requested plan"})
	service.close()
	if err != nil || paused.Status != bot.EmployeeChatNeedsInput || effects.Load() != 0 {
		t.Fatalf("pause %+v err=%v effects=%d completion=%+v approval=%+v", paused, err, effects.Load(), service.completion.Err, repo.a)
	}
	waiting, ok := capture.checkpoint()
	if !ok || repo.record.Stage != store.BotPlanAwaitingApproval {
		t.Fatal("missing prepared checkpoint")
	}
	for _, e := range trace.events {
		if err := ss.AppendEvent(ctx, mustEmployeePreparedSession(t, ctx, ss, req), e); err != nil {
			t.Fatal(err)
		}
	}
	persistedEvents := len(trace.events)
	req.Run.Checkpoint = waiting.Checkpoint
	req.Run.Lease.Fence++
	repo.a.Status = store.ApprovalApproved
	finalCalls := 0
	final := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			finalCalls++
			if len(q.Tools) != 0 || (q.Config != nil && len(q.Config.Tools) != 0) {
				t.Error("Employee final retained tools")
			}
			yield(&model.LLMResponse{Content: genai.NewContentFromText("The saved private plan was published.", "model")}, nil)
		}
	}}
	var cleaned atomic.Int64
	factory := botChatTestFactory(t, ss, final, nil, &cleaned)
	if interactive {
		factory = func(c context.Context, r bot.RunRequest) (*BotChatResources, error) {
			if r.Policy != nil || execution.RunPolicyFromContext(c) != nil {
				t.Fatal("interactive continuation minted policy")
			}
			return &BotChatResources{Agent: agent.NewChatAgent(final, nil, nil, ss, &agent.SystemPromptBuilder{}, false, false), Authorization: r.Authorization, SessionService: ss, AppName: studioChatAppName, Cleanup: func() { cleaned.Add(1) }}, nil
		}
	}
	adapter, err := NewBotChatRuntimeAdapter(func(c context.Context, r bot.RunRequest) (*BotChatResources, error) {
		res, err := factory(c, r)
		res.Transcript, res.WorkerFactory = trace, workerFactory
		return res, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var visible []string
	originalDigest := repo.a.ActionDigest
	repo.a.ActionDigest = "foreign-exact-action"
	if _, err := adapter.Resume(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil }); err == nil {
		t.Fatal("changed exact approval resumed private plan")
	}
	repo.a.ActionDigest = originalDigest
	if effects.Load() != 0 || repo.consumed != 0 || finalCalls != 0 {
		t.Fatal("foreign approval crossed dispatch boundary")
	}
	result, err := adapter.Resume(ctx, req, func(_ context.Context, event bot.RuntimeEvent) error {
		if event.Type == "text" {
			visible = append(visible, string(event.Payload))
		}
		return nil
	})
	if err != nil || result.Status != store.RunSucceeded || effects.Load() != 1 || repo.consumed != 1 || finalCalls != 1 {
		t.Fatalf("resume %+v err=%v cause=%v effects=%d consumed=%d finals=%d", result, err, errors.Unwrap(errors.Unwrap(err)), effects.Load(), repo.consumed, finalCalls)
	}
	if mirrored.Goal != "PRIVATE_PLAN" || len(repo.record.Snapshot) == 0 || repo.record.Stage != store.BotPlanPublished {
		t.Fatal("prepared work was not persisted")
	}
	if result.Report.Summary != "The saved private plan was published." || strings.Contains(strings.Join(visible, ""), "PRIVATE_PLAN") {
		t.Fatalf("private output leaked: %+v", visible)
	}
	if err := result.Report.ValidateEmployeeMessageIntegrity(result.Status); err != nil {
		t.Fatal(err)
	}
	// Persisted resuming/settled ownership prevents replay even after reconstruction.
	for _, e := range trace.events[persistedEvents:] {
		if err := ss.AppendEvent(ctx, mustEmployeePreparedSession(t, ctx, ss, req), e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := adapter.Resume(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil }); err == nil {
		t.Fatal("published private worker replayed")
	}
	if effects.Load() != 1 || repo.consumed != 1 || finalCalls != 1 {
		t.Fatal("replay dispatched effects or Employee")
	}
	foreign := req
	foreign.Run.ID = uuid.New()
	foreign.Run.Lease.RunID = foreign.Run.ID
	if _, err := adapter.Resume(ctx, foreign, func(context.Context, bot.RuntimeEvent) error { return nil }); err == nil {
		t.Fatal("foreign run resumed private plan")
	}
}

func mustEmployeePreparedSession(t *testing.T, ctx context.Context, ss session.Service, req bot.RunRequest) session.Session {
	t.Helper()
	saved, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	return saved.Session
}

func TestEmployeeWorkerHistoryAndScope(t *testing.T) {
	testEmployeeWorkerHistoryAndScope(t, false)
}

func TestEmployeeWorkerInheritedModelOverrideJournaling(t *testing.T) {
	testEmployeeWorkerHistoryAndScope(t, true)
}

func testEmployeeWorkerHistoryAndScope(t *testing.T, inheritedOverride bool) {
	ctx, req, _ := botChatTestRequest(t)
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = execution.WithRunPolicy(ctx, req.Policy)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
	trace := &employeeWorkerTranscript{}
	var calls atomic.Int64
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read controlled real tool"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(ctx, "read_file", args); err != nil {
			return nil, err
		}
		calls.Add(1)
		return map[string]any{"content": "PRIVATE_SOURCE"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "source"}), TextTurn("PRIVATE_WORKER"), TextTurn("PRIVATE_FOLLOWUP"))
	if inheritedOverride {
		// The parent context carries the same bare provider. Chat must still use
		// the worker-owned decorators, not silently bypass invocation capture.
		ctx = agent.WithLLM(ctx, llm)
	}
	resources := &BotChatResources{AppName: studioChatAppName, Transcript: trace}
	resources.WorkerFactory = func(ctx context.Context, request BotEmployeeWorkerRequest) (*BotChatResources, error) {
		if request.Request.Run.SessionID != req.Run.SessionID || request.Request.Budget != req.Budget {
			t.Fatal("execution changed")
		}
		return &BotChatResources{Agent: agent.NewChatAgent(llm, []tool.Tool{read}, nil, request.Sessions, &agent.SystemPromptBuilder{}, false, false), AppName: studioChatAppName, Cleanup: func() {}}, nil
	}
	service := &botEmployeeChatService{req: req, resources: resources, workers: make(map[string]*botEmployeeWorker)}
	defer service.close()
	first, err := service.Send(ctx, bot.EmployeeChatRequest{Message: "Inspect the source"})
	if err != nil || first.Status != bot.EmployeeChatCompleted || first.PrivateText != "PRIVATE_WORKER" {
		t.Fatalf("first %+v %v", first, err)
	}
	if service.completion.Report.EffectState != store.BotReportEffectsReadOnly {
		t.Fatalf("private invocation bypassed journal: effect=%q", service.completion.Report.EffectState)
	}
	if service.workers[first.WorkerID].resources.Agent.LLM != llm {
		t.Fatal("worker model decorator was not restored")
	}
	second, err := service.Send(ctx, bot.EmployeeChatRequest{WorkerID: first.WorkerID, Message: "Explain the same source"})
	if err != nil || second.PrivateText != "PRIVATE_FOLLOWUP" || calls.Load() != 1 {
		t.Fatalf("follow-up %+v %v calls=%d", second, err, calls.Load())
	}
	raw, _ := json.Marshal(llm.Calls[len(llm.Calls)-1])
	if !strings.Contains(string(raw), "PRIVATE_SOURCE") || !strings.Contains(string(raw), "PRIVATE_WORKER") {
		t.Fatalf("lost history %s", raw)
	}
	if _, err := service.Send(ctx, bot.EmployeeChatRequest{WorkerID: uuid.NewString(), Message: "foreign"}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("foreign handle %v", err)
	}
	for _, e := range trace.events {
		if e.Content != nil || e.Author != "bot_runtime" {
			t.Fatal("private content entered public transcript")
		}
	}
	restored := &botEmployeeChatService{req: req, resources: resources, workers: make(map[string]*botEmployeeWorker)}
	if err := restored.restore(botConversationEvents{current: botConversationAppendEvents{appended: trace.events}}); err != nil {
		t.Fatal(err)
	}
	if restored.workers[first.WorkerID].pending != "" {
		t.Fatal("settled dispatch restored as uncertain")
	}
	if err := restored.record(ctx, botEmployeeWorkerTrace{WorkerID: first.WorkerID, DispatchID: "crashed", Phase: "started"}); err != nil {
		t.Fatal(err)
	}
	crashed := &botEmployeeChatService{req: req, resources: resources, workers: make(map[string]*botEmployeeWorker)}
	if err := crashed.restore(botConversationEvents{current: botConversationAppendEvents{appended: trace.events}}); err != nil {
		t.Fatal(err)
	}
	uncertain, err := crashed.Send(ctx, bot.EmployeeChatRequest{WorkerID: first.WorkerID, Message: "retry"})
	if err != nil || uncertain.Status != bot.EmployeeChatPartial || calls.Load() != 1 {
		t.Fatalf("replayed uncertain %+v %v", uncertain, err)
	}
}

// This uses a real temporary HTTP listener, not a recorder or fake ChatRunner.
// HTTP only drives the worker here; it is deliberately not a production route.
func TestBotChatRuntimeAdapterHTTPToolless(t *testing.T) {
	testBotChatRuntimeAdapterHTTPToolless(t, false)
}

func TestBotChatRuntimeAdapterInheritedModelOverride(t *testing.T) {
	testBotChatRuntimeAdapterHTTPToolless(t, true)
}

func testBotChatRuntimeAdapterHTTPToolless(t *testing.T, inheritedOverride bool) {
	ctx, req, ss := botChatTestRequest(t)
	var cleaned atomic.Int64
	llm := NewMockLLM(TextTurn("Observed: all checks green."))
	inherited := NewMockLLM(TextTurn("Inherited model must not run."))
	if inheritedOverride {
		ctx = agent.WithLLM(ctx, inherited)
	}
	adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, nil, &cleaned))
	if err != nil {
		t.Fatal(err)
	}
	var events []bot.RuntimeEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := adapter.Start(ctx, req, func(_ context.Context, event bot.RuntimeEvent) error { events = append(events, event); return nil })
		if err != nil {
			t.Errorf("Start: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result bot.RunResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || result.Status != store.RunSucceeded || result.Report.Summary != "Observed: all checks green." || len(result.Report.Unverified) != 0 {
		t.Fatalf("result: %+v, HTTP %d", result, response.StatusCode)
	}
	if cleaned.Load() != 1 || len(llm.Calls) != 1 || len(inherited.Calls) != 0 {
		t.Fatalf("cleanup=%d model calls=%d inherited calls=%d", cleaned.Load(), len(llm.Calls), len(inherited.Calls))
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) || event.RunID != req.Run.ID || event.SessionID != req.Run.SessionID || event.TurnID != events[0].TurnID {
			t.Fatalf("event identity/sequence: %+v", event)
		}
	}
	if len(events) < 3 || events[len(events)-1].Type != "done" {
		t.Fatalf("missing final transport event: %+v", events)
	}
	stored, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: req.Bot.OwnerID.String(), SessionID: req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := stored.Session.State().Get("execution_owner")
	if owner != "bot" {
		t.Fatalf("execution owner changed: %v", owner)
	}
}

// A trusted, mechanically constrained classifier and live guard are installed
// for this one test tool. This does not claim native production paths are ready.
type botChatTestClassifier struct{}

func (botChatTestClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}

type botChatTestGuard struct{}

func (botChatTestGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	return execution.EvaluateRunPolicy(ctx, action, time.Now()), nil
}

func TestBotChatRuntimeAdapterHTTPToolSequence(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
	var cleaned, executed atomic.Int64
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read controlled fixture"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(ctx, "read_file", args); err != nil {
			return nil, err
		}
		executed.Add(1)
		return map[string]any{"content": "fixture value"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "fixture"}), TextTurn("Fixture value observed."))
	adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{read}, &cleaned))
	var events []bot.RuntimeEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		result, err := adapter.Start(ctx, req, func(_ context.Context, e bot.RuntimeEvent) error { events = append(events, e); return nil })
		if err != nil {
			t.Errorf("Start: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result bot.RunResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunPartial || len(result.Report.Unverified) == 0 || executed.Load() != 1 || len(llm.Calls) != 2 || cleaned.Load() != 1 {
		t.Fatalf("result=%+v executed=%d calls=%d cleanup=%d", result, executed.Load(), len(llm.Calls), cleaned.Load())
	}
	calls, results := 0, 0
	for _, event := range events {
		if event.Type == "tool_call" {
			calls++
		}
		if event.Type == "tool_result" {
			results++
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("calls=%d results=%d events=%+v", calls, results, events)
	}
}

func TestBotChatRuntimeAdapterRejectsMissingAuthority(t *testing.T) {
	for _, name := range []string{"budget", "policy", "principal", "owner", "session", "lease", "checkpoint"} {
		t.Run(name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			switch name {
			case "budget":
				req.Budget = nil
			case "policy":
				req.Policy = nil
			case "principal":
				ctx = context.Background()
			case "owner":
				req.Bot.OwnerID = uuid.New()
			case "session":
				req.Run.SessionID = uuid.Nil
			case "lease":
				req.Run.Lease.Fence = 0
			case "checkpoint":
				req.Run.Checkpoint = json.RawMessage(`{}`)
			}
			adapter, _ := NewBotChatRuntimeAdapter(func(context.Context, bot.RunRequest) (*BotChatResources, error) {
				t.Error("factory called without authority")
				return nil, nil
			})
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestBotChatRuntimeAdapterSinkFailureAndModelFailure(t *testing.T) {
	for _, failure := range []string{"sink", "model", "empty"} {
		t.Run(failure, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			var cleaned atomic.Int64
			turns := []*MockTurn{TextTurn("success must not escape")}
			if failure == "model" {
				turns = []*MockTurn{ErrorTurn("failed", "controlled failure")}
			}
			if failure == "empty" {
				turns = []*MockTurn{TextTurn("")}
			}
			adapter, _ := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, NewMockLLM(turns...), nil, &cleaned))
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error {
				if failure == "sink" {
					return errors.New("persistence unavailable")
				}
				return nil
			})
			if err == nil || result.Status == store.RunSucceeded || result.Failure == nil || cleaned.Load() != 1 {
				t.Fatalf("result=%+v err=%v cleaned=%d", result, err, cleaned.Load())
			}
		})
	}
}

func TestBotChatRuntimeAdapterTypedCompletionNotDone(t *testing.T) {
	for _, eventType := range []string{"done", "error", "error_info", "approval", "network_denial_hint", "chat_question"} {
		t.Run(eventType, func(t *testing.T) {
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.emitEvent(eventType, map[string]any{"error": "failure"})
			completion := cr.Completion()
			if completion.Final {
				t.Fatal("wire event became successful completion")
			}
			if eventType == "error" || eventType == "error_info" {
				if completion.Err == nil {
					t.Fatal("lost failure")
				}
			}
			if eventType == "approval" || eventType == "network_denial_hint" || eventType == "chat_question" {
				if !completion.Blocked {
					t.Fatal("lost block")
				}
			}
		})
	}
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("claim", "model")}})
	cr.observeCompletion(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "execute_tool", Response: map[string]any{"state": string(agent.ProgressiveStateAuthorizationDenied)}}}}}}})
	cr.emitEvent("done", map[string]any{"done": true})
	if !cr.Completion().Blocked {
		t.Fatal("done erased blocked result")
	}
}

func TestBotChatRuntimeAdapterSinkBeforePublish(t *testing.T) {
	cr := newChatRunner("session", "user", studioChatAppName, false)
	defer cr.cancel()
	var mu sync.Mutex
	persisted := 0
	cr.SetEventSink(func(context.Context, ChatEvent) error {
		mu.Lock()
		defer mu.Unlock()
		if len(cr.GetHistory()) != persisted {
			t.Error("event published before persistence")
		}
		persisted++
		return nil
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { cr.emitEvent("text", map[string]any{"text": "event"}) })
	}
	wg.Wait()
	if len(cr.GetHistory()) != 20 || persisted != 20 {
		t.Fatal("event loss")
	}
	cr.SetEventSink(func(context.Context, ChatEvent) error { return errors.New("no persistence") })
	cr.emitEvent("text", map[string]any{"text": "must not publish"})
	if len(cr.GetHistory()) != 20 || cr.Completion().Err == nil || cr.ctx.Err() == nil {
		t.Fatal("sink failure did not fail closed")
	}
}
