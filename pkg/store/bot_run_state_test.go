package store

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotAgentWorkProjectionContract(t *testing.T) {
	w := NewBotAgentWorkState(attachmentWorkContract())
	w.AgentWork.Notes = "private working notes"
	p := BuildBotWorkProjection(w)
	if p == nil || p.Validate() != nil {
		t.Fatal("missing valid work projection")
	}
	state := BotRunState{Work: p}
	run := BotRun{Work: p}
	report := BotReport{Work: p}
	if state.Work.Protocol != run.Work.Protocol || report.Work.RunID != p.RunID {
		t.Fatal("inconsistent projection contracts")
	}
	p.SchemaVersion++
	if p.Validate() == nil {
		t.Fatal("unknown projection schema accepted")
	}
}

func TestBotRunStateEvent(t *testing.T) {
	b := Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	run := BotRun{ID: uuid.New(), BotID: b.ID, SessionID: uuid.New(), Presentation: BotRunPresentationHomeInteractive, UserEventID: "user-event"}
	for _, status := range []RunStatus{RunQueued, RunRunning, RunWaitingApproval, RunInterrupted, RunSucceeded, RunPartial, RunFailed, RunSkipped, RunCancelled} {
		run.Status = status
		event, err := NewBotRunStateEvent(b, run, 9, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		state := event.Actions.StateDelta[BotRunStateKey].(BotRunState)
		if event.Content != nil || state.Status != status || state.StateSequence != 9 || state.UserEventID != run.UserEventID {
			t.Fatalf("event: %+v", event)
		}
		retry, err := NewBotRunStateEvent(b, run, 9, time.Now())
		if err != nil || retry.ID != event.ID {
			t.Fatal("non deterministic event ID")
		}
	}
	run.Status = RunStatus("unknown")
	if _, err := NewBotRunStateEvent(b, run, 9, time.Now()); err == nil {
		t.Fatal("unknown status accepted")
	}
	run.Status = RunQueued
	if _, err := NewBotRunStateEvent(b, run, 0, time.Now()); err == nil {
		t.Fatal("invalid sequence accepted")
	}
}
