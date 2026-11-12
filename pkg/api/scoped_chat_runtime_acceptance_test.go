package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/sandbox"
	plantools "github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Runs the real HTTP handler, ChatAgent, runner, SQLite session persistence and
// history projection. Only the model and a deterministic native tool are fake.
// planAcceptancePool is a sandbox transport fixture, not a plan/verifier mock:
// shell_command actually executes in a session-specific temporary directory.
// It cannot reach a daemon-global PlanVerify callback.
type planAcceptancePool struct {
	sandbox.ToolNodePool
	root string
	ids  []string
}
type planAcceptanceClient struct{ dir string }

func (p *planAcceptancePool) GetOrCreateWithImage(id, _ string, _ []string, _ string) sandbox.ToolNodeClient {
	p.ids = append(p.ids, id)
	dir := filepath.Join(p.root, id)
	_ = os.MkdirAll(dir, 0700)
	return &planAcceptanceClient{dir: dir}
}
func (*planAcceptanceClient) BindSession(string)       {}
func (*planAcceptanceClient) EnsureReady(string) error { return nil }
func (c *planAcceptanceClient) Call(_, name string, args map[string]any) (json.RawMessage, error) {
	if name != "shell_command" {
		return nil, fmt.Errorf("unexpected tool %s", name)
	}
	code, out, err := agent.DefaultPlanVerify(c.dir, args["command"].(string))
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"exit_code": code, "stdout": out})
}

func TestPlanRevisionHTTPAcceptance(t *testing.T) {
	announce, err := plantools.NewAnnouncePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	update, err := plantools.NewUpdatePlanTool()
	if err != nil {
		t.Fatal(err)
	}
	index := agent.NewLexicalToolIndex()
	if err := index.PrimeTools(context.Background(), []tool.Tool{update}, nil); err != nil {
		t.Fatal(err)
	}
	bridge, err := agent.NewProgressiveToolBridge(index)
	if err != nil {
		t.Fatal(err)
	}
	researchCalls := 0
	research, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Research the draft"}, func(tool.Context, struct{}) (map[string]any, error) {
		researchCalls++
		return map[string]any{"content": "research available"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	script := NewMockLLM()
	env := setupMemoryHTTPAcceptance(t, script, append([]tool.Tool{announce, update, research}, bridge...))
	env.ChatAgent.ToolIndex = index
	pool := &planAcceptancePool{root: t.TempDir()}
	install := func() {
		if err := GetChatManager().ensureReady(context.Background()); err != nil {
			t.Fatal(err)
		}
		GetChatManager().components.SandboxPool = pool
		env.ChatAgent.PlanVerify = func(string) (int, string, error) { t.Error("daemon host verifier used"); return 0, "forged", nil }
	}
	install()
	queue := func(turns ...*MockTurn) {
		script.mu.Lock()
		script.turns = turns
		script.turnIndex = 0
		script.mu.Unlock()
	}
	turn := func(req StudioChatRequest) []ChatEvent {
		return consumeMemoryAcceptanceSSE(t, env.Request(t, http.MethodPost, "/api/studio/chat", req, memoryIdentityAdmin))
	}
	snapshot := func(id string) agent.PlanSnapshot {
		detail := env.Detail(t, id)
		for i := len(detail.Messages) - 1; i >= 0; i-- {
			m := detail.Messages[i]
			if m.Snapshot != nil {
				return *m.Snapshot
			}
		}
		t.Fatalf("snapshot missing: %+v", detail.Messages)
		return agent.PlanSnapshot{}
	}
	externalTarget := filepath.Join(t.TempDir(), "hello.txt")
	steps := []agent.PlanStepDefinition{{Name: "one", Description: "First phase", Details: "Write Hello without a newline", Summary: "First capability", Outcome: "Hello without a newline", Files: []agent.PlanFileChange{{Path: externalTarget, Kind: "new"}}, Verify: "printf verified > marker; printf verified", VerifyKind: "unit"}, {Name: "two", Description: "Second phase", Details: "Implement second", Summary: "Second capability", Outcome: "second works", Files: []agent.PlanFileChange{{Path: "pkg/lib/two.go", Kind: "modify"}}, Verify: "printf second", VerifyKind: "unit"}}
	args := map[string]any{"goal": "Deliver capability", "context": "Design", "what_not_to_do": "Preserve public API", "verification": "Marker exists", "steps": steps}
	queue(ToolCallTurn("announce_plan", args), TextTurn("Review this plan."))
	initial := turn(StudioChatRequest{Message: "Plan work", PlanMode: true, AutoApprove: true})
	id := toString(assertHasEvent(t, initial, "session").Data["sessionId"])
	s := snapshot(id)
	if s.Revision != 1 || s.Steps[0].Status != "pending" {
		t.Fatalf("initial %+v events=%+v", s, initial)
	}
	live := assertHasEvent(t, initial, "subtask_progress")
	assertEventData(t, live, "event_type", "plan_snapshot")
	// Both direct and progressive status tools must refuse execution before
	// approval, in Normal and Plan modes, without invoking any verifier.
	for _, planMode := range []bool{false, true} {
		for _, wrapped := range []bool{false, true} {
			name, updateArgs := "update_plan", map[string]any{"step": "one", "status": "complete"}
			if wrapped {
				name, updateArgs = "execute_tool", map[string]any{"name": name, "arguments": updateArgs}
			}
			before := snapshot(id)
			queue(ToolCallTurn(name, updateArgs), TextTurn("Approval required."))
			got := turn(StudioChatRequest{SessionID: id, Message: "Try completion", PlanMode: planMode, AutoApprove: true})
			raw, _ := json.Marshal(got)
			if !strings.Contains(string(raw), agent.PlanRuntimeUnapproved) || len(pool.ids) != 0 || !reflect.DeepEqual(before, snapshot(id)) {
				t.Fatalf("unapproved planMode=%v wrapped=%v mutated or verified: %s", planMode, wrapped, raw)
			}
		}
		queue(ToolCallTurn("read_file", map[string]any{}), TextTurn("Research done."))
		turn(StudioChatRequest{SessionID: id, Message: "Research", PlanMode: planMode, AutoApprove: true})
	}
	if researchCalls != 2 || snapshot(id).Steps[0].Status != "pending" {
		t.Fatal("unapproved research blocked or started execution")
	}
	// Request changes grants one replacement, not an unlimited announce slot.
	args["goal"] = "Corrected capability"
	firstAnnounce, secondAnnounce := ToolCallTurn("announce_plan", args), ToolCallTurn("announce_plan", args)
	queue(&MockTurn{Parts: append(firstAnnounce.Parts, secondAnnounce.Parts...)}, TextTurn("Corrected draft."))
	corrected := turn(StudioChatRequest{SessionID: id, Message: "Request changes: correct the draft", PlanMode: true, AutoApprove: true})
	correctedRaw, _ := json.Marshal(corrected)
	if snapshot(id).Goal != "Corrected capability" || !strings.Contains(string(correctedRaw), agent.PlanRuntimeReplacementBlocked) {
		t.Fatalf("initial replacement: %s", correctedRaw)
	}
	queue(TextTurn("Approved."))
	turn(StudioChatRequest{SessionID: id, Message: "Start", ApprovedPlanExecution: true, AutoApprove: true})
	s = snapshot(id)
	if s.Document.Lifecycle != agent.PlanLifecycleApproved {
		t.Fatal(s)
	}
	queue(ToolCallTurn("update_plan", map[string]any{"step": "one", "status": "complete"}), TextTurn("First verified."))
	turn(StudioChatRequest{SessionID: id, Message: "Verify first", AutoApprove: true})
	s = snapshot(id)
	if s.Steps[0].Status != "complete" || !strings.Contains(s.Steps[0].Evidence, "verified") {
		t.Fatalf("not verified: %+v", s)
	}
	if raw, err := os.ReadFile(filepath.Join(pool.root, id, "marker")); err != nil || string(raw) != "verified" {
		t.Fatalf("real marker: %s %v", raw, err)
	}
	// Normal mode implementation correction is applied, invalidating old evidence.
	revision := func(scope bool) {
		s = snapshot(id)
		req := agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "new information", Steps: s.Definition().Steps}
		if scope {
			req.Steps[0].Outcome = "HelloWorld without a newline"
			req.Steps[0].Details = "Write HelloWorld without a newline"
			req.Steps[0].Verify = fmt.Sprintf("test \"$(wc -c < %q)\" -eq 10", externalTarget)
			req.RequiresApproval = true
			doc := s.Document.Definition()
			doc.Verification = "Check exact HelloWorld bytes"
			req.Document = &doc
		} else {
			req.Steps[0].Details += " corrected"
		}
		queue(ToolCallTurn("update_plan", map[string]any{"action": "revise", "revision": req}), TextTurn("Revision recorded."))
		turn(StudioChatRequest{SessionID: id, Message: "Revise", AutoApprove: true})
	}
	revision(false)
	s = snapshot(id)
	if s.Revision != 2 || s.Steps[0].Evidence != "" || s.Steps[0].Status != "pending" {
		t.Fatalf("revision %+v", s)
	}
	beforeScope := snapshot(id)
	beforeRevisionVerifies := len(pool.ids)
	revision(true)
	applied := snapshot(id)
	if applied.PendingRevision != nil || applied.Revision != beforeScope.Revision+1 || applied.Steps[0].Outcome == beforeScope.Steps[0].Outcome {
		t.Fatalf("scope revision not applied: %+v", applied)
	}
	env.Reopen(t)
	install()
	if !reflect.DeepEqual(applied, snapshot(id)) {
		t.Fatal("applied revision lost on reopen")
	}
	if len(pool.ids) != beforeRevisionVerifies || applied.Steps[0].Files[0].Path != externalTarget {
		t.Fatal("revision verified or changed target")
	}
	if _, err := os.Stat(externalTarget); !os.IsNotExist(err) {
		t.Fatal("revision created implementation file")
	}
	queue(TextTurn("Continuing revised HelloWorld plan."))
	continued := turn(StudioChatRequest{SessionID: id, Message: "Continue", AutoApprove: true})
	rawContinued, _ := json.Marshal(continued)
	if !strings.Contains(string(rawContinued), "Continuing revised HelloWorld plan") {
		t.Fatal("revision stopped execution awaiting a decision")
	}
	// Retired decisions cannot mutate accepted scope, even for the old identity.
	for _, d := range []agent.PlanRevisionDecision{agent.PlanRevisionReject, agent.PlanRevisionRequestChanges, agent.PlanRevisionApprove} {
		req := StudioChatRequest{SessionID: id, PlanRevisionDecision: &agent.TrustedPlanRevisionDecision{PlanID: applied.PlanID, ProposalID: "obsolete", BaseRevision: applied.Revision, Decision: d}}
		result := turn(req)
		assertEventData(t, assertHasEvent(t, result, "error"), "code", agent.PlanRuntimeRevisionObsolete)
		if !reflect.DeepEqual(applied, snapshot(id)) {
			t.Fatal("obsolete decision changed authority")
		}
		foreign := env.Request(t, http.MethodPost, "/api/studio/chat", req, memoryIdentityForeign)
		if foreign.StatusCode < 400 {
			t.Fatal("foreign decision admitted")
		}
		foreign.Body.Close()
		conflict := req
		conflict.PlanMode = true
		resp := env.Request(t, http.MethodPost, "/api/studio/chat", conflict, memoryIdentityAdmin)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("mode: %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
	// A second session gets a different identity and never borrows the first plan.
	queue(TextTurn("Empty session."))
	second := turn(StudioChatRequest{Message: "Hello"})
	id2 := toString(assertHasEvent(t, second, "session").Data["sessionId"])
	noPlan := turn(StudioChatRequest{SessionID: id2, PlanRevisionDecision: &agent.TrustedPlanRevisionDecision{PlanID: s.PlanID, ProposalID: "foreign", BaseRevision: 1, Decision: agent.PlanRevisionApprove}})
	assertEventData(t, assertHasEvent(t, noPlan, "error"), "code", agent.PlanRuntimeNoPlan)
	queue(ToolCallTurn("announce_plan", args), TextTurn("Second independent plan."))
	turn(StudioChatRequest{SessionID: id2, Message: "Plan", PlanMode: true})
	if snapshot(id2).PlanID == snapshot(id).PlanID {
		t.Fatal("sessions share plan")
	}
	env.Reopen(t)
	install()
	if snapshot(id2).Revision != 1 || snapshot(id).Revision != 3 {
		t.Fatal("independent restore failed")
	}
	// First prove lack of approval, independently of verifier availability.
	beforeVerify := len(pool.ids)
	queue(ToolCallTurn("update_plan", map[string]any{"step": "one", "status": "complete"}), TextTurn("Approval required."))
	unapproved := turn(StudioChatRequest{SessionID: id2, Message: "Verify", AutoApprove: true})
	rawUnapproved, _ := json.Marshal(unapproved)
	if len(pool.ids) != beforeVerify || !strings.Contains(string(rawUnapproved), agent.PlanRuntimeUnapproved) {
		t.Fatalf("unapproved verifier invoked: %s", rawUnapproved)
	}
	queue(TextTurn("Approved."))
	turn(StudioChatRequest{SessionID: id2, Message: "Approve", ApprovedPlanExecution: true, AutoApprove: true})
	// No pool must not fall through to host execution after genuine approval.
	GetChatManager().components.SandboxPool = nil
	queue(ToolCallTurn("update_plan", map[string]any{"step": "one", "status": "complete"}), TextTurn("Verification unavailable."))
	turn(StudioChatRequest{SessionID: id2, Message: "Verify", AutoApprove: true})
	if snapshot(id2).Steps[0].Status == "complete" {
		t.Fatal("missing sandbox fabricated success")
	}
	// Plan mode replaces even an approved plan with failed execution.
	oldID := snapshot(id2).PlanID
	args["goal"] = "Replacement after execution"
	queue(ToolCallTurn("announce_plan", args), TextTurn("Review replacement."))
	turn(StudioChatRequest{SessionID: id2, Message: "New plan", PlanMode: true, AutoApprove: true})
	replaced := snapshot(id2)
	if replaced.PlanID == oldID || replaced.Goal != "Replacement after execution" || replaced.Document.Lifecycle != "" || replaced.Revision != 1 {
		t.Fatalf("replacement failed: %+v", replaced)
	}
	for _, step := range replaced.Steps {
		if step.Status != "pending" || step.Evidence != "" {
			t.Fatal("replacement inherited execution")
		}
	}
	env.Reopen(t)
	if snapshot(id2).PlanID != replaced.PlanID {
		t.Fatal("replacement lost after reopen")
	}
}

func TestPlanRevisionAdmissionBusyAndOrdinarySession(t *testing.T) {
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(TextTurn("hello")), nil)
	events := env.Turn(t, "", "hello", false)
	id := toString(assertHasEvent(t, events, "session").Data["sessionId"])
	runner := newChatRunner(id, env.UserID, studioChatAppName, false)
	getChatRunnerRegistry().Register(id, runner)
	defer getChatRunnerRegistry().Unregister(id)
	defer runner.Stop()
	request := StudioChatRequest{SessionID: id, PlanRevisionDecision: &agent.TrustedPlanRevisionDecision{PlanID: "plan", ProposalID: "proposal", BaseRevision: 1, Decision: agent.PlanRevisionApprove}}
	resp := env.Request(t, http.MethodPost, "/api/studio/chat", request, memoryIdentityAdmin)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("busy decision: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// A non-plan slash command must still work while ordinary chat runs.
	reply := consumeMemoryAcceptanceSSE(t, env.Request(t, http.MethodPost, "/api/studio/chat", StudioChatRequest{SessionID: id, Message: "/fleet"}, memoryIdentityAdmin))
	assertHasEvent(t, reply, "fleet_redirect")
	p := execution.Principal{OrgSlug: "org", TeamSlug: "team-a", Subject: "user"}
	unlock := lockPlanSession(p, "app", "id")
	defer unlock()
	p.TeamSlug = "team-b"
	done := make(chan struct{})
	go func() { release := lockPlanSession(p, "app", "id"); release(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("admission lock crossed team boundary")
	}
}

func TestPlanRevisionValidationBeforeInitialization(t *testing.T) {
	valid := agent.TrustedPlanRevisionDecision{PlanID: "plan", ProposalID: "proposal", BaseRevision: 1, Decision: agent.PlanRevisionApprove}
	cases := []StudioChatRequest{
		{PlanRevisionDecision: &valid},
		{SessionID: "session", PlanRevisionDecision: &valid, PlanMode: true},
		{SessionID: "session", PlanRevisionDecision: &valid, ApprovedPlanExecution: true},
		{SessionID: "session", PlanRevisionDecision: &valid, Message: "/fleet"},
	}
	for _, mutate := range []func(*agent.TrustedPlanRevisionDecision){
		func(d *agent.TrustedPlanRevisionDecision) { d.PlanID = " " },
		func(d *agent.TrustedPlanRevisionDecision) { d.ProposalID = "" },
		func(d *agent.TrustedPlanRevisionDecision) { d.BaseRevision = 0 },
		func(d *agent.TrustedPlanRevisionDecision) { d.Decision = "yes" },
	} {
		d := valid
		mutate(&d)
		cases = append(cases, StudioChatRequest{SessionID: "session", PlanRevisionDecision: &d})
	}
	for i, req := range cases {
		raw, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		// No principal/provider/runtime: validation must precede all of them.
		StudioChatHandler(w, httptest.NewRequest(http.MethodPost, "/api/studio/chat", bytes.NewReader(raw)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("case %d: %d", i, w.Code)
		}
	}
	wire, _ := json.Marshal(client.ChatRequest{SessionID: "session", PlanRevisionDecision: &valid})
	var decoded StudioChatRequest
	if err := json.Unmarshal(wire, &decoded); err != nil || !reflect.DeepEqual(decoded.PlanRevisionDecision, &valid) {
		t.Fatalf("client decision contract: %s %v", wire, err)
	}
}

func TestPlanRevisionInitialConsentGuards(t *testing.T) {
	for _, tc := range []struct {
		name, lifecycle, results, code string
		pending                        bool
		empty                          bool
	}{
		{name: "empty", empty: true, code: agent.PlanRuntimeNoPlan},
		{name: "completed", lifecycle: agent.PlanLifecycleCompleted, code: agent.PlanRuntimeCompleted},
		{name: "results", results: "done", code: agent.PlanRuntimeCompleted},
		{name: "executing", lifecycle: agent.PlanLifecycleExecuting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := agent.PlanSnapshot{PlanID: "plan", Revision: 1, Sequence: 1, Goal: "goal", Document: agent.PlanDocumentInfo{Lifecycle: tc.lifecycle, Results: tc.results}, Steps: []agent.PlanStepInfo{{Name: "one", Status: "pending"}}}
			if tc.pending {
				s.PendingRevision = &agent.PlanRevisionProposal{ID: "proposal", BaseRevision: 1}
			}
			runtime := agent.NewPlanRuntimeFromSnapshot(s, agent.PlanRuntimeOptions{})
			if tc.empty {
				runtime = agent.NewPlanRuntime(nil, agent.PlanRuntimeOptions{})
			}
			before := runtime.Snapshot()
			err := approveInitialSessionPlan(runtime)
			if tc.code != "" && (err == nil || agent.PlanRuntimeErrorCode(err) != tc.code) {
				t.Fatalf("want %s got %v", tc.code, err)
			}
			if tc.code == "" && err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, runtime.Snapshot()) {
				t.Fatal("guard changed snapshot")
			}
		})
	}
}

func TestPlanRevisionLegacyReplayBootstrapAndRedaction(t *testing.T) {
	ctx := context.Background()
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	secret := "long-secret-in-plan"
	args := map[string]any{"goal": "legacy", "context": "design", "what_not_to_do": "boundaries", "verification": "acceptance", "steps": []any{map[string]any{"name": "one", "description": secret, "details": "full spec", "verify": "printf legacy", "status": "complete", "evidence": "forged"}}}
	appendPart := func(part *genai.Part) {
		t.Helper()
		e := &session.Event{ID: fmt.Sprint(created.Session.Events().Len()), Author: "model", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}}
		if err := svc.AppendEvent(ctx, created.Session, e); err != nil {
			t.Fatal(err)
		}
	}
	appendPart(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "a", Name: "announce_plan", Args: args}})
	if _, ok := sessionPlanSnapshot(created.Session); ok {
		t.Fatal("call-only plan accepted")
	}
	appendPart(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "a", Name: "announce_plan", Response: map[string]any{"status": "announced"}}})
	appendPart(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "u", Name: "update_plan", Args: map[string]any{"step": "one", "status": "complete"}}})
	appendPart(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "u", Name: "update_plan", Response: map[string]any{"status": "ok", "step": "one", "applied": "running"}}})
	runtime, err := newSessionPlanRuntime(ctx, svc, "user", "app", created.Session.ID(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := runtime.Snapshot()
	if s.PlanID == "" || s.Revision != 1 || s.Steps[0].Status != "running" || s.Steps[0].Evidence != "" || s.Steps[0].Verify != "printf legacy" {
		t.Fatalf("bad legacy bootstrap: %+v", s)
	}
	again, err := newSessionPlanRuntime(ctx, svc, "user", "app", created.Session.ID(), nil, nil)
	if err != nil || !reflect.DeepEqual(s, again.Snapshot()) {
		t.Fatal("bootstrap identity not durable", err)
	}
	redactor := credentials.NewRedactor()
	redactor.AddSecret("secret", secret)
	// Session services may return independent handles; history reads reload the
	// durable session rather than reusing the pre-bootstrap Create handle.
	reloaded, err := svc.Get(ctx, &session.GetRequest{AppName: "app", UserID: "user", SessionID: created.Session.ID()})
	if err != nil {
		t.Fatal(err)
	}
	messages := overlaySessionPlan(eventsToMessages(reloaded.Session.Events(), redactor), reloaded.Session, redactor)
	if len(messages) != 1 {
		t.Fatalf("legacy bootstrap produced %d messages", len(messages))
	}
	if messages[0].Snapshot == nil {
		t.Fatalf("legacy bootstrap missing snapshot on %s", messages[0].Type)
	}
	if messages[0].Snapshot.PlanID != s.PlanID {
		t.Fatalf("legacy bootstrap identity %q, want %q", messages[0].Snapshot.PlanID, s.PlanID)
	}
	raw, _ := json.Marshal(messages)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("snapshot secret leaked")
	}
	var restored []client.StudioMessage
	if err := json.Unmarshal(raw, &restored); err != nil || len(restored) == 0 || restored[len(restored)-1].Snapshot == nil {
		t.Fatalf("client snapshot contract: %v", err)
	}
	if runtime.Snapshot().Steps[0].Description != secret {
		t.Fatal("redaction mutated authority")
	}
}

type failingPlanSessionService struct{ session.Service }

func (s failingPlanSessionService) AppendEvent(context.Context, session.Session, *session.Event) error {
	return errors.New("disk unavailable")
}

func TestPlanRevisionSnapshotReplayOrderingAndErrors(t *testing.T) {
	ctx := context.Background()
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(event *session.Event) {
		t.Helper()
		event.ID = fmt.Sprint(created.Session.Events().Len())
		if err := svc.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
	}
	appendPart := func(part *genai.Part) {
		appendEvent(&session.Event{Author: "model", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}})
	}
	appendPart(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "rejected", Name: "announce_plan", Args: map[string]any{"goal": "unaccepted", "steps": []any{map[string]any{"name": "forged"}}}}})
	appendPart(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "rejected", Name: "announce_plan", Response: map[string]any{"status": "error", "message": "invalid plan"}}})
	messages := eventsToMessages(created.Session.Events(), nil)
	if len(messages) != 1 || messages[0].Type != "tool_result" {
		t.Fatalf("rejected announcement not visible: %+v", messages)
	}
	if _, ok := sessionPlanSnapshot(created.Session); ok {
		t.Fatal("rejected announcement became authoritative")
	}

	secret := "nested-plan-secret-value"
	s := agent.PlanSnapshot{PlanID: "plan", Revision: 2, Sequence: 5, Goal: "accepted", Steps: []agent.PlanStepInfo{{Name: "one", Status: "running", Evidence: secret}}}
	s.PendingRevision = &agent.PlanRevisionProposal{ID: "proposal", BaseRevision: 2, Reason: secret, Candidate: s.Definition()}
	s.PendingRevision.Candidate.Steps[0].Details = secret
	appendEvent(&session.Event{Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: s}}})
	older := s.Clone()
	older.Sequence = 4
	older.Steps[0].Status = "pending"
	appendPart(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "old", Name: "update_plan", Response: map[string]any{"status": "ok", "snapshot": older}}})
	// Failed updates must stay visible without replacing verified state with
	// the model's requested completion, even after an out-of-order response.
	appendPart(&genai.Part{FunctionCall: &genai.FunctionCall{ID: "failed", Name: "update_plan", Args: map[string]any{"step": "one", "status": "complete"}}})
	appendPart(&genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "failed", Name: "update_plan", Response: map[string]any{"status": "error", "message": "verification failed"}}})
	redactor := credentials.NewRedactor()
	redactor.AddSecret("secret", secret)
	messages = overlaySessionPlan(eventsToMessages(created.Session.Events(), redactor), created.Session, redactor)
	plans, failures := 0, 0
	for _, msg := range messages {
		if msg.Type == "tool_result" {
			failures++
		}
		if msg.Type == "plan" {
			plans++
			if msg.Snapshot == nil || msg.Snapshot.Sequence != 5 || msg.Snapshot.Steps[0].Status != "running" || msg.Steps[0].Status != "running" {
				t.Fatalf("replay fabricated completion or regressed: %+v", msg)
			}
		}
	}
	if plans != 1 || failures != 2 {
		t.Fatalf("want one plan and two failures, got %d and %d", plans, failures)
	}
	raw, _ := json.Marshal(messages)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("nested proposal or evidence secret leaked")
	}
	durable, ok := sessionPlanSnapshot(created.Session)
	if !ok || !reflect.DeepEqual(durable, s) {
		t.Fatal("projection changed durable authority")
	}
}

func TestPlanRevisionPersistenceFailureAndVerifierGuards(t *testing.T) {
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindLocal, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: "owner", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	pool := &planAcceptancePool{root: t.TempDir()}
	for _, tc := range []struct {
		ctx  context.Context
		id   string
		pool sandbox.ToolNodePool
	}{
		{context.Background(), "id", pool}, {store.WithDisabledTools(ctx, []string{"shell_command"}), "id", pool}, {ctx, "", pool}, {ctx, "id", nil},
	} {
		if code, _, err := scopedPlanVerifier(tc.ctx, tc.pool, tc.id)("printf forbidden"); err == nil || code == 0 {
			t.Fatal("verifier failed open")
		}
	}
	if len(pool.ids) != 0 {
		t.Fatal("blocked verification allocated sandbox")
	}
	svc := session.InMemoryService()
	created, err := svc.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	s := agent.PlanSnapshot{PlanID: "plan", Revision: 1, Sequence: 1, Goal: "goal", Steps: []agent.PlanStepInfo{{Name: "one", Status: "pending"}}}
	if err := svc.AppendEvent(ctx, created.Session, &session.Event{ID: "seed", Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: s}}}); err != nil {
		t.Fatal(err)
	}
	runtime, err := newSessionPlanRuntime(ctx, failingPlanSessionService{svc}, "user", "app", created.Session.ID(), nil, func(string, map[string]any) { t.Error("failed commit emitted snapshot") })
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.Snapshot()
	if err := runtime.SetApproved(); err == nil || agent.PlanRuntimeErrorCode(err) != agent.PlanRuntimePersistFailed {
		t.Fatal("persistence failure not surfaced", err)
	}
	if !reflect.DeepEqual(before, runtime.Snapshot()) {
		t.Fatal("failed commit changed runtime")
	}
}

func TestScopedChatRuntimeHTTPToolQuestionRestore(t *testing.T) {
	var observed execution.Principal
	probe, err := functiontool.New(functiontool.Config{Name: "ask_user", Description: "Ask a scoped question"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
		observed, _ = execution.PrincipalFromContext(ctx)
		if store.FromContext(ctx) == nil || store.SessionServiceFromContext(ctx) == nil {
			t.Error("HTTP runtime did not bind scoped services and child-session store")
		}
		if store.UserIDFromContext(ctx) != observed.Subject {
			t.Error("lost HTTP user identity")
		}
		return map[string]any{"status": "ok", "kind": "text", "prompt": "Which tenant report?", "questionId": "scoped-question"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	env := setupMemoryHTTPAcceptance(t, NewMockLLM(ToolCallTurn("ask_user", map[string]any{}), TextTurn("Please specify a report.")), []tool.Tool{probe})
	events := env.Turn(t, "", "Ask for a report", false)
	assertHasEvent(t, events, "tool_call")
	assertHasEvent(t, events, "tool_result")
	question := assertHasEvent(t, events, "chat_question")
	assertEventData(t, question, "prompt", "Which tenant report?")
	assertHasEvent(t, events, "done")
	if observed.Subject != env.UserID || observed.OrgSlug != env.OrgSlug || observed.TeamSlug != env.TeamSlug {
		t.Fatalf("tool identity=%+v", observed)
	}
	sessionID := toString(assertHasEvent(t, events, "session").Data["sessionId"])
	detail := env.Detail(t, sessionID)
	for _, msg := range detail.Messages {
		if msg.QuestionID == "scoped-question" && msg.QuestionKind == "text" && msg.Prompt == "Which tenant report?" {
			return
		}
	}
	t.Fatalf("question missing from restored history: %#v", detail.Messages)
}

func TestScopedChatRuntimeChannelPrincipalPreservesContext(t *testing.T) {
	cached, err := json.Marshal([]map[string]any{{"name": "tenant_search", "description": "Scoped search", "inputSchema": map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	principal := execution.Principal{Kind: execution.PrincipalKindLocal, Authentication: execution.AuthMethodLocalOS, Surface: execution.SurfaceCode, Subject: "channel-user", Authenticated: true}
	ctx, err = execution.WithPrincipal(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithServices(ctx, &store.Services{Mode: store.ModePlatform, PlatformMCPServers: &channelRuntimeMCPStore{servers: []store.MCPServer{{Name: "scoped-channel", Transport: "sse", URL: "https://example.invalid/mcp", Enabled: boolPtr(true), CachedTools: cached}}}})
	ctx = agent.WithPromptOverrides(ctx, &agent.PromptOverrides{SessionContext: "existing channel context"})
	bound := WithChannelRuntimeContext(ctx, nil)
	if agent.RequestMCPGroupsFromContext(bound)["mcp:scoped-channel"] == nil {
		t.Fatal("principal channel did not bind scoped catalog")
	}
	if p, ok := execution.PrincipalFromContext(bound); !ok || p.Subject != principal.Subject {
		t.Fatal("lost principal")
	}
	if agent.PromptOverridesFromContext(bound).SessionContext != "existing channel context" {
		t.Fatal("lost channel prompt context")
	}
	cancel()
	if bound.Err() != context.Canceled {
		t.Fatal("lost cancellation")
	}
}

func TestPlanRevisionLegacyPendingSessionMigration(t *testing.T) {
	for _, lifecycle := range []string{"", agent.PlanLifecycleApproved} {
		t.Run(lifecycle, func(t *testing.T) {
			ctx := context.Background()
			svc := session.InMemoryService()
			created, err := svc.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			s := agent.PlanSnapshot{PlanID: "legacy", Revision: 2, Sequence: 4, Goal: "accepted", Document: agent.PlanDocumentInfo{Lifecycle: lifecycle}, Steps: []agent.PlanStepInfo{{Name: "one", Status: "pending"}}}
			s.PendingRevision = &agent.PlanRevisionProposal{ID: "obsolete", BaseRevision: 2, Candidate: agent.PlanDefinition{Goal: "DO NOT APPLY"}}
			if err := svc.AppendEvent(ctx, created.Session, &session.Event{ID: "legacy", Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: s}}}); err != nil {
				t.Fatal(err)
			}
			// Failed migration must remain visible and must not emit a clean snapshot.
			if failed, err := newSessionPlanRuntime(ctx, failingPlanSessionService{svc}, "user", "app", created.Session.ID(), nil, func(string, map[string]any) { t.Error("failed migration emitted snapshot") }); err == nil || failed != nil || agent.PlanRuntimeErrorCode(err) != agent.PlanRuntimePersistFailed {
				t.Fatalf("migration persistence failure hidden: %v", err)
			}
			stillPending, err := svc.Get(ctx, &session.GetRequest{AppName: "app", UserID: "user", SessionID: created.Session.ID()})
			if err != nil {
				t.Fatal(err)
			}
			if unchanged, ok := sessionPlanSnapshot(stillPending.Session); !ok || unchanged.PendingRevision == nil || unchanged.Sequence != s.Sequence {
				t.Fatal("failed migration changed durable state")
			}
			runtime, err := newSessionPlanRuntime(ctx, svc, "user", "app", created.Session.ID(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := runtime.Snapshot()
			if got.PendingRevision != nil || got.Document.PendingRevision != nil || got.Goal != s.Goal || got.Document.Lifecycle != lifecycle || got.Revision != 2 || got.Sequence != 5 {
				t.Fatalf("bad migration: %+v", got)
			}
			restored, err := newSessionPlanRuntime(ctx, svc, "user", "app", created.Session.ID(), nil, nil)
			if err != nil || !reflect.DeepEqual(got, restored.Snapshot()) {
				t.Fatalf("migration not durable: %v", err)
			}
		})
	}
}
