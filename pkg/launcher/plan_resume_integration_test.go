package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/tools"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/tool"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// TestPlanResumesAfterCompactionAndRestart drives the full user-visible chain
// the durability bug broke: approve → compact → restart → resume → complete.
func TestPlanResumesAfterCompactionAndRestart(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	id := seedSession(t, b, "implement the plan")
	planPath := b.planFilePath(id)

	// --- ChatAgent #1: announce and approve ---
	c1 := &agent.ChatAgent{}
	c1.SetPlanFilePath(planPath)
	plan := agent.NewPlanState("Durable plans", agent.PlanDocumentInfo{}, []agent.PlanStepInfo{
		{Name: "phase-one", Description: "first", Verify: "true", VerifyKind: "unit", Status: "complete", Evidence: "exit 0; ok"},
		{Name: "phase-two", Description: "second", Verify: "true", VerifyKind: "unit"},
	})
	if !c1.TrySetActivePlan(plan) {
		t.Fatal("announce should be accepted")
	}
	c1.MarkActivePlanApproved()

	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(data), "## Status") {
		t.Fatalf("approval not persisted to PLAN.md:\n%s", data)
	}

	// --- Session records the approval the way RecordPlanDecision does ---
	sess := getSession(t, b, id)
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:        "plan-decision-approved",
		Author:    "system",
		Timestamp: time.Now(),
		Actions: adksession.EventActions{StateDelta: map[string]any{
			planLifecycleStateKey: string(events.PlanApproved),
		}},
	}); err != nil {
		t.Fatalf("append plan decision: %v", err)
	}

	// --- Simulate compaction ---
	compacted := []*adksession.Event{{
		ID:          "summary",
		Author:      "model",
		LLMResponse: modelTextResponse("[Context Summary — earlier messages compacted]"),
	}}
	if _, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), id, compacted); err != nil {
		t.Fatalf("ArchiveAndReplaceEvents: %v", err)
	}

	// --- Simulate restart: fresh ChatAgent, nothing in memory ---
	c2 := &agent.ChatAgent{}
	c2.SetPlanFilePath(planPath)
	if c2.IsActivePlanApproved() {
		t.Fatal("fresh agent must not start sealed")
	}

	if !b.shouldContinueApprovedPlan(ctx, id, planPath, c2) {
		t.Fatal("restarted session must keep executing the approved plan")
	}

	if err := c2.RestoreApprovedPlan(); err != nil {
		t.Fatalf("RestoreApprovedPlan: %v", err)
	}
	if !c2.IsActivePlanApproved() {
		t.Fatal("restored plan must be sealed")
	}
	restored := c2.GetActivePlan()
	if restored == nil {
		t.Fatal("restored plan is nil")
	}
	_, steps := restored.SnapshotInfo()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(steps))
	}
	if steps[0].Status != "complete" || !strings.Contains(steps[0].Evidence, "exit 0") {
		t.Fatalf("phase one not resumed as complete with evidence: %+v", steps[0])
	}
	if steps[1].Status != "pending" {
		t.Fatalf("phase two status = %q, want pending (resume, not restart)", steps[1].Status)
	}

	// --- Finish the interrupted work ---
	restored.SetStepStatus("phase-two", "complete")
	res := c2.AnnounceCompletion("plans survive restart and compaction", "")
	if res.Code != agent.PlanCompletionOK {
		t.Fatalf("AnnounceCompletion code = %q (%s), want ok", res.Code, res.Message)
	}
	final, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(final), "## Results") {
		t.Fatalf("PLAN.md has no Results section after completion:\n%s", final)
	}
}

// TestSessionStateSurvivesCompactionForRoutingBadges guards the positional
// matching contract loadHistory depends on: routing events must survive
// compaction, in order.
func TestSessionStateSurvivesCompactionForRoutingBadges(t *testing.T) {
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	ctx := context.Background()
	id := seedSession(t, b, "route me")
	sess := getSession(t, b, id)

	appendState := func(evID, tier, model string) {
		t.Helper()
		if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
			ID:        evID,
			Author:    "system",
			Timestamp: time.Now(),
			Actions: adksession.EventActions{StateDelta: map[string]any{
				routingInfoStateKey: map[string]any{"tier": tier, "model": model},
			}},
		}); err != nil {
			t.Fatalf("append routing event: %v", err)
		}
	}
	appendState("route-1", "strong", "m1")
	if err := b.sessionSvc.AppendEvent(ctx, sess, &adksession.Event{
		ID:          "mid",
		Author:      "model",
		LLMResponse: modelTextResponse("some answer"),
	}); err != nil {
		t.Fatalf("append content event: %v", err)
	}
	appendState("route-2", "weak", "m2")

	compacted := []*adksession.Event{{
		ID:          "summary",
		Author:      "model",
		LLMResponse: modelTextResponse("[Context Summary]"),
	}}
	if _, err := b.fileStore.ArchiveAndReplaceEvents(codeAppName, b.effectiveUserID(), id, compacted); err != nil {
		t.Fatalf("ArchiveAndReplaceEvents: %v", err)
	}

	reloaded := getSession(t, b, id)
	var routeIDs []string
	for ev := range reloaded.Events().All() {
		if len(ev.Actions.StateDelta) > 0 && ev.LLMResponse.Content == nil {
			routeIDs = append(routeIDs, ev.ID)
		}
	}
	if len(routeIDs) != 2 || routeIDs[0] != "route-1" || routeIDs[1] != "route-2" {
		t.Fatalf("routing events after compaction = %v, want [route-1 route-2]", routeIDs)
	}
}

type revisionScriptModel struct {
	calls int
	next  func(*adkmodel.LLMRequest) *genai.Content
}

func (*revisionScriptModel) Name() string { return "revision-script" }
func (m *revisionScriptModel) GenerateContent(_ context.Context, req *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		m.calls++
		yield(&adkmodel.LLMResponse{Content: m.next(req)}, nil)
	}
}

// TestPlanRevisionLocalRunnerAcceptance exercises the real local drive loop,
// ChatAgent, ADK function tools, disk session service, and real shell verifier.
func TestPlanRevisionLocalRunnerAcceptance(t *testing.T) {
	restore := redirectLogsForTUI(false)
	defer restore()
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, "revision-owner")
	b.workingDir = t.TempDir()
	id := seedSession(t, b, "plan revision acceptance")
	ctx := context.Background()
	m := &revisionScriptModel{}
	announce, _ := tools.NewAnnouncePlanTool()
	update, err := tools.NewUpdatePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	complete, _ := tools.NewAnnounceCompletionTool()
	c := agent.NewChatAgent(m, []tool.Tool{announce, update, complete}, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)
	root, err := adkagent.New(adkagent.Config{Name: codeAppName, Run: c.Run})
	if err != nil {
		t.Fatal(err)
	}
	rnr, err := runner.New(runner.Config{AppName: codeAppName, Agent: root, SessionService: b.sessionSvc})
	if err != nil {
		t.Fatal(err)
	}
	var emitted []string
	emit := func(kind string, data map[string]any) {
		emitted = append(emitted, kind)
		if kind == "tool_result" {
			if result, ok := data["result"].(map[string]any); ok {
				t.Logf("%s status=%v message=%v", data["name"], result["status"], result["message"])
			}
		}
		if kind == "error" {
			t.Fatalf("surface error: %v", data)
		}
	}
	call := func(name string, args any) {
		raw, _ := json.Marshal(args)
		var a map[string]any
		_ = json.Unmarshal(raw, &a)
		first := true
		m.next = func(*adkmodel.LLMRequest) *genai.Content {
			if first {
				first = false
				return &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("call-%d", m.calls), Name: name, Args: a}}}}
			}
			return genai.NewContentFromText("waiting", "model")
		}
	}
	run := func(po *agent.PromptOverrides) {
		b.driveTurn(agent.WithPromptOverrides(ctx, po), rnr, c, id, 0, genai.NewContentFromText("yes", "user"), emit, nil)
	}
	snapshot := func() agent.PlanSnapshot {
		rt, err := b.localPlanRuntime(ctx, id, emit)
		if err != nil {
			t.Fatal(err)
		}
		return rt.Snapshot()
	}
	steps := []tools.PlanStepInput{}
	for i := 1; i <= 3; i++ {
		steps = append(steps, tools.PlanStepInput{Name: fmt.Sprintf("phase-%d", i), Description: "Implement phase", Details: "Implement exact change and its test", Summary: "The user can check the result", Outcome: "The marker exists", Files: []tools.PlanFileChangeInput{{Path: "marker", Kind: "modify"}}, Verify: fmt.Sprintf("printf verified%d >> marker", i), VerifyKind: "behavior"})
	}
	call("announce_plan", tools.AnnouncePlanArgs{Goal: "Revision acceptance", Context: "## Problem\nNeed durable revisions\n## Root causes\nmarker:1\n## Approach\nVerify markers\n## Boundaries\nKeep API", WhatNotToDo: "Do not change other files", Verification: "Run all phases and observe the marker", Steps: steps})
	run(&agent.PromptOverrides{PlanMode: true})
	s := snapshot()
	if s.PlanID == "" || len(s.Steps) != 3 {
		t.Fatalf("announcement not durable: %+v", s)
	}
	call("update_plan", tools.UpdatePlanArgs{Step: "phase-1", Status: "complete"})
	run(&agent.PromptOverrides{ApprovedPlanExecution: true})
	s = snapshot()
	if s.Steps[0].Status != "complete" || !strings.Contains(s.Steps[0].Evidence, "exit 0") {
		t.Fatalf("verify failed: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(b.workingDir, "marker")); err != nil {
		t.Fatal(err)
	}
	// Same-file detail change applies immediately and preserves phase one's evidence.
	d := s.Definition()
	d.Steps[1].Details += "; use a safer implementation"
	call("update_plan", tools.UpdatePlanArgs{Action: tools.UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "implementation correction", Steps: d.Steps}})
	run(&agent.PromptOverrides{})
	s = snapshot()
	if s.Revision != 2 || s.Steps[0].Status != "complete" {
		t.Fatalf("immediate: %+v", s)
	}
	// Scope changes are committed immediately, without pausing the model loop.
	d = s.Definition()
	d.Steps[1].Files = append(d.Steps[1].Files, agent.PlanFileChange{Path: "extra", Kind: "new"})
	call("update_plan", tools.UpdatePlanArgs{Action: tools.UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "expanded scope", RequiresApproval: true, Steps: d.Steps}})
	before := m.calls
	run(&agent.PromptOverrides{})
	if m.calls != before+2 || snapshot().Revision != 3 || snapshot().PendingRevision != nil {
		t.Fatal("scope revision paused or was not applied")
	}
	if _, err := os.Stat(filepath.Join(b.workingDir, "extra")); !os.IsNotExist(err) {
		t.Fatal("revision created implementation file")
	}
	// A fresh backend restores the new scope and continues without a decision.
	workspace := b.workingDir
	b = newFileStoreBackend(t, dir, "revision-owner")
	b.workingDir = workspace
	c = agent.NewChatAgent(m, []tool.Tool{announce, update, complete}, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)
	root, err = adkagent.New(adkagent.Config{Name: codeAppName, Run: c.Run})
	if err != nil {
		t.Fatal(err)
	}
	rnr, err = runner.New(runner.Config{AppName: codeAppName, Agent: root, SessionService: b.sessionSvc})
	if err != nil {
		t.Fatal(err)
	}
	before = m.calls
	run(&agent.PromptOverrides{})
	if m.calls != before+1 || snapshot().Revision != 3 || snapshot().PendingRevision != nil {
		t.Fatal("restored revision did not continue")
	}
	sess := getSession(t, b, id)
	applied := 0
	for ev := range sess.Events().All() {
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Response["status"] == "applied" {
					applied++
				}
				if p.FunctionResponse != nil && p.FunctionResponse.Response["status"] == "proposed" {
					t.Fatal("generated obsolete proposal")
				}
			}
		}
	}
	if applied != 2 {
		t.Fatalf("persisted revision responses=%d", applied)
	}
	// Force a sidecar projection failure after a durable commit. The session
	// snapshot stays authoritative and a subsequent restore repairs the mirror.
	accepted := snapshot()
	path := b.planFilePath(id)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	mirrorErrors := 0
	rt, err := b.localPlanRuntime(ctx, id, func(kind string, data map[string]any) {
		if kind == "error" && strings.Contains(fmt.Sprint(data["error"]), "mirror failed") {
			mirrorErrors++
		}
	})
	if err != nil || mirrorErrors == 0 || rt.Snapshot().Revision != accepted.Revision {
		t.Fatalf("mirror failure lost state: errors=%d err=%v", mirrorErrors, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if snapshot().Revision != accepted.Revision {
		t.Fatal("repair changed accepted revision")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, _, err := agent.ParsePlanDocument(string(raw))
	if err != nil || doc.Revision != accepted.Revision {
		t.Fatalf("mirror not repaired: %+v %v", doc, err)
	}
}

// TestRunTurnSessionPlanSurvivesMirrorFailure checks the actual model request,
// not just the restored runtime: a committed revision must replace stale text.
func TestRunTurnSessionPlanSurvivesMirrorFailure(t *testing.T) {
	for _, mode := range []string{"stale", "unwritable", "missing", "explicit-stale-context", "legacy-flag-unwritable"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, "mirror-owner")
			b.workingDir = t.TempDir()
			if err := os.Mkdir(filepath.Join(b.workingDir, ".codegraph"), 0755); err != nil {
				t.Fatal(err)
			}
			id := seedSession(t, b, "resume accepted work")
			path := b.planFilePath(id)
			legacy := &agent.ChatAgent{}
			legacy.SetPlanFilePath(path)
			legacy.TrySetActivePlan(agent.NewPlanState("Mirror recovery", agent.PlanDocumentInfo{}, []agent.PlanStepInfo{{Name: "phase", Description: "Implement", Details: "OLD_IMPLEMENTATION", Summary: "Implement safely", Outcome: "Works", Files: []agent.PlanFileChange{{Path: "target", Kind: "modify"}}, Verify: "true", VerifyKind: "unit"}}))
			legacy.MarkActivePlanApproved()
			mirrorFailures := 0
			rt, err := b.localPlanRuntime(ctx, id, func(kind string, data map[string]any) {
				if kind == "error" && strings.Contains(fmt.Sprint(data["error"]), "mirror failed") {
					mirrorFailures++
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			staleContext := agent.BuildPlanExecutionSystemContext(path)
			// Block projection before committing the new definition.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
			s := rt.Snapshot()
			steps := s.Definition().Steps
			steps[0].Details = "NEW_ACCEPTED_IMPLEMENTATION"
			if _, err := rt.Revise(agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "safer details", Steps: steps, RequiresApproval: mode == "legacy-flag-unwritable"}); err != nil {
				t.Fatal(err)
			}
			if mirrorFailures == 0 {
				t.Fatal("expected failed mirror projection after durable commit")
			}
			if rt.Snapshot().Revision != s.Revision+1 {
				t.Fatal("revision not committed")
			}
			if !strings.Contains(mode, "unwritable") {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if mode != "missing" {
					if err := os.WriteFile(path, old, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			workspace := b.workingDir
			b = newFileStoreBackend(t, dir, "mirror-owner")
			b.workingDir, b.sessionID, b.configured = workspace, id, true
			var prompt string
			m := &revisionScriptModel{next: func(req *adkmodel.LLMRequest) *genai.Content {
				raw, _ := json.Marshal(req)
				prompt = string(raw)
				return genai.NewContentFromText("Continuing the accepted implementation.", "model")
			}}
			c := agent.NewChatAgent(m, nil, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)
			b.result = &ChatFactoryResult{ChatAgent: c}
			opts := backend.TurnOptions{}
			if mode == "explicit-stale-context" {
				opts.ApprovedPlanExecution, opts.SystemContext = true, staleContext
			}
			ch, err := b.RunTurn(ctx, "continue", opts)
			if err != nil {
				t.Fatal(err)
			}
			for ev := range ch {
				if ev.Kind == events.KindError && !strings.Contains(ev.Text, "mirror failed") {
					t.Errorf("turn error: %+v", ev)
				}
			}
			if m.calls == 0 || !strings.Contains(prompt, "NEW_ACCEPTED_IMPLEMENTATION") || strings.Contains(prompt, "OLD_IMPLEMENTATION") {
				t.Fatalf("model did not receive only the accepted definition (calls=%d): %s", m.calls, prompt)
			}
			if !strings.Contains(mode, "unwritable") {
				raw, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(raw), "NEW_ACCEPTED_IMPLEMENTATION") {
					t.Fatalf("mirror not repaired: %s %v", raw, err)
				}
			}
		})
	}
}

func TestPlanInitialRequestChangesLocalAcceptance(t *testing.T) {
	b := newFileStoreBackend(t, t.TempDir(), "initial-owner")
	b.workingDir = t.TempDir()
	if err := os.Mkdir(filepath.Join(b.workingDir, ".codegraph"), 0755); err != nil {
		t.Fatal(err)
	}
	id := seedSession(t, b, "initial plan")
	b.sessionID, b.configured = id, true
	announce, _ := tools.NewAnnouncePlanTool()
	update, _ := tools.NewUpdatePlanTool()
	m := &revisionScriptModel{}
	c := agent.NewChatAgent(m, []tool.Tool{announce, update}, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)
	b.result = &ChatFactoryResult{ChatAgent: c}
	args := tools.AnnouncePlanArgs{Goal: "Initial", Context: "Design", WhatNotToDo: "Preserve interfaces", Verification: "Observe marker", Steps: []tools.PlanStepInput{{Name: "one", Description: "Implement", Details: "Implement and test", Summary: "Marker exists", Outcome: "Marker exists", Files: []tools.PlanFileChangeInput{{Path: "marker", Kind: "new"}}, Verify: "printf verified > marker", VerifyKind: "behavior"}}}
	setCalls := func(name string, args any, count int) {
		raw, _ := json.Marshal(args)
		var a map[string]any
		_ = json.Unmarshal(raw, &a)
		first := true
		m.next = func(*adkmodel.LLMRequest) *genai.Content {
			if !first {
				return genai.NewContentFromText("Review ready", "model")
			}
			first = false
			parts := []*genai.Part{}
			for i := 0; i < count; i++ {
				parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("call-%d-%d", m.calls, i), Name: name, Args: a}})
			}
			return &genai.Content{Role: "model", Parts: parts}
		}
	}
	run := func(opts backend.TurnOptions) {
		ch, err := b.RunTurn(context.Background(), "request changes", opts)
		if err != nil {
			t.Fatal(err)
		}
		for ev := range ch {
			if ev.Kind == events.KindError {
				t.Fatalf("turn error: %+v", ev)
			}
		}
	}
	snapshot := func() agent.PlanSnapshot {
		r, err := b.localPlanRuntime(context.Background(), id, func(string, map[string]any) {})
		if err != nil {
			t.Fatal(err)
		}
		return r.Snapshot()
	}
	setCalls("announce_plan", args, 1)
	run(backend.TurnOptions{PlanMode: true})
	initial := snapshot()
	args.Goal = "Corrected"
	setCalls("announce_plan", args, 2)
	run(backend.TurnOptions{PlanMode: true})
	corrected := snapshot()
	if corrected.Goal != "Corrected" || corrected.Revision != initial.Revision+1 || corrected.Document.Lifecycle != "" {
		t.Fatalf("replacement slot: %+v", corrected)
	}
	blocked, ok := 0, 0
	for ev := range getSession(t, b, id).Events().All() {
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionResponse == nil || p.FunctionResponse.Name != "announce_plan" {
				continue
			}
			switch p.FunctionResponse.Response["status"] {
			case agent.PlanRuntimeReplacementBlocked:
				blocked++
			case "ok":
				ok++
			}
		}
	}
	if blocked != 1 || ok != 2 {
		t.Fatalf("simultaneous announcements: ok=%d blocked=%d", ok, blocked)
	}
	setCalls("update_plan", tools.UpdatePlanArgs{Step: "one", Status: "complete"}, 1)
	run(backend.TurnOptions{ApprovedPlanExecution: true})
	if snapshot().Steps[0].Status != "complete" {
		t.Fatal("corrected plan not approved and verified")
	}
	if raw, err := os.ReadFile(filepath.Join(b.workingDir, "marker")); err != nil || string(raw) != "verified" {
		t.Fatalf("marker %q %v", raw, err)
	}
	setCalls("announce_plan", args, 1)
	run(backend.TurnOptions{PlanMode: true})
	if snapshot().Revision != corrected.Revision {
		t.Fatal("Plan mode replaced approved plan")
	}
}

func TestPlanDelegateStartDurableBeforeCompletionAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, "delegate-owner")
	b.workingDir = t.TempDir()
	id := seedSession(t, b, "delegate durable work")
	var live []agent.PlanSnapshot
	r, err := b.localPlanRuntime(ctx, id, func(kind string, data map[string]any) {
		if kind == "plan_snapshot" {
			if s, ok := data["snapshot"].(agent.PlanSnapshot); ok {
				live = append(live, s)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Announce("Delegate", agent.PlanDocumentInfo{Context: "Design", WhatNotToDo: "Preserve interfaces", Verification: "Observe marker"}, []agent.PlanStepInfo{{Name: "one", Description: "Work", Details: "Implement marker", Summary: "Marker exists", Outcome: "Marker exists", Files: []agent.PlanFileChange{{Path: "marker", Kind: "new"}}, Verify: "printf verified > marker", VerifyKind: "behavior"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetApproved(); err != nil {
		t.Fatal(err)
	}
	c := &agent.ChatAgent{}
	var forwarded []agent.SubTaskProgressEvent
	c.SubTaskProgressCallback = func(e agent.SubTaskProgressEvent) { forwarded = append(forwarded, e) }
	unbind := c.BindSessionPlanRuntime(id, r)
	start := agent.SubTaskProgressEvent{Type: "task_start", SessionID: id, PlanStep: "one", TaskName: "worker"}
	forwardPlanDelegateProgress(c, start)
	if len(forwarded) != 1 || forwarded[0].Type != "task_start" {
		t.Fatal("original start not forwarded")
	}
	// Inspect a newly restored runtime while the worker is still running.
	restored, err := b.localPlanRuntime(context.Background(), id, func(string, map[string]any) {})
	if err != nil || restored.Snapshot().Steps[0].Status != "running" {
		t.Fatalf("start not durable: %v %+v", err, restored.Snapshot())
	}
	if len(live) == 0 || live[len(live)-1].Steps[0].Status != "running" {
		t.Fatal("live snapshot missed running")
	}
	if _, err := os.Stat(filepath.Join(b.workingDir, "marker")); !os.IsNotExist(err) {
		t.Fatal("start ran verifier")
	}
	cancel()
	unbind()
	reopened := newFileStoreBackend(t, dir, "delegate-owner")
	reopened.workingDir = b.workingDir
	r2, err := reopened.localPlanRuntime(context.Background(), id, func(string, map[string]any) {})
	if err != nil || r2.Snapshot().Steps[0].Status != "running" {
		t.Fatalf("cancel/reopen lost running: %v", err)
	}
	// Finish on the original runtime separately: only completion runs verify.
	defer c.BindSessionPlanRuntime(id, r)()
	forwardPlanDelegateProgress(c, agent.SubTaskProgressEvent{Type: "task_complete", SessionID: id, PlanStep: "one", TaskName: "worker"})
	if r.Snapshot().Steps[0].Status != "complete" {
		t.Fatal("delegate finish did not verify completion")
	}
	if raw, err := os.ReadFile(filepath.Join(b.workingDir, "marker")); err != nil || string(raw) != "verified" {
		t.Fatalf("delegate verifier marker: %q %v", raw, err)
	}
}

func TestPlanDelegatePersistenceErrorsVisible(t *testing.T) {
	for _, failure := range []string{"start", "finish", "verify-commit"} {
		t.Run(failure, func(t *testing.T) {
			persistCalls, verifyCalls := 0, 0
			failAt := map[string]int{"start": 1, "finish": 2, "verify-commit": 3}[failure]
			r := agent.NewPlanRuntime(agent.NewPlanState("Delegate", agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleApproved}, []agent.PlanStepInfo{{Name: "one", Verify: "true", VerifyKind: "unit"}}), agent.PlanRuntimeOptions{
				CommitSnapshot: func(agent.PlanSnapshot) error {
					persistCalls++
					if persistCalls == failAt {
						return fmt.Errorf("durable storage unavailable")
					}
					return nil
				},
				Verify: func(string) (int, string, error) { verifyCalls++; return 0, "verified", nil },
			})
			c := &agent.ChatAgent{}
			var got []agent.SubTaskProgressEvent
			c.SubTaskProgressCallback = func(e agent.SubTaskProgressEvent) { got = append(got, e) }
			defer c.BindSessionPlanRuntime("session", r)()
			forwardPlanDelegateProgress(c, agent.SubTaskProgressEvent{Type: "task_start", SessionID: "session", PlanStep: "one", TaskName: "worker"})
			if failure != "start" {
				forwardPlanDelegateProgress(c, agent.SubTaskProgressEvent{Type: "task_complete", SessionID: "session", PlanStep: "one", TaskName: "worker"})
			}
			visible := false
			for _, e := range got {
				if e.Reason == agent.PlanRuntimePersistFailed && strings.Contains(e.Error, "durable storage unavailable") {
					visible = true
				}
				if e.StepStatus == "complete" {
					t.Fatal("published false completion")
				}
			}
			if !visible || r.Snapshot().Steps[0].Status == "complete" {
				t.Fatalf("persistence failure hidden: %+v", got)
			}
			wantVerify := 0
			if failure == "verify-commit" {
				wantVerify = 1
			}
			if verifyCalls != wantVerify {
				t.Fatalf("verify calls=%d want=%d", verifyCalls, wantVerify)
			}
			wantLast := "task_complete"
			if failure == "start" {
				wantLast = "task_start"
			}
			if got[len(got)-1].Type != wantLast {
				t.Fatal("original event not forwarded")
			}
		})
	}
}

// TestPlanRevisionExternalPathLocalRunnerAcceptance drives actual ADK tools and
// FileStore restoration, but intentionally never executes the external write.
func TestPlanRevisionExternalPathLocalRunnerAcceptance(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	workspace, outside := filepath.Join(parent, "workspace"), filepath.Join(parent, "outside")
	for _, dir := range []string{workspace, outside, filepath.Join(workspace, ".codegraph")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(outside, "hello.txt")
	storeDir := t.TempDir()
	b := newFileStoreBackend(t, storeDir, "external-revision-owner")
	b.workingDir = workspace
	id := seedSession(t, b, "write Hello without a newline")
	b.sessionID, b.configured = id, true
	announce, err := tools.NewAnnouncePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	update, err := tools.NewUpdatePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	m := &revisionScriptModel{}
	c := agent.NewChatAgent(m, []tool.Tool{announce, update}, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)
	b.result = &ChatFactoryResult{ChatAgent: c}
	setCall := func(name string, args any) {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		var a map[string]any
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		first := true
		m.next = func(*adkmodel.LLMRequest) *genai.Content {
			if !first {
				return genai.NewContentFromText("Waiting for review; no execution.", "model")
			}
			first = false
			return &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("external-%d", m.calls), Name: name, Args: a}}}}
		}
	}
	run := func(opts backend.TurnOptions) {
		ch, err := b.RunTurn(ctx, "change the content to HelloWorld without a newline", opts)
		if err != nil {
			t.Fatal(err)
		}
		for ev := range ch {
			if ev.Kind == events.KindError {
				t.Fatalf("turn error: %+v", ev)
			}
		}
	}
	snapshot := func() agent.PlanSnapshot {
		rt, err := b.localPlanRuntime(ctx, id, func(string, map[string]any) {})
		if err != nil {
			t.Fatal(err)
		}
		return rt.Snapshot()
	}
	assertNotExecuted := func() {
		t.Helper()
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("revision/approval created external file: %v", err)
		}
		for _, step := range snapshot().Steps {
			if step.Status != "pending" || step.Evidence != "" {
				t.Fatalf("revision/approval executed phase: %+v", step)
			}
		}
	}
	setCall("announce_plan", tools.AnnouncePlanArgs{Goal: "Write the greeting", Context: "Design a greeting file", WhatNotToDo: "Do not create any other target or append a newline", Verification: "Check the greeting's exact bytes", Steps: []tools.PlanStepInput{{Name: "greeting", Description: "Write greeting", Details: "Write Hello without a newline", Summary: "The greeting is available", Outcome: "Hello without a newline", Files: []tools.PlanFileChangeInput{{Path: target, Kind: "new"}}, Verify: fmt.Sprintf("test \"$(wc -c < %q)\" -eq 5", target), VerifyKind: agent.VerifyKindBehavior}}})
	run(backend.TurnOptions{PlanMode: true})
	initial := snapshot()
	if initial.PlanID == "" || len(initial.Steps) != 1 || initial.Steps[0].Files[0].Path != target {
		t.Fatalf("initial external announcement missing: %+v", initial)
	}
	// Consent to initial execution, with a text-only model turn: no write/verify.
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Approved, waiting for instructions.", "model")
	}
	run(backend.TurnOptions{ApprovedPlanExecution: true})
	s := snapshot()
	if s.Document.Lifecycle != agent.PlanLifecycleApproved && s.Document.Lifecycle != agent.PlanLifecycleExecuting {
		t.Fatalf("not approved: %+v", s)
	}
	assertNotExecuted()
	// Same external target, changed implementation and verifier: immediate.
	steps := s.Definition().Steps
	steps[0].Details = "Write HelloWorld without a newline"
	steps[0].Verify = fmt.Sprintf("test \"$(wc -c < %q)\" -eq 10", target)
	setCall("update_plan", tools.UpdatePlanArgs{Action: tools.UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "Correct greeting implementation", Steps: steps}})
	run(backend.TurnOptions{})
	immediate := snapshot()
	if immediate.Revision != s.Revision+1 || immediate.PendingRevision != nil || immediate.Steps[0].Details != steps[0].Details {
		t.Fatalf("same external target rejected instead of immediate revision: %+v", immediate)
	}
	assertNotExecuted()
	// Outcome changes also apply immediately.
	steps = immediate.Definition().Steps
	steps[0].Outcome = "HelloWorld without a newline"
	setCall("update_plan", tools.UpdatePlanArgs{Action: tools.UpdatePlanRevise, Revision: &agent.PlanRevisionRequest{PlanID: immediate.PlanID, BaseRevision: immediate.Revision, Reason: "Correct user-visible greeting", Steps: steps}})
	run(backend.TurnOptions{})
	accepted := snapshot()
	if accepted.PendingRevision != nil || accepted.Revision != immediate.Revision+1 || accepted.Steps[0].Outcome != steps[0].Outcome {
		t.Fatalf("outcome revision not applied: %+v", accepted)
	}
	assertNotExecuted()
	// Fresh FileStore/backend restores the revision and continues without consent.
	b = newFileStoreBackend(t, storeDir, "external-revision-owner")
	b.workingDir, b.sessionID, b.configured = workspace, id, true
	c = agent.NewChatAgent(m, []tool.Tool{announce, update}, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)
	b.result = &ChatFactoryResult{ChatAgent: c}
	if restored := snapshot(); restored.PendingRevision != nil || restored.Revision != accepted.Revision {
		t.Fatal("applied revision did not restore")
	}
	m.next = func(*adkmodel.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Continuing; no execution requested.", "model")
	}
	beforeCalls := m.calls
	run(backend.TurnOptions{})
	if m.calls != beforeCalls+1 {
		t.Fatal("restore stopped for revision consent")
	}
	assertNotExecuted()
	b = newFileStoreBackend(t, storeDir, "external-revision-owner")
	b.workingDir = workspace
	restored := snapshot()
	if restored.PlanID != initial.PlanID || restored.Revision != accepted.Revision || restored.Steps[0].Files[0].Path != target || restored.Steps[0].Details != steps[0].Details || restored.Steps[0].Outcome != steps[0].Outcome {
		t.Fatalf("accepted external revision did not restore: %+v", restored)
	}
	raw, err := os.ReadFile(b.planFilePath(id))
	if err != nil {
		t.Fatal(err)
	}
	_, _, mirrorSteps, err := agent.ParsePlanDocument(string(raw))
	if err != nil || len(mirrorSteps) != 1 || mirrorSteps[0].Files[0].Path != target || mirrorSteps[0].Outcome != steps[0].Outcome {
		t.Fatalf("sidecar lost external target: %+v %v", mirrorSteps, err)
	}
	assertNotExecuted()
	statuses := map[string]int{}
	for ev := range getSession(t, b, id).Events().All() {
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.Name == "update_plan" {
				statuses[fmt.Sprint(p.FunctionResponse.Response["status"])]++
			}
		}
	}
	if statuses["applied"] != 2 || statuses["proposed"] != 0 {
		t.Fatalf("real tool responses not persisted: %v", statuses)
	}
	t.Log("ADK/FileStore: external Hello announcement, initial approval without execution, immediate HelloWorld implementation and outcome revision, restore same target; no file created.")
}

func modelTextResponse(text string) adkmodel.LLMResponse {
	return adkmodel.LLMResponse{Content: genai.NewContentFromText(text, genai.RoleModel)}
}

func TestPlanRevisionLocalLegacyPendingMigration(t *testing.T) {
	for _, lifecycle := range []string{"", agent.PlanLifecycleApproved} {
		t.Run(lifecycle, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, "migration-owner")
			b.workingDir = t.TempDir()
			id := seedSession(t, b, "legacy plan")
			s := agent.PlanSnapshot{PlanID: "legacy", Revision: 2, Sequence: 4, Goal: "accepted", Document: agent.PlanDocumentInfo{Lifecycle: lifecycle}, Steps: []agent.PlanStepInfo{{Name: "one", Status: "pending"}}}
			s.PendingRevision = &agent.PlanRevisionProposal{ID: "obsolete", BaseRevision: 2, Candidate: agent.PlanDefinition{Goal: "DO NOT APPLY"}}
			if err := b.sessionSvc.AppendEvent(ctx, getSession(t, b, id), &adksession.Event{ID: "legacy", Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: s}}}); err != nil {
				t.Fatal(err)
			}
			emit := func(kind string, data map[string]any) {
				if kind == "error" {
					t.Fatal(data)
				}
			}
			runtime, err := b.localPlanRuntime(ctx, id, emit)
			if err != nil {
				t.Fatal(err)
			}
			got := runtime.Snapshot()
			if got.PendingRevision != nil || got.Document.PendingRevision != nil || got.Goal != s.Goal || got.Document.Lifecycle != lifecycle || got.Revision != 2 || got.Sequence != 5 {
				t.Fatalf("bad migration: %+v", got)
			}
			b = newFileStoreBackend(t, dir, "migration-owner")
			restored, err := b.localPlanRuntime(ctx, id, emit)
			if err != nil || !reflect.DeepEqual(got, restored.Snapshot()) {
				t.Fatalf("migration not durable: %v", err)
			}
			raw, err := os.ReadFile(b.planFilePath(id))
			if err != nil {
				t.Fatal(err)
			}
			doc, goal, _, err := agent.ParsePlanDocument(string(raw))
			if err != nil || doc.PendingRevision != nil || goal != "accepted" || doc.Lifecycle != lifecycle {
				t.Fatalf("sidecar migration: %+v %v", doc, err)
			}
		})
	}
}
