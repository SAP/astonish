package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

type testBotBackend struct {
	staticBackend
	selected *store.Bot
	decision store.BotApproval
	status   store.ApprovalStatus
}

func (b *testBotBackend) ListBots(context.Context) ([]store.Bot, error) {
	return []store.Bot{{ID: uuid.New(), Name: "Monitor", Status: store.BotPaused}}, nil
}
func (b *testBotBackend) SelectBot(context.Context, string) (*store.Bot, error) {
	return b.selected, nil
}
func (b *testBotBackend) SelectedBot() *store.Bot { return b.selected }
func (b *testBotBackend) DetachBot()              { b.selected = nil }
func (b *testBotBackend) WatchBot(ctx context.Context, emit func(events.Event) error) error {
	<-ctx.Done()
	return ctx.Err()
}
func (b *testBotBackend) BotApprovals(context.Context) ([]store.BotApproval, error) { return nil, nil }
func (b *testBotBackend) DecideBotApproval(_ context.Context, a store.BotApproval, s store.ApprovalStatus) error {
	b.decision = a
	b.status = s
	return nil
}
func (b *testBotBackend) ApproveBotAndContinue(_ context.Context, a store.BotApproval, _ *store.BotInteractionResponse) (*store.BotApproveContinueResult, error) {
	b.decision = a
	b.status = store.ApprovalApproved
	return &store.BotApproveContinueResult{Approval: a, RunID: a.RunID, Continuation: store.BotContinuationQueued}, nil
}
func TestBotPickerStatesAndCodeGating(t *testing.T) {
	m := newModel(context.Background(), Config{Backend: staticBackend{}})
	m.ta.SetValue("/bot")
	m.syncSlashCompletion()
	if m.slash.active {
		t.Fatal("code advertises Bots")
	}
	next, cmd := m.handleSlash("/bots")
	if cmd != nil || next.(model).bots.open {
		t.Fatal("unsupported picker opened")
	}
	m = newModel(context.Background(), Config{Backend: &testBotBackend{}})
	m.width = 100
	m.height = 30
	next, cmd = m.handleSlash("/bots")
	m = next.(model)
	if m.sessions.open || !m.bots.open || !strings.Contains(m.renderBotsOverlay(), "Loading") {
		t.Fatal("wrong picker")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Monitor") {
		t.Fatal("missing bot")
	}
	m.bots.items = nil
	if !strings.Contains(m.renderBotsOverlay(), "No Bots") {
		t.Fatal("empty state missing")
	}
	m.bots.err = "denied"
	if !strings.Contains(m.renderBotsOverlay(), "denied") {
		t.Fatal("error missing")
	}
}
func TestBotDurableDecisionAndStaleWatch(t *testing.T) {
	id := uuid.New()
	b := &testBotBackend{selected: &store.Bot{ID: id}}
	m := newModel(context.Background(), Config{Backend: b})
	a := store.BotApproval{ID: uuid.New(), BotID: id, RunID: uuid.New(), ActionDigest: "exact-digest", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour)}
	m.bots = botsState{open: true, approvals: true, decisions: []store.BotApproval{a}}
	next, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(model)
	if cmd != nil || b.status != "" {
		t.Fatal("decision not confirmed")
	}
	next, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = next
	_ = cmd()
	if b.decision.ID != a.ID || b.decision.ActionDigest != a.ActionDigest || b.status != store.ApprovalApproved {
		t.Fatal("wrong durable decision")
	}
	m.botGeneration = 2
	before := len(m.tr.Items)
	next, _ = m.applyBotWatch(botWatchMsg{generation: 1, event: events.NewText("stale")})
	if len(next.(model).tr.Items) != before {
		t.Fatal("stale feed entered transcript")
	}
	next, _ = m.botCommand("/bots detach")
	m = next.(model)
	if b.selected != nil || m.botGeneration != 3 {
		t.Fatal("detach did not invalidate feed")
	}
}
