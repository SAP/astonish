package entstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Forward only the additional capability required by reconciliation. The parent
// failure fixture still fails later, at receipt/mandate metadata persistence.
type queuedScheduleFailDB struct{ botDraftDB }
type queuedScheduleFailTx struct{ authorizedFailTx }

func (d queuedScheduleFailDB) Transaction(ctx context.Context, f func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return f(queuedScheduleFailTx{authorizedFailTx{tx}}) })
}
func (d queuedScheduleFailTx) InsertActivity(ctx context.Context, a store.BotActivity, key string) error {
	if strings.HasPrefix(key, "schedule-reconcile:") {
		return errors.New("injected receipt persistence failure")
	}
	return d.botDraftDB.InsertActivity(ctx, a, key)
}

func (d queuedScheduleFailTx) saveScheduledQueueRun(ctx context.Context, run botStoredRun, version int64) error {
	return d.botDraftDB.(botScheduledQueueDB).saveScheduledQueueRun(ctx, run, version)
}

func applyQueuedScheduleWithBarriers(t *testing.T, r *botRepository, s *botAuthorizedOperationRuntime, owner context.Context, id uuid.UUID, operation store.BotControlOperation, tr store.TriggerSpec, applyHooks ...func(func() error) error) {
	t.Helper()
	b, err := r.Get(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: id, Version: b.Version, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	ref := *a.Run.MandateRef
	worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: id, RunID: a.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: id, RunID: a.Run.ID, WorkerID: "queue-edit", ExpiresAt: r.clock().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.EnqueueAuthorizedOperation(worker, store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: ref, IdempotencyKey: "queue-edit", Payload: store.BotControlPayload{Operation: operation, Reason: "owner requested", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.ListRuns(owner, id, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyAuthorizedOperation(context.Background(), ref, pending.ID); !errors.Is(err, store.ErrBotBusy) {
		t.Fatalf("active lease not fenced: %v", err)
	}
	after, err := r.ListRuns(owner, id, store.BotPage{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("busy apply mutated runs", err)
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "captured"}}); err != nil {
		t.Fatal(err)
	}
	before, err = r.ListRuns(owner, id, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := r.db.GetBot(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	versionBefore, err := r.db.ReadVersion(owner, id, root.Bot.Version)
	if err != nil {
		t.Fatal(err)
	}
	jobs := r.db.(botTriggerDB).triggerScheduler()
	jobsBefore := map[string]*store.ScheduledJob{}
	for _, trigger := range versionBefore.Triggers {
		if trigger.ScheduleID != nil {
			jobsBefore[trigger.ScheduleID.String()] = jobs.Get(owner, trigger.ScheduleID.String())
		}
	}
	original := s.repo.db
	s.repo.db = queuedScheduleFailDB{original}
	_, err = s.ApplyAuthorizedOperation(context.Background(), ref, pending.ID)
	s.repo.db = original
	if err == nil || err.Error() != "injected receipt persistence failure" {
		t.Fatalf("late rollback: %v", err)
	}
	after, err = r.ListRuns(owner, id, store.BotPage{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("queue escaped rollback", err)
	}
	rootAfter, err := r.db.GetBot(owner, id)
	if err != nil || !reflect.DeepEqual(root, rootAfter) {
		t.Fatal("root escaped rollback", err)
	}
	for jobID, beforeJob := range jobsBefore {
		if !reflect.DeepEqual(beforeJob, jobs.Get(owner, jobID)) {
			t.Fatal("scheduler escaped rollback")
		}
	}
	if _, err = r.db.ReadVersion(owner, id, root.Bot.Version+1); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatalf("version escaped rollback: %v", err)
	}
	apply := func() error {
		_, e := s.ApplyAuthorizedOperation(context.Background(), ref, pending.ID)
		return e
	}
	if len(applyHooks) > 0 {
		err = applyHooks[0](apply)
	} else {
		err = apply()
	}
	if err != nil {
		t.Fatal(err)
	}
	after, err = r.ListRuns(owner, id, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyAuthorizedOperation(context.Background(), ref, pending.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := r.ListRuns(owner, id, store.BotPage{})
	if err != nil || !reflect.DeepEqual(after, replay) {
		t.Fatal("apply replay mutated queue", err)
	}
}
