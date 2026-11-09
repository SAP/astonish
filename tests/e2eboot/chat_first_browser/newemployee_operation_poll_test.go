package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func employeeChangePollResult(s store.BotSnapshot, rr []store.BotRun, prior, seen map[uuid.UUID]bool, operation store.BotControlOperation, exception bool) (done bool, failure any) {
	defer func() { failure = recover() }()
	// These fixtures name their target independently of operation provenance.
	var target store.BotRun
	for _, r := range rr {
		if !prior[r.ID] && r.Trigger != store.TriggerCron && r.UserEventID != "queued-peer" {
			target = r
			break
		}
	}
	done = employeeChangePoll(func() store.BotSnapshot { return s }, func() []store.BotRun { return rr }, prior, seen, target, operation, exception, func(store.BotOperationView, store.Bot) {})
	return
}

func TestEmployeeChangePollRecordedCadenceSequence(t *testing.T) {
	// Reduced from employee-update-live.md and employee-update-primary-evidence.json:
	// source completed 04:47:00.678; pending edit applied at 04:47:04.202.
	s, r := employeeScheduleFixture()
	s.Bot.ID = uuid.MustParse("0a5f12dd-1b84-402f-9b1d-07d13d2ba13f")
	s.Bot.HomeSessionID = uuid.MustParse("fbb08d61-738d-4ae4-9fde-dfcb820c7fd1")
	r.ID = uuid.MustParse("a456e5fb-63cc-4529-bd37-7e6672db232b")
	r.BotID, r.Status = s.Bot.ID, store.RunSucceeded
	r.UserEventID = "bot-message:" + r.ID.String()
	op := &s.Operations[0]
	op.SourceUserEventID = r.UserEventID
	op.BotID, op.HomeSessionID, op.SourceRunID = s.Bot.ID, s.Bot.HomeSessionID, r.ID
	op.OperationID = uuid.MustParse("39ddf82a-f943-5763-92d6-321663b993d1")
	op.Operation, op.StateSequence = store.BotControlScheduleEdit, 71
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationPending, store.BotOperationApplied} {
		op.Status = status
		if status == store.BotOperationApplied {
			next := time.Date(2026, 11, 9, 4, 48, 0, 0, time.UTC)
			op.Revision, op.StateSequence = 2, 79
			op.VerifiedSchedule = &store.BotOperationScheduleView{TriggerID: uuid.MustParse("47d291b8-3c1e-5d95-a26a-47f00082fdc1"), ScheduleID: uuid.MustParse("6a7cc74a-85de-5a5b-9255-b2c0275f0335"), Version: 3, Enabled: true, NextRun: &next}
		}
		done, failure := employeeChangePollResult(s, []store.BotRun{r}, nil, nil, store.BotControlScheduleEdit, false)
		if failure != nil || done != (status == store.BotOperationApplied) {
			t.Fatalf("recorded cadence %s: done=%v failure=%v", status, done, failure)
		}
	}
}

func TestEmployeeChangePollOperationTypes(t *testing.T) {
	for _, operation := range []store.BotControlOperation{store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlMemoryRemember, store.BotControlPriorityPause, store.BotControlGoalCreate, store.BotControlResumeTasks, store.BotControlMemoryForget} {
		for _, runStatus := range []store.RunStatus{store.RunQueued, store.RunPartial, store.RunSucceeded} {
			t.Run(fmt.Sprintf("%s/%s", operation, runStatus), func(t *testing.T) {
				s, r := employeeScheduleFixture()
				r.Status = runStatus
				s.Operations[0].Operation = operation
				for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied, store.BotOperationFailed, store.BotOperationCancelled, store.BotOperationBlocked} {
					s.Operations[0].Status = status
					done, failure := employeeChangePollResult(s, []store.BotRun{r}, nil, nil, operation, false)
					wantFailure := status == store.BotOperationFailed || status == store.BotOperationCancelled || status == store.BotOperationBlocked
					if done != (status == store.BotOperationApplied) || (failure != nil) != wantFailure {
						t.Fatalf("%s: done=%v failure=%v", status, done, failure)
					}
				}
			})
		}
	}
}

func TestEmployeeChangePollReadsRunsBeforeSnapshot(t *testing.T) {
	s, r := employeeScheduleFixture()
	s.Operations[0].Operation = store.BotControlScheduleEdit
	order := ""
	done := employeeChangePoll(func() store.BotSnapshot {
		order += "snapshot"
		if order != "runs,snapshot" {
			t.Fatalf("torn read order: %s", order)
		}
		return s
	}, func() []store.BotRun { order += "runs,"; return []store.BotRun{r} }, nil, nil, r, store.BotControlScheduleEdit, false, func(store.BotOperationView, store.Bot) { t.Fatal("pending receipt") })
	if done || order != "runs,snapshot" {
		t.Fatalf("done=%v order=%s", done, order)
	}
}

func TestEmployeeChangePollProvenance(t *testing.T) {
	for _, name := range []string{"missing", "source", "bot", "home", "event", "empty-event", "run-bot", "type", "seen", "old-run", "invalid-view"} {
		for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied} {
			t.Run(fmt.Sprintf("%s/%s", name, status), func(t *testing.T) {
				s, r := employeeScheduleFixture()
				prior, seen := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
				op := &s.Operations[0]
				op.Operation, op.Status = store.BotControlScheduleEdit, status
				rr := []store.BotRun{r}
				switch name {
				case "missing":
					s.Operations = nil
				case "source":
					op.SourceRunID = uuid.New()
				case "bot":
					op.BotID = uuid.New()
				case "home":
					op.HomeSessionID = uuid.New()
				case "event":
					op.SourceUserEventID = "other"
				case "empty-event":
					op.SourceUserEventID = ""
				case "run-bot":
					rr[0].BotID = uuid.New()
				case "type":
					op.Operation = store.BotControlScheduleCreate
				case "seen":
					seen[op.OperationID] = true
				case "old-run":
					old := r
					old.ID = uuid.New()
					rr = append(rr, old)
					prior[old.ID] = true
					op.SourceRunID = old.ID
				case "invalid-view":
					op.SchemaVersion = 0
				}
				done, failure := employeeChangePollResult(s, rr, prior, seen, store.BotControlScheduleEdit, false)
				if done || failure == nil {
					t.Fatalf("unrelated/invalid operation accepted: done=%v failure=%v", done, failure)
				}
			})
		}
	}
}

func TestEmployeeChangePollMixedQueuedRuns(t *testing.T) {
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied} {
		for _, peerStatus := range []store.RunStatus{store.RunQueued, store.RunSucceeded} {
			for _, peerCapture := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/capture=%v", status, peerStatus, peerCapture), func(t *testing.T) {
					s, r := employeeScheduleFixture()
					s.Operations[0].Operation, s.Operations[0].Status = store.BotControlScheduleEdit, status
					peer := r
					peer.ID, peer.UserEventID, peer.Status = uuid.New(), "queued-peer", peerStatus
					if peerCapture {
						op := s.Operations[0]
						op.OperationID, op.SourceRunID, op.SourceUserEventID, op.Status = uuid.New(), peer.ID, peer.UserEventID, store.BotOperationPending
						s.Operations = append(s.Operations, op)
					}
					for _, rr := range [][]store.BotRun{{r, peer}, {peer, r}} {
						done, failure := employeeChangePollResult(s, rr, nil, nil, store.BotControlScheduleEdit, false)
						wantFailure := false // unrelated runs never decide the target's operation
						if (failure != nil) != wantFailure || done != (!wantFailure && status == store.BotOperationApplied) {
							t.Fatalf("done=%v failure=%v", done, failure)
						}
					}
				})
			}
		}
	}
	// Historical sources and their operations cannot poison the new pending edit.
	s, r := employeeScheduleFixture()
	s.Operations[0].Operation = store.BotControlScheduleEdit
	old := r
	old.ID = uuid.New()
	stale := s.Operations[0]
	stale.OperationID, stale.SourceRunID, stale.Status = uuid.New(), old.ID, store.BotOperationFailed
	s.Operations = append(s.Operations, stale)
	if done, failure := employeeChangePollResult(s, []store.BotRun{old, r}, map[uuid.UUID]bool{old.ID: true}, nil, store.BotControlScheduleEdit, false); done || failure != nil {
		t.Fatalf("old run: done=%v failure=%v", done, failure)
	}
}

func TestEmployeeChangePollSiblingFailureBeforeReceipt(t *testing.T) {
	for _, readyStatus := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplied, store.BotOperationBlocked} {
		for _, badStatus := range []store.BotOperationStatus{store.BotOperationFailed, store.BotOperationCancelled, store.BotOperationBlocked} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/reverse=%v", readyStatus, badStatus, reverse), func(t *testing.T) {
					s, r := employeeScheduleFixture()
					s.Operations[0].Status = readyStatus
					exception := readyStatus == store.BotOperationBlocked
					if exception {
						s.Operations[0].Decision = store.BotOperationRequireAuthorization
					}
					bad := s.Operations[0]
					bad.OperationID, bad.Status, bad.Decision = uuid.New(), badStatus, store.BotOperationExecute
					s.Operations = append(s.Operations, bad)
					if reverse {
						s.Operations[0], s.Operations[1] = s.Operations[1], s.Operations[0]
					}
					done, failure := employeeChangePollResult(s, []store.BotRun{r}, nil, nil, store.BotControlScheduleCreate, exception)
					if done || failure == nil {
						t.Fatalf("sibling masked failure: done=%v failure=%v", done, failure)
					}
				})
			}
		}
	}
}

func TestEmployeeChangePollEmptyAndReviewStates(t *testing.T) {
	for _, state := range []string{"empty", "queued-no-operation", "terminal-no-operation", "proposal", "approval"} {
		t.Run(state, func(t *testing.T) {
			s, r := employeeScheduleFixture()
			var rr []store.BotRun
			switch state {
			case "empty":
				s.Operations = nil
			case "queued-no-operation":
				s.Operations = nil
				r.Status = store.RunQueued
				rr = []store.BotRun{r}
			case "terminal-no-operation":
				s.Operations = nil
				rr = []store.BotRun{r}
			case "proposal":
				s.PendingProposals = []store.BotControlProposal{{SourceRunID: r.ID}}
				rr = []store.BotRun{r}
			case "approval":
				s.PendingApprovals = []store.BotApproval{{RunID: r.ID}}
				rr = []store.BotRun{r}
			}
			done, failure := employeeChangePollResult(s, rr, nil, nil, store.BotControlScheduleCreate, false)
			wantFailure := state != "empty" && state != "queued-no-operation"
			if done || (failure != nil) != wantFailure {
				t.Fatalf("done=%v failure=%v", done, failure)
			}
		})
	}
}

func TestEmployeeChangePollExceptionDecision(t *testing.T) {
	for _, exception := range []bool{false, true} {
		for _, status := range []store.BotOperationStatus{store.BotOperationBlocked, store.BotOperationApplied} {
			for _, decision := range []store.BotOperationPolicyDecision{store.BotOperationExecute, store.BotOperationRequireAuthorization} {
				t.Run(fmt.Sprintf("exception=%v/%s/%s", exception, status, decision), func(t *testing.T) {
					s, r := employeeScheduleFixture()
					s.Operations[0].Operation, s.Operations[0].Status, s.Operations[0].Decision = store.BotControlMemoryForget, status, decision
					done, failure := employeeChangePollResult(s, []store.BotRun{r}, nil, nil, store.BotControlMemoryForget, exception)
					expected := (exception && status == store.BotOperationBlocked && decision == store.BotOperationRequireAuthorization) || (!exception && status == store.BotOperationApplied && decision == store.BotOperationExecute)
					if done != expected || (failure == nil) != expected {
						t.Fatalf("done=%v failure=%v", done, failure)
					}
				})
			}
		}
	}
}
