package events

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotProgressOrderingAndParity(t *testing.T) {
	bot, home, run, session := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	s := store.BotRunState{SchemaVersion: 1, BotID: bot, HomeSessionID: home, RunID: run, SessionID: session, Status: store.RunRunning, StateSequence: 2, CommittedAt: time.Now(), Progress: &store.BotProgressProjection{SchemaVersion: 1, BotID: bot, RunID: run, SessionID: session, Revision: 2, Phase: store.BotProgressEvaluating}}
	tr := NewTranscript()
	tr.BotID, tr.SessionID = bot.String(), home.String()
	apply := func(s store.BotRunState) { tr.Apply(Event{Kind: KindBotRunState, BotRunState: &s}) }
	apply(s)
	if tr.Status != "Checking progress" || !tr.Streaming {
		t.Fatalf("scheduled Home progress lost: %s", tr.Status)
	}
	for _, mutate := range []func(*store.BotRunState){
		func(s *store.BotRunState) { s.Progress.RunID = uuid.New() },
		func(s *store.BotRunState) { s.Progress.SessionID = uuid.New() },
		func(s *store.BotRunState) { s.Progress.Revision = 1 },
		func(s *store.BotRunState) { s.Progress.ActiveDurationNanos = -1 },
		func(s *store.BotRunState) { s.HomeSessionID = uuid.New() },
	} {
		bad := s
		bad.Progress = s.Progress.Clone()
		bad.StateSequence = 3
		mutate(&bad)
		apply(bad)
		if tr.BotRunStates[run.String()].StateSequence != 2 {
			t.Fatal("invalid progress accepted")
		}
	}
	phases := map[store.BotProgressPhase]string{store.BotProgressWorking: "Working", store.BotProgressEvaluating: "Checking progress", store.BotProgressReplanning: "Adjusting approach", store.BotProgressSynthesizing: "Preparing result", store.BotProgressCheckpointing: "Saving progress", store.BotProgressSuspended: "Paused", store.BotProgressWaitingUser: "Waiting for your input", store.BotProgressWaitingBudget: "Waiting for budget", store.BotProgressWaitingAuthorization: "Authorization required", store.BotProgressReconciling: "Checking an unconfirmed action"}
	for phase, label := range phases {
		s.StateSequence++
		s.Progress = s.Progress.Clone()
		s.Progress.Phase = phase
		apply(s)
		if tr.Status != label {
			t.Fatalf("%s: %s", phase, tr.Status)
		}
	}
	unknown := s
	unknown.Progress = s.Progress.Clone()
	unknown.Progress.SchemaVersion = 99
	unknown.StateSequence++
	apply(unknown)
	if tr.Status != "Thinking…" {
		t.Fatal("unknown optional progress did not fall back")
	}
	s.StateSequence = unknown.StateSequence + 1
	s.Status = store.RunSucceeded
	apply(s)
	if tr.Status != "Completed" || tr.Streaming {
		t.Fatal("checkpoint overrode terminal status")
	}
	restored := NewTranscript()
	restored.BotID, restored.SessionID = bot.String(), home.String()
	restored.Apply(Event{Kind: KindBotRunState, BotRunState: &s})
	if restored.Status != tr.Status {
		t.Fatal("live/restore mismatch")
	}
}
