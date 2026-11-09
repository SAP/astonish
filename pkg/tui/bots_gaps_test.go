package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

type gapBotBackend struct {
	testBotBackend
	run      store.BotRun
	session  uuid.UUID
	response *store.BotInteractionResponse
}

func (b *gapBotBackend) BotRuns(context.Context) ([]store.BotRun, error) {
	return []store.BotRun{b.run}, nil
}
func (b *gapBotBackend) SelectBotSession(_ context.Context, id uuid.UUID) error {
	b.session = id
	return nil
}
func (b *gapBotBackend) AnswerBotApproval(_ context.Context, a store.BotApproval, r store.BotInteractionResponse) error {
	b.decision = a
	b.response = &r
	return nil
}
func (b *gapBotBackend) ApproveBotAndContinue(_ context.Context, a store.BotApproval, r *store.BotInteractionResponse) (*store.BotApproveContinueResult, error) {
	b.decision = a
	b.response = r
	return &store.BotApproveContinueResult{Approval: a, Continuation: store.BotContinuationQueued}, nil
}
func TestBotRunPickerAndHome(t *testing.T) {
	b := &gapBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}}, run: store.BotRun{ID: uuid.New(), SessionID: uuid.New()}}
	m := newModel(context.Background(), Config{Backend: b})
	next, cmd := m.botCommand("/bots runs")
	m = next.(model)
	if cmd == nil || !m.bots.open {
		t.Fatal("run picker absent")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	next, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("run selection absent")
	}
	_ = cmd()
	if b.session != b.run.SessionID {
		t.Fatal("wrong run session")
	}
	next, cmd = m.botCommand("/bots home")
	_ = next
	if cmd == nil {
		t.Fatal("home absent")
	}
	_ = cmd()
	if b.session != uuid.Nil {
		t.Fatal("home not selected")
	}
}
func TestBotTypedAnswerConfirmAndEscape(t *testing.T) {
	b := &gapBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	a := store.BotApproval{ID: uuid.New(), BotID: b.selected.ID, ActionDigest: "bound", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"select","prompt":"Choose target","options":[{"id":"safe","label":"Safe"},{"id":"other","label":"Other"}]}`)}}
	m := newModel(context.Background(), Config{Backend: b})
	m.bots = botsState{open: true, approvals: true, decisions: []store.BotApproval{a}}
	next, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(model)
	if cmd != nil {
		t.Fatal("early submit")
	}
	next, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil {
		t.Fatal("missing review")
	}
	canceled, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil || canceled.(model).bots.open || b.response != nil {
		t.Fatal("escape decided")
	}
	_, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("typed answer not available")
	}
	_ = cmd()
	if b.response == nil || b.response.QuestionID != "q" || b.response.Answer != "safe" || b.decision.ActionDigest != "bound" {
		t.Fatalf("unbound response: %+v", b.response)
	}
}
