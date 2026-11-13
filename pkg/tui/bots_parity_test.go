package tui

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"strings"
	"testing"
)

type parityBotBackend struct {
	testBotBackend
	fail      bool
	submitted string
}

func (b *parityBotBackend) RunTurn(_ context.Context, text string, _ backend.TurnOptions) (<-chan events.Event, error) {
	b.submitted = text
	if b.fail {
		return nil, errors.New("admission rejected")
	}
	ch := make(chan events.Event)
	close(ch)
	return ch, nil
}
func (b *parityBotBackend) BotSnapshot(context.Context) (*store.BotSnapshot, error) {
	return &store.BotSnapshot{Bot: *b.selected, PendingApprovals: []store.BotApproval{{ID: uuid.New()}}}, nil
}
func (b *parityBotBackend) BotGoals(context.Context) ([]store.BotGoal, error) { return nil, nil }
func (b *parityBotBackend) BotStudioURL() string                              { return "https://platform/chat?bot=test" }
func TestBotDecisionReplyDoesNotReopenClosedOverlay(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	next, cmd := m.Update(botDecisionMsg{generation: m.botGeneration})
	if next.(model).bots.open || cmd != nil {
		t.Fatal("completed decision reopened dismissed overlay")
	}
}

func TestBotPickerIgnoresPreviousOverlayResponse(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	next, old := m.botCommand("/bots")
	m = next.(model)
	next, _ = m.botCommand("/bots goals")
	m = next.(model)
	next, _ = m.Update(old())
	m = next.(model)
	if !m.bots.loading || m.bots.overview != botOverviewGoals {
		t.Fatal("old picker response contaminated new overlay")
	}
}

func TestBotComposerSingleChronologyAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Status: store.BotPaused}}, fail: fail}
		m := newModel(context.Background(), Config{Backend: b})
		m.ta.SetValue("hello")
		next, _ := m.submit()
		m = next.(model)
		if b.submitted != "hello" {
			t.Fatal("paused conversation not submitted")
		}
		if fail && m.ta.Value() != "hello" {
			t.Fatal("failed input lost")
		}
		if !fail && m.ta.Value() != "" {
			t.Fatal("accepted input retained")
		}
		for _, item := range m.tr.Items {
			if item.Kind == events.ItemUser {
				t.Fatal("optimistic user echo duplicated durable chronology")
			}
		}
	}
}
func TestBotComposerReadOnlyTraceGuidance(t *testing.T) {
	const guidance = "selected run trace is read-only; /bots home to converse"
	for _, status := range []store.BotStatus{store.BotDraft, store.BotPaused, store.BotActive} {
		for _, selection := range []string{"trace", "home", "unspecified"} {
			t.Run(string(status)+"/"+selection, func(t *testing.T) {
				home := uuid.New()
				b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{
					ID: uuid.New(), HomeSessionID: home, Status: status,
				}}}
				m := newModel(context.Background(), Config{Backend: b})
				m.width = 120
				switch selection {
				case "trace":
					m.info.SessionID = uuid.New().String()
				case "home":
					m.info.SessionID = home.String()
				case "unspecified":
					m.info.SessionID = ""
				}
				const draft = "  keep my draft  "
				m.ta.SetValue(draft)
				next, cmd := m.submit()
				m = next.(model)
				for _, item := range m.tr.Items {
					if item.Kind == events.ItemUser {
						t.Error("submission created an optimistic user echo")
					}
				}
				if selection != "trace" {
					if b.submitted != strings.TrimSpace(draft) || m.ta.Value() != "" {
						t.Fatal("Home submission was blocked or retained accepted input")
					}
					return
				}
				if b.submitted != "" {
					t.Error("read-only trace called RunTurn")
				}
				if cmd != nil {
					t.Error("read-only trace started an admission command")
				}
				if m.ta.Value() != draft || len(m.history) != 0 {
					t.Error("read-only trace changed the composer or submission history")
				}
				if len(m.tr.Items) != 1 || m.tr.Items[0].Kind != events.ItemSystem || m.tr.Items[0].Content != guidance {
					t.Errorf("missing safe local system notice: %+v", m.tr.Items)
				}
				rendered, _, _ := m.renderTranscript()
				if !strings.Contains(rendered, guidance) {
					t.Errorf("actionable guidance hidden by rendering: %s", rendered)
				}
			})
		}
	}
}

func TestBotOverviewLiveAndRestoreWorkUseOrderedReducer(t *testing.T) {
	id, home, run, session := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: id, HomeSessionID: home}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.width = 140
	m.tr.BotID, m.tr.SessionID = id.String(), home.String()
	work := store.BotWorkProjection{SchemaVersion: 1, Protocol: store.BotWorkProtocolAgentLedV1, BotID: id, RunID: run, WorkRevision: 2, Disposition: store.AgentWorkPartial}
	state := store.BotRunState{SchemaVersion: 1, BotID: id, RunID: run, SessionID: session, HomeSessionID: home, Presentation: store.BotRunPresentationHomeInteractive, StateSequence: 2, Status: store.RunPartial, Work: &work}
	staleWork := work
	staleWork.WorkRevision, staleWork.Disposition = 1, store.AgentWorkAnswered
	stale := state
	stale.StateSequence, stale.Work = 1, &staleWork
	m.bots = botsState{open: true, overview: botOverviewSchedule, snapshot: &store.BotSnapshot{Bot: *b.selected, RunStates: []store.BotRunState{stale}, LatestOutcome: &store.BotRun{ID: run, BotID: id, Work: &staleWork}}}
	m.tr.Apply(events.Event{Kind: events.KindBotRunState, BotRunState: &state})
	live := m.renderBotOverview()
	if !strings.Contains(live, "Partial answer") || strings.Contains(live, "Answer provided") {
		t.Fatal(live)
	}
	// Replay into a fresh transcript must display exactly the same work.
	m.tr = events.NewTranscript()
	m.tr.BotID, m.tr.SessionID = id.String(), home.String()
	m.bots.snapshot.RunStates = []store.BotRunState{state, stale, state}
	if restored := m.renderBotOverview(); restored != live {
		t.Fatalf("restore differs: %s\n%s", live, restored)
	}
	foreign := state
	foreign.BotID, foreign.RunID = uuid.New(), uuid.New()
	m.bots.snapshot.RunStates = append(m.bots.snapshot.RunStates, foreign)
	if got := m.overviewRunStates(m.bots.snapshot); len(got) != 1 {
		t.Fatalf("cross-Bot work leaked: %+v", got)
	}
}

func TestBotOverviewStatesAndStaleRequest(t *testing.T) {
	b := &parityBotBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Name: "Monitor", Status: store.BotPaused}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.width = 100
	next, cmd := m.botCommand("/bots goals")
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Loading") {
		t.Fatal("missing loading")
	}
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(model)
	for _, s := range []string{"No goals", "Pending decisions: 1", "Next run: unavailable", "paused"} {
		if !strings.Contains(m.renderBotsOverlay(), s) {
			t.Fatal(s)
		}
	}
	m.bots.requestID = uuid.New()
	m.bots.loading = true
	next, _ = m.Update(msg)
	if !next.(model).bots.loading {
		t.Fatal("stale overview accepted")
	}
	m.bots.err = "denied"
	m.bots.loading = false
	if !strings.Contains(m.renderBotsOverlay(), "Press r to retry") || !strings.Contains(m.renderBotsOverlay(), "Last known work details") {
		t.Fatal("error or retained-data warning hidden")
	}
	next, cmd = m.openBotOverview(m.bots.overview)
	m = next.(model)
	if cmd == nil || !m.bots.loading || m.bots.snapshot == nil {
		t.Fatal("refresh lost retained snapshot")
	}
}
