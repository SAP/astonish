package launcher

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotSelectedTraceAndInteractionBinding(t *testing.T) {
	b := newBotPlatformBackend(&platformBackend{}, "personal")
	home, run := uuid.New(), uuid.New()
	b.selected = &store.Bot{ID: uuid.New(), HomeSessionID: home}
	b.sessionID = run
	if b.Info().SessionID != run.String() {
		t.Fatal("run info not selected")
	}
	_, sid := b.watchSelection()
	if sid != run {
		t.Fatal("watch not selected")
	}
	a := store.BotApproval{BotID: b.selected.ID, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"yesno","prompt":"Continue?"}`)}}
	for _, r := range []store.BotInteractionResponse{{QuestionID: "wrong", Answer: "yes"}, {QuestionID: "q", Answer: "invalid"}} {
		if err := b.AnswerBotApproval(context.Background(), a, r); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	b.DetachBot()
	if _, sid = b.watchSelection(); sid != uuid.Nil {
		t.Fatal("selection survives detach")
	}
}
