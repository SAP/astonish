package events

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotRunStateMultiRunOrdering(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		tr := NewTranscript()
		bot, home := uuid.New(), uuid.New()
		tr.BotID, tr.SessionID = bot.String(), home.String()
		a := store.BotRunState{SchemaVersion: 1, BotID: bot, HomeSessionID: home, SessionID: uuid.New(), RunID: uuid.New(), Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunSucceeded, StateSequence: 99}
		b := a
		b.RunID = uuid.New()
		b.Status = store.RunQueued
		b.StateSequence = 1
		states := []store.BotRunState{a, b}
		if reverse {
			states[0], states[1] = b, a
		}
		for _, s := range states {
			tr.Apply(Event{Kind: KindBotRunState, BotRunState: &s})
		}
		if !tr.Streaming || tr.Status != "Queued…" {
			t.Fatalf("old terminal hid queued run: %+v", tr)
		}
		b.Status = store.RunRunning
		b.StateSequence++
		tr.Apply(Event{Kind: KindBotRunState, BotRunState: &b})
		a.StateSequence++
		tr.Apply(Event{Kind: KindBotRunState, BotRunState: &a})
		for _, e := range []Event{NewText("draft"), NewUser("answer"), NewDone(), NewError("transport disconnected")} {
			tr.Apply(e)
			if !tr.Streaming || tr.Status != "Thinking…" {
				t.Fatalf("lost live run after %s: %+v", e.Kind, tr)
			}
		}
		b.Status = store.RunWaitingApproval
		b.StateSequence++
		tr.Apply(Event{Kind: KindBotRunState, BotRunState: &b})
		stale := b
		stale.Status = store.RunRunning
		stale.StateSequence--
		tr.Apply(Event{Kind: KindBotRunState, BotRunState: &stale})
		if tr.Streaming || tr.Status != "Waiting for answer or review" {
			t.Fatalf("waiting state lost: %+v", tr)
		}
	}
}
