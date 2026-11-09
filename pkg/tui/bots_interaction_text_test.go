package tui

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotQuestionTextCustomAndNo(t *testing.T) {
	for _, tc := range []struct {
		payload string
		keys    []string
		answer  string
	}{
		{`{"questionId":"q","kind":"text","prompt":"Draft details"}`, []string{"j", "k"}, "jk"},
		{`{"questionId":"q","kind":"select","prompt":"Destination","allow_custom_answer":true,"options":[{"id":"saved","label":"Saved label"}]}`, []string{"down", "j", "k"}, "jk"},
		{`{"questionId":"q","kind":"yesno","prompt":"Include appendix?"}`, []string{"down"}, "no"},
		{`{"questionId":"q","kind":"select","prompt":"Destination","options":[{"id":"saved","label":"Saved label"}]}`, nil, "saved"},
	} {
		b := &gapBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
		a := store.BotApproval{ID: uuid.New(), BotID: b.selected.ID, Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(tc.payload)}}
		m := newModel(context.Background(), Config{Backend: b})
		m.bots = botsState{open: true, answering: true, decisions: []store.BotApproval{a}}
		for _, k := range tc.keys {
			next, cmd := m.handleBotAnswerKey(k)
			m = next.(model)
			if cmd != nil {
				t.Fatal("early submit")
			}
		}
		next, cmd := m.handleBotAnswerKey("enter")
		m = next.(model)
		if cmd != nil || m.bots.response == nil || m.bots.response.Answer != tc.answer {
			t.Fatalf("review: %+v", m.bots)
		}
		_, cmd = m.handleBotAnswerKey("enter")
		if cmd == nil {
			t.Fatal("missing submit")
		}
		cmd()
		if b.response == nil || b.response.Answer != tc.answer {
			t.Fatalf("wrong response %+v", b.response)
		}
	}
}
