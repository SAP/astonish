package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/credentials"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

type localHandoffContextModel struct {
	capture func(context.Context)
}

func (*localHandoffContextModel) Name() string { return "local-handoff-context" }
func (m *localHandoffContextModel) GenerateContent(ctx context.Context, _ *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		if m.capture != nil {
			m.capture(ctx)
		}
		yield(&adkmodel.LLMResponse{Content: genai.NewContentFromText("done", "model")}, nil)
	}
}

func TestDelegationLocalRunTurnPlanSource(t *testing.T) {
	for _, persistent := range []bool{true, false} {
		t.Run(fmt.Sprintf("persistent=%v", persistent), func(t *testing.T) {
			b := newFileStoreBackend(t, t.TempDir(), "owner")
			b.sessionID = seedSession(t, b, "parent")
			b.workingDir = t.TempDir()
			if err := os.Mkdir(filepath.Join(b.workingDir, ".codegraph"), 0755); err != nil {
				t.Fatal(err)
			}
			if !persistent {
				b.fileStore = nil
			}
			want := b.planFilePath(b.sessionID)
			captured := make(chan agent.DelegationPlanCapture, 1)
			model := &localHandoffContextModel{capture: func(ctx context.Context) { captured <- agent.CaptureDelegationPlan(ctx) }}
			b.result = &ChatFactoryResult{ChatAgent: agent.NewChatAgent(model, nil, nil, b.sessionSvc, &agent.SystemPromptBuilder{CodeMode: true}, false, true)}
			b.configured = true
			stream, err := b.RunTurn(context.Background(), "check source", backend.TurnOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for event := range stream {
				if event.Kind == events.KindError {
					t.Fatalf("turn error: %+v", event)
				}
			}
			var capture agent.DelegationPlanCapture
			select {
			case capture = <-captured:
			default:
				t.Fatal("model was not invoked")
			}
			seed, err := capture.ForTask("work", "", "")
			if err != nil {
				t.Fatal(err)
			}
			manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
			manager.SessionService = adksession.InMemoryService()
			manager.AppName, manager.UserID = "astonish", "owner"
			manager.LLM = &localHandoffContextModel{}
			result := manager.RunTask(context.Background(), agent.SubAgentTask{Name: "worker", Description: "work", ParentID: b.sessionID, BatchID: "batch", TaskID: "task", ChildSessionID: "child", Handoff: seed})
			if result.Status != "success" {
				t.Fatalf("delegate: %+v", result)
			}
			child, err := manager.SessionService.Get(context.Background(), &adksession.GetRequest{AppName: manager.AppName, UserID: manager.UserID, SessionID: "child"})
			if err != nil {
				t.Fatal(err)
			}
			value, err := child.Session.State().Get(agent.DelegationHandoffStateKey)
			if err != nil {
				t.Fatal(err)
			}
			var handoff agent.DelegationHandoff
			if err := json.Unmarshal([]byte(value.(string)), &handoff); err != nil {
				t.Fatal(err)
			}
			if handoff.Plan.SourceMirror != want {
				t.Fatalf("source=%q want=%q", handoff.Plan.SourceMirror, want)
			}
		})
	}
}

func TestDelegationLocalHandoffReopen(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mutate          func(*agent.DelegationHandoff)
		raw             func(string) any
		available       bool
		persistIdentity bool
	}{
		{name: "captured", available: true, persistIdentity: true},
		{name: "initial identity absent after reopen", available: true},
		{name: "legacy", raw: func(string) any { return nil }},
		{name: "malformed", raw: func(string) any { return "{bad" }},
		{name: "wrong type", raw: func(string) any { return 12 }},
		{name: "trailing JSON", raw: func(s string) any { return s + " {}" }},
		{name: "raw prompt", raw: func(s string) any {
			return strings.Replace(s, `"version":1`, `"system_prompt":"PRIVATE-PROMPT","version":1`, 1)
		}},
		{name: "oversized", raw: func(string) any { return strings.Repeat(" ", agent.DelegationHandoffMaxBytes+1) }},
		{name: "unsupported", mutate: func(h *agent.DelegationHandoff) { h.Version++ }},
		{name: "foreign parent", mutate: func(h *agent.DelegationHandoff) { h.ParentSessionID = "foreign" }},
		{name: "foreign child", mutate: func(h *agent.DelegationHandoff) { h.ChildSessionID = "foreign" }},
		{name: "wrong task name", mutate: func(h *agent.DelegationHandoff) { h.Name = "Worker" }},
		{name: "wrong batch", persistIdentity: true, mutate: func(h *agent.DelegationHandoff) { h.BatchID = "wrong" }},
		{name: "wrong task", persistIdentity: true, mutate: func(h *agent.DelegationHandoff) { h.TaskID = "wrong" }},
		{name: "wrong attempt", persistIdentity: true, mutate: func(h *agent.DelegationHandoff) { h.Attempt++ }},
		{name: "invalid attempt", mutate: func(h *agent.DelegationHandoff) { h.Attempt = 0 }},
		{name: "unknown plan status", mutate: func(h *agent.DelegationHandoff) { h.Plan.Status = "future" }},
		{name: "missing plan definition", mutate: func(h *agent.DelegationHandoff) { h.Plan.Document = nil }},
		{name: "mismatched phase", mutate: func(h *agent.DelegationHandoff) { h.Plan.Step.Name = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, "owner")
			parent := seedSession(t, b, "parent")
			identity := map[string]any{persistentsession.StateKeyParentID: parent, "batch_id": "batch", "task_id": "task", "attempt": 2}
			child, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{AppName: "astonish", UserID: "owner", State: identity})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.fileStore.SetSessionTitle(ctx, child.Session.ID(), "worker"); err != nil {
				t.Fatal(err)
			}
			const secret = "fixture-secret-12345"
			h := agent.DelegationHandoff{
				Version: agent.DelegationHandoffVersion, ParentSessionID: parent, ChildSessionID: child.Session.ID(),
				BatchID: "batch", TaskID: "task", Attempt: 2, Name: "worker", OriginalTask: "original", EffectiveTask: "effective",
				EffectiveInstructions: "findings " + secret, PreferredTools: []string{secret}, PlanStep: "phase",
				Plan: agent.DelegationPlanContext{Status: agent.DelegationPlanMatched, PlanID: "plan", Revision: 9007199254740993, Sequence: 9007199254740995,
					Step: &agent.PlanStepDefinition{Name: "phase", Details: secret}, Document: &agent.PlanDocumentDefinition{Context: secret}},
			}
			if tc.mutate != nil {
				tc.mutate(&h)
			}
			raw, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			var value any = string(raw)
			if tc.raw != nil {
				value = tc.raw(string(raw))
			}
			delta := map[string]any{"system_prompt": "PRIVATE-PROMPT", "history": "PRIVATE-HISTORY"}
			if value != nil {
				delta[agent.DelegationHandoffStateKey] = value
			}
			if tc.persistIdentity {
				for k, v := range identity {
					delta[k] = v
				}
			}
			if err := b.sessionSvc.AppendEvent(ctx, child.Session, &adksession.Event{ID: "handoff", Author: "system", Actions: adksession.EventActions{StateDelta: delta}}); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"path": "a.go", "old_string": "old", "new_string": "new", "replace_all": true}
			result := map[string]any{"diff": "-old\n+new", "verification_context": "exact context", "success": true}
			for i, part := range []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "edit-2", Name: "edit_file", Args: args}},
				{FunctionResponse: &genai.FunctionResponse{ID: "edit-2", Name: "edit_file", Response: result}},
				{Text: "done"},
			} {
				if err := b.sessionSvc.AppendEvent(ctx, child.Session, &adksession.Event{ID: fmt.Sprintf("event-%d", i), Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}}); err != nil {
					t.Fatal(err)
				}
			}
			// A new store instance reads the state-only record from disk, not cached state.
			b = newFileStoreBackend(t, dir, "owner")
			b.sessionID = parent
			redactor := credentials.NewRedactor()
			redactor.RegisterCredential("quoted\"name\n", &credentials.Credential{Type: credentials.CredAPIKey, Value: secret})
			b.result = &ChatFactoryResult{ChatAgent: &agent.ChatAgent{Redactor: redactor}}
			for _, name := range []string{"worker", ""} {
				rows, err := b.LoadDelegationDetail(ctx, events.DelegationTaskState{Name: name, ChildSessionID: child.Session.ID()})
				if err != nil || len(rows) != 4 || rows[0].Type != "handoff" {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
				if (rows[0].Text != "") != tc.available {
					t.Fatalf("availability: %+v", rows[0])
				}
				if tc.available {
					var got agent.DelegationHandoff
					if err := json.Unmarshal([]byte(rows[0].Text), &got); err != nil {
						t.Fatal(err)
					}
					if got.ChildSessionID != child.Session.ID() || got.Plan.Revision != 9007199254740993 || got.Plan.Sequence != 9007199254740995 || got.OriginalTask != "original" {
						t.Fatalf("lost handoff: %+v", got)
					}
				}
				if strings.Contains(rows[0].Text, secret) || strings.Contains(rows[0].Text, "PRIVATE-") {
					t.Fatalf("private data exposed: %s", rows[0].Text)
				}
				if rows[1].Type != "tool_call" || rows[1].CallID != "edit-2" || !reflect.DeepEqual(rows[1].Args, args) || rows[2].Type != "tool_result" || rows[2].CallID != "edit-2" || !reflect.DeepEqual(rows[2].Result, result) || rows[3].Text != "done" {
					t.Fatalf("lost rich detail: %+v", rows)
				}
			}
			b.sessionID = seedSession(t, b, "foreign parent")
			if _, err := b.LoadDelegationDetail(ctx, events.DelegationTaskState{ChildSessionID: child.Session.ID()}); err == nil {
				t.Fatal("foreign child exposed")
			}
		})
	}
}
