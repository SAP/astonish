package events

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotAgentWorkStateOrderingAndOverlap(t *testing.T) {
	tr := NewTranscript()
	bot, home, run := uuid.New(), uuid.New(), uuid.New()
	tr.BotID, tr.SessionID = bot.String(), home.String()
	work := store.BotWorkProjection{SchemaVersion: 1, Protocol: store.BotWorkProtocolAgentLedV1, BotID: bot, RunID: run, WorkRevision: 2, Phase: store.BotWorkWorking}
	state := store.BotRunState{SchemaVersion: 1, BotID: bot, RunID: run, HomeSessionID: home, SessionID: uuid.New(), Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunRunning, StateSequence: 2, Work: &work}
	apply := func(s store.BotRunState) { tr.Apply(Event{Kind: KindBotRunState, BotRunState: &s}) }
	apply(state)
	for _, mutate := range []func(*store.BotRunState){
		func(s *store.BotRunState) { s.StateSequence = 1 },
		func(s *store.BotRunState) { s.SessionID = uuid.New() },
		func(s *store.BotRunState) { s.BotID = uuid.New() },
		func(s *store.BotRunState) { s.HomeSessionID = uuid.New() },
		func(s *store.BotRunState) { w := *s.Work; w.WorkRevision = 1; s.Work = &w },
		func(s *store.BotRunState) { w := *s.Work; w.RunID = uuid.New(); s.Work = &w },
	} {
		bad := state
		bad.StateSequence = 3
		mutate(&bad)
		apply(bad)
		if got := tr.BotRunStates[run.String()]; got.StateSequence != 2 {
			t.Fatalf("invalid update accepted: %+v", got)
		}
	}
	other := state
	other.RunID = uuid.New()
	other.SessionID = uuid.New()
	other.Work = nil
	apply(other)
	state.StateSequence = 3
	state.Status = store.RunPartial
	state.Work = nil
	apply(state)
	if !tr.Streaming || tr.BotRunStates[run.String()].Work.WorkRevision != 2 {
		t.Fatal("final cleared unrelated live run or lost work")
	}
	unknown := work
	unknown.SchemaVersion = 99
	unknown.WorkRevision = 3
	state.Work = &unknown
	state.StateSequence = 4
	apply(state)
	if tr.BotRunStates[run.String()].Work.SchemaVersion != 99 {
		t.Fatal("unknown schema unavailable state lost")
	}
}

func TestBotRunStateAuthority(t *testing.T) {
	tr := NewTranscript()
	bot, home, run := uuid.New(), uuid.New(), uuid.New()
	tr.BotID = bot.String()
	tr.SessionID = home.String()
	state := store.BotRunState{SchemaVersion: 1, BotID: bot, RunID: run, HomeSessionID: home, SessionID: uuid.New(), Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunRunning, StateSequence: 2}
	tr.Apply(Event{Kind: KindBotRunState, BotRunState: &state})
	for _, e := range []Event{NewUser("hello"), NewText("draft"), NewDone(), NewError("EOF")} {
		tr.Apply(e)
		if !tr.Streaming || tr.Status != "Thinking…" {
			t.Fatalf("lost durable state after %s: %+v", e.Kind, tr)
		}
	}
	state.Status = store.RunWaitingApproval
	state.StateSequence = 3
	tr.Apply(Event{Kind: KindBotRunState, BotRunState: &state})
	tr.Apply(NewText("old text"))
	if tr.Streaming {
		t.Fatal("waiting run still thinking")
	}
	state.Status = store.RunRunning
	state.StateSequence = 2
	tr.Apply(Event{Kind: KindBotRunState, BotRunState: &state})
	if tr.Streaming {
		t.Fatal("stale state won")
	}
	tr.Reset()
	if len(tr.BotRunStates) != 0 || tr.BotID != "" {
		t.Fatal("reset leaked authority")
	}
}
