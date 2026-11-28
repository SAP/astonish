package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestCodePlanPermissionLeaseLifetime(t *testing.T) {
	for _, mode := range []CodePlanExecutionMode{CodePlanRunToCompletion, CodePlanReviewEachPhase} {
		t.Run(string(mode), func(t *testing.T) {
			s := codeExecutionFixture(mode)
			s.Document.Lifecycle = ""
			r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { return 0, "verified", nil }})
			p := NewSessionAuthPolicy(t.TempDir())
			if err := p.GrantCodePlanFullAccess("owner", r); err == nil {
				t.Fatal("unapproved grant")
			}
			if err := r.ApproveCodeExecution(s.PlanID, s.Revision, mode); err != nil {
				t.Fatal(err)
			}
			if err := p.GrantCodePlanFullAccess("owner", r); err != nil {
				t.Fatal(err)
			}
			base := WithPlanRuntime(WithPromptOverrides(context.Background(), &PromptOverrides{ApprovedPlanExecution: true}), r)
			ctx := p.BindCodePlanExecution(base, "owner", r)
			if !p.CodePlanFullAccessAllowed(ctx, "owner") || p.CodePlanFullAccessAllowed(ctx, "other") || p.CodePlanFullAccessAllowed(base, "owner") {
				t.Fatal("binding scope")
			}
			for _, po := range []*PromptOverrides{{PlanMode: true}, {GraphPlanMode: true}, {AskMode: true}, {}} {
				ordinary := p.BindCodePlanExecution(WithPromptOverrides(context.Background(), po), "owner", r)
				if p.CodePlanFullAccessAllowed(ordinary, "owner") {
					t.Fatal("ordinary turn gained authority")
				}
			}
			p.ResetForNewTurn()
			if !p.CodePlanFullAccessAllowed(ctx, "owner") {
				t.Fatal("turn reset revoked lease")
			}
			if NewSessionAuthPolicy(p.Root()).CodePlanFullAccessAllowed(ctx, "owner") {
				t.Fatal("new process/session inherited lease")
			}
			if p.AllToolsAllowedForSession() || p.ToolAuthorized("shell_command") || p.PathAllowed("/outside/secret") {
				t.Fatal("permanent grant leaked")
			}
			for _, step := range r.Snapshot().Steps {
				if got := r.UpdateStep(step.Name, "complete"); got.Code != PlanStepOK {
					t.Fatal(got)
				}
				if !p.CodePlanFullAccessAllowed(ctx, "owner") {
					t.Fatal("phase/review revoked lease")
				}
				if err := r.ContinueCodeReview(); err != nil {
					t.Fatal(err)
				}
			}
			if got := r.Complete("verified", ""); got.Code != PlanCompletionOK {
				t.Fatal(got)
			}
			if p.CodePlanFullAccessAllowed(ctx, "owner") {
				t.Fatal("completion retained lease")
			}
			if !ctx.Value(codePlanInvocationKey{}).(codePlanInvocation).lease.revoked.Load() {
				t.Fatal("completion did not revoke reference")
			}
		})
	}
}

func TestCodePlanPermissionApprovalAtomic(t *testing.T) {
	s := codeExecutionFixture(CodePlanRunToCompletion)
	s.Document.Lifecycle = ""
	fail := true
	r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: true, CommitSnapshot: func(PlanSnapshot) error {
		if fail {
			return errors.New("disk failed")
		}
		return nil
	}})
	p := NewSessionAuthPolicy(t.TempDir())
	if err := r.ApproveCodeExecution("other", s.Revision, CodePlanRunToCompletion); PlanRuntimeErrorCode(err) != PlanRuntimeStale {
		t.Fatal(err)
	}
	if err := r.ApproveCodeExecution(s.PlanID, s.Revision+1, CodePlanRunToCompletion); PlanRuntimeErrorCode(err) != PlanRuntimeStale {
		t.Fatal(err)
	}
	if err := r.ApproveCodeExecution(s.PlanID, s.Revision, CodePlanRunToCompletion); PlanRuntimeErrorCode(err) != PlanRuntimePersistFailed {
		t.Fatal(err)
	}
	if r.CodeExecutionActive() || p.GrantCodePlanFullAccess("owner", r) == nil {
		t.Fatal("failed approval granted authority")
	}
	fail = false
	if err := r.ApproveCodeExecution(s.PlanID, s.Revision, CodePlanRunToCompletion); err != nil {
		t.Fatal(err)
	}
	if err := p.GrantCodePlanFullAccess("owner", r); err != nil {
		t.Fatal(err)
	}
	if err := r.ApproveCodeExecution(s.PlanID, s.Revision, CodePlanRunToCompletion); PlanRuntimeErrorCode(err) != PlanRuntimeStale {
		t.Fatal("replayed approval", err)
	}
	ctx := p.BindCodePlanExecution(WithPromptOverrides(context.Background(), &PromptOverrides{ApprovedPlanExecution: true}), "owner", r)
	next := r.Snapshot().Definition().Steps
	next[0].Summary = "corrected summary"
	if _, err := r.Revise(PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "clarify", Steps: next}); err != nil {
		t.Fatal(err)
	}
	if !p.CodePlanFullAccessAllowed(ctx, "owner") {
		t.Fatal("same plan revision lost lease")
	}
	next[0].Summary = "planning summary"
	if _, err := r.ReviseForPlanning(PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: r.Snapshot().Revision, Reason: "replan", Steps: next}); err != nil {
		t.Fatal(err)
	}
	if p.CodePlanFullAccessAllowed(ctx, "owner") {
		t.Fatal("planning retained lease")
	}
	if err := r.SetApproved(); err != nil {
		t.Fatal(err)
	}
	if p.CodePlanFullAccessAllowed(ctx, "owner") {
		t.Fatal("legacy reapproval resurrected lease")
	}
}

func TestCodePlanCompletedAuthorityAndFailedPersistence(t *testing.T) {
	failCommit := false
	r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(CodePlanRunToCompletion), PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { return 0, "passed", nil }, CommitSnapshot: func(PlanSnapshot) error {
		if failCommit {
			return errors.New("disk unavailable")
		}
		return nil
	}})
	loop := newCodePlanLoop(WithPlanRuntime(context.Background(), r))
	for _, step := range r.Snapshot().Steps {
		if got := r.UpdateStep(step.Name, "complete"); got.Code != PlanStepOK {
			t.Fatal(got)
		}
	}
	if !r.CodeExecutionActive() || r.Snapshot().IsFullyAccepted() {
		t.Fatal("checkboxes alone ended execution")
	}
	failCommit = true
	if got := r.Complete("verified", ""); got.Code == PlanCompletionOK {
		t.Fatal("failed persistence accepted")
	}
	if !r.CodeExecutionActive() {
		t.Fatal("failed completion ended execution")
	}
	failCommit = false
	if got := r.Complete("verified", ""); got.Code != PlanCompletionOK {
		t.Fatal(got)
	}
	if !r.CodeExecutionEnabled() || r.CodeExecutionActive() || loop.active() || !r.Snapshot().IsFullyAccepted() || !r.PlanState().IsFullyAccepted() {
		t.Fatal("completion authority mismatch")
	}
	before := r.Snapshot()
	if name, err := r.RegisterDelegate("one", "unrelated"); name != "" || err != nil {
		t.Fatal(name, err)
	}
	if name, err := r.FinishDelegate("one", "late"); name != "" || err != nil {
		t.Fatal(name, err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) || len(r.plan.taskRegistry) != 0 {
		t.Fatal("late callback changed history")
	}
	if got := r.UpdateStep("one", "running"); got.Code != PlanRuntimeCompleted {
		t.Fatal(got)
	}
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("\nCode execution runtime: "+CodePlanRecoveryGuidance, "system")}}
	if _, err := loop.beforeModel(nil, req); err != nil {
		t.Fatal(err)
	}
	if len(req.Config.SystemInstruction.Parts) != 0 {
		t.Fatal("same-invocation guidance survived")
	}
}

func codeExecutionFixture(mode CodePlanExecutionMode) PlanSnapshot {
	s := runtimeFixture()
	s.Document.CodeExecutionMode = mode
	for i := range s.Steps {
		s.Steps[i].Status = "pending"
		s.Steps[i].Evidence = ""
	}
	return s
}

func TestCodePlanBatchAdmissionIsAtomicAndRecovers(t *testing.T) {
	calls := 0
	r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(CodePlanRunToCompletion), PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { calls++; return 0, "verified", nil }})
	tasks := []SubAgentTask{{Name: "same", TaskID: "first", PlanStep: "one"}, {Name: "same", TaskID: "second", PlanStep: "two"}}
	before := r.Snapshot()
	if err := r.AdmitCodeDelegates(tasks); err == nil {
		t.Fatal("dependent batch admitted")
	}
	if !reflect.DeepEqual(before, r.Snapshot()) || calls != 0 || len(r.plan.taskRegistry) != 0 {
		t.Fatal("blocked batch performed work")
	}
	recovery := r.CodeRecovery("blocked", "prerequisites")
	if recovery.Code != "blocked" || len(recovery.Blockers) != 2 || recovery.Recovery == "" || !reflect.DeepEqual(before, recovery.Snapshot) {
		t.Fatalf("recovery: %+v", recovery)
	}
	if got := r.UpdateStep("one", "running"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if got := r.UpdateStep("one", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if err := r.AdmitCodeDelegates(tasks[1:]); err != nil {
		t.Fatal(err)
	}
	if got := r.UpdateStep("two", "complete"); got.Code != PlanRuntimeBusy || calls != 1 {
		t.Fatalf("verified before child finish: %+v calls=%d", got, calls)
	}
	r.FinishCodeDelegates(tasks[1:])
	if r.Snapshot().Steps[1].Status != "running" || calls != 1 {
		t.Fatal("child auto-completed phase")
	}
	if got := r.UpdateStep("two", "complete"); got.Code != PlanStepOK || calls != 2 {
		t.Fatalf("explicit verification: %+v calls=%d", got, calls)
	}
	if got := r.Complete("both phases work", ""); got.Code != PlanCompletionOK {
		t.Fatal(got)
	}
}

func TestCodePlanFailedPhaseRetryAndDelegateIDs(t *testing.T) {
	calls := 0
	r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(CodePlanRunToCompletion), PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) {
		calls++
		if calls == 1 {
			return 1, "broken", nil
		}
		return 0, "fixed", nil
	}})
	if got := r.UpdateStep("one", "complete"); got.Code != PlanStepVerifyFailed || got.Applied != "failed" {
		t.Fatal(got)
	}
	if got := r.UpdateStep("one", "running"); got.Code != PlanStepOK || r.Snapshot().Steps[0].Evidence != "" {
		t.Fatalf("failed-to-running retry did not clear old evidence: %+v", got)
	}
	tasks := []SubAgentTask{{Name: "duplicate", TaskID: "id-a", PlanStep: "one"}, {Name: "duplicate", TaskID: "id-b", PlanStep: "one"}}
	if err := r.AdmitCodeDelegates(tasks); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Steps[0].Status != "running" {
		t.Fatal("failed phase not restarted")
	}
	if name, err := r.RegisterDelegate("one", "duplicate"); err != nil || name != "" {
		t.Fatalf("legacy register: %q %v", name, err)
	}
	if name, err := r.FinishDelegate("one", "duplicate"); err != nil || name != "" {
		t.Fatalf("legacy callback attempted whole-phase verification: %q %v", name, err)
	}
	r.FinishCodeDelegates(tasks[:1])
	r.FinishCodeDelegates(tasks[:1]) // Duplicate finish must not release its sibling.
	if got := r.UpdateStep("one", "complete"); got.Code != PlanRuntimeBusy || calls != 1 {
		t.Fatalf("IDs collapsed: %+v calls=%d", got, calls)
	}
	r.FinishCodeDelegates(tasks[1:])
	if calls != 1 || r.Snapshot().Steps[0].Status != "running" {
		t.Fatal("narrow child verified whole phase")
	}
	if got := r.UpdateStep("one", "complete"); got.Code != PlanStepOK || calls != 2 {
		t.Fatalf("retry: %+v calls=%d", got, calls)
	}
	if !strings.Contains(r.Snapshot().Steps[0].Evidence, "fixed") {
		t.Fatal("retry did not replace failed evidence")
	}
}

func TestCodePlanReviewCheckpointBlocksToolsAndPhases(t *testing.T) {
	r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(CodePlanReviewEachPhase), PlanRuntimeOptions{CodeExecution: true, Verify: func(string) (int, string, error) { return 0, "review evidence", nil }})
	loop := newCodePlanLoop(WithPlanRuntime(context.Background(), r))
	if got := r.UpdateStep("one", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	before := r.Snapshot()
	if !r.CodeReviewPending() || !strings.Contains(loop.checkpoint(), "review evidence") {
		t.Fatal("missing review checkpoint/evidence")
	}
	if out, err := loop.beforeTool(nil, nil, nil); err != nil || out["code"] != CodePlanReviewRequired {
		t.Fatalf("tool not blocked: %v %v", out, err)
	}
	if got := r.UpdateStep("two", "running"); got.Code != CodePlanReviewRequired {
		t.Fatal(got)
	}
	if err := r.AdmitCodeDelegates([]SubAgentTask{{TaskID: "new", PlanStep: "two"}}); PlanRuntimeErrorCode(err) != CodePlanReviewRequired {
		t.Fatal(err)
	}
	if resume, err := loop.resume(context.Background()); resume || err != nil {
		t.Fatalf("review resumed: %v %v", resume, err)
	}
	if !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal("review boundary mutated state")
	}
	if err := r.ContinueCodeReview(); err != nil {
		t.Fatal(err)
	}
	after := r.Snapshot()
	if r.CodeReviewPending() || after.Document.CodeExecutionMode != CodePlanReviewEachPhase || after.Steps[0].Evidence != before.Steps[0].Evidence {
		t.Fatal("continuation changed evidence or mode")
	}
	if out, err := loop.beforeTool(nil, nil, nil); out != nil || err != nil {
		t.Fatalf("tool still blocked: %v %v", out, err)
	}
	if got := r.UpdateStep("two", "running"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if got := r.UpdateStep("two", "complete"); got.Code != PlanStepOK {
		t.Fatal(got)
	}
	if r.CodeReviewPending() {
		t.Fatal("final phase prevented completion")
	}
	if got := r.Complete("all reviewed", ""); got.Code != PlanCompletionOK {
		t.Fatal(got)
	}
}

func TestCodePlanTrustedModeRoundTripAndRevision(t *testing.T) {
	for _, mode := range []CodePlanExecutionMode{CodePlanRunToCompletion, CodePlanReviewEachPhase} {
		t.Run(string(mode), func(t *testing.T) {
			s := codeExecutionFixture(mode)
			s.Document.CodeReviewPending = "one"
			r := NewPlanRuntimeFromSnapshot(s, PlanRuntimeOptions{CodeExecution: true})
			md := renderPlanMarkdownWithDoc(r.plan.goal, r.plan.doc, r.plan.steps)
			doc, goal, steps, err := ParsePlanDocument(md)
			if err != nil {
				t.Fatal(err)
			}
			restored := NewPlanRuntime(NewPlanState(goal, doc, steps), PlanRuntimeOptions{CodeExecution: true})
			if got := restored.Snapshot(); got.Document.CodeExecutionMode != mode || got.Document.CodeReviewPending != "one" {
				t.Fatalf("markdown lost trusted mode: %+v", got.Document)
			}
			raw, err := json.Marshal(r.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip PlanSnapshot
			if err = json.Unmarshal(raw, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if roundTrip.Document.CodeExecutionMode != mode || roundTrip.Document.CodeReviewPending != "one" {
				t.Fatal("snapshot lost trusted fields")
			}
			req := revisionRequest(r.Snapshot())
			document := s.Document.Definition()
			document.Context = "new implementation findings"
			req.Document = &document
			if _, err := BuildPlanRevisionCandidate(r.Snapshot(), req, t.TempDir(), ""); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Revise(req); err != nil {
				t.Fatal(err)
			}
			if got := r.Snapshot(); got.Document.CodeExecutionMode != mode || got.Document.CodeReviewPending != "one" {
				t.Fatalf("model revision changed trusted approval: %+v", got.Document)
			}
		})
	}
}

func TestCodePlanModeRequiresTrustedLocalOptIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local bool
		mode  CodePlanExecutionMode
	}{{"remote", false, ""}, {"local-no-approval", true, ""}, {"remote-with-metadata", false, CodePlanReviewEachPhase}} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(tc.mode), PlanRuntimeOptions{CodeExecution: tc.local})
			if r.CodeExecutionEnabled() {
				t.Fatal("unapproved or remote runtime enabled Code execution")
			}
			loop := newCodePlanLoop(WithPlanRuntime(context.Background(), r))
			req := &model.LLMRequest{}
			if _, err := loop.beforeModel(nil, req); err != nil || req.Config != nil {
				t.Fatal("inactive loop changed request")
			}
			if resume, err := loop.resume(context.Background()); resume || err != nil {
				t.Fatal("inactive loop resumed")
			}
			if name, err := r.AdvanceOnToolStart(); err != nil || name != "one" {
				t.Fatalf("legacy autostart changed: %q %v", name, err)
			}
		})
	}
	if newCodePlanLoop(context.Background()).active() || newCodePlanLoop(WithPlanRuntime(context.Background(), nil)).active() {
		t.Fatal("unbound loop active")
	}
}

func TestCodePlanModePersistenceFailureIsAtomic(t *testing.T) {
	r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(""), PlanRuntimeOptions{CodeExecution: true, CommitSnapshot: func(PlanSnapshot) error { return errors.New("disk unavailable") }})
	before := r.Snapshot()
	if err := r.SetCodeExecutionMode("model-invented"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := r.SetCodeExecutionMode(CodePlanRunToCompletion); err == nil {
		t.Fatal("failed commit accepted")
	}
	if !reflect.DeepEqual(before, r.Snapshot()) {
		t.Fatal("failed mode commit mutated snapshot")
	}
}

func TestCodePlanLoopInjectionAndStopBoundaries(t *testing.T) {
	r := NewPlanRuntimeFromSnapshot(codeExecutionFixture(CodePlanRunToCompletion), PlanRuntimeOptions{CodeExecution: true})
	original := &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("original", "system")}
	req := &model.LLMRequest{Config: original}
	loop := newCodePlanLoop(WithPlanRuntime(context.Background(), r))
	if _, err := loop.beforeModel(nil, req); err != nil {
		t.Fatal(err)
	}
	if req.Config == original || len(original.SystemInstruction.Parts) != 1 || len(req.Config.SystemInstruction.Parts) != 2 || !strings.Contains(req.Config.SystemInstruction.Parts[1].Text, r.Snapshot().PlanID) {
		t.Fatal("snapshot injection mutated shared config or omitted state")
	}
	for _, key := range []string{"awaiting_approval", "requires_user_input", "awaiting_user_input", "human_handoff"} {
		t.Run(key, func(t *testing.T) {
			l := newCodePlanLoop(WithPlanRuntime(context.Background(), r))
			l.observe(&session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "tool", Response: map[string]any{key: true}}}}}}})
			if yes, err := l.resume(context.Background()); yes || err != nil {
				t.Fatalf("continued across user input: %v %v", yes, err)
			}
		})
	}
	loop.observe(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Phase progress is ready.", "model")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if yes, err := loop.resume(ctx); yes || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v %v", yes, err)
	}
	loop.stagnant = 2
	loop.observe(&session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Shall I continue?", "model")}})
	if yes, err := loop.resume(context.Background()); !yes || err != nil {
		t.Fatalf("routine narration stopped autonomous work: %v %v", yes, err)
	}
}
