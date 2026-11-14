package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotKnowledgeCommitSourceAtomic(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			report := finishedAgentWorkReport(t, r, worker, lease, "Short correction retained", store.AgentWorkAnswered)
			source := store.BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: true, Conversation: "complete canonical turn"}
			worker = WithBotKnowledgeCommitSource(worker, func(context.Context, store.BotLease) (store.BotKnowledgeExtractionInput, error) { return source, nil })
			deny := WithBotKnowledgeSourceVerifier(worker, func(context.Context, store.BotKnowledgeExtractionInput) error { return store.ErrBotForbidden })
			q := store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}
			if e := r.CommitRun(deny, q); !errors.Is(e, store.ErrBotForbidden) {
				t.Fatalf("deny %v", e)
			}
			unchanged, e := r.GetRun(owner, run.BotID, run.ID)
			if e != nil || unchanged.Status != store.RunRunning {
				t.Fatalf("not atomic %+v %v", unchanged, e)
			}
			trusted := WithBotKnowledgeSourceVerifier(worker, func(_ context.Context, i store.BotKnowledgeExtractionInput) error {
				if i.Source.BotID != run.BotID || i.Source.RunID != run.ID || i.Conversation != source.Conversation || i.Outcome.Answer != report.Summary {
					return store.ErrBotConflict
				}
				return nil
			})
			if e := r.CommitRun(trusted, q); e != nil {
				t.Fatal(e)
			}
			activity, e := r.db.FindActivity(owner, run.BotID, "result:"+run.ID.String()+":"+knowledgeFenceString(lease.Fence))
			if e != nil {
				t.Fatal(e)
			}
			var payload struct {
				Source store.BotKnowledgeExtractionSource `json:"knowledge_source"`
			}
			if e = json.Unmarshal(activity.Payload, &payload); e != nil {
				t.Fatal(e)
			}
			record, e := r.ReadBotKnowledgeExtraction(owner, payload.Source)
			if e != nil || record.State != store.BotKnowledgeExtractionPending || record.Input.Source.Sequence != activity.Sequence {
				t.Fatalf("enqueue %+v %v", record, e)
			}
			if e = r.CommitRun(trusted, q); e != nil {
				t.Fatal(e)
			}
			again, e := r.ReadBotKnowledgeExtraction(owner, payload.Source)
			if e != nil || again.ID != record.ID {
				t.Fatalf("duplicate %+v %v", again, e)
			}
		})
	}
}
