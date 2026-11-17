package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

type progressReviewBackend struct {
	gapBotBackend
	snapshot store.BotSnapshot
	reviewed store.BotRunState
	revision int64
	key      string
	calls    int
}

func (b *progressReviewBackend) BotGoals(context.Context) ([]store.BotGoal, error) { return nil, nil }
func (b *progressReviewBackend) BotStudioURL() string                              { return "" }
func (b *progressReviewBackend) BotSnapshot(context.Context) (*store.BotSnapshot, error) {
	return &b.snapshot, nil
}
func (b *progressReviewBackend) ContinueBotProgress(_ context.Context, s store.BotRunState, revision int64, key string) (*store.BotRun, error) {
	b.calls++
	b.reviewed = s
	b.revision = revision
	b.key = key
	return &b.run, nil
}
func TestBotProgressReviewBindsCheckpointAndRevision(t *testing.T) {
	b := &progressReviewBackend{}
	b.selected = &store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Revision: 4}
	b.run = store.BotRun{ID: uuid.New(), BotID: b.selected.ID, SessionID: uuid.New(), Status: store.RunInterrupted}
	s := store.BotRunState{SchemaVersion: 1, BotID: b.selected.ID, HomeSessionID: b.selected.HomeSessionID, RunID: b.run.ID, SessionID: b.run.SessionID, StateSequence: 1, Status: store.RunInterrupted}
	s.Progress = &store.BotProgressProjection{SchemaVersion: 1, BotID: s.BotID, RunID: s.RunID, SessionID: s.SessionID, Revision: 7, Phase: store.BotProgressSuspended, Reason: store.BotProgressReasonPaused}
	b.run.Progress = s.Progress
	b.snapshot = store.BotSnapshot{Bot: *b.selected, RunStates: []store.BotRunState{s}}
	m := newModel(context.Background(), Config{Backend: b})
	m.width = 160
	n, cmd := m.botCommand("/bots continue")
	m = n.(model)
	n, _ = m.Update(cmd())
	m = n.(model)
	n, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = n.(model)
	if cmd != nil || b.calls != 0 || !strings.Contains(m.renderBotsOverlay(), "checkpoint 7 at Bot revision 4") {
		t.Fatalf("review must not mutate: cmd=%v calls=%d err=%q overlay=%q", cmd != nil, b.calls, m.bots.err, m.renderBotsOverlay())
	}
	canceled, cmd := m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil || canceled.(model).bots.open || b.calls != 0 {
		t.Fatal("Escape mutated")
	}
	// Polling cannot silently replace the checkpoint or revision the owner saw.
	b.snapshot.Bot.Revision = 9
	b.snapshot.RunStates[0].Progress.Revision = 12
	_, cmd = m.handleBotsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("missing confirmed continuation")
	}
	cmd()
	if b.calls != 1 || b.revision != 4 || b.reviewed.Progress.Revision != 7 || b.key == "" {
		t.Fatalf("binding changed: %+v", b)
	}
}

func TestBotProgressDetailsRecordedCoalescing(t *testing.T) {
	for _, count := range []int64{0, 7, -1} {
		m, b := newProposalReviewModel()
		m.width = 160
		state := store.BotRunState{SchemaVersion: 1, BotID: b.snapshot.Bot.ID, HomeSessionID: b.snapshot.Bot.HomeSessionID, RunID: uuid.New(), SessionID: uuid.New(), StateSequence: 999, Status: store.RunRunning}
		state.Progress = &store.BotProgressProjection{SchemaVersion: 1, BotID: state.BotID, RunID: state.RunID, SessionID: state.SessionID, Revision: 3, Phase: store.BotProgressEvaluating, CoalescedOccurrences: count}
		b.snapshot.RunStates = []store.BotRunState{state}
		m.bots.snapshot = &b.snapshot
		text := m.renderBotOverview()
		if count > 0 && !strings.Contains(text, "Coalesced schedule ticks: 7") {
			t.Fatalf("recorded count absent: %s", text)
		}
		if count <= 0 && strings.Contains(text, "Coalesced schedule ticks:") {
			t.Fatalf("invented/invalid count: %s", text)
		}
		if strings.Contains(text, "Coalesced schedule ticks: 999") {
			t.Fatal("derived count from state sequence")
		}
	}
}

func TestBotProgressSnapshotUncertaintyAndAccessClear(t *testing.T) {
	for _, status := range []int{0, 401, 403, 404, 410} {
		m, b := newProposalReviewModel()
		m.bots = botsState{}
		s := store.BotRunState{SchemaVersion: 1, BotID: b.snapshot.Bot.ID, HomeSessionID: b.snapshot.Bot.HomeSessionID, RunID: uuid.New(), SessionID: uuid.New(), StateSequence: 1, Status: store.RunRunning}
		s.Progress = &store.BotProgressProjection{SchemaVersion: 1, BotID: s.BotID, RunID: s.RunID, SessionID: s.SessionID, Revision: 1, Phase: store.BotProgressEvaluating}
		b.snapshot.RunStates = []store.BotRunState{s}
		m.tr.BotID, m.tr.SessionID = s.BotID.String(), s.HomeSessionID.String()
		m.tr.Apply(events.NewUser("protected conversation"))
		n, _ := m.applyBotSnapshot(botSnapshotMsg{generation: m.botGeneration, snapshot: &b.snapshot})
		m = n.(model)
		var err error = errors.New("connection unavailable")
		if status != 0 {
			err = &client.BotResponseError{StatusCode: status, Cause: err}
		}
		n, _ = m.applyBotSnapshot(botSnapshotMsg{generation: m.botGeneration, err: err})
		m = n.(model)
		if status == 0 {
			if m.botSnapshot == nil || m.tr.Status != "Checking progress" || !strings.Contains(m.botHeaderTitle(), "Updates unavailable") {
				t.Fatal("transport failure erased execution")
			}
		} else if m.botSelected() || m.botSnapshot != nil || len(m.tr.BotRunStates) != 0 {
			t.Fatalf("HTTP %d retained protected state", status)
		}
	}
}
