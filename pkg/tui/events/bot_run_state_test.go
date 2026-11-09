package events

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

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
