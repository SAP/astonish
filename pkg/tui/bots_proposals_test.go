package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/google/uuid"
)

type proposalReviewBackend struct {
	parityBotBackend
	snapshot  store.BotSnapshot
	decisions int
	reviewed  store.BotControlProposal
	confirm   bool
	err       error
}

func (b *proposalReviewBackend) Info() backend.Info {
	return backend.Info{SessionID: b.snapshot.Bot.HomeSessionID.String()}
}
func (b *proposalReviewBackend) BotSnapshot(context.Context) (*store.BotSnapshot, error) {
	return &b.snapshot, nil
}
func (b *proposalReviewBackend) BotProposals(context.Context) ([]store.BotControlProposal, error) {
	return b.snapshot.PendingProposals, nil
}
func (b *proposalReviewBackend) DecideBotProposal(_ context.Context, p store.BotControlProposal, confirm bool) error {
	b.decisions++
	b.reviewed = p
	b.confirm = confirm
	return b.err
}
func newProposalReviewModel() (model, *proposalReviewBackend) {
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Status: store.BotPaused}
	p := store.BotControlProposal{ID: uuid.New(), BotID: bot.ID, Version: 7, ExpectedRevision: 9, Digest: "exact-saved-digest", Status: store.BotControlProposed, ExpiresAt: time.Now().Add(time.Hour)}
	b := &proposalReviewBackend{parityBotBackend: parityBotBackend{testBotBackend: testBotBackend{selected: &bot}}, snapshot: store.BotSnapshot{Bot: bot, PendingProposals: []store.BotControlProposal{p}, PendingApprovals: []store.BotApproval{{ID: uuid.New()}}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.width = 100
	n, cmd := m.openBotProposals()
	m = n.(model)
	n, _ = m.Update(cmd())
	return n.(model), b
}
func TestBotProposalReviewDefaultCancelAndExactDecision(t *testing.T) {
	m, b := newProposalReviewModel()
	for _, want := range []string{"Pending decisions: 2", "exact-saved-digest", "revision: 9", "[Cancel]"} {
		if !strings.Contains(m.renderBotsOverlay(), want) {
			t.Fatal(want)
		}
	}
	n, cmd := m.handleBotProposalKey("enter")
	if n.(model).bots.open || cmd != nil || b.decisions != 0 {
		t.Fatal("default Cancel mutated")
	}
	for _, key := range []string{"y", "n"} {
		m, b = newProposalReviewModel()
		n, _ = m.handleBotProposalKey(key)
		m = n.(model)
		n, cmd = m.handleBotProposalKey("enter")
		m = n.(model)
		if cmd == nil || !m.bots.loading {
			t.Fatal("explicit decision not sent")
		}
		msg := cmd()
		if b.decisions != 1 || b.reviewed.Digest != "exact-saved-digest" || b.confirm != (key == "y") {
			t.Fatal("decision binding lost")
		}
		m.bots = botsState{}
		n, follow := m.Update(msg)
		if n.(model).bots.open || follow != nil {
			t.Fatal("stale reply reopened review")
		}
	}
}
func TestBotProposalExpiredAndConflictNeverRetry(t *testing.T) {
	m, b := newProposalReviewModel()
	m.bots.proposalItems[0].ExpiresAt = time.Now().Add(-time.Second)
	n, _ := m.handleBotProposalKey("y")
	m = n.(model)
	n, cmd := m.handleBotProposalKey("enter")
	if cmd != nil || b.decisions != 0 || !strings.Contains(n.(model).bots.err, "expired") {
		t.Fatal("expired proposal submitted")
	}
	m, b = newProposalReviewModel()
	b.err = errors.New("409 stale digest")
	n, _ = m.handleBotProposalKey("y")
	m = n.(model)
	n, cmd = m.handleBotProposalKey("enter")
	m = n.(model)
	n, _ = m.Update(cmd())
	m = n.(model)
	n, _ = m.handleBotProposalKey("y")
	m = n.(model)
	_, cmd = m.handleBotProposalKey("enter")
	if cmd != nil || b.decisions != 1 {
		t.Fatal("conflicting proposal retried")
	}
}
func TestBotProposalReferenceDedupAndDetach(t *testing.T) {
	m, b := newProposalReviewModel()
	m.info.SessionID = b.snapshot.Bot.HomeSessionID.String()
	msg := botSnapshotMsg{generation: m.botGeneration, snapshot: &b.snapshot}
	n, _ := m.applyBotSnapshot(msg)
	m = n.(model)
	count := len(m.tr.Items)
	n, _ = m.applyBotSnapshot(msg)
	m = n.(model)
	if len(m.tr.Items) != count || count != 1 {
		t.Fatalf("references duplicated: %d/%d", count, len(m.tr.Items))
	}
	m.detachBotWatch()
	n, _ = m.applyBotSnapshot(msg)
	if len(n.(model).tr.Items) != count || n.(model).botSnapshot != nil {
		t.Fatal("stale snapshot accepted after detach")
	}
}
func TestBotReviewHeaderUsesReviewedSnapshot(t *testing.T) {
	m, b := newProposalReviewModel()
	newer := b.snapshot
	newer.PendingProposals = nil
	newer.PendingApprovals = nil
	m.botSnapshot = &newer
	if got := m.botHeaderStatus(); got != "Decisions 2" {
		t.Fatalf("header drifted from reviewed snapshot: %s", got)
	}
}
