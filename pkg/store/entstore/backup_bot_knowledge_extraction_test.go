package entstore

import (
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestBotKnowledgeExtractionRestoreQuiescence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, c, input, _ := knowledgeStorageFixture(t, scope)
			rec, e := r.EnqueueBotKnowledgeExtraction(c, input)
			if e != nil {
				t.Fatal(e)
			}
			l, e := r.ClaimBotKnowledgeExtraction(c, input.Source, "worker", time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = r.BeginBotKnowledgeExtraction(c, l, rec.Revision); e != nil {
				t.Fatal(e)
			}
			env, e := r.db.(botKnowledgeDB).readKnowledge(c, input.Source.BotID, rec.ID)
			if e != nil {
				t.Fatal(e)
			}
			doc, e := botJSONMap(env)
			if e != nil {
				t.Fatal(e)
			}
			s := input.Source
			row := map[string]any{"id": rec.ID.String(), "bot_id": s.BotID.String(), "run_id": s.RunID.String(), "source_sequence": s.Sequence, "source_version": s.Version, "outcome_digest": s.OutcomeDigest, "revision": env.Record.Revision, "state": string(env.Record.State), "fence": l.Fence, "worker_id": l.WorkerID, "lease_expires_at": l.ExpiresAt, "data": doc}
			if e = quiesceRestoredBotRow("bot_knowledge_extractions", row); e != nil {
				t.Fatal(e)
			}
			restoredDoc, e := restoredBotDocument(row["data"])
			if e != nil {
				t.Fatal(e)
			}
			var out knowledgeEnvelope
			if e = botDecode(restoredDoc, &out); e != nil {
				t.Fatal(e)
			}
			if out.Record.State != store.BotKnowledgeExtractionHeld || out.Record.AttemptID != env.Record.AttemptID || out.Lease.Fence <= l.Fence || !out.Lease.ExpiresAt.IsZero() || row["lease_expires_at"] != nil {
				t.Fatalf("restore %+v %+v", out, row)
			}
			row["source_sequence"] = fmt.Sprint(s.Sequence + 1)
			if quiesceRestoredBotRow("bot_knowledge_extractions", row) == nil {
				t.Fatal("forged restore source")
			}
		})
	}
}
