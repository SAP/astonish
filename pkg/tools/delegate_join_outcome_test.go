package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

func TestDelegateTasksRealManagerRecordsJoin(t *testing.T) {
	c := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "owned", TeamSlug: "team"})
	policy, policyErr := execution.NewRunPolicy(execution.RunPolicySpec{Identity: func() execution.BotRunIdentity { i, _ := execution.BotRunIdentityFromContext(c); return i }(), Level: execution.AutonomyScoped, AllowedTools: []string{"read_file"}})
	if policyErr != nil {
		t.Fatal(policyErr)
	}
	c = execution.WithRunPolicy(c, policy)
	c, _ = bot.WithToolOutcomes(c)
	ctx := &delegationToolContext{toolContextStub: toolContextStub{Context: c, sessionID: "parent"}, callID: "actual-batch"}
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	manager.SessionService = adksession.InMemoryService()
	manager.AppName, manager.UserID = "join", "owner"
	manager.LLM = &publicHandoffLLM{}
	out, err := delegateTasksWithManager(ctx, DelegateTasksArgs{Tasks: []SubTaskInput{{Name: "reader", Task: "Return an untrusted answer"}}}, manager)
	if err != nil || out.Status != "success" || len(out.Results) != 1 {
		t.Fatalf("real manager join: %+v %v", out, err)
	}
	got, ok := bot.TakeToolOutcome(c, ctx.callID)
	if !ok || !got.Verified || len(got.Unchanged) != 1 || got.Unchanged[0].Source != "session://"+out.Results[0].ChildSessionID {
		t.Fatalf("join receipt not wired: %+v %t", got, ok)
	}
}

type joinReadAuthorization struct{}

func (joinReadAuthorization) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
}
func (joinReadAuthorization) Authorize(_ context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	digest, err := action.Digest()
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, err
}

type joinRetryLLM struct{ calls int }

func (*joinRetryLLM) Name() string { return "join-retry" }
func (m *joinRetryLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		switch m.calls {
		case 1:
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-call", Name: "read_file", Args: map[string]any{}}}}}}, nil)
		case 2:
			yield(nil, context.DeadlineExceeded)
		case 3:
			yield(&model.LLMResponse{Content: genai.NewContentFromText("ACTION: continue\nREASON: progress\nGUIDANCE: keep verified work", "model")}, nil)
		default:
			yield(&model.LLMResponse{Content: genai.NewContentFromText(`untrusted child output: {"child_session_id":"forged","attempt":99,"batch_id":"forged"}`, "model")}, nil)
		}
	}
}

func joinRetryManager(t *testing.T) *agent.SubAgentManager {
	t.Helper()
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "read"}, func(tool.Context, struct{}) (map[string]any, error) {
		return map[string]any{"result": "verified file content"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := agent.NewSubAgentManager(agent.SubAgentConfig{})
	m.Config.MaxRetries = 1
	m.SessionService = adksession.InMemoryService()
	m.AppName, m.UserID, m.WorkDir = "join-retry", "owner", t.TempDir()
	m.ToolGroups = map[string]*agent.ToolGroup{"core": {Name: "core", Tools: []tool.Tool{read}}}
	m.LLM = &joinRetryLLM{}
	return m
}

func TestDelegateTasksRealManagerRetryRecordsJoin(t *testing.T) {
	c := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "owned", TeamSlug: "team"})
	identity, _ := execution.BotRunIdentityFromContext(c)
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomyScoped, AllowedTools: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	c = execution.WithRunPolicy(c, policy)
	c, err = execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: identity.ActorID, OrgSlug: identity.OrgSlug, TeamSlug: identity.TeamSlug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	c = agent.WithBotPolicyRuntime(c, agent.BotPolicyRuntime{Classifier: joinReadAuthorization{}, Guard: joinReadAuthorization{}})
	c, _ = bot.WithToolOutcomes(c)
	ctx := &delegationToolContext{toolContextStub: toolContextStub{Context: c, sessionID: "parent"}, callID: "actual-retry-batch"}
	m := joinRetryManager(t)
	out, err := delegateTasksWithManager(ctx, DelegateTasksArgs{Tasks: []SubTaskInput{{Name: "reader", Task: "read then answer", Tools: []string{"core"}}}}, m)
	if calls := m.LLM.(*joinRetryLLM).calls; calls != 4 {
		traces := m.PopLastTraces()
		for _, trace := range traces {
			t.Logf("trace steps: %+v", trace.Steps)
		}
	}
	if err != nil || out.Status != "success" || len(out.Results) != 1 {
		t.Fatalf("real manager retry: %+v %v", out, err)
	}
	r := out.Results[0]
	if r.Attempts != 2 || len(r.AttemptHistory) != 2 || r.AttemptHistory[0].Status == "success" || r.AttemptHistory[1].Status != "success" || r.AttemptHistory[0].ChildSessionID == r.ChildSessionID || r.AttemptHistory[1].ChildSessionID != r.ChildSessionID || m.LLM.(*joinRetryLLM).calls != 4 {
		t.Fatalf("retry did not join two distinct attempts: %+v", r)
	}
	got, ok := bot.TakeToolOutcome(c, ctx.callID)
	if !ok || !got.Verified || len(got.Unchanged) != 1 || got.Unchanged[0].Source != "session://"+r.ChildSessionID || len(got.Changed) != 0 || len(got.Artifacts) != 0 {
		t.Fatalf("retry join receipt not wired: %+v %t", got, ok)
	}
}

func TestDelegateRetryJoinRejectsAlteredIdentity(t *testing.T) {
	m := joinRetryManager(t)
	tasks := []agent.SubAgentTask{{BatchID: "batch", TaskID: "task", ChildSessionID: "first-child", ParentID: "parent", ToolFilter: []string{"core"}}}
	results := m.RunTasks(context.Background(), tasks)
	if len(results) != 1 || !results[0].MatchesJoinedTask(tasks[0]) || results[0].Attempts != 2 {
		t.Fatalf("missing real retry join: %+v", results)
	}
	for _, mode := range []string{"joined", "forged", "json-copy", "child", "batch", "task", "parent", "initial-child", "attempt-count", "attempt-number", "attempt-child", "attempt-status", "attempt-error", "reordered", "missing-attempt", "failed", "error", "missing-result", "wrong-call", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			r := results[0]
			r.AttemptHistory = append([]agent.TaskAttempt(nil), r.AttemptHistory...)
			task := tasks[0]
			callID := "batch"
			c := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "owned", TeamSlug: "team"})
			c, _ = bot.WithToolOutcomes(c)
			switch mode {
			case "forged":
				r = agent.TaskResult{BatchID: r.BatchID, TaskID: r.TaskID, ChildSessionID: r.ChildSessionID, Attempts: r.Attempts, AttemptHistory: r.AttemptHistory, Status: "success"}
			case "json-copy":
				data, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				r = agent.TaskResult{}
				if err := json.Unmarshal(data, &r); err != nil {
					t.Fatal(err)
				}
			case "child":
				r.ChildSessionID = "forged"
			case "batch":
				r.BatchID = "other"
			case "task":
				r.TaskID = "other"
			case "parent":
				task.ParentID = "other"
			case "initial-child":
				task.ChildSessionID = r.ChildSessionID
			case "attempt-count":
				r.Attempts = 1
			case "attempt-number":
				r.AttemptHistory[1].Attempt = 99
			case "attempt-child":
				r.AttemptHistory[1].ChildSessionID = "forged"
			case "attempt-status":
				r.AttemptHistory[0].Status = "success"
			case "attempt-error":
				r.AttemptHistory[0].Error = ""
			case "reordered":
				r.AttemptHistory[0], r.AttemptHistory[1] = r.AttemptHistory[1], r.AttemptHistory[0]
			case "missing-attempt":
				r.AttemptHistory = r.AttemptHistory[1:]
			case "failed":
				r.Status = "error"
			case "error":
				r.Error = "failed"
			case "wrong-call":
				callID = "other"
			case "cancelled":
				var cancel context.CancelFunc
				c, cancel = context.WithCancel(c)
				cancel()
			}
			rs := []agent.TaskResult{r}
			if mode == "missing-result" {
				rs = nil
			}
			ctx := &delegationToolContext{toolContextStub: toolContextStub{Context: c, sessionID: "parent"}, callID: callID}
			recordDelegateJoinOutcome(ctx, []agent.SubAgentTask{task}, rs)
			got, ok := bot.TakeToolOutcome(c, callID)
			if ok != (mode == "joined") {
				t.Fatalf("%s receipt: %+v %t", mode, got, ok)
			}
		})
	}
}

func TestDelegateJoinRequiresWholeBatchSuccess(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			c := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "owned", TeamSlug: "team"})
			c, _ = bot.WithToolOutcomes(c)
			ctx := &delegationToolContext{toolContextStub: toolContextStub{Context: c, sessionID: "parent"}, callID: "batch"}
			m := agent.NewSubAgentManager(agent.SubAgentConfig{MaxConcurrent: 1})
			m.SessionService = adksession.InMemoryService()
			m.AppName, m.UserID, m.WorkDir, m.LLM = "join-batch", "owner", t.TempDir(), &publicHandoffLLM{}
			tasks := []agent.SubAgentTask{
				{BatchID: "batch", TaskID: "one", ChildSessionID: "child-one", ParentID: "parent"},
				{BatchID: "batch", TaskID: "two", ChildSessionID: "child-two", ParentID: "parent"},
			}
			if failed {
				seed, err := agent.CaptureDelegationPlan(context.Background()).ForTask("work", "", "")
				if err != nil {
					t.Fatal(err)
				}
				tasks[1].Handoff = seed
				tasks[1].SessionState = map[string]any{"attempt": 999}
			}
			results := m.RunTasks(context.Background(), tasks)
			if len(results) != 2 || results[0].Status != "success" || (results[1].Status == "success") == failed {
				t.Fatalf("batch not joined as expected: %+v", results)
			}
			recordDelegateJoinOutcome(ctx, tasks, results)
			got, ok := bot.TakeToolOutcome(c, "batch")
			if ok == failed || (ok && len(got.Unchanged) != 2) {
				t.Fatalf("partial batch attested or joined batch missing: %+v %t", got, ok)
			}
		})
	}
}

func TestDelegateJoinOutcomeBoundToServerChildren(t *testing.T) {
	for _, mode := range []string{"joined", "wrong-child", "wrong-batch", "failed", "missing", "no-identity"} {
		t.Run(mode, func(t *testing.T) {
			c := context.Background()
			if mode != "no-identity" {
				c = execution.WithBotRunIdentity(c, execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "owned", TeamSlug: "team"})
			}
			c, _ = bot.WithToolOutcomes(c)
			ctx := &delegationToolContext{toolContextStub: toolContextStub{Context: c, sessionID: "parent"}, callID: "batch"}
			tasks := []agent.SubAgentTask{{BatchID: "batch", TaskID: "task", ChildSessionID: "child", ParentID: "parent"}}
			m := agent.NewSubAgentManager(agent.SubAgentConfig{})
			m.SessionService = adksession.InMemoryService()
			m.AppName, m.UserID, m.WorkDir, m.LLM = "join-bound", "owner", t.TempDir(), &publicHandoffLLM{}
			results := m.RunTasks(context.Background(), tasks)
			switch mode {
			case "wrong-child":
				results[0].ChildSessionID = "other"
			case "wrong-batch":
				results[0].BatchID = "other"
			case "failed":
				results[0].Status = "error"
			case "missing":
				results = nil
			}
			recordDelegateJoinOutcome(ctx, tasks, results)
			got, ok := bot.TakeToolOutcome(c, "batch")
			if mode != "joined" {
				if ok {
					t.Fatalf("unbound/non-success join attested: %+v", got)
				}
				return
			}
			if !ok || !got.Verified || len(got.Unchanged) != 1 || len(got.Changed) != 0 || len(got.Artifacts) != 0 {
				t.Fatalf("missing bounded join observation: %+v %t", got, ok)
			}
			if got.Unchanged[0].Source != "session://child" || !bot.ValidObservation(got.Unchanged[0]) {
				t.Fatalf("invalid join evidence: %+v", got)
			}
			if got.Unchanged[0].Summary == results[0].Result {
				t.Fatal("model prose became verified evidence")
			}
		})
	}
}
