package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func employeeScheduleFixture() (store.BotSnapshot, store.BotRun) {
	b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	r := store.BotRun{ID: uuid.New(), BotID: b.ID, UserEventID: "schedule-message", Trigger: store.TriggerManual, Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunPartial}
	op := store.BotOperationView{SchemaVersion: 1, BotID: b.ID, HomeSessionID: b.HomeSessionID, OperationID: uuid.New(), Revision: 1, StateSequence: 1, SourceRunID: r.ID, SourceUserEventID: r.UserEventID, Operation: store.BotControlScheduleCreate, Status: store.BotOperationPending, Decision: store.BotOperationExecute}
	return store.BotSnapshot{Bot: b, Operations: []store.BotOperationView{op}}, r
}

func employeeSchedulePollResult(s store.BotSnapshot, rr []store.BotRun, before, pending map[uuid.UUID]bool) (done bool, failure any) {
	defer func() { failure = recover() }()
	done = employeeSchedulePoll(func() store.BotSnapshot { return s }, func() []store.BotRun { return rr }, before, pending, rr[0], func() {}, func(op store.BotOperationView, b store.Bot) { must(validateEmployeeSchedule(op, b, pending)) })
	return
}

func TestEmployeeSchedulePollWaitsForSourceOperation(t *testing.T) {
	for _, runStatus := range []store.RunStatus{store.RunPartial, store.RunSucceeded} {
		for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying} {
			t.Run(string(runStatus)+"/"+string(status), func(t *testing.T) {
				s, r := employeeScheduleFixture()
				r.Status = runStatus
				s.Operations[0].Status = status
				pending := map[uuid.UUID]bool{}
				for i := 0; i < 3; i++ {
					if done, failure := employeeSchedulePollResult(s, []store.BotRun{r}, nil, pending); done || failure != nil {
						t.Fatalf("captured operation must wait, not pass or fail: done=%v failure=%v", done, failure)
					}
				}
				if pending[s.Operations[0].OperationID] != (status == store.BotOperationPending) {
					t.Fatal("applying must not invent pending evidence")
				}
			})
		}
	}
}

func TestEmployeeSchedulePollUnrelatedCannotSuppressTerminalFailure(t *testing.T) {
	for _, name := range []string{"missing", "source", "bot", "home", "event", "missing-event", "operation", "old-run"} {
		t.Run(name, func(t *testing.T) {
			s, r := employeeScheduleFixture()
			before := map[uuid.UUID]bool{}
			switch name {
			case "missing":
				s.Operations = nil
			case "source":
				s.Operations[0].SourceRunID = uuid.New()
			case "bot":
				s.Operations[0].BotID = uuid.New()
			case "home":
				s.Operations[0].HomeSessionID = uuid.New()
			case "event":
				s.Operations[0].SourceUserEventID = "other-message"
			case "missing-event":
				s.Operations[0].SourceUserEventID = ""
			case "operation":
				s.Operations[0].Operation = store.BotControlMemoryRemember
			case "old-run":
				old := uuid.New()
				before[old] = true
				s.Operations[0].SourceRunID = old
			}
			for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied} {
				if len(s.Operations) > 0 {
					s.Operations[0].Status = status
				}
				done, failure := employeeSchedulePollResult(s, []store.BotRun{r}, before, map[uuid.UUID]bool{})
				if done || !strings.Contains(fmt.Sprint(failure), "employee turn terminal without expected durable operation") {
					t.Fatalf("%s: unrelated operation hid missing source operation: done=%v failure=%v", status, done, failure)
				}
			}
		})
	}
}

func TestEmployeeSchedulePollTerminalOperationFails(t *testing.T) {
	for _, status := range []store.BotOperationStatus{store.BotOperationFailed, store.BotOperationBlocked, store.BotOperationCancelled} {
		t.Run(string(status), func(t *testing.T) {
			s, r := employeeScheduleFixture()
			other := s.Operations[0]
			other.OperationID = uuid.New()
			s.Operations[0].Status = status
			// A pending sibling must not mask a failed operation from this source.
			s.Operations = append([]store.BotOperationView{other}, s.Operations...)
			done, failure := employeeSchedulePollResult(s, []store.BotRun{r}, nil, map[uuid.UUID]bool{})
			if done || !strings.Contains(fmt.Sprint(failure), "employee operation terminal/blocked: "+string(status)) {
				t.Fatalf("done=%v failure=%v", done, failure)
			}
		})
	}
}

func TestEmployeeSchedulePollReadsRunsBeforeSnapshot(t *testing.T) {
	s, r := employeeScheduleFixture()
	runRead := false
	defer func() {
		if failure := recover(); failure != nil {
			t.Fatalf("torn observation: %v", failure)
		}
	}()
	done := employeeSchedulePoll(func() store.BotSnapshot {
		if !runRead {
			s.Operations = nil
		} // Capture races with source completion.
		return s
	}, func() []store.BotRun { runRead = true; return []store.BotRun{r} }, nil, map[uuid.UUID]bool{}, r, func() {}, func(store.BotOperationView, store.Bot) { t.Fatal("not yet applied") })
	if done {
		t.Fatal("pending is not completion")
	}
}

func TestEmployeeSchedulePollPendingApplyingApplied(t *testing.T) {
	s, r := employeeScheduleFixture()
	pending := map[uuid.UUID]bool{}
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationApplying, store.BotOperationApplied} {
		s.Operations[0].Status = status
		s.Operations[0].Revision++
		if status == store.BotOperationApplied {
			next := time.Now().Add(time.Minute)
			s.Operations[0].VerifiedSchedule = &store.BotOperationScheduleView{TriggerID: uuid.New(), ScheduleID: uuid.New(), Version: 2, Enabled: true, NextRun: &next}
		}
		done, failure := employeeSchedulePollResult(s, []store.BotRun{r}, nil, pending)
		if failure != nil || done != (status == store.BotOperationApplied) {
			t.Fatalf("%s: done=%v failure=%v", status, done, failure)
		}
	}
}

func TestEmployeeSchedulePollMissingAfterPendingStillFails(t *testing.T) {
	s, r := employeeScheduleFixture()
	pending := map[uuid.UUID]bool{}
	if done, failure := employeeSchedulePollResult(s, []store.BotRun{r}, nil, pending); done || failure != nil {
		t.Fatalf("pending: done=%v failure=%v", done, failure)
	}
	s.Operations = nil
	if done, failure := employeeSchedulePollResult(s, []store.BotRun{r}, nil, pending); done || failure == nil {
		t.Fatalf("historical pending cannot hide missing current operation: done=%v failure=%v", done, failure)
	}
}

func TestEmployeeSchedulePollAppliedIgnoresOtherTerminalRun(t *testing.T) {
	s, r := employeeScheduleFixture()
	s.Operations[0].Status = store.BotOperationApplied
	next := time.Now().Add(time.Minute)
	s.Operations[0].VerifiedSchedule = &store.BotOperationScheduleView{TriggerID: uuid.New(), ScheduleID: uuid.New(), Version: 2, Enabled: true, NextRun: &next}
	peer := r
	peer.ID = uuid.New()
	pending := map[uuid.UUID]bool{s.Operations[0].OperationID: true}
	done, failure := employeeSchedulePollResult(s, []store.BotRun{r, peer}, nil, pending)
	if !done || failure != nil {
		t.Fatalf("unrelated terminal run poisoned target operation: done=%v failure=%v", done, failure)
	}
}

func TestEmployeeSchedulePollAppliedStillRequiresDurableEvidence(t *testing.T) {
	for _, name := range []string{"pending", "schedule", "job", "trigger", "version", "enabled", "removed", "next-run", "zero-next-run"} {
		t.Run(name, func(t *testing.T) {
			s, r := employeeScheduleFixture()
			op := &s.Operations[0]
			op.Status = store.BotOperationApplied
			next := time.Now().Add(time.Minute)
			op.VerifiedSchedule = &store.BotOperationScheduleView{TriggerID: uuid.New(), ScheduleID: uuid.New(), Version: 2, Enabled: true, NextRun: &next}
			pending := map[uuid.UUID]bool{op.OperationID: true}
			switch name {
			case "pending":
				pending = nil
			case "schedule":
				op.VerifiedSchedule = nil
			case "job":
				op.VerifiedSchedule.ScheduleID = uuid.Nil
			case "trigger":
				op.VerifiedSchedule.TriggerID = uuid.Nil
			case "version":
				op.VerifiedSchedule.Version = 0
			case "enabled":
				op.VerifiedSchedule.Enabled = false
			case "removed":
				op.VerifiedSchedule.Removed = true
			case "next-run":
				op.VerifiedSchedule.NextRun = nil
			case "zero-next-run":
				op.VerifiedSchedule.NextRun = &time.Time{}
			}
			if done, failure := employeeSchedulePollResult(s, []store.BotRun{r}, nil, pending); done || failure == nil {
				t.Fatalf("invalid applied operation accepted: done=%v failure=%v", done, failure)
			}
		})
	}
}

func TestEmployeeRestartRequiresExactTriggerAndNewExecution(t *testing.T) {
	restarted := time.Now()
	started := restarted.Add(time.Second)
	bot, trigger := uuid.New(), uuid.New()
	r := store.BotRun{BotID: bot, Trigger: store.TriggerCron, TriggerID: trigger, CreatedAt: started, StartedAt: &started}
	if !employeePostRestartRun(r, bot, trigger, restarted) {
		t.Fatal("valid run rejected")
	}
	for _, mutate := range []func(*store.BotRun){
		func(r *store.BotRun) { r.BotID = uuid.New() },
		func(r *store.BotRun) { r.Trigger = store.TriggerManual },
		func(r *store.BotRun) { r.TriggerID = uuid.New() },
		func(r *store.BotRun) { r.CreatedAt = restarted.Add(-time.Second) },
		func(r *store.BotRun) { r.StartedAt = nil },
		func(r *store.BotRun) { r.StartedAt = &restarted },
	} {
		bad := r
		mutate(&bad)
		if employeePostRestartRun(bad, bot, trigger, restarted) {
			t.Fatal("stale/unrelated run accepted")
		}
	}
}

func TestEmployeeForgetLedgerRejectsReplayInAnyState(t *testing.T) {
	denied, approved := uuid.New(), uuid.New()
	ops := []store.BotOperationView{{Operation: store.BotControlMemoryForget, OperationID: denied, Status: store.BotOperationCancelled}, {Operation: store.BotControlMemoryForget, OperationID: approved, Status: store.BotOperationApplied}}
	if err := employeeForgetLedger(ops, denied, approved); err != nil {
		t.Fatal(err)
	}
	for _, status := range []store.BotOperationStatus{store.BotOperationPending, store.BotOperationBlocked, store.BotOperationApplied, store.BotOperationCancelled} {
		bad := append(append([]store.BotOperationView{}, ops...), store.BotOperationView{Operation: store.BotControlMemoryForget, OperationID: uuid.New(), Status: status})
		if employeeForgetLedger(bad, denied, approved) == nil {
			t.Fatal("replay accepted", status)
		}
	}
	ops[0].Status = store.BotOperationApplied
	if employeeForgetLedger(ops, denied, approved) == nil {
		t.Fatal("denial executed")
	}
}

func TestEmployeeHomeVolumeDecodesOpaqueBinding(t *testing.T) {
	bot := uuid.New()
	outer, _ := json.Marshal(store.BotHomeBinding{ID: uuid.New(), BotID: bot, Scope: store.BotScopePersonal, Backend: string(sandbox.BackendKindDocker), StorageKey: "owned-home-volume", Revision: 1})
	got, err := employeeHomeVolume(string(outer), bot.String())
	if err != nil || got != "owned-home-volume" {
		t.Fatal(got, err)
	}
	if _, err := employeeHomeVolume(string(outer), uuid.NewString()); err == nil {
		t.Fatal("peer binding accepted")
	}
	if _, err := employeeHomeVolume(`{"storage_key":"not-a-volume"}`, bot.String()); err == nil {
		t.Fatal("opaque JSON mistaken for volume")
	}
}

func TestEmployeeWebEvidenceRequiresPairedIdentity(t *testing.T) {
	good := `{"messages":[{"type":"tool_call","toolId":"x","toolName":"http_request"},{"type":"tool_result","toolId":"x","toolName":"http_request","toolResult":{"status":200,"body":"Orlando weather today is sunny and warm with a light breeze"}}]}`
	if !employeeWebEvidence(good) {
		t.Fatal("paired HTTP result rejected")
	}
	for _, raw := range []string{`{"messages":[{"type":"tool_result","toolResult":{"status":200,"body":"Orlando weather today is sunny and warm with a light breeze"}}]}`, `{"messages":[{"type":"tool_result","toolId":"x","toolName":"http_request","toolResult":{"status":200,"body":"Orlando weather today is sunny and warm with a light breeze"}}]}`} {
		if employeeWebEvidence(raw) {
			t.Fatal("unpaired result accepted")
		}
	}
}

func TestEmployeeDiskRunUsesActualSucceededConstant(t *testing.T) {
	r := store.BotRun{ID: uuid.New(), BotID: uuid.New(), Status: store.RunSucceeded, Trigger: store.TriggerCron}
	for _, value := range []any{r, map[string]any{"run": r}} {
		raw, _ := json.Marshal(value)
		got := employeeRun(employeeDiskRow{ID: r.ID.String(), Data: string(raw)})
		if got.Status != store.RunSucceeded || got.Trigger != store.TriggerCron {
			t.Fatal(got)
		}
	}
	var disk employeeDisk
	if err := json.Unmarshal([]byte(`{"events":[{"id":3}],"jobs":[{"id":"job"}]}`), &disk); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeExceptionRequiresExactSavedMemory(t *testing.T) {
	id := uuid.New()
	op := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: id, Revision: 1, StateSequence: 1, Operation: store.BotControlMemoryForget, Status: store.BotOperationBlocked, Decision: store.BotOperationRequireAuthorization, Exception: &store.BotOperationExceptionView{OperationID: id, PayloadDigest: "digest", ExpiresAt: time.Now().Add(time.Minute), Fields: []store.BotOperationFieldView{{Caption: "Memory", Value: "memory-id"}}}}
	if err := employeeException(op); err != nil {
		t.Fatal(err)
	}
	card := employeeExceptionDOM{ID: id.String(), Heading: "Forget information · Blocked", Scope: "Permission requested for this saved action only.", Fields: []store.BotOperationFieldView{{Caption: "Memory", Value: "memory-id"}, {Caption: "Expires", Value: op.Exception.ExpiresAt.UTC().Format(time.RFC3339Nano)}}}
	if err := employeeExceptionCard(op, card); err != nil {
		t.Fatalf("exact bounded card must not require an invented permissions caption: %v", err)
	}
	card.Fields[0].Value = "another-memory"
	if employeeExceptionCard(op, card) == nil {
		t.Fatal("card for a different memory accepted")
	}
	for _, mutate := range []func(*store.BotOperationView){
		func(o *store.BotOperationView) { o.Operation = store.BotControlTaskOnceOperation },
		func(o *store.BotOperationView) { o.Exception = nil },
		func(o *store.BotOperationView) { o.Status = store.BotOperationApplied },
	} {
		bad := op
		mutate(&bad)
		if employeeException(bad) == nil {
			t.Fatal("unsafe exception accepted")
		}
	}
}

func TestEmployeeScheduleRequiresActualSourceAndTransition(t *testing.T) {
	b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	now := time.Now().Add(time.Minute)
	v := store.BotOperationView{SchemaVersion: 1, BotID: b.ID, HomeSessionID: b.HomeSessionID, OperationID: uuid.New(), Revision: 3, StateSequence: 5, SourceRunID: uuid.New(), SourceUserEventID: "event", Operation: store.BotControlScheduleCreate, Status: store.BotOperationApplied, Decision: store.BotOperationExecute, VerifiedSchedule: &store.BotOperationScheduleView{TriggerID: uuid.New(), ScheduleID: uuid.New(), Version: 2, Enabled: true, NextRun: &now}}
	pending := map[uuid.UUID]bool{v.OperationID: true}
	if err := validateEmployeeSchedule(v, b, pending); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schema", "home", "source", "pending", "enabled", "next-run", "job", "status"} {
		t.Run(name, func(t *testing.T) {
			c := v
			s := *v.VerifiedSchedule
			c.VerifiedSchedule = &s
			p := pending
			switch name {
			case "schema":
				c.SchemaVersion = 0
			case "home":
				c.HomeSessionID = uuid.New()
			case "source":
				c.SourceUserEventID = ""
			case "pending":
				p = nil
			case "enabled":
				s.Enabled = false
			case "next-run":
				s.NextRun = nil
			case "job":
				s.ScheduleID = uuid.Nil
			case "status":
				c.Status = store.BotOperationPending
			}
			if validateEmployeeSchedule(c, b, p) == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}
