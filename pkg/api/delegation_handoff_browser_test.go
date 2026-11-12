package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod/lib/launcher"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// TestDelegationHandoffBrowserAcceptance exercises the production SPA, scoped
// HTTP handlers and temporary SQLite. The parent transcript and edit evidence
// are seeded; the captured child handoff is written by the real SubAgentManager.
// No browser fetch, React state or DOM is mocked. Explicit opt-in never skips
// missing prerequisites. Build web first, then run:
// ASTONISH_STREAM_BROWSER_ACCEPTANCE=1 go test ./pkg/api -run '^TestDelegationHandoffBrowserAcceptance$' -count=1 -timeout=3m -v
func TestDelegationHandoffBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_STREAM_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("explicit browser acceptance: set ASTONISH_STREAM_BROWSER_ACCEPTANCE=1")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	data, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatalf("build Studio SPA first: %v", err)
	}
	bin := os.Getenv("ASTONISH_STREAM_BROWSER_PATH")
	if bin == "" {
		var found bool
		bin, found = launcher.LookPath()
		if !found {
			t.Fatal("browser prerequisite missing; set ASTONISH_STREAM_BROWSER_PATH")
		}
	}

	env := setupMemoryHTTPAcceptance(t, &MockLLM{funcLLM: handoffReopenLLM{}}, nil)
	env.Router.HandleFunc("/api/studio/sessions/{id}/subtask-events", StudioSubtaskEventsHandler).Methods(http.MethodGet)
	ctx := context.Background()
	svc := env.Services.PersonalSessions
	const parentID = "handoff-browser-parent"
	parent, err := svc.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: env.UserID, SessionID: parentID})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSessionTitle(ctx, parentID, "Delegation handoff acceptance"); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(sess session.Session, id, author string, parts ...*genai.Part) {
		t.Helper()
		if err := svc.AppendEvent(ctx, sess, &session.Event{ID: id, Author: author, Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: author, Parts: parts}}}); err != nil {
			t.Fatal(err)
		}
	}

	const originalTask = "Repair the captured child file"
	const effectiveTask = "Repair the captured child file using prior findings"
	const originalInstructions = "Keep the public signature unchanged"
	const instructions = "Use exact path child.go; <img src=\"https://example.invalid/private\"> stays inert"
	snapshot := agent.PlanSnapshot{
		PlanID: "captured-browser-plan", Revision: 7, Sequence: 19, Goal: "Captured scoped browser goal",
		Document: agent.PlanDocumentInfo{Context: "Captured parent context", WhatNotToDo: "Do not change unrelated files", Verification: "Inspect signed child diff in Studio"},
		Steps: []agent.PlanStepInfo{
			{Name: "repair", Description: "Repair the selected child", Summary: "Preserve the public signature", Details: "Use the captured phase definition, not a mutable mirror", Files: []agent.PlanFileChange{{Path: "child.go", Kind: "modify"}}, Outcome: "Selected child shows the recorded edit", Verify: "go test ./child", VerifyKind: "unit", ParallelGroup: "wave-1"},
			{Name: "sibling", Details: "SIBLING_PHASE_MUST_NOT_LEAK"},
		},
	}
	planCtx := agent.WithDelegationPlanSource(agent.WithPlanRuntime(ctx, agent.NewPlanRuntimeFromSnapshot(snapshot, agent.PlanRuntimeOptions{})), "/sessions/parent/PLAN.md")
	seed, err := agent.CaptureDelegationPlan(planCtx).ForTask(originalTask, originalInstructions, "repair")
	if err != nil {
		t.Fatal(err)
	}
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	manager.SessionService, manager.AppName, manager.UserID = svc, studioChatAppName, env.UserID
	manager.LLM = handoffReopenLLM{}
	manager.WorkDir = t.TempDir()
	result := manager.RunTask(ctx, agent.SubAgentTask{Name: "captured-worker", Description: effectiveTask, Instructions: instructions, ParentID: parentID, BatchID: "handoff-batch", TaskID: "handoff-batch/task-1", ChildSessionID: "captured-child", Attempt: 1, PlanStep: "repair", Handoff: seed})
	if result.Status != "success" {
		t.Fatalf("real child handoff writer failed: %+v", result)
	}
	child, err := svc.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: env.UserID, SessionID: "captured-child"})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately different requested vs recorded content proves that the rich
	// preview renders tool-result evidence, not a reconstruction of the request.
	appendEvent(child.Session, "edit-call", "model", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "signed-edit", Name: "edit_file", Args: map[string]any{"path": "child.go", "old_string": "old", "new_string": "REQUESTED_NOT_ACTUAL"}}})
	appendEvent(child.Session, "edit-result", "user", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "signed-edit", Name: "edit_file", Response: map[string]any{"result": "ok", "path": "child.go", "diff": "-old recorded line\n+new recorded line"}}})
	appendEvent(child.Session, "child-answer", "model", &genai.Part{Text: "Captured child completed"})

	legacy, err := svc.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: env.UserID, SessionID: "legacy-child", State: map[string]any{"parent_session_id": parentID, "batch_id": "handoff-batch", "task_id": "handoff-batch/task-2", "attempt": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateSessionMeta(ctx, "legacy-child", func(meta *store.SessionMeta) { meta.ParentID = parentID; meta.Title = "legacy-worker" }); err != nil {
		t.Fatal(err)
	}
	appendEvent(legacy.Session, "legacy-answer", "model", &genai.Part{Text: "Legacy child completed"})
	appendEvent(parent.Session, "parent-user", "user", &genai.Part{Text: "Inspect the captured and legacy delegates"})
	appendEvent(parent.Session, "delegate-call", "model", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "handoff-batch", Name: "delegate_tasks", Args: map[string]any{"tasks": []any{map[string]any{"name": "captured-worker", "task": originalTask}, map[string]any{"name": "legacy-worker", "task": "Inspect legacy child"}}}}})
	appendEvent(parent.Session, "delegate-result", "user", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "handoff-batch", Name: "delegate_tasks", Response: map[string]any{"batch_id": "handoff-batch", "status": "complete", "results": []any{map[string]any{"name": "captured-worker", "task_id": "handoff-batch/task-1", "child_session_id": "captured-child", "attempts": 1, "status": "success"}, map[string]any{"name": "legacy-worker", "task_id": "handoff-batch/task-2", "child_session_id": "legacy-child", "attempts": 1, "status": "success"}}}}})
	appendEvent(parent.Session, "parent-answer", "model", &genai.Part{Text: "Handoff browser fixture ready"})

	page := newStreamBrowserPage(t, env, bin, dist)
	page.MustNavigate(env.URL + "/#/chat/" + parentID)
	assertActivity := func() {
		t.Helper()
		page.MustElement(`[data-testid="tool-activity-block"] > button`).MustClick()
		page.MustElementR("button", "edit_file").MustClick()
		preview := page.MustElement(`[data-testid="file-edit-preview"]`)
		preview.MustVisible()
		text := preview.MustText()
		for _, expected := range []string{"child.go — Recorded edit", "-old recorded line", "+new recorded line"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("rich preview missing %q: %s", expected, text)
			}
		}
		if strings.Contains(text, "REQUESTED_NOT_ACTUAL") {
			t.Fatal("preview showed requested rather than recorded edit")
		}
		if !page.MustEval(`() => { const lines=[...document.querySelectorAll('[aria-label="File edit diff"] span')]; const removed=lines.find(e=>e.textContent.startsWith('-')); const added=lines.find(e=>e.textContent.startsWith('+')); return removed && added && getComputedStyle(removed).color!==getComputedStyle(added).color }`).Bool() {
			t.Fatal("signed added/removed diff lines lost distinct colors")
		}
	}
	assertHandoff := func() {
		t.Helper()
		page.MustElementR("button", "^Handoff$").MustClick()
		page.MustElementR("dt", "^Original task$").MustVisible()
		fields := map[string]string{
			"Original task": originalTask, "Effective task": effectiveTask,
			"Original instructions": originalInstructions, "Effective instructions": instructions,
			"Parent session": parentID, "Child session": "captured-child", "Batch": "handoff-batch", "Task ID": "handoff-batch/task-1", "Attempt": "1",
			"Plan context status": "matched", "Plan ID": "captured-browser-plan", "Captured revision": "7", "Captured sequence": "19",
			"Plan context": "Captured parent context", "Boundaries / what not to do": "Do not change unrelated files", "Acceptance verification": "Inspect signed child diff in Studio",
			"Phase details": "Use the captured phase definition, not a mutable mirror", "Phase files": "modify: child.go", "Phase verify command": "go test ./child", "Plan source mirror (captured label)": "/sessions/parent/PLAN.md",
		}
		for label, want := range fields {
			got := page.MustEval(`label => [...document.querySelectorAll('dt')].find(e=>e.textContent===label)?.nextElementSibling?.textContent`, label).Str()
			if got != want {
				t.Errorf("handoff %s=%q, want %q", label, got, want)
			}
		}
		if page.MustEval(`() => !!document.querySelector('dl img') || document.body.textContent.includes('SIBLING_PHASE_MUST_NOT_LEAK') || !!document.querySelector('[data-testid="file-edit-preview"]')`).Bool() {
			t.Fatal("handoff leaked sibling, interpreted markup, or retained Activity preview")
		}
	}
	for _, stage := range []string{"initial load", "persisted reload"} {
		page.MustElementR("p", "^Handoff browser fixture ready$").MustVisible()
		page.MustElementR("button", "captured-worker.*Repair").MustClick()
		page.MustElementR("button", "^Activity$").MustVisible()
		assertActivity()
		assertHandoff()
		page.MustElementR("button", "^Activity$").MustClick()
		assertActivity()
		page.MustElementR("button", "captured-worker.*Repair").MustClick()
		page.MustElementR("button", "legacy-worker.*Inspect legacy").MustClick()
		page.MustElementR("p", "^Legacy child completed$").MustVisible()
		page.MustElementR("button", "^Handoff$").MustClick()
		page.MustElementR("p[role=status]", "^Handoff unavailable\\. No supported captured record exists for this attempt\\.$").MustVisible()
		if page.MustEval(`() => [...document.querySelectorAll('dt')].some(e=>e.textContent==='Original task')`).Bool() {
			t.Fatal("legacy attempt borrowed another child's handoff")
		}
		page.MustElementR("button", "^Activity$").MustClick()
		page.MustElementR("p", "^Legacy child completed$").MustVisible()
		t.Logf("PASS %s: selected captured child → signed recorded edit → scoped Handoff → Activity toggle → legacy unavailable → Activity", stage)
		if stage == "initial load" {
			page.MustReload()
		}
	}
	t.Logf("PASS built Studio browser acceptance; browser=%s SPA=%s", bin, dist)
}
