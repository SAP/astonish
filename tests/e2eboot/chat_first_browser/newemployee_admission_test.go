package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// A click is not admission. A scheduler can finish in the gap before the
// owner's message/run appears, and must not provide either success or failure.
func TestEmployeePauseScheduledOverlapAdmission(t *testing.T) {
	s, user := employeeScheduleFixture()
	user.Status = store.RunQueued
	s.Operations[0].Operation = store.BotControlPriorityPause
	cron := store.BotRun{ID: uuid.New(), BotID: s.Bot.ID, Trigger: store.TriggerCron, Status: store.RunSucceeded}
	operation := s.Operations[0]
	s.Operations = nil
	if done, failure := employeeChangePollResult(s, []store.BotRun{cron}, nil, nil, store.BotControlPriorityPause, false); done || failure != nil {
		t.Fatalf("click pending admission: scheduled result must not decide pause: done=%v failure=%v", done, failure)
	}
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied} {
		operation.Status = status
		s.Operations = []store.BotOperationView{operation}
		if done, failure := employeeChangePollResult(s, []store.BotRun{user, cron}, nil, nil, store.BotControlPriorityPause, false); failure != nil || done != (status == store.BotOperationApplied) {
			t.Fatalf("target user operation %s: done=%v failure=%v", status, done, failure)
		}
	}
}

func TestEmployeePauseOverlapPersistedAdmissionToReceipt(t *testing.T) {
	s, user := employeeScheduleFixture()
	user.HistoryCursor = 142
	user.Status = store.RunQueued
	message := "Pause all background work until I explicitly ask you to resume."
	cron := user
	cron.ID = uuid.New()
	cron.Trigger = store.TriggerCron
	cron.UserEventID = "scheduled"
	op := s.Operations[0]
	op.Operation = store.BotControlPriorityPause
	s.Operations = nil
	s.PendingProposals = []store.BotControlProposal{{SourceRunID: cron.ID}}
	s.PendingApprovals = []store.BotApproval{{RunID: cron.ID}}
	target := store.BotRun{}
	receipts := 0
	rr := []store.BotRun{cron}
	poll := func() bool {
		return employeeChangePoll(func() store.BotSnapshot { return s }, func() []store.BotRun { return rr }, nil, nil, target, store.BotControlPriorityPause, false, func(v store.BotOperationView, _ store.Bot) {
			if v.SourceRunID != user.ID || v.SourceUserEventID != user.UserEventID {
				t.Fatal("wrong receipt source")
			}
			receipts++
		})
	}
	// Scheduler already running -> click returns -> no persisted user event yet.
	for _, status := range []store.RunStatus{store.RunRunning, store.RunSucceeded, store.RunFailed} {
		rr[0].Status = status
		var err error
		target, err = employeeAdmittedRun("event: done\ndata: {\"persisted\":true}\n\n", message, s.Bot, nil, rr)
		if err != nil || poll() || receipts != 0 {
			t.Fatalf("pending admission: %v", err)
		}
	}
	// Actual persisted user event and run arrive after the click, then operation.
	rr = append(rr, user)
	var err error
	target, err = employeeAdmittedRun(admissionHistory(user.UserEventID, "user", message), message, s.Bot, nil, rr)
	if err != nil || target.ID != user.ID || poll() {
		t.Fatalf("admission: %v", err)
	}
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied} {
		op.Status = status
		s.Operations = []store.BotOperationView{op}
		if poll() != (status == store.BotOperationApplied) {
			t.Fatal(status)
		}
	}
	if receipts != 1 {
		t.Fatalf("receipts=%d", receipts)
	}
}

func admissionHistory(event, author, message string) string {
	raw, _ := json.Marshal(map[string]any{"id": event, "author": author, "content": map[string]any{"role": author, "parts": []map[string]string{{"text": message}}}})
	return "event: transcript\ndata: " + string(raw) + "\n\nevent: done\ndata: {\"persisted\":true}\n\n"
}

func TestEmployeeAdmissionCorrelatesPersistedUserEvent(t *testing.T) {
	s, user := employeeScheduleFixture()
	user.HistoryCursor = 142
	message := "Pause all background work until I explicitly ask you to resume."
	raw := admissionHistory(user.UserEventID, "user", message)
	for _, test := range []struct {
		name    string
		history string
		mutate  func(*store.BotRun)
		old     bool
		want    bool
	}{
		{name: "exact", history: raw, want: true},
		{name: "pending", history: "event: done\ndata: {\"persisted\":true}\n\n"},
		{name: "wrong-text", history: admissionHistory(user.UserEventID, "user", "weather")},
		{name: "assistant-echo", history: admissionHistory(user.UserEventID, "model", message)},
		{name: "wrong-event", history: admissionHistory("peer", "user", message)},
		{name: "cron", history: raw, mutate: func(r *store.BotRun) { r.Trigger = store.TriggerCron }},
		{name: "legacy", history: raw, mutate: func(r *store.BotRun) { r.Presentation = store.BotRunPresentationLegacy }},
		{name: "peer-bot", history: raw, mutate: func(r *store.BotRun) { r.BotID = uuid.New() }},
		{name: "no-cursor", history: raw, mutate: func(r *store.BotRun) { r.HistoryCursor = 0 }},
		{name: "old", history: raw, old: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := user
			if test.mutate != nil {
				test.mutate(&r)
			}
			before := map[uuid.UUID]bool{r.ID: test.old}
			got, err := employeeAdmittedRun(test.history, message, s.Bot, before, []store.BotRun{r})
			if err != nil || (got.ID != uuid.Nil) != test.want {
				t.Fatalf("got=%v err=%v", got.ID, err)
			}
		})
	}
	for _, bad := range []string{"data: {broken}\n\n", ""} {
		if _, err := employeeAdmittedRun(bad, message, s.Bot, nil, []store.BotRun{user}); err == nil {
			t.Fatal("malformed/incomplete admitted")
		}
	}
	peer := user
	peer.ID = uuid.New()
	peer.UserEventID = "second-user"
	duplicate := admissionHistory(user.UserEventID, "user", message) + admissionHistory(peer.UserEventID, "user", message)
	if _, err := employeeAdmittedRun(duplicate, message, s.Bot, nil, []store.BotRun{user, peer}); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestEmployeeAdmissionStudioTrimmedPayload(t *testing.T) {
	s, user := employeeScheduleFixture()
	user.HistoryCursor = 142
	draft := "Save this document:\n\n# Operations brief\n\nBudget: unchanged.\n"
	persisted := "Save this document:\n\n# Operations brief\n\nBudget: unchanged."
	for _, test := range []struct {
		name, draft, persisted string
		want                   bool
	}{
		{name: "document-final-newline", draft: draft, persisted: persisted, want: true},
		{name: "outer-spaces", draft: " \t" + draft + "\r\n", persisted: persisted, want: true},
		{name: "javascript-bom-nbsp", draft: "\ufeff\u00a0" + draft + "\u2029", persisted: persisted, want: true},
		{name: "nel-not-trimmed", draft: "\u0085" + persisted, persisted: "\u0085" + persisted, want: true},
		{name: "nel-must-not-disappear", draft: "\u0085" + persisted, persisted: persisted},
		{name: "interior-newline-changed", draft: draft, persisted: "Save this document:\n# Operations brief\n\nBudget: unchanged."},
		{name: "interior-indent-changed", draft: draft, persisted: "Save this document:\n\n # Operations brief\n\nBudget: unchanged."},
		{name: "persisted-not-normalized", draft: draft, persisted: " " + persisted},
		{name: "empty-draft", draft: " \n\ufeff", persisted: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := employeeAdmittedRun(admissionHistory(user.UserEventID, "user", test.persisted), test.draft, s.Bot, nil, []store.BotRun{user})
			if err != nil || (got.ID == user.ID) != test.want {
				t.Fatalf("Studio payload correlation: got=%v want=%v err=%v", got.ID, test.want, err)
			}
		})
	}
	got, err := employeeAdmittedRun("event: done\ndata: {\"persisted\":true}\n\n", "\n", s.Bot, nil, []store.BotRun{user})
	if err != nil || got.ID != uuid.Nil {
		t.Fatalf("missing event must not match an empty draft: got=%v err=%v", got.ID, err)
	}
}

func TestEmployeeComposerRequiresExactEnabledOwnedUI(t *testing.T) {
	good := employeeComposerState{URL: "owned-home", Input: "Pause", Present: true}
	if err := validateEmployeeComposer(good, "owned-home", "Pause"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*employeeComposerState){
		func(s *employeeComposerState) { s.Disabled = true },
		func(s *employeeComposerState) { s.Present = false },
		func(s *employeeComposerState) { s.Input = "PausePause" },
		func(s *employeeComposerState) { s.URL = "peer-home" },
		func(s *employeeComposerState) { s.Alert = "Bot changed; reload and review before retrying" },
	} {
		bad := good
		mutate(&bad)
		if validateEmployeeComposer(bad, "owned-home", "Pause") == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestEmployeePauseIgnoresScheduledEvidenceAfterAdmission(t *testing.T) {
	for _, runStatus := range []store.RunStatus{store.RunRunning, store.RunSucceeded, store.RunFailed, store.RunCancelled} {
		for _, opStatus := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied, store.BotOperationFailed, store.BotOperationCancelled, store.BotOperationBlocked} {
			for _, targetApplied := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/target-applied=%v", runStatus, opStatus, targetApplied), func(t *testing.T) {
					s, user := employeeScheduleFixture()
					user.Status = store.RunQueued
					user.HistoryCursor = 142
					message := "Pause all background work until I explicitly ask you to resume."
					cron := user
					cron.ID, cron.UserEventID, cron.Trigger, cron.Status = uuid.New(), "scheduled", store.TriggerCron, runStatus
					op := s.Operations[0]
					op.Operation, op.Status = store.BotControlPriorityPause, store.BotOperationApplied
					unrelated := op
					unrelated.OperationID, unrelated.SourceRunID, unrelated.SourceUserEventID, unrelated.Status = uuid.New(), cron.ID, cron.UserEventID, opStatus
					s.Operations = []store.BotOperationView{unrelated}
					if targetApplied {
						s.Operations = append(s.Operations, op)
					}
					s.PendingProposals = []store.BotControlProposal{{SourceRunID: cron.ID}}
					s.PendingApprovals = []store.BotApproval{{RunID: cron.ID}}
					for _, rr := range [][]store.BotRun{{cron, user}, {user, cron}} {
						target, err := employeeAdmittedRun(admissionHistory(user.UserEventID, "user", message), message, s.Bot, nil, rr)
						if err != nil || target.ID != user.ID {
							t.Fatalf("admission: target=%v err=%v", target.ID, err)
						}
						receipts := 0
						done := employeeChangePoll(func() store.BotSnapshot { return s }, func() []store.BotRun { return rr }, nil, nil, target, store.BotControlPriorityPause, false, func(v store.BotOperationView, _ store.Bot) {
							if v.SourceRunID != user.ID || v.SourceUserEventID != user.UserEventID {
								t.Fatal("scheduled evidence generated receipt")
							}
							receipts++
						})
						if done != targetApplied || receipts != map[bool]int{false: 0, true: 1}[targetApplied] {
							t.Fatalf("done=%v receipts=%d", done, receipts)
						}
					}
				})
			}
		}
	}
}

func TestEmployeePauseTargetFailureNotHiddenByScheduledOperation(t *testing.T) {
	for _, cronStatus := range []store.RunStatus{store.RunSucceeded, store.RunFailed, store.RunCancelled} {
		s, user := employeeScheduleFixture()
		user.Status = store.RunSucceeded
		cron := user
		cron.ID = uuid.New()
		cron.UserEventID = "cron"
		cron.Trigger = store.TriggerCron
		cron.Status = cronStatus
		op := s.Operations[0]
		op.Operation = store.BotControlPriorityPause
		op.SourceRunID = cron.ID
		op.SourceUserEventID = cron.UserEventID
		op.Status = store.BotOperationApplied
		s.Operations = []store.BotOperationView{op}
		if done, failure := employeeChangePollResult(s, []store.BotRun{user, cron}, nil, nil, store.BotControlPriorityPause, false); done || failure == nil {
			t.Fatalf("scheduled op hid terminal target: %v %v", done, failure)
		}
	}
}
