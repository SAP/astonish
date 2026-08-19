package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/SAP/astonish/pkg/tui/backend"
)

// rollbackBackend is a staticBackend that also implements RollbackBackend.
type rollbackBackend struct {
	staticBackend
	points    []backend.RollbackPoint
	rolledTo  string
	entries   []backend.HistoryEntry
	rollbackN int
}

func (b *rollbackBackend) ListRollbackPoints(context.Context) ([]backend.RollbackPoint, error) {
	return b.points, nil
}

func (b *rollbackBackend) RollbackTo(_ context.Context, id string) ([]backend.HistoryEntry, error) {
	b.rolledTo = id
	b.rollbackN++
	return b.entries, nil
}

func newRollbackModel(b backend.Backend) model {
	return newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
}

func TestRollbackCapabilityGating(t *testing.T) {
	// A plain backend without RollbackBackend must not expose /rollback.
	plain := newRollbackModel(staticBackend{})
	if plain.rollbackCap() != nil {
		t.Fatal("plain backend should not advertise rollback capability")
	}
	if got := filterSlashCommandsForModel(plain, "roll"); len(got) != 0 {
		t.Fatalf("plain backend offered rollback completion: %v", got)
	}

	// A rollback-capable backend exposes the command in completion.
	rb := newRollbackModel(&rollbackBackend{})
	if rb.rollbackCap() == nil {
		t.Fatal("rollback backend should advertise rollback capability")
	}
	got := filterSlashCommandsForModel(rb, "roll")
	if len(got) != 1 || got[0].Name != "rollback" {
		t.Fatalf("expected /rollback in completion, got %v", got)
	}
}

// filterSlashCommandsForModel mirrors syncSlashCompletion's capability gating so
// the test exercises the same extra-command wiring.
func filterSlashCommandsForModel(m model, query string) []slashCommand {
	var extra []slashCommand
	if m.providerAdmin() != nil {
		extra = append(extra, providerSlashCommand)
	}
	if m.rollbackCap() != nil {
		extra = append(extra, rollbackSlashCommand)
	}
	return filterSlashCommands(query, extra...)
}

func TestRollbackRequiresConfirmation(t *testing.T) {
	b := &rollbackBackend{
		points: []backend.RollbackPoint{
			{ID: "0", Label: "first", MessageText: "first message", TurnNumber: 1},
			{ID: "4", Label: "second", MessageText: "the full second message text", TurnNumber: 2, FileCount: 2},
		},
		entries: []backend.HistoryEntry{{Kind: "user", Text: "first"}},
	}
	m := newRollbackModel(b)
	m.rollback = rollbackState{
		open:   true,
		points: b.points,
		cursor: 1,
	}

	// Enter opens confirmation, does not roll back yet.
	next, cmd := m.handleRollbackKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil {
		t.Fatal("selecting a point should not call backend before confirmation")
	}
	if !m.rollback.confirmRevert {
		t.Fatal("expected confirmation state")
	}
	if b.rollbackN != 0 {
		t.Fatalf("rolled back before confirmation: %d", b.rollbackN)
	}

	// Confirm with 'y' triggers the rollback command.
	next, cmd = m.handleRollbackKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(model)
	if cmd == nil {
		t.Fatal("confirming should return a command")
	}
	if !m.rollback.loading || m.rollback.confirmRevert {
		t.Fatalf("expected loading without confirm, loading=%v confirm=%v", m.rollback.loading, m.rollback.confirmRevert)
	}
	msg := cmd()
	if b.rolledTo != "4" {
		t.Fatalf("rolledTo=%q want 4", b.rolledTo)
	}

	// Applying the result rebuilds history and closes the overlay.
	updated, _ := m.Update(msg)
	m = updated.(model)
	if m.rollback.open {
		t.Fatal("expected rollback overlay to close after applying result")
	}
	// The rolled-back message's full text is prefilled into the composer so the
	// user can edit and resend it without retyping.
	if got := m.ta.Value(); got != "the full second message text" {
		t.Fatalf("composer value = %q, want the rolled-back message text prefilled", got)
	}
}

func TestRollbackEscCancels(t *testing.T) {
	b := &rollbackBackend{points: []backend.RollbackPoint{{ID: "0", Label: "x"}}}
	m := newRollbackModel(b)
	m.rollback = rollbackState{open: true, points: b.points}

	next, _ := m.handleRollbackKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.rollback.open {
		t.Fatal("esc should close the rollback overlay")
	}
	if b.rollbackN != 0 {
		t.Fatal("esc must not roll back")
	}
}

func TestRollbackLoadedDefaultsCursorToMostRecent(t *testing.T) {
	m := newRollbackModel(&rollbackBackend{})
	m.rollback = rollbackState{open: true, loading: true}
	next, _ := m.applyRollbackLoaded(rollbackLoadedMsg{points: []backend.RollbackPoint{
		{ID: "0"}, {ID: "2"}, {ID: "5"},
	}})
	m = next.(model)
	if m.rollback.loading {
		t.Fatal("loading should clear")
	}
	if m.rollback.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (most recent)", m.rollback.cursor)
	}
}
