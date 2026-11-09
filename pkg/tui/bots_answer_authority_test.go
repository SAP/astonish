package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

type expiredAnswerBackend struct {
	explicitContinueBackend
	answers int
}

func (b *expiredAnswerBackend) ApproveBotAndContinue(context.Context, store.BotApproval, *store.BotInteractionResponse) (*store.BotApproveContinueResult, error) {
	b.answers++
	return nil, fmt.Errorf("original authority expired; explicit continuation required")
}
func TestBotExpiredAnswerRequiresExplicitContinue(t *testing.T) {
	b := &expiredAnswerBackend{}
	b.selected = &store.Bot{ID: uuid.New(), Revision: 3}
	b.run = store.BotRun{ID: uuid.New(), BotID: b.selected.ID, ExecutionMode: execution.BotExecutionModeChatInteractive, Status: store.RunInterrupted}
	a := store.BotApproval{ID: uuid.New(), BotID: b.selected.ID, RunID: b.run.ID, ActionDigest: "saved", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"yesno","prompt":"Proceed?"}`)}}
	m := newModel(context.Background(), Config{Backend: b})
	m.bots = botsState{open: true, answering: true, decisions: []store.BotApproval{a}}
	next, cmd := m.handleBotAnswerKey("enter")
	m = next.(model)
	if cmd != nil {
		t.Fatal("review submitted")
	}
	_, cmd = m.handleBotAnswerKey("enter")
	if cmd == nil {
		t.Fatal("no answer command")
	}
	result := cmd().(botDecisionMsg)
	if result.err == nil || b.answers != 1 || b.calls != 0 {
		t.Fatalf("answer renewed authority: %+v calls=%d", result, b.calls)
	}
	next, cmd = m.botCommand("/bots continue")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	next, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || b.calls != 0 {
		t.Fatal("review renewed authority")
	}
	_, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no explicit continuation")
	}
	cmd()
	if b.calls != 1 {
		t.Fatalf("explicit continue calls=%d", b.calls)
	}
}
