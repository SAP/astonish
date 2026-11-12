package launcher

import (
	"context"
	"fmt"
	"testing"
	"time"

	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

func appendHistoryPlanSnapshot(t *testing.T, b *localAgentBackend, sess adksession.Session, s agent.PlanSnapshot) {
	t.Helper()
	if err := b.sessionSvc.AppendEvent(context.Background(), sess, &adksession.Event{ID: fmt.Sprintf("snapshot-%s-%d", s.PlanID, s.Sequence), Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: s}}}); err != nil {
		t.Fatal(err)
	}
}

func appendHistoryPlanImplementation(t *testing.T, b *localAgentBackend, sess adksession.Session) {
	t.Helper()
	callID := fmt.Sprintf("implementation-%d", sess.Events().Len())
	appendEvent(t, b, sess, "chat", &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: callID, Name: "shell_command", Args: map[string]any{"command": "go test ./..."}}}}})
	appendEvent(t, b, sess, "chat", &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: callID, Name: "shell_command", Response: map[string]any{"exit_code": 0}}}}})
}

func historyPlanSnapshot(id string, sequence int64) agent.PlanSnapshot {
	return agent.PlanSnapshot{PlanID: id, Revision: sequence, Sequence: sequence, Goal: "Implement " + id, Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleCompleted}, Steps: []agent.PlanStepInfo{{Name: "implementation", Status: "complete"}}}
}

func projectPlanHistory(entries []backend.HistoryEntry) *events.Transcript {
	tr := events.NewTranscript()
	tr.LinearThread = true
	var msgs []events.HistoryMsg
	for _, e := range entries {
		msgs = append(msgs, events.HistoryMsg{Kind: e.Kind, Text: e.Text, ToolName: e.ToolName, ToolID: e.ToolID, Args: e.Args, Result: e.Result, PlanStatus: e.PlanStatus, Options: e.Options, Delegation: e.Delegation})
	}
	tr.LoadHistory(msgs)
	return tr
}

func assertPlanHistory(t *testing.T, entries []backend.HistoryEntry, want []string) *events.Transcript {
	t.Helper()
	tr := projectPlanHistory(entries)
	var got []string
	for _, item := range tr.Items {
		if item.Kind == events.ItemActivity {
			got = append(got, "implementation")
		} else if item.Kind == events.ItemPlan {
			if item.PlanSnapshot == nil {
				got = append(got, "legacy-plan")
			} else {
				got = append(got, "plan:"+item.PlanSnapshot.PlanID)
			}
		} else {
			got = append(got, item.Content)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("projected transcript = %q, want %q", got, want)
	}
	return tr
}

func TestPlanHistoryOrderResumeAndRollback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b := newFileStoreBackend(t, dir, codeUserID)
	id := seedSession(t, b, "Initial request") // event 0
	sess := getSession(t, b, id)
	appendHistoryPlanSnapshot(t, b, sess, historyPlanSnapshot("first", 1))                              // event 1, no Content
	appendHistoryPlanImplementation(t, b, sess)                                                         // events 2-3
	appendEvent(t, b, sess, "chat", genai.NewContentFromText("Implementation answer", genai.RoleModel)) // event 4
	appendEvent(t, b, sess, "user", genai.NewContentFromText("Later request", genai.RoleUser))          // event 5
	appendHistoryPlanSnapshot(t, b, sess, historyPlanSnapshot("first", 2))                              // event 6
	appendEvent(t, b, sess, "chat", genai.NewContentFromText("Final answer", genai.RoleModel))
	reopened := newFileStoreBackend(t, dir, codeUserID)
	hist, err := reopened.ResumeSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tr := assertPlanHistory(t, hist, []string{"Initial request", "plan:first", "implementation", "Implementation answer", "Later request", "Final answer"})
	if tr.Items[1].PlanSnapshot.Revision != 2 || tr.Awaiting {
		t.Fatalf("latest completed plan not restored: %+v", tr.Items[1])
	}
	hist, err = reopened.RollbackTo(ctx, id+":5")
	if err != nil {
		t.Fatal(err)
	}
	tr = assertPlanHistory(t, hist, []string{"Initial request", "plan:first", "implementation", "Implementation answer"})
	if tr.Items[1].PlanSnapshot.Revision != 1 {
		t.Fatal("rolled-back snapshot revision resurrected")
	}
	hist, err = reopened.ResumeSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tr = assertPlanHistory(t, hist, []string{"Initial request", "plan:first", "implementation", "Implementation answer"})
	if tr.Items[1].PlanSnapshot.Revision != 1 {
		t.Fatal("same-process resume used stale cached plan")
	}
	hist, err = reopened.RollbackTo(ctx, id+":0")
	if err != nil {
		t.Fatal(err)
	}
	assertPlanHistory(t, hist, nil)
	reopened = newFileStoreBackend(t, dir, codeUserID)
	hist, err = reopened.ResumeSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanHistory(t, hist, nil)
}

func TestPlanHistoryOrderRollbackBoundaries(t *testing.T) {
	for _, beforeSnapshot := range []bool{false, true} {
		t.Run(fmt.Sprint(beforeSnapshot), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, codeUserID)
			id := seedSession(t, b, "Initial request")
			sess := getSession(t, b, id)
			appendEvent(t, b, sess, "user", genai.NewContentFromText("Plan request", genai.RoleUser))           // event 1
			appendHistoryPlanSnapshot(t, b, sess, historyPlanSnapshot("first", 1))                              // event 2
			appendHistoryPlanImplementation(t, b, sess)                                                         // events 3-4
			appendEvent(t, b, sess, "chat", genai.NewContentFromText("Implementation answer", genai.RoleModel)) // event 5
			appendEvent(t, b, sess, "user", genai.NewContentFromText("Later request", genai.RoleUser))          // event 6
			appendHistoryPlanSnapshot(t, b, sess, historyPlanSnapshot("first", 2))
			appendEvent(t, b, sess, "chat", genai.NewContentFromText("Final answer", genai.RoleModel))
			b = newFileStoreBackend(t, dir, codeUserID)
			if _, err := b.ResumeSession(ctx, id); err != nil {
				t.Fatal(err)
			}
			index := 6
			want := []string{"Initial request", "Plan request", "plan:first", "implementation", "Implementation answer"}
			if beforeSnapshot {
				index, want = 1, []string{"Initial request"}
			}
			hist, err := b.RollbackTo(ctx, fmt.Sprintf("%s:%d", id, index))
			if err != nil {
				t.Fatal(err)
			}
			tr := assertPlanHistory(t, hist, want)
			if !beforeSnapshot && tr.Items[2].PlanSnapshot.Revision != 1 {
				t.Fatal("rollback kept a removed revision")
			}
			for _, restored := range []*localAgentBackend{b, newFileStoreBackend(t, dir, codeUserID)} {
				hist, err = restored.ResumeSession(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				tr = assertPlanHistory(t, hist, want)
				if !beforeSnapshot && tr.Items[2].PlanSnapshot.Revision != 1 {
					t.Fatal("resume resurrected a removed revision")
				}
			}
		})
	}
}

func appendHistoryPlanAnnouncement(t *testing.T, b *localAgentBackend, sess adksession.Session, callID, goal string, response map[string]any) {
	t.Helper()
	appendEvent(t, b, sess, "chat", &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: callID, Name: "announce_plan", Args: map[string]any{"goal": goal, "steps": []any{map[string]any{"name": "implementation", "description": "Implement"}}}}}}})
	appendEvent(t, b, sess, "chat", &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: callID, Name: "announce_plan", Response: response}}}})
}

func TestPlanHistoryOrderLegacyAndMultipleIdentities(t *testing.T) {
	for _, beforeResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(beforeResponse), func(t *testing.T) {
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, codeUserID)
			id := seedSession(t, b, "Initial request")
			sess := getSession(t, b, id)
			first := historyPlanSnapshot("first", 1)
			if beforeResponse {
				appendHistoryPlanSnapshot(t, b, sess, first)
			}
			appendHistoryPlanAnnouncement(t, b, sess, "announce-first", first.Goal, map[string]any{"status": "ok"})
			appendHistoryPlanImplementation(t, b, sess)
			appendEvent(t, b, sess, "chat", genai.NewContentFromText("First answer", genai.RoleModel))
			if !beforeResponse {
				appendHistoryPlanSnapshot(t, b, sess, first)
			}
			appendEvent(t, b, sess, "user", genai.NewContentFromText("Second request", genai.RoleUser))
			second := historyPlanSnapshot("second", 1)
			appendHistoryPlanSnapshot(t, b, sess, second)
			appendHistoryPlanAnnouncement(t, b, sess, "announce-second", second.Goal, map[string]any{"status": "ok", "snapshot": second})
			second.Revision, second.Sequence, second.Goal = 2, 2, "Revised second plan"
			appendHistoryPlanSnapshot(t, b, sess, second)
			appendHistoryPlanImplementation(t, b, sess)
			appendEvent(t, b, sess, "chat", genai.NewContentFromText("Final answer", genai.RoleModel))
			b = newFileStoreBackend(t, dir, codeUserID)
			hist, err := b.ResumeSession(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			tr := assertPlanHistory(t, hist, []string{"Initial request", "plan:first", "implementation", "First answer", "Second request", "plan:second", "implementation", "Final answer"})
			if tr.Items[5].PlanSnapshot.Goal != second.Goal {
				t.Fatal("latest definition lost")
			}
			plans := 0
			for _, e := range hist {
				if e.Kind == "plan" || (e.Delegation != nil && e.Delegation.Kind == events.KindPlanSnapshot) {
					plans++
				}
			}
			if plans != 2 {
				t.Fatalf("adapter emitted %d plan entries, want 2", plans)
			}
		})
	}
}

func TestPlanHistoryOrderInheritedState(t *testing.T) {
	for _, anchored := range []bool{false, true} {
		t.Run(fmt.Sprint(anchored), func(t *testing.T) {
			b := newFileStoreBackend(t, t.TempDir(), codeUserID)
			s := historyPlanSnapshot("inherited", 2)
			created, err := b.sessionSvc.Create(context.Background(), &adksession.CreateRequest{AppName: codeAppName, UserID: codeUserID, State: map[string]any{agent.PlanSnapshotStateKey: s}})
			if err != nil {
				t.Fatal(err)
			}
			sess := created.Session
			appendEvent(t, b, sess, "user", genai.NewContentFromText("Retained request", genai.RoleUser))
			if anchored {
				appendHistoryPlanAnnouncement(t, b, sess, "legacy", s.Goal, map[string]any{"status": "ok"})
				appendHistoryPlanImplementation(t, b, sess)
			}
			appendEvent(t, b, sess, "chat", genai.NewContentFromText("Final answer", genai.RoleModel))
			hist, err := b.ResumeSession(context.Background(), sess.ID())
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"plan:inherited", "Retained request", "Final answer"}
			if anchored {
				want = []string{"Retained request", "plan:inherited", "implementation", "Final answer"}
			}
			assertPlanHistory(t, hist, want)
		})
	}
}

func TestPlanHistoryOrderRejectedAnnouncementDoesNotCreatePlan(t *testing.T) {
	b := newFileStoreBackend(t, t.TempDir(), codeUserID)
	id := seedSession(t, b, "Initial request")
	appendHistoryPlanAnnouncement(t, b, getSession(t, b, id), "rejected", "Must not appear", map[string]any{"status": "incomplete_plan"})
	hist, err := b.ResumeSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanHistory(t, hist, []string{"Initial request"})
}

func TestPlanHistoryOrderPendingAndRejected(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, codeUserID)
			id := seedSession(t, b, "Initial request")
			sess := getSession(t, b, id)
			pending := historyPlanSnapshot("pending", 1)
			pending.Document.Lifecycle = ""
			pending.Steps[0].Status = "pending"
			appendHistoryPlanSnapshot(t, b, sess, pending)
			if rejected {
				appendHistoryPlanAnnouncement(t, b, sess, "rejected", "Must not appear", map[string]any{"status": "blocked_active_approved_plan", "snapshot": pending})
			}
			b = newFileStoreBackend(t, dir, codeUserID)
			hist, err := b.ResumeSession(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			tr := assertPlanHistory(t, hist, []string{"Initial request", "plan:pending"})
			if !tr.Awaiting || tr.ApprovalIdx != 1 {
				t.Fatal("pending plan approval lost")
			}
		})
	}
}
