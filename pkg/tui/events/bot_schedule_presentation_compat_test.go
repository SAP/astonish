package events

import (
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotSchedulePresentationIndependentHeads(t *testing.T) {
	a := schedulePresentationView()
	b := a
	b.OperationID = uuid.New()
	b.StateSequence = 1
	tr := NewTranscript()
	tr.BotID, tr.SessionID = a.BotID.String(), a.HomeSessionID.String()
	for _, v := range []store.BotOperationView{a, b} {
		tr.Apply(Event{Kind: KindBotOperation, BotOperation: &v})
	}
	a.Revision++
	a.StateSequence++
	a.Status = store.BotOperationCancelled
	a.Presentation = store.BotOperationPresentationConversation
	a.Summary = "The first change was cancelled."
	tr.Apply(Event{Kind: KindBotOperation, BotOperation: &a})
	if len(tr.Items) != 1 || !tr.Streaming || len(tr.BotOperations) != 2 {
		t.Fatal("one final operation hid unrelated pending work")
	}
	b.Revision++
	b.StateSequence++
	b.Status = store.BotOperationCancelled
	b.Presentation = store.BotOperationPresentationConversation
	b.Summary = "The second change was cancelled."
	tr.Apply(Event{Kind: KindBotOperation, BotOperation: &b})
	if len(tr.Items) != 2 || tr.Streaming {
		t.Fatal("operation-local ordering rejected unrelated final")
	}
}

func TestBotScheduleLegacyItemUpgradesInPlace(t *testing.T) {
	v := schedulePresentationView()
	v.Presentation = ""
	tr := NewTranscript()
	tr.BotID, tr.SessionID = v.BotID.String(), v.HomeSessionID.String()
	tr.Apply(Event{Kind: KindBotOperation, BotOperation: &v})
	if len(tr.Items) != 1 || tr.Items[0].Kind != ItemSystem {
		t.Fatal("missing legacy pending item")
	}
	v.Revision++
	v.StateSequence++
	v.Status = store.BotOperationCancelled
	v.Presentation = store.BotOperationPresentationConversation
	v.Summary = "The schedule change was cancelled."
	tr.Apply(Event{Kind: KindBotOperation, BotOperation: &v})
	if len(tr.Items) != 1 || tr.Items[0].Kind != ItemAgent || tr.Items[0].Content != v.Summary {
		t.Fatal("final confirmation did not replace legacy operation item")
	}
}
