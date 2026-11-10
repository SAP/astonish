//go:build integration

package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// TestBotWorkPostgresCASAndReopen exercises only databases owned by the existing
// disposable fixture; the guard rejects ordinary development/production DSNs.
func TestBotWorkPostgresCASAndReopen(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		workStore := f.bots.(store.BotWorkStore)
		original, err := workStore.LoadWork(f.worker, f.lease)
		if err != nil || original == nil {
			t.Fatalf("load pinned work: %+v %v", original, err)
		}
		if original.Phase != store.BotWorkPreparing || original.Contract.BotID != f.run.BotID || original.Contract.RunID != f.run.ID || original.Contract.Instructions != "Observe ledger" {
			t.Fatalf("unexpected pinned contract: %+v", original)
		}
		next := original.Clone()
		next.Phase = store.BotWorkProducing
		next.Criteria = []store.BotWorkCriterion{{ID: "ledger", SourceIDs: []string{"assignment", "charter"}, Text: original.Contract.Instructions}}
		const attempts = 8
		start, results := make(chan struct{}), make(chan error, attempts)
		for range attempts {
			go func() {
				<-start
				_, e := workStore.SaveWork(f.worker, store.BotWorkUpdate{Lease: f.lease, ExpectedRevision: original.Revision, State: *next.Clone()})
				results <- e
			}()
		}
		close(start)
		successes := 0
		for range attempts {
			if e := <-results; e == nil {
				successes++
			} else if !errors.Is(e, store.ErrBotConflict) {
				t.Errorf("CAS race: %v", e)
			}
		}
		if successes != 1 {
			t.Fatalf("CAS winners=%d want 1", successes)
		}
		saved, err := workStore.LoadWork(f.worker, f.lease)
		if err != nil || saved == nil || saved.Revision != original.Revision+1 || saved.Phase != store.BotWorkProducing || !reflect.DeepEqual(saved.Contract, original.Contract) || !reflect.DeepEqual(saved.Criteria, next.Criteria) {
			t.Fatalf("CAS snapshot: %+v %v", saved, err)
		}
		changed := saved.Clone()
		changed.Contract.Instructions = "weakened instructions"
		if _, err = workStore.SaveWork(f.worker, store.BotWorkUpdate{Lease: f.lease, ExpectedRevision: saved.Revision, State: *changed}); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("mutable assignment: %v", err)
		}
		foreign := f.lease
		foreign.RunID = uuid.New()
		if _, err = workStore.LoadWork(f.worker, foreign); err == nil {
			t.Fatal("foreign run read accepted")
		}
		if _, err = workStore.SaveWork(f.worker, store.BotWorkUpdate{Lease: foreign, ExpectedRevision: saved.Revision, State: *saved.Clone()}); err == nil {
			t.Fatal("foreign run write accepted")
		}
		staleLease := f.lease
		staleLease.Fence++
		if _, err = workStore.SaveWork(f.worker, store.BotWorkUpdate{Lease: staleLease, ExpectedRevision: saved.Revision, State: *saved.Clone()}); err == nil {
			t.Fatal("unowned fence write accepted")
		}
		f.reopenReviewed(t)
		workStore = f.bots.(store.BotWorkStore)
		restored, err := workStore.LoadWork(f.worker, f.lease)
		if err != nil || !reflect.DeepEqual(saved, restored) {
			t.Fatalf("snapshot changed after close/reopen: %+v %v", restored, err)
		}
		if _, err = workStore.SaveWork(f.worker, store.BotWorkUpdate{Lease: f.lease, ExpectedRevision: original.Revision, State: *next}); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("stale revision after reopen: %v", err)
		}
		restored, err = workStore.LoadWork(f.worker, f.lease)
		if err != nil || !reflect.DeepEqual(saved, restored) {
			t.Fatalf("rejected update changed reopened work: %+v %v", restored, err)
		}
	})
}

// All late-change cases use actual routed PG stores and the ownership-guarded DSN.
func latestPostgresAuthorizedFixture(t *testing.T, scope store.BotScope) (*postgresBotServicesFixture, *botAuthorizedOperationRuntime, store.BotAuthorizedOperationRequest) {
	t.Helper()
	requireDraftActivationPostgres(t)
	f := newPostgresBotServicesFixture(t, scope, 100, 100)
	if err := f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, f.worker, f.lease, "fixture complete")}); err != nil {
		t.Fatal(err)
	}
	f.owner = store.WithBotMandateAdmission(mandateOwnerContext(t, f.owner), authorizedSchedulePolicy)
	b, err := f.repo.Get(f.owner, f.run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.repo.AdmitMessage(f.owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	ref := *a.Run.MandateRef
	f.worker = execution.WithBotRunIdentity(f.owner, execution.BotRunIdentity{BotID: b.ID, RunID: a.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
	f.lease, err = f.repo.ClaimRun(f.worker, store.BotClaimRequest{BotID: b.ID, RunID: a.Run.ID, WorkerID: "latest-pg", ExpiresAt: time.Now().Add(4 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	f.run = &a.Run
	runtime, err := NewBotAuthorizedOperationRuntime(f.bots, authorizedSchedulePolicy, func(_ context.Context, a store.BotScheduleAuthorization) error {
		if a.Instructions != "Every day at nine, read the report. Keep other schedules unchanged." || a.Provenance.UserEventID == "" {
			return store.ErrBotForbidden
		}
		return nil
	}, func(context.Context, store.Bot, store.BotVersion) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	q := store.BotAuthorizedOperationRequest{Lease: f.lease, MandateRef: ref, IdempotencyKey: "schedule", Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "Requested daily report", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: "UTC", Instructions: "read the report", Enabled: true}}}}
	return f, runtime.(*botAuthorizedOperationRuntime), q
}

func latestPostgresReopen(t *testing.T, f *postgresBotServicesFixture, s *botAuthorizedOperationRuntime) {
	t.Helper()
	f.reopenReviewed(t)
	s.repo = f.repo
}

func TestIntegrationPostgresBotLatestSourceBoundDenial(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, legacy := range []bool{false, true} {
			name := "source-bound"
			if legacy {
				name = "legacy"
			}
			t.Run(name, func(t *testing.T) {
				f, s, q := latestPostgresAuthorizedFixture(t, scope)
				s.policy = func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					p, e := authorizedSchedulePolicy(ctx, ref)
					p.ExceptionConstraints = []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{RoutineControlResource(store.BotControlMemoryForget)}}}
					return p, e
				}
				b, err := f.repo.Get(f.owner, q.MandateRef.BotID)
				if err != nil {
					t.Fatal(err)
				}
				memoryID := uuid.New()
				if err = insertControlMemory(f.owner, f.repo.db, memoryID, f.run.SessionID, b.OwnerID, "Celsius", "fact", nil, time.Now()); err != nil {
					t.Fatal(err)
				}
				original, err := f.repo.db.ReadCorrectionMemory(f.owner, memoryID.String())
				if err != nil {
					t.Fatal(err)
				}
				q.Payload = store.BotControlPayload{Operation: store.BotControlMemoryForget, Reason: "forget preference", Memory: &store.BotControlMemory{ExpectedRevision: b.Revision, MemoryID: memoryID.String(), ExpectedFingerprint: store.MemoryContentFingerprint(*original)}}
				op, err := s.EnqueueAuthorizedOperation(f.worker, q)
				if err != nil {
					t.Fatal(err)
				}
				denied, err := s.DecideRoutineException(f.owner, BotRoutineExceptionDecision{MandateRef: q.MandateRef, OperationID: op.ID, ExpectedRevision: op.Revision, PayloadDigest: op.Provenance.PayloadDigest})
				if err != nil {
					t.Fatal(err)
				}
				if legacy {
					if err = f.repo.db.Transaction(f.owner, func(tx botDraftDB) error {
						rec, e := tx.GetBot(f.owner, b.ID)
						if e != nil {
							return e
						}
						m, e := readMandate(f.owner, tx, q.MandateRef)
						if e != nil {
							return e
						}
						p, e := s.policy(f.owner, q.MandateRef)
						if e != nil {
							return e
						}
						delete(rec.Metadata, routineSourceSuppressionKey(q.Payload, m.Mandate, p, op.Provenance.SourceDigest))
						rec.Metadata[routineSuppressionKey(q.Payload, m.Mandate, p)] = true
						return saveMandateMetadata(f.owner, tx, rec)
					}); err != nil {
						t.Fatal(err)
					}
				}
				latestPostgresReopen(t, f, s)
				retry := q
				retry.IdempotencyKey = "model-new-call"
				retry.Payload.Reason = "owner changed their mind (model claim only)"
				if _, err = s.EnqueueAuthorizedOperation(f.worker, retry); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("same-source suppression: %v", err)
				}
				same, err := s.EnqueueAuthorizedOperation(f.worker, q)
				if err != nil || !reflect.DeepEqual(same, denied) {
					t.Fatalf("denial replay: %+v %v", same, err)
				}
				if err = f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, f.worker, q.Lease, "denied")}); err != nil {
					t.Fatal(err)
				}
				before, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				for range 3 {
					if err = s.ReconcileAuthorizedOperations(f.owner, b.ID, 32); err != nil {
						t.Fatal(err)
					}
				}
				after, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil || before.LastSequence != after.LastSequence {
					t.Fatalf("denial tick duplicated receipt: %v", err)
				}
				unchanged, err := f.repo.db.ReadCorrectionMemory(f.owner, memoryID.String())
				if err != nil || !reflect.DeepEqual(original, unchanged) {
					t.Fatalf("denial mutated memory: %v", err)
				}
				const instruction = "I changed my mind. Forget my saved temperature preference now."
				a, err := f.repo.AdmitMessage(f.owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: instruction, IdempotencyKey: "fresh-owner"})
				if err != nil {
					t.Fatal(err)
				}
				ref := *a.Run.MandateRef
				worker := execution.WithBotRunIdentity(f.owner, execution.BotRunIdentity{BotID: b.ID, RunID: a.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
				lease, err := f.repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: a.Run.ID, WorkerID: "fresh-owner", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				freshQ := q
				freshQ.Lease, freshQ.MandateRef = lease, ref
				s.authorize = func(context.Context, store.BotScheduleAuthorization) error { return store.ErrBotForbidden }
				if _, err = s.EnqueueAuthorizedOperation(worker, freshQ); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("fresh source bypassed authorizer: %v", err)
				}
				s.authorize = func(_ context.Context, a store.BotScheduleAuthorization) error {
					if a.Instructions != instruction || a.Provenance.UserEventID == op.Provenance.UserEventID {
						return store.ErrBotForbidden
					}
					return nil
				}
				fresh, err := s.EnqueueAuthorizedOperation(worker, freshQ)
				if err != nil {
					t.Fatal(err)
				}
				if fresh.ID == denied.ID || fresh.Status != store.BotOperationBlocked || fresh.Decision != store.BotOperationRequireAuthorization || fresh.Exception == nil {
					t.Fatalf("fresh exception: %+v", fresh)
				}
				if _, err = s.ApplyAuthorizedOperation(f.owner, ref, fresh.ID); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("auto-granted: %v", err)
				}
				if err = f.repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, worker, lease, "authorization required")}); err != nil {
					t.Fatal(err)
				}
				latestPostgresReopen(t, f, s)
				decision := BotRoutineExceptionDecision{MandateRef: ref, OperationID: fresh.ID, ExpectedRevision: fresh.Revision, PayloadDigest: fresh.Provenance.PayloadDigest, Approve: true, ExpiresAt: time.Now().Add(time.Minute)}
				if _, err = s.DecideRoutineException(f.owner, decision); err != nil {
					t.Fatal(err)
				}
				postgresReviewedRace(t, func() error { _, e := s.ApplyAuthorizedOperation(f.owner, ref, fresh.ID); return e })
				applied, err := s.ReadAuthorizedOperation(f.owner, ref, fresh.ID)
				if err != nil || applied.Status != store.BotOperationApplied {
					t.Fatalf("applied: %+v %v", applied, err)
				}
				changed, err := f.repo.db.ReadCorrectionMemory(f.owner, memoryID.String())
				if err != nil || changed.Attribution == nil || changed.Attribution.SupersededBy != "routine-operation:"+fresh.ID.String() {
					t.Fatalf("forget effect: %+v %v", changed, err)
				}
				before, err = f.repo.db.GetBot(f.owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				latestPostgresReopen(t, f, s)
				for range 3 {
					again, e := s.ApplyAuthorizedOperation(f.owner, ref, fresh.ID)
					if e != nil || !reflect.DeepEqual(applied, again) {
						t.Fatalf("apply replay: %+v %v", again, e)
					}
					if e = s.ReconcileAuthorizedOperations(f.owner, b.ID, 32); e != nil {
						t.Fatal(e)
					}
				}
				after, err = f.repo.db.GetBot(f.owner, b.ID)
				if err != nil || before.LastSequence != after.LastSequence {
					t.Fatalf("duplicate effect/receipt: %v", err)
				}
				prior, err := s.ReadAuthorizedOperation(f.owner, q.MandateRef, denied.ID)
				if err != nil || !reflect.DeepEqual(prior, denied) {
					t.Fatalf("old denial changed: %v", err)
				}
				if _, err = s.ApplyAuthorizedOperation(f.owner, q.MandateRef, denied.ID); err == nil {
					t.Fatal("old denial applied")
				}
				if _, err = s.DecideRoutineException(f.owner, decision); err == nil {
					t.Fatal("approval replay accepted")
				}
			})
		}
	})
}

func TestIntegrationPostgresBotLatestPausedCapture(t *testing.T) {
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, kind := range []store.BotControlOperation{store.BotControlGoalCreate, store.BotControlResumeTasks} {
			t.Run(string(kind), func(t *testing.T) {
				f, s, q := latestPostgresAuthorizedFixture(t, scope)
				created, err := s.EnqueueAuthorizedOperation(f.worker, q)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, f.worker, q.Lease, "scheduled")}); err != nil {
					t.Fatal(err)
				}
				created, err = s.ApplyAuthorizedOperation(f.owner, q.MandateRef, created.ID)
				if err != nil {
					t.Fatal(err)
				}
				scheduleID := created.Receipt.Schedule.ScheduleID
				b, err := f.repo.Get(f.owner, q.MandateRef.BotID)
				if err != nil {
					t.Fatal(err)
				}
				v, err := f.repo.GetVersion(f.owner, b.ID, b.Version)
				if err != nil {
					t.Fatal(err)
				}
				triggerID := v.Triggers[0].ID
				if triggerID == scheduleID {
					t.Fatal("fixture identities must differ")
				}
				b, err = f.repo.Pause(f.owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "pause"})
				if err != nil {
					t.Fatal(err)
				}
				policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					p, e := authorizedSchedulePolicy(ctx, ref)
					p.Constraints = append(p.Constraints, execution.BotActionResourceConstraint{Action: execution.CapabilityToolExecute, Resources: []string{"propose_bot_control", RoutineControlResource(kind)}})
					return p, e
				}
				registry := execution.NewBotTurnAuthorizationRegistry(10)
				defer registry.Close()
				f.owner = store.WithBotMessageAdmission(store.WithBotMandateAdmission(f.owner, policy), registry)
				a, err := f.repo.AdmitMessage(f.owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Make the exact requested change only", IdempotencyKey: "paused-owner"})
				if err != nil {
					t.Fatal(err)
				}
				ref := *a.Run.MandateRef
				worker := execution.WithBotRunIdentity(f.owner, execution.BotRunIdentity{BotID: b.ID, RunID: a.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
				lease, err := f.repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: a.Run.ID, WorkerID: "paused-owner", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				s.policy = policy
				s.authorize = func(context.Context, store.BotScheduleAuthorization) error { return nil }
				q = store.BotAuthorizedOperationRequest{Lease: lease, MandateRef: ref, IdempotencyKey: "correctable-call", Payload: store.BotControlPayload{Operation: kind, Reason: "exact owner change"}}
				jobs := f.repo.db.(botTriggerDB).triggerScheduler()
				jobBefore := jobs.Get(f.owner, scheduleID.String())
				foreign := *jobBefore
				foreign.ID = uuid.NewString()
				foreign.Payload.BotID = uuid.NewString()
				if err = jobs.Add(f.owner, &foreign); err != nil {
					t.Fatal(err)
				}
				foreign.Enabled = false
				foreign.NextRun = nil
				if err = jobs.Update(f.owner, &foreign); err != nil {
					t.Fatal(err)
				}
				before, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				if kind == store.BotControlResumeTasks {
					q.Payload.ResumeTargets = &store.BotControlResumeTargets{}
					for _, bad := range []uuid.UUID{triggerID, uuid.New(), uuid.MustParse(foreign.ID)} {
						q.Payload.ResumeTargets.ScheduleIDs = []uuid.UUID{scheduleID, bad}
						op, e := s.EnqueueAuthorizedOperation(worker, q)
						var targetErr *store.BotResumeTargetError
						if !errors.Is(e, store.ErrBotInvalid) || op != nil || !errors.As(e, &targetErr) || targetErr.ScheduleID != bad {
							t.Fatalf("invalid target captured: %+v %v", op, e)
						}
						after, e := f.repo.db.GetBot(f.owner, b.ID)
						if e != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(jobBefore, jobs.Get(f.owner, scheduleID.String())) {
							t.Fatalf("invalid target mutated state: %v", e)
						}
					}
					q.Payload.ResumeTargets.ScheduleIDs = []uuid.UUID{scheduleID}
				} else {
					q.Payload.Goal = &store.BotControlGoal{Goal: store.BotGoal{BotID: b.ID, Title: "Weather clarity", Description: "Easy to understand weather", Status: store.BotGoalActive}}
					if op, e := s.EnqueueAuthorizedOperation(worker, q); !errors.Is(e, store.ErrBotInvalid) || op != nil {
						t.Fatalf("incomplete goal captured: %+v %v", op, e)
					}
					q.Payload.Goal.Goal.CompletionCriteria = "Every update is easy to understand."
				}
				ops, err := s.ListAuthorizedOperations(f.owner, ref)
				if err != nil || len(ops) != 0 {
					t.Fatalf("bad capture persisted: %+v %v", ops, err)
				}
				if _, err = f.repo.db.FindActivity(f.owner, b.ID, routineGrantKey(uuid.NewSHA1(lease.RunID, []byte("authorized-routine:"+q.IdempotencyKey)))); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("invalid capture persisted grant: %v", err)
				}
				op, err := s.EnqueueAuthorizedOperation(worker, q)
				if err != nil || op.Status != store.BotOperationPending || op.Decision != store.BotOperationExecute {
					t.Fatalf("corrected pending capture: %+v %v", op, err)
				}
				if _, err = s.ApplyAuthorizedOperation(f.owner, ref, op.ID); !errors.Is(err, store.ErrBotBusy) {
					t.Fatalf("applied before source completion: %v", err)
				}
				if err = f.repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, worker, lease, "Captured pending, not applied")}); err != nil {
					t.Fatal(err)
				}
				latestPostgresReopen(t, f, s)
				if err = s.ReconcileAuthorizedOperations(f.owner, b.ID, 32); err != nil {
					t.Fatal(err)
				}
				op, err = s.ReadAuthorizedOperation(f.owner, ref, op.ID)
				if err != nil || op.Status != store.BotOperationApplied {
					t.Fatalf("apply: %+v %v", op, err)
				}
				after, err := f.repo.db.GetBot(f.owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				jobs = f.repo.db.(botTriggerDB).triggerScheduler()
				job := jobs.Get(f.owner, scheduleID.String())
				if jobs.Get(f.owner, foreign.ID).Enabled {
					t.Fatal("foreign schedule resumed")
				}
				if kind == store.BotControlGoalCreate {
					goal, e := f.repo.db.ReadGoal(f.owner, b.ID, uuid.NewSHA1(op.ID, []byte("goal")))
					if e != nil || goal.CompletionCriteria != q.Payload.Goal.Goal.CompletionCriteria {
						t.Fatalf("goal: %+v %v", goal, e)
					}
					if after.Bot.Status != store.BotPaused || after.ActiveRunID != nil || job == nil || job.Enabled || job.NextRun != nil {
						t.Fatal("goal resumed independent work")
					}
					if _, e := s.VerifyBotMandate(f.owner, ref, execution.BotMandatePurposeTask); e == nil {
						t.Fatal("goal granted task authority")
					}
				} else {
					if job == nil || !job.Enabled || job.NextRun == nil {
						t.Fatal("canonical target not resumed")
					}
					if !reflect.DeepEqual(before.Metadata[scheduledTaskHead(triggerID)], after.Metadata[scheduledTaskHead(triggerID)]) {
						t.Fatal("resume replaced original finite grant")
					}
				}
				source, err := f.repo.GetRun(f.owner, b.ID, lease.RunID)
				if err != nil || source.Status != store.RunSucceeded || source.Report.Summary != "Captured pending, not applied" {
					t.Fatalf("source report: %+v %v", source, err)
				}
			})
		}
	})
}
