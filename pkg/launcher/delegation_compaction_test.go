package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestDelegationLocalCompactionReopen(t *testing.T) {
	for _, identity := range []struct{ app, user string }{
		{codeAppName, "owner"}, {"astonish", "owner"}, {"astonish", store.SystemUserID},
	} {
		t.Run(identity.app+"/"+identity.user, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			b := newFileStoreBackend(t, dir, "owner")
			parent := seedSession(t, b, "parent")
			child, err := b.sessionSvc.Create(ctx, &adksession.CreateRequest{AppName: identity.app, UserID: identity.user, State: map[string]any{persistentsession.StateKeyParentID: parent}})
			if err != nil {
				t.Fatal(err)
			}
			id := child.Session.ID()
			if err := b.fileStore.SetSessionTitle(ctx, id, "worker"); err != nil {
				t.Fatal(err)
			}
			h := agent.DelegationHandoff{Version: agent.DelegationHandoffVersion, ParentSessionID: parent, ChildSessionID: id, Name: "worker", BatchID: "batch", TaskID: "task", Attempt: 2, OriginalTask: "original", EffectiveTask: "effective", Plan: agent.DelegationPlanContext{Status: agent.DelegationPlanMissing}}
			raw, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			appendEvent := func(ev *adksession.Event) {
				t.Helper()
				if err := b.sessionSvc.AppendEvent(ctx, child.Session, ev); err != nil {
					t.Fatal(err)
				}
			}
			appendEvent(&adksession.Event{ID: "handoff", Author: "system", Actions: adksession.EventActions{StateDelta: map[string]any{agent.DelegationHandoffStateKey: string(raw), "batch_id": "batch", "task_id": "task", "attempt": 2}}})
			args := map[string]any{"path": "a.go", "old_string": "old", "new_string": "new"}
			result := map[string]any{"diff": "-old\n+new", "success": true, "verification_context": "exact"}
			parts := []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "edit-2", Name: "edit_file", Args: args}}, {FunctionResponse: &genai.FunctionResponse{ID: "edit-2", Name: "edit_file", Response: result}}}
			for i, part := range parts {
				appendEvent(&adksession.Event{ID: fmt.Sprintf("edit-%d", i), Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}})
			}
			tail := &adksession.Event{ID: "retained-tail", Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: genai.NewContentFromText("retained once", "model")}}
			appendEvent(tail)
			var archives []string
			for i := range 2 {
				summary := &adksession.Event{ID: fmt.Sprintf("summary-%d", i), Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: genai.NewContentFromText("summary", "model")}}
				archive, err := b.fileStore.ArchiveAndReplaceEvents(identity.app, identity.user, id, []*adksession.Event{summary, tail})
				if err != nil {
					t.Fatal(err)
				}
				archives = append(archives, archive)
			}
			b = newFileStoreBackend(t, dir, "owner")
			b.sessionID = parent
			task := events.DelegationTaskState{ChildSessionID: id, Name: "worker", BatchID: "batch", TaskID: "task", Attempt: 2}
			rows, err := b.LoadDelegationDetail(ctx, task)
			if err != nil {
				t.Fatalf("exact attempt inaccessible after compaction and reopen: %v", err)
			}
			if len(rows) != 6 || rows[3].Text != "retained once" || rows[0].Type != "handoff" || rows[0].Text == "" || rows[1].CallID != "edit-2" || !reflect.DeepEqual(rows[1].Args, args) || rows[2].CallID != "edit-2" || !reflect.DeepEqual(rows[2].Result, result) {
				t.Fatalf("lost archived handoff/diff: %+v", rows)
			}
			for _, bad := range []events.DelegationTaskState{{ChildSessionID: id, BatchID: "wrong"}, {ChildSessionID: id, TaskID: "wrong"}, {ChildSessionID: id, Attempt: 1}, {ChildSessionID: id, Name: "wrong"}} {
				if _, err := b.LoadDelegationDetail(ctx, bad); err == nil {
					t.Fatalf("accepted mismatched selector: %+v", bad)
				}
			}
			b.sessionID = seedSession(t, b, "foreign parent")
			if _, err := b.LoadDelegationDetail(ctx, task); err == nil {
				t.Fatal("foreign parent accessed compacted child")
			}
			b.sessionID = parent
			for _, tc := range []struct {
				name   string
				mutate func(*persistentsession.SessionMeta)
			}{
				{"foreign owner", func(m *persistentsession.SessionMeta) { m.UserID = "foreign" }},
				{"foreign app", func(m *persistentsession.SessionMeta) { m.AppName = "foreign" }},
				{"unrelated parent", func(m *persistentsession.SessionMeta) { m.ParentID = "foreign" }},
				{"cycle", func(m *persistentsession.SessionMeta) { m.ParentID = id }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					original, err := b.fileStore.GetSessionMeta(archives[1])
					if err != nil {
						t.Fatal(err)
					}
					if err := b.fileStore.Index().Update(archives[1], tc.mutate); err != nil {
						t.Fatal(err)
					}
					if _, err := b.LoadDelegationDetail(ctx, task); err == nil {
						t.Fatal("accepted invalid archive path")
					}
					if err := b.fileStore.Index().Update(archives[1], func(m *persistentsession.SessionMeta) { *m = *original }); err != nil {
						t.Fatal(err)
					}
				})
			}
			// Corrupt a real intermediate archive's captured identity. Parent links
			// alone must never authorize traversing it to the requested attempt.
			archive, err := b.sessionSvc.Get(ctx, &adksession.GetRequest{AppName: identity.app, UserID: identity.user, SessionID: archives[1]})
			if err != nil {
				t.Fatal(err)
			}
			h.ChildSessionID = "another-attempt"
			raw, _ = json.Marshal(h)
			if err := b.sessionSvc.AppendEvent(ctx, archive.Session, &adksession.Event{ID: "spoof", Author: "system", Actions: adksession.EventActions{StateDelta: map[string]any{agent.DelegationHandoffStateKey: string(raw)}}}); err != nil {
				t.Fatal(err)
			}
			b = newFileStoreBackend(t, dir, "owner")
			b.sessionID = parent
			if _, err := b.LoadDelegationDetail(ctx, task); err == nil {
				t.Fatal("accepted spoofed archive identity")
			}
		})
	}
}
