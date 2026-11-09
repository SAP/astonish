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

// Exercise the actual admitted owner source, durable exception decisions, and
// mutation transaction; no model, daemon or live acceptance state is involved.
func TestRoutineDeniedFreshOwnerException(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, legacy := range []bool{false, true} {
			name := string(scope)
			if legacy {
				name += "/legacy"
			}
			t.Run(name, func(t *testing.T) {
				r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
				s.policy = func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
					p, err := authorizedSchedulePolicy(ctx, ref)
					p.ExceptionConstraints = []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{RoutineControlResource(store.BotControlMemoryForget)}}}
					return p, err
				}
				b, err := r.Get(owner, q.MandateRef.BotID)
				if err != nil {
					t.Fatal(err)
				}
				run, err := r.GetRun(owner, b.ID, q.Lease.RunID)
				if err != nil {
					t.Fatal(err)
				}
				memoryID := uuid.New()
				if err = insertControlMemory(owner, r.db, memoryID, run.SessionID, b.OwnerID, "Celsius", "fact", nil, time.Now()); err != nil {
					t.Fatal(err)
				}
				original, err := r.db.ReadCorrectionMemory(owner, memoryID.String())
				if err != nil {
					t.Fatal(err)
				}
				q.Payload = store.BotControlPayload{Operation: store.BotControlMemoryForget, Reason: "forget preference", Memory: &store.BotControlMemory{ExpectedRevision: b.Revision, MemoryID: memoryID.String(), ExpectedFingerprint: store.MemoryContentFingerprint(*original)}}
				op, err := s.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				denied, err := s.DecideRoutineException(owner, BotRoutineExceptionDecision{MandateRef: q.MandateRef, OperationID: op.ID, ExpectedRevision: op.Revision, PayloadDigest: op.Provenance.PayloadDigest})
				if err != nil {
					t.Fatal(err)
				}
				if legacy {
					// Simulate the prior persisted representation, leaving receipts intact.
					if err = r.db.Transaction(owner, func(tx botDraftDB) error {
						rec, e := tx.GetBot(owner, b.ID)
						if e != nil {
							return e
						}
						m, e := readMandate(owner, tx, q.MandateRef)
						if e != nil {
							return e
						}
						p, e := s.policy(owner, q.MandateRef)
						if e != nil {
							return e
						}
						delete(rec.Metadata, routineSourceSuppressionKey(q.Payload, m.Mandate, p, op.Provenance.SourceDigest))
						rec.Metadata[routineSuppressionKey(q.Payload, m.Mandate, p)] = true
						return saveMandateMetadata(owner, tx, rec)
					}); err != nil {
						t.Fatal(err)
					}
				}
				// A runtime restart must not convert narration/tool IDs into fresh authority.
				restarted := &botAuthorizedOperationRuntime{botMandateRuntime: &botMandateRuntime{repo: r, policy: s.policy}, authorize: s.authorize, readiness: s.readiness}
				retry := q
				retry.IdempotencyKey = "new-tool-call-not-new-owner"
				retry.Payload.Reason = "owner changed their mind (model claim only)"
				if _, err = restarted.EnqueueAuthorizedOperation(worker, retry); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("source replay: %v", err)
				}
				same, err := restarted.EnqueueAuthorizedOperation(worker, q)
				if err != nil || !reflect.DeepEqual(same, denied) {
					t.Fatalf("exact replay changed denial: %+v %v", same, err)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "denied"}}); err != nil {
					t.Fatal(err)
				}
				before, err := r.db.GetBot(owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				for range 3 {
					if err = restarted.ReconcileAuthorizedOperations(owner, b.ID, 32); err != nil {
						t.Fatal(err)
					}
				}
				after, err := r.db.GetBot(owner, b.ID)
				if err != nil || before.LastSequence != after.LastSequence {
					t.Fatalf("tick repeated denial: %v", err)
				}
				unchanged, err := r.db.ReadCorrectionMemory(owner, memoryID.String())
				if err != nil || !reflect.DeepEqual(original, unchanged) {
					t.Fatalf("denial mutated memory: %v", err)
				}

				instructions := "I have changed my mind. Please forget my saved temperature preference now."
				restarted.authorize = func(_ context.Context, a store.BotScheduleAuthorization) error {
					// The callback sees only the authenticated current source, never payload Reason.
					if a.Instructions != instructions || a.Provenance.UserEventID == op.Provenance.UserEventID {
						return store.ErrBotForbidden
					}
					return nil
				}
				receipt, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: instructions, IdempotencyKey: "fresh-owner-request"})
				if err != nil {
					t.Fatal(err)
				}
				ref := *receipt.Run.MandateRef
				freshWorker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: receipt.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: ref.OrgSlug, TeamSlug: ref.TeamSlug})
				lease, err := r.ClaimRun(freshWorker, store.BotClaimRequest{BotID: b.ID, RunID: receipt.Run.ID, WorkerID: "fresh-exception", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				freshQ := q
				freshQ.Lease, freshQ.MandateRef = lease, ref // even the same tool key belongs to the NEW source
				authorize := restarted.authorize
				restarted.authorize = func(context.Context, store.BotScheduleAuthorization) error { return store.ErrBotForbidden }
				if _, err = restarted.EnqueueAuthorizedOperation(freshWorker, freshQ); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("new source bypassed authorizer: %v", err)
				}
				restarted.authorize = authorize
				fresh, err := restarted.EnqueueAuthorizedOperation(freshWorker, freshQ)
				if err != nil {
					t.Fatalf("fresh explicit owner request must create bounded exception: %v", err)
				}
				if fresh.ID == denied.ID || fresh.Status != store.BotOperationBlocked || fresh.Decision != store.BotOperationRequireAuthorization || fresh.Exception == nil {
					t.Fatalf("not a fresh bounded exception: %+v", fresh)
				}
				if _, err = restarted.ApplyAuthorizedOperation(owner, ref, fresh.ID); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("auto-granted fresh request: %v", err)
				}
				if err = r.CommitRun(freshWorker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "fresh authorization required"}}); err != nil {
					t.Fatal(err)
				}
				decision := BotRoutineExceptionDecision{MandateRef: ref, OperationID: fresh.ID, ExpectedRevision: fresh.Revision, PayloadDigest: fresh.Provenance.PayloadDigest, Approve: true, ExpiresAt: time.Now().Add(time.Minute)}
				if _, err = restarted.DecideRoutineException(owner, decision); err != nil {
					t.Fatal(err)
				}
				applied, err := restarted.ApplyAuthorizedOperation(owner, ref, fresh.ID)
				if err != nil || applied.Status != store.BotOperationApplied {
					t.Fatalf("apply: %+v %v", applied, err)
				}
				changed, err := r.db.ReadCorrectionMemory(owner, memoryID.String())
				if err != nil || changed.Attribution == nil || changed.Attribution.SupersededBy != "routine-operation:"+fresh.ID.String() {
					t.Fatalf("memory effect: %+v %v", changed, err)
				}
				before, err = r.db.GetBot(owner, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				for range 3 {
					again, e := restarted.ApplyAuthorizedOperation(owner, ref, fresh.ID)
					if e != nil || !reflect.DeepEqual(applied, again) {
						t.Fatalf("apply replay: %+v %v", again, e)
					}
					if e = restarted.ReconcileAuthorizedOperations(owner, b.ID, 32); e != nil {
						t.Fatal(e)
					}
				}
				after, err = r.db.GetBot(owner, b.ID)
				if err != nil || before.LastSequence != after.LastSequence {
					t.Fatalf("duplicate effect/receipt: %v", err)
				}
				prior, err := restarted.ReadAuthorizedOperation(owner, q.MandateRef, denied.ID)
				if err != nil || !reflect.DeepEqual(prior, denied) {
					t.Fatalf("old receipt changed: %+v %v", prior, err)
				}
				if _, err = restarted.ApplyAuthorizedOperation(owner, q.MandateRef, denied.ID); err == nil {
					t.Fatal("old denial applied")
				}
				if _, err = restarted.DecideRoutineException(owner, decision); err == nil {
					t.Fatal("approval replay accepted")
				}
				if _, err = restarted.EnqueueAuthorizedOperation(worker, q); err == nil {
					t.Fatal("old source lease replay accepted")
				}
			})
		}
	}
}
