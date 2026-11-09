package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

type explicitContinueBackend struct {
	gapBotBackend
	calls int
}

func (b *explicitContinueBackend) ContinueBotInteractive(_ context.Context, r store.BotRun, response *store.BotInteractionResponse) (*store.BotRun, error) {
	b.calls++
	return &r, nil
}
func TestBotExplicitContinuationReviewAndEscape(t *testing.T) {
	b := &explicitContinueBackend{}
	b.selected = &store.Bot{ID: uuid.New(), Revision: 3}
	b.run = store.BotRun{ID: uuid.New(), BotID: b.selected.ID, ExecutionMode: execution.BotExecutionModeChatInteractive}
	m := newModel(context.Background(), Config{Backend: b})
	next, cmd := m.botCommand("/bots continue")
	m = next.(model)
	if cmd == nil {
		t.Fatal("missing run picker")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	next, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || b.calls != 0 {
		t.Fatal("review executed continuation")
	}
	canceled, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil || canceled.(model).bots.open || b.calls != 0 {
		t.Fatal("escape executed continuation")
	}
	_, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("missing explicit confirm")
	}
	cmd()
	if b.calls != 1 {
		t.Fatal("continuation not called once")
	}
}
