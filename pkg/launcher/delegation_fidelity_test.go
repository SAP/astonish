package launcher

import (
	"context"
	"fmt"
	"testing"

	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
)

func TestDelegationLocalRunnerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, app, user string
		allowed         bool
	}{
		{"code owner", codeAppName, "owner", true},
		{"shared runner", "astonish", store.SystemUserID, true},
		{"request scoped runner", "astonish", "owner", true},
		{"foreign owner", "astonish", "other", false},
		{"foreign code owner", codeAppName, "other", false},
		{"foreign app", "other_app", "owner", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, "owner")
			b.sessionID = seedSession(t, b, "parent")
			var tasks []events.DelegationTaskState
			for attempt := 1; attempt <= 2; attempt++ {
				child, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{
					AppName: tc.app, UserID: tc.user,
					State: map[string]any{persistentsession.StateKeyParentID: b.sessionID, "batch_id": "batch", "task_id": "task", "attempt": attempt},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := b.fileStore.SetSessionTitle(ctx, child.Session.ID(), "bot-ui-build"); err != nil {
					t.Fatal(err)
				}
				callID := fmt.Sprintf("call-%d", attempt)
				for i, part := range []*genai.Part{
					{FunctionCall: &genai.FunctionCall{ID: callID, Name: "read_file", Args: map[string]any{"path": "README.md"}}},
					{FunctionResponse: &genai.FunctionResponse{ID: callID, Name: "read_file", Response: map[string]any{"content": "read result"}}},
					{Text: fmt.Sprintf("attempt %d finished", attempt)},
				} {
					ev := &adksession.Event{ID: fmt.Sprintf("event-%d", i), Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}}
					if err := b.sessionSvc.AppendEvent(ctx, child.Session, ev); err != nil {
						t.Fatal(err)
					}
				}
				tasks = append(tasks, events.DelegationTaskState{Name: "bot-ui-build", ChildSessionID: child.Session.ID(), BatchID: "batch", TaskID: "task", Attempt: attempt})
			}
			for _, restored := range []bool{false, true} {
				if restored {
					parent := b.sessionID
					b = newFileStoreBackend(t, dir, "owner")
					b.sessionID = parent
				}
				for _, task := range tasks {
					rows, err := b.LoadDelegationDetail(ctx, task)
					if !tc.allowed {
						if err == nil {
							t.Fatal("accepted foreign child identity")
						}
						continue
					}
					callID := fmt.Sprintf("call-%d", task.Attempt)
					if err != nil || len(rows) != 4 || rows[0].Type != "handoff" || rows[0].Text != "" || rows[1].Type != "tool_call" || rows[1].CallID != callID || rows[2].Type != "tool_result" || rows[2].CallID != callID || rows[3].Text != fmt.Sprintf("attempt %d finished", task.Attempt) {
						t.Fatalf("restored=%v attempt=%d: rows=%+v err=%v", restored, task.Attempt, rows, err)
					}
					if restored {
						tupleOnly := task
						tupleOnly.ChildSessionID = ""
						if _, err := b.LoadDelegationDetail(ctx, tupleOnly); err == nil {
							t.Fatal("accepted tuple-only lookup without persisted metadata")
						}
					}
					if !restored {
						bad := task
						bad.Attempt = 3
						if _, err := b.LoadDelegationDetail(ctx, bad); err == nil {
							t.Fatal("accepted mismatched attempt")
						}
					}
				}
				if _, err := b.LoadDelegationDetail(ctx, events.DelegationTaskState{Name: "bot-ui-build"}); err == nil {
					t.Fatal("accepted ambiguous name-only selection")
				}
			}
			// A shared runner identity never authorizes a child of another parent.
			b.sessionID = seedSession(t, b, "unrelated parent")
			if _, err := b.LoadDelegationDetail(ctx, tasks[0]); err == nil {
				t.Fatal("accepted child of another parent")
			}
		})
	}
}

func TestDelegationLocalExactSelectors(t *testing.T) {
	ctx := context.Background()
	b := newFileStoreBackend(t, t.TempDir(), "owner")
	b.sessionID = seedSession(t, b, "parent")
	child, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: "owner", State: map[string]any{persistentsession.StateKeyParentID: b.sessionID, "batch_id": "b", "task_id": "t", "attempt": 2}})
	if err != nil {
		t.Fatal(err)
	}
	task := events.DelegationTaskState{ChildSessionID: child.Session.ID(), BatchID: "b", TaskID: "t", Attempt: 2}
	if _, err = b.LoadDelegationDetail(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []events.DelegationTaskState{{ChildSessionID: task.ChildSessionID, BatchID: "wrong"}, {ChildSessionID: task.ChildSessionID, TaskID: "wrong"}, {ChildSessionID: task.ChildSessionID, Attempt: 1}} {
		if _, err = b.LoadDelegationDetail(ctx, bad); err == nil {
			t.Fatalf("accepted mismatched selector: %+v", bad)
		}
	}
}

func TestDelegationLocalUnansweredHistoryDescription(t *testing.T) {
	ctx := context.Background()
	b := newFileStoreBackend(t, t.TempDir(), "owner")
	b.sessionID = seedSession(t, b, "parent")
	parent, err := b.sessionSvc.Get(ctx, &adksession.GetRequest{AppName: codeAppName, UserID: "owner", SessionID: b.sessionID})
	if err != nil {
		t.Fatal(err)
	}
	ev := &adksession.Event{ID: "delegate", Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "b", Name: "delegate_tasks", Args: map[string]any{"tasks": []any{map[string]any{"name": "worker", "description": "original instructions"}}}}}}}}}
	if err = b.sessionSvc.AppendEvent(ctx, parent.Session, ev); err != nil {
		t.Fatal(err)
	}
	history, err := b.LoadHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr := events.NewTranscript()
	var rows []events.HistoryMsg
	for _, row := range history {
		rows = append(rows, events.HistoryMsg{Kind: row.Kind, Text: row.Text, Delegation: row.Delegation})
	}
	tr.LoadHistory(rows)
	if len(tr.Delegation) != 1 || tr.Delegation[0].Description != "original instructions" || tr.Delegation[0].Status != "interrupted" {
		t.Fatalf("history fidelity: %+v", tr.Delegation)
	}
}
