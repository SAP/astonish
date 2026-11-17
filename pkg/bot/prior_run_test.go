package bot

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotPriorRunContextValidFor(t *testing.T) {
	current := store.BotRun{BotID: uuid.New(), ID: uuid.New(), Trigger: store.TriggerCron, TriggerID: uuid.New(), CreatedAt: time.Now()}
	prior := BotPriorRunContext{BotID: current.BotID, RunID: uuid.New(), TriggerID: current.TriggerID, CreatedAt: current.CreatedAt.Add(-time.Minute), OutcomeAt: current.CreatedAt.Add(-time.Second), Status: store.RunInterrupted, FailureKind: store.BotReportFailureInterrupted}
	if !prior.ValidFor(current) {
		t.Fatal("interrupted predecessor rejected")
	}
	for _, status := range []store.RunStatus{store.RunSucceeded, store.RunFailed, store.RunPartial, store.RunCancelled, store.RunSkipped} {
		p := prior
		p.Status, p.FailureKind = status, ""
		if !p.ValidFor(current) {
			t.Fatalf("terminal predecessor %s rejected", status)
		}
	}
	for _, status := range []store.RunStatus{store.RunQueued, store.RunRunning, store.RunWaitingApproval, "invented"} {
		p := prior
		p.Status = status
		if p.ValidFor(current) {
			t.Fatalf("nonterminal %s accepted", status)
		}
	}
	manual := current
	manual.Trigger = store.TriggerManual
	if prior.ValidFor(manual) {
		t.Fatal("manual run inherited schedule context")
	}
	p := prior
	p.FailureKind = store.BotReportFailureKind("injected private error")
	if p.ValidFor(current) {
		t.Fatal("unbounded failure kind accepted")
	}
	p = prior
	p.CreatedAt = current.CreatedAt
	if p.ValidFor(current) {
		t.Fatal("current occurrence accepted")
	}
	p = prior
	p.OutcomeAt = current.CreatedAt
	if p.ValidFor(current) {
		t.Fatal("later outcome accepted")
	}
}
