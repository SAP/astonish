package api

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/credentials"
	persistent "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func handoffFixture() agent.DelegationHandoff {
	return agent.DelegationHandoff{Version: 1, ParentSessionID: "parent", ChildSessionID: "child", BatchID: "batch", TaskID: "task", Attempt: 1, Name: "worker", OriginalTask: "original", EffectiveTask: "effective", Plan: agent.DelegationPlanContext{Status: agent.DelegationPlanMissing}}
}

func TestSubtaskHandoffPlatformUnavailableAndSelectedState(t *testing.T) {
	valid, _ := json.Marshal(handoffFixture())
	for _, tc := range []struct {
		name      string
		value     any
		available bool
	}{
		{"captured", string(valid), true}, {"legacy", nil, false}, {"malformed", "{bad", false},
		{"future", strings.Replace(string(valid), `"version":1`, `"version":2`, 1), false},
		{"other-child", strings.Replace(string(valid), `"child"`, `"other-child"`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &selectorSessionStore{service: session.InMemoryService(), metas: map[string]*store.SessionMeta{"parent": {ID: "parent", UserID: "caller", AppName: studioChatAppName}}}
			state := map[string]any{"batch_id": "batch", "task_id": "task", "attempt": 1, agent.DelegationHandoffStateKey: tc.value, "system_prompt": "PRIVATE-SYSTEM"}
			if _, err := s.service.Create(context.Background(), &session.CreateRequest{AppName: studioChatAppName, UserID: "caller", SessionID: "child", State: state}); err != nil {
				t.Fatal(err)
			}
			s.children = []store.SessionMeta{{ID: "child", Title: "worker", UserID: "caller", ParentID: "parent"}}
			req := mux.SetURLVars(httptest.NewRequest("GET", "/?child_session_id=child", nil), map[string]string{"id": "parent"})
			ctx := WithPlatformUser(req.Context(), &PlatformUser{ID: "caller", OrgSlug: "tenant-a", TeamSlug: "team"})
			req = req.WithContext(store.WithServices(ctx, &store.Services{Mode: store.ModePlatform, PersonalSessions: s}))
			w := httptest.NewRecorder()
			StudioSubtaskEventsHandler(w, req)
			var body struct {
				Events []struct {
					Type, Text string
					Child      string `json:"child_session_id"`
				}
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Events) != 1 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			row := body.Events[0]
			if row.Type != "handoff" || row.Child != "child" || (row.Text != "") != tc.available || strings.Contains(w.Body.String(), "PRIVATE-SYSTEM") {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestScopedDelegationHandoffValidationAndRedaction(t *testing.T) {
	identity := map[string]any{"batch_id": "batch", "task_id": "task", "attempt": 1}
	h := handoffFixture()
	secret := "fixture-secret-12345"
	h.EffectiveInstructions = secret
	h.Plan = agent.DelegationPlanContext{Status: agent.DelegationPlanMatched, PlanID: "plan", Revision: 9007199254740993, Step: &agent.PlanStepDefinition{Name: "phase", Details: secret}, Document: &agent.PlanDocumentDefinition{Context: secret}}
	h.PlanStep = "phase"
	h.PreferredTools = []string{secret}
	raw, _ := json.Marshal(h)
	redactor := credentials.NewRedactor()
	redactor.RegisterCredential("quoted\"name\n", &credentials.Credential{Type: credentials.CredAPIKey, Value: secret})
	got := scopedDelegationHandoff(string(raw), "parent", "child", identity, redactor)
	if !json.Valid([]byte(got)) || strings.Contains(got, secret) || !strings.Contains(got, "9007199254740993") {
		t.Fatalf("invalid/redaction/precision: %s", got)
	}
	for _, value := range []any{nil, 12, "{bad", `{ "version": 2 }`, string(raw) + ` {}`, strings.Replace(string(raw), `"child"`, `"foreign"`, 1), strings.Replace(string(raw), `"version":1`, `"system_prompt":"private","version":1`, 1)} {
		if got := scopedDelegationHandoff(value, "parent", "child", identity, redactor); got != "" {
			t.Fatalf("invalid record exposed: %s", got)
		}
	}
}

type handoffReopenLLM struct{}

func (handoffReopenLLM) Name() string { return "handoff-reopen-test" }
func (handoffReopenLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText("done", "model")}, nil)
	}
}

// Real loopback requests reopen the durable store after compaction. No daemon,
// production credentials, provider or browser is required.
func TestSubtaskHandoffHTTPReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fs, err := persistent.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"parent", "other-parent"} {
		if _, err := fs.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: studioChatUserID, SessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	h := handoffFixture()
	h.EffectiveInstructions = "fixture-secret-12345"
	seed, err := agent.CaptureDelegationPlan(ctx).ForTask(h.OriginalTask, h.EffectiveInstructions, "")
	if err != nil {
		t.Fatal(err)
	}
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	manager.SessionService, manager.AppName, manager.UserID = fs, studioChatAppName, studioChatUserID
	manager.LLM = handoffReopenLLM{}
	// Use the production writer; never manually persist the handoff or tuple.
	result := manager.RunTask(ctx, agent.SubAgentTask{
		Name: h.Name, Description: h.EffectiveTask, Instructions: h.EffectiveInstructions,
		ParentID: h.ParentSessionID, BatchID: h.BatchID, TaskID: h.TaskID,
		ChildSessionID: h.ChildSessionID, Attempt: h.Attempt, Handoff: seed,
	})
	if result.Status != "success" {
		t.Fatalf("delegate failed: %+v", result)
	}
	child, err := fs.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: studioChatUserID, SessionID: h.ChildSessionID})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(e *session.Event) {
		t.Helper()
		e.Timestamp = time.Now()
		if err := fs.AppendEvent(ctx, child.Session, e); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(&session.Event{ID: "private-state", Author: "system", Actions: session.EventActions{StateDelta: map[string]any{"system_prompt": "DO NOT EXPOSE"}}})
	events := []*session.Event{
		{ID: "read-call", Author: "model", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-1", Name: "read_file", Args: map[string]any{"path": "a.go"}}}}}}},
		{ID: "read-result", Author: "tool", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "read-1", Name: "read_file", Response: map[string]any{"content": "original file contents"}}}}}}},
		{ID: "call", Author: "model", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "edit-1", Name: "edit_file", Args: map[string]any{"path": "a.go", "old_string": "old", "new_string": "new"}}}}}}},
		{ID: "result", Author: "tool", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "edit-1", Name: "edit_file", Response: map[string]any{"result": "ok", "diff": "-old\n+new", "verification_context": "real context"}}}}}}},
		{ID: "text", Author: "model", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "done"}}}}},
	}
	for _, e := range events {
		appendEvent(e)
	}
	// First retain an overlapping tail; then remove every tool event. Both
	// archives must be read, and the overlap must not duplicate edit activity.
	if _, err := fs.ArchiveAndReplaceEvents(studioChatAppName, studioChatUserID, "child", events[2:]); err != nil {
		t.Fatal(err)
	}
	summary := &session.Event{ID: "summary", Author: "model", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("compacted summary", "model")}}
	if _, err := fs.ArchiveAndReplaceEvents(studioChatAppName, studioChatUserID, "child", []*session.Event{summary}); err != nil {
		t.Fatal(err)
	}
	// A same-identity side branch must never be concatenated into the selected
	// child's activity, and a real grandchild is not a direct delegate of parent.
	rawHandoff, _ := child.Session.State().Get(agent.DelegationHandoffStateKey)
	for _, fixture := range []struct{ id, parent string }{{"archive-branch", "parent"}, {"grandchild", "child"}} {
		created, err := fs.Create(ctx, &session.CreateRequest{AppName: studioChatAppName, UserID: studioChatUserID, SessionID: fixture.id, State: map[string]any{
			persistent.StateKeyParentID: fixture.parent, "batch_id": "batch", "task_id": "task", "attempt": 1, agent.DelegationHandoffStateKey: rawHandoff,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := fs.AppendEvent(ctx, created.Session, &session.Event{ID: fixture.id + "-text", Author: "model", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("UNRELATED-ACTIVITY", "model")}}); err != nil {
			t.Fatal(err)
		}
	}
	fs, err = persistent.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	active, err := fs.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: studioChatUserID, SessionID: "child"})
	if err != nil {
		t.Fatal(err)
	}
	for e := range active.Session.Events().All() {
		if e.Content == nil {
			continue
		}
		for _, part := range e.Content.Parts {
			if part.FunctionCall != nil || part.FunctionResponse != nil {
				t.Fatal("compaction fixture retained a tool event in the active transcript")
			}
		}
	}
	redactor := credentials.NewRedactor()
	redactor.RegisterCredential("fixture", &credentials.Credential{Type: credentials.CredAPIKey, Value: h.EffectiveInstructions})
	cm := GetChatManager()
	cm.mu.Lock()
	old := cm.components
	cm.components = &StudioChatComponents{SessionService: fs, ChatAgent: &agent.ChatAgent{Redactor: redactor}}
	cm.mu.Unlock()
	t.Cleanup(func() { cm.mu.Lock(); cm.components = old; cm.mu.Unlock() })
	router := mux.NewRouter()
	router.HandleFunc("/api/studio/sessions/{id}/subtask-events", StudioSubtaskEventsHandler)
	server := httptest.NewServer(router)
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/studio/sessions/parent/subtask-events?child_session_id=grandchild")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("arbitrary grandchild status %d", response.StatusCode)
	}
	for _, parent := range []string{"parent", "other-parent"} {
		response, err := server.Client().Get(server.URL + "/api/studio/sessions/" + parent + "/subtask-events?child_session_id=child")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if parent != "parent" {
			if response.StatusCode != 404 {
				t.Fatalf("cross-parent status %d", response.StatusCode)
			}
			continue
		}
		if response.StatusCode != 200 {
			t.Fatalf("%d %s", response.StatusCode, body)
		}
		var result struct {
			Events []struct {
				Type, Text string
				CallID     string         `json:"call_id"`
				ChildID    string         `json:"child_session_id"`
				BatchID    string         `json:"batch_id"`
				TaskID     string         `json:"task_id"`
				Attempt    int            `json:"attempt"`
				Sequence   int            `json:"sequence"`
				ToolResult map[string]any `json:"tool_result"`
			}
		}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Events) == 0 || result.Events[0].Type != "handoff" || result.Events[0].ChildID != "child" {
			t.Fatalf("events=%s", body)
		}
		if strings.Contains(string(body), h.EffectiveInstructions) || strings.Contains(string(body), "DO NOT EXPOSE") || strings.Contains(string(body), "UNRELATED-ACTIVITY") || !json.Valid([]byte(result.Events[0].Text)) {
			t.Fatalf("unsafe handoff: %s", body)
		}
		var calls []string
		for i, event := range result.Events {
			if event.ChildID != "child" || event.BatchID != "batch" || event.TaskID != "task" || event.Attempt != 1 || event.Sequence != i+1 {
				t.Fatalf("lost captured identity or sequence: %s", body)
			}
			if event.CallID != "" {
				calls = append(calls, event.Type+":"+event.CallID)
			}
			if event.Type == "task_tool_result" {
				switch event.CallID {
				case "read-1":
					if event.ToolResult["content"] != "original file contents" {
						t.Fatalf("lost archived read result: %s", body)
					}
				case "edit-1":
					if event.ToolResult["diff"] != "-old\n+new" || event.ToolResult["verification_context"] != "real context" {
						t.Fatalf("lost rich edit identity/result: %s", body)
					}
				}
			}
		}
		if strings.Join(calls, ",") != "task_tool_call:read-1,task_tool_result:read-1,task_tool_call:edit-1,task_tool_result:edit-1" {
			t.Fatalf("archived activity lost, duplicated or reordered: %s", body)
		}
		if result.Events[len(result.Events)-1].Text != "compacted summary" {
			t.Fatalf("current transcript missing or out of order: %s", body)
		}
	}
}
