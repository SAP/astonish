package events

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotOperationOrdering(t *testing.T) {
	bot, home := uuid.New(), uuid.New()
	tr := Transcript{BotID: bot.String(), SessionID: home.String()}
	a := store.BotOperationView{SchemaVersion: 1, BotID: bot, HomeSessionID: home, OperationID: uuid.New(), Revision: 1, StateSequence: 100, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Summary: "Queued"}
	apply := func(v store.BotOperationView) { tr.Apply(Event{Kind: KindBotOperation, BotOperation: &v}) }
	apply(a)
	tr.Apply(NewDone())
	if !tr.Streaming {
		t.Fatal("Done cleared pending")
	}
	b := a
	b.OperationID = uuid.New()
	b.StateSequence = 1
	apply(b)
	a.Status = store.BotOperationApplied
	a.Revision++
	a.StateSequence++
	apply(a)
	if !tr.Streaming || len(tr.Items) != 2 {
		t.Fatal("one operation cleared unrelated work")
	}
	b.Status = store.BotOperationApplied
	b.Revision++
	b.StateSequence++
	apply(b)
	apply(b)
	if tr.Streaming || len(tr.Items) != 2 {
		t.Fatal("duplicate receipt or stale spinner")
	}
	b.Revision--
	b.StateSequence--
	b.Status = store.BotOperationPending
	apply(b)
	if tr.Streaming {
		t.Fatal("stale operation won")
	}
	b.OperationID = uuid.New()
	b.BotID = uuid.New()
	apply(b)
	if len(tr.Items) != 2 {
		t.Fatal("cross scope accepted")
	}
	tr.Reset()
	if len(tr.BotOperations) != 0 {
		t.Fatal("selection leaked")
	}
}
