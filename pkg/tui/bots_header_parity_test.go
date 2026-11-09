package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotHeaderSnapshotRefreshAndStaleGeneration(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Name: "Monitor", Status: store.BotActive}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 160, Height: 30})
	for _, status := range []store.BotStatus{store.BotPaused, store.BotDeleted} {
		snap := &store.BotSnapshot{Bot: *b.selected, PendingApprovals: []store.BotApproval{{ID: uuid.New()}}}
		snap.Bot.Status = status
		next, _ := m.Update(botSnapshotMsg{generation: m.botGeneration, snapshot: snap})
		m = next.(model)
		header := stripANSI(m.renderHeader())
		for _, want := range []string{string(status), "Decisions 1"} {
			if !strings.Contains(header, want) {
				t.Fatalf("header missing %q: %s", want, header)
			}
		}
	}
	old := m.botGeneration
	m.detachBotWatch()
	next, cmd := m.Update(botSnapshotMsg{generation: old, snapshot: &store.BotSnapshot{Bot: *b.selected}})
	if next.(model).botSnapshot != nil || cmd != nil {
		t.Fatal("stale snapshot accepted after detach")
	}
}

func TestBotHeaderSnapshotFailureIsUnavailable(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Name: "Monitor", Status: store.BotActive}}}
	m := newModel(context.Background(), Config{Backend: b, Width: 160})
	next, _ := m.Update(botSnapshotMsg{generation: m.botGeneration, err: errors.New("not found")})
	header := stripANSI(next.(model).renderHeader())
	if !strings.Contains(header, "status unavailable") || !strings.Contains(header, "Decisions unavailable") {
		t.Fatal(header)
	}
}

func TestBotSnapshotWorkerReadsAndCancels(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Status: store.BotPaused}}}
	ctx, cancel := context.WithCancel(context.Background())
	ch := watchBotSnapshots(ctx, b, 7)
	select {
	case msg := <-ch:
		if msg.generation != 7 || msg.snapshot.Bot.Status != store.BotPaused {
			t.Fatal(msg)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot did not arrive asynchronously")
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected snapshot after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not cancel")
	}
}

func TestBotAdmissionOnlyAcceptsNoticeAndGuardsGeneration(t *testing.T) {
	b := &testBotBackend{selected: &store.Bot{ID: uuid.New()}}
	m := newModel(context.Background(), Config{Backend: b})
	before := len(m.tr.Items)
	next, _ := m.Update(botAdmissionMsg{generation: m.botGeneration, event: events.NewUser("fabricated")})
	m = next.(model)
	if len(m.tr.Items) != before {
		t.Fatal("admission fabricated transcript")
	}
	next, _ = m.Update(botAdmissionMsg{generation: m.botGeneration, event: events.NewSystem("Run queued: run-id. /bots runs")})
	m = next.(model)
	if len(m.tr.Items) != before+1 {
		t.Fatal("admission notice hidden")
	}
	m.detachBotWatch()
	next, cmd := m.Update(botAdmissionMsg{generation: m.botGeneration - 1, event: events.NewSystem("stale")})
	if len(next.(model).tr.Items) != before+1 || cmd != nil {
		t.Fatal("stale admission accepted")
	}
}
