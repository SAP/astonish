package tools

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
)

func codeDelegateFixture(mode agent.CodePlanExecutionMode) agent.PlanSnapshot {
	return agent.PlanSnapshot{PlanID: "code-plan", Revision: 1, Sequence: 1, Goal: "execute approved work", Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved, CodeExecutionMode: mode, Context: "context", WhatNotToDo: "boundaries", Verification: "acceptance"}, Steps: []agent.PlanStepInfo{
		{Name: "first", Description: "prerequisite", Details: "implement prerequisite", Outcome: "prerequisite works", Verify: "verify first", VerifyKind: agent.VerifyKindUnit, Status: "pending"},
		{Name: "second", Description: "dependent", Details: "implement dependent", Outcome: "dependent works", Verify: "verify second", VerifyKind: agent.VerifyKindUnit, Status: "pending"},
	}}
}

func TestCodePlanDelegateBlockedBatchDispatchesNothingThenRecovers(t *testing.T) {
	verifies := 0
	runtime := agent.NewPlanRuntimeFromSnapshot(codeDelegateFixture(agent.CodePlanRunToCompletion), agent.PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { verifies++; return 0, "passed", nil }})
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	manager.SessionService = adksession.InMemoryService()
	manager.AppName, manager.UserID = "code-delegate-test", "user"
	llm := &publicHandoffLLM{}
	manager.LLM = llm
	var mu sync.Mutex
	var events []agent.SubTaskProgressEvent
	manager.SubTaskProgress = func(e agent.SubTaskProgressEvent) { mu.Lock(); defer mu.Unlock(); events = append(events, e) }
	ctx := &delegationToolContext{toolContextStub: toolContextStub{Context: agent.WithPlanRuntime(context.Background(), runtime), sessionID: "parent"}, callID: "blocked-batch"}
	before := runtime.Snapshot()
	out, err := delegateTasksWithManager(ctx, DelegateTasksArgs{Tasks: []SubTaskInput{{Name: "worker", Task: "narrow prerequisite", PlanStep: "first"}, {Name: "worker", Task: "narrow dependent", PlanStep: "second"}}}, manager)
	if err != nil || out.Status != "blocked" || out.PlanRecovery == nil {
		t.Fatalf("normal recovery response: %+v %v", out, err)
	}
	if llm.calls != 0 || verifies != 0 || !reflect.DeepEqual(before, runtime.Snapshot()) {
		t.Fatal("blocked batch dispatched work or changed phases")
	}
	if out.BatchID != "blocked-batch" || len(events) != 1 || events[0].Type != string(agent.DelegationComplete) || events[0].BatchID != out.BatchID || events[0].Status != "blocked" {
		t.Fatalf("blocked batch did not clear busy indicator: %+v", events)
	}
	if got := runtime.UpdateStep("first", "complete"); got.Code != agent.PlanStepOK {
		t.Fatal(got)
	}
	events = nil
	ctx.callID = "accepted-batch"
	out, err = delegateTasksWithManager(ctx, DelegateTasksArgs{Tasks: []SubTaskInput{{Name: "worker", Task: "only a narrow slice of the second phase", PlanStep: "second"}}}, manager)
	if err != nil || out.Status != "success" || len(out.Results) != 1 || llm.calls != 1 {
		t.Fatalf("retry did not dispatch: %+v %v calls=%d", out, err, llm.calls)
	}
	result := out.Results[0]
	if result.BatchID != ctx.callID || result.TaskID == "" || result.ChildSessionID == "" {
		t.Fatalf("missing identities: %+v", result)
	}
	if out.PlanRecovery == nil || out.PlanRecovery.Snapshot.Steps[1].Status != "running" || verifies != 1 {
		t.Fatal("narrow child auto-verified phase or omitted recovery")
	}
	var start, complete *agent.SubTaskProgressEvent
	for i := range events {
		e := &events[i]
		if e.Type == string(agent.DelegationStart) {
			start = e
		}
		if e.Type == string(agent.DelegationComplete) {
			complete = e
		}
	}
	if start == nil || complete == nil || len(start.Tasks) != 1 || start.Tasks[0].TaskID != result.TaskID || start.Tasks[0].ChildSessionID != result.ChildSessionID || complete.BatchID != ctx.callID || complete.Sequence <= start.Sequence {
		t.Fatalf("uncorrelated progress: %+v", events)
	}
	if got := runtime.UpdateStep("second", "complete"); got.Code != agent.PlanStepOK || verifies != 2 {
		t.Fatalf("batch left registry busy: %+v verifies=%d", got, verifies)
	}
}

type codeDelegateFailLLM struct{}

func (*codeDelegateFailLLM) Name() string { return "code-delegate-failure" }
func (*codeDelegateFailLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, errors.New("deterministic provider failure"))
	}
}

func TestCodePlanDelegateFailureReleasesBatchRegistry(t *testing.T) {
	runtime := agent.NewPlanRuntimeFromSnapshot(codeDelegateFixture(agent.CodePlanRunToCompletion), agent.PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { return 0, "parent checked failure recovery", nil }})
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	manager.SessionService = adksession.InMemoryService()
	manager.AppName, manager.UserID = "failure-delegate-test", "user"
	manager.LLM = &codeDelegateFailLLM{}
	var events []agent.SubTaskProgressEvent
	manager.SubTaskProgress = func(e agent.SubTaskProgressEvent) { events = append(events, e) }
	ctx := &delegationToolContext{toolContextStub: toolContextStub{Context: agent.WithPlanRuntime(context.Background(), runtime), sessionID: "parent"}, callID: "failed-batch"}
	out, err := delegateTasksWithManager(ctx, DelegateTasksArgs{Tasks: []SubTaskInput{{Name: "worker", Task: "narrow slice", PlanStep: "first"}}}, manager)
	if err != nil || out.Status != "error" || out.PlanRecovery == nil {
		t.Fatalf("child failure response: %+v %v", out, err)
	}
	if len(events) == 0 || events[len(events)-1].Type != string(agent.DelegationComplete) || events[len(events)-1].BatchID != ctx.callID {
		t.Fatalf("failure left busy event: %+v", events)
	}
	if runtime.Snapshot().Steps[0].Status != "running" {
		t.Fatal("child failure incorrectly completed phase")
	}
	if got := runtime.UpdateStep("first", "complete"); got.Code != agent.PlanStepOK {
		t.Fatalf("failure leaked delegate registry: %+v", got)
	}
}

func TestCodePlanDelegateAbsentModePreservesLegacy(t *testing.T) {
	runtime := agent.NewPlanRuntimeFromSnapshot(codeDelegateFixture(""), agent.PlanRuntimeOptions{CodeExecution: true})
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	manager.SessionService = adksession.InMemoryService()
	manager.AppName, manager.UserID = "legacy-delegate-test", "user"
	llm := &publicHandoffLLM{}
	manager.LLM = llm
	ctx := &toolContextStub{Context: agent.WithPlanRuntime(context.Background(), runtime), sessionID: "parent"}
	before := runtime.Snapshot()
	out, err := delegateTasksWithManager(ctx, DelegateTasksArgs{Tasks: []SubTaskInput{{Name: "worker", Task: "legacy dependent work", PlanStep: "second"}}}, manager)
	if err != nil || out.Status != "success" || out.PlanRecovery != nil || llm.calls != 1 {
		t.Fatalf("legacy delegation changed: %+v %v", out, err)
	}
	if !reflect.DeepEqual(before, runtime.Snapshot()) {
		t.Fatal("inactive Code admission mutated legacy plan")
	}
}

func TestCodePlanDelegateReviewCheckpointDispatchesNothing(t *testing.T) {
	s := codeDelegateFixture(agent.CodePlanReviewEachPhase)
	s.Steps[0].Status = "complete"
	s.Steps[0].Evidence = "exit 0"
	s.Document.CodeReviewPending = "first"
	runtime := agent.NewPlanRuntimeFromSnapshot(s, agent.PlanRuntimeOptions{CodeExecution: true})
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	llm := &publicHandoffLLM{}
	manager.LLM = llm
	var events []agent.SubTaskProgressEvent
	manager.SubTaskProgress = func(e agent.SubTaskProgressEvent) { events = append(events, e) }
	ctx := &toolContextStub{Context: agent.WithPlanRuntime(context.Background(), runtime), sessionID: "parent"}
	out, err := delegateTasksWithManager(ctx, DelegateTasksArgs{Tasks: []SubTaskInput{{Name: "worker", Task: "next phase", PlanStep: "second"}}}, manager)
	if err != nil || out.Status != "blocked" || out.PlanRecovery == nil || llm.calls != 0 {
		t.Fatalf("review dispatched work: %+v %v", out, err)
	}
	if len(events) != 1 || events[0].Type != string(agent.DelegationComplete) {
		t.Fatalf("review batch left busy: %+v", events)
	}
}
