package launcher

import (
	"context"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/events"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
	"testing"
)

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
