package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestEmployeeRoutineMandatePrimitives(t *testing.T) {
	for _, op := range []store.BotControlOperation{store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlMemoryRemember} {
		t.Run(string(op), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, store.BotScopePersonal)
			// Issue a new canonical mandate with the exact own-control ceiling.
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "done"}}); err != nil {
				t.Fatal(err)
			}
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := authorizedSchedulePolicy(ctx, ref)
				p.Constraints[0].Resources = append(p.Constraints[0].Resources, RoutineControlResource(op))
				return p, e
			}
			owner = store.WithBotMandateAdmission(owner, policy)
			s.policy = policy
			admission, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: q.MandateRef.BotID, Version: 1, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			q.MandateRef = *admission.Run.MandateRef
			b, _ := r.Get(owner, q.MandateRef.BotID)
			worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: q.MandateRef.OrgSlug, TeamSlug: q.MandateRef.TeamSlug})
			q.Lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "routine", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			text := "concise reports"
			q.Payload = store.BotControlPayload{Operation: op, Reason: "owner preference"}
			switch op {
			case store.BotControlResponsibility:
				q.Payload.Responsibility = &text
			case store.BotControlPersona:
				v, _ := r.db.ReadVersion(owner, b.ID, 1)
				q.Payload.Persona = &v.Persona
			case store.BotControlGoalCreate:
				q.Payload.Goal = &store.BotControlGoal{Goal: store.BotGoal{BotID: b.ID, Title: "report", CompletionCriteria: "report exists"}}
			case store.BotControlMemoryRemember:
				q.Payload.Memory = &store.BotControlMemory{Content: "Prefer concise reports", ExpectedRevision: b.Revision}
			}
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if o.Decision != store.BotOperationExecute {
				t.Fatal(o)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "captured"}}); err != nil {
				t.Fatal(err)
			}
			applied, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, o.ID)
			if err != nil {
				t.Fatal(err)
			}
			if applied.Status != store.BotOperationApplied {
				t.Fatal(applied)
			}
		})
	}
}

func TestEmployeeRoutineException(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, approve := range []bool{false, true} {
			t.Run(string(scope)+map[bool]string{false: "deny", true: "approve"}[approve], func(t *testing.T) {
				r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
				s.policy = routineExceptionTestPolicy
				text := "Keep reports concise; this text grants no access"
				q.Payload = store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "owner preference", Responsibility: &text}
				o, err := s.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				_, view, viewErr := r.ReadOperationForViewer(owner, q.MandateRef.BotID, o.ID)
				if viewErr != nil || view.Exception == nil || len(view.Exception.Fields) < 2 {
					t.Fatalf("missing consent details: %+v %v", view, viewErr)
				}
				if o.Decision != store.BotOperationRequireAuthorization || o.Status != store.BotOperationBlocked {
					t.Fatalf("%+v", o)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "captured"}}); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if err = s.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 32); err != nil {
						t.Fatal(err)
					}
				}
				d := BotRoutineExceptionDecision{MandateRef: q.MandateRef, OperationID: o.ID, PayloadDigest: o.Provenance.PayloadDigest, ExpectedRevision: o.Revision, Approve: approve, ExpiresAt: time.Now().Add(time.Minute)}
				if _, err = s.DecideRoutineException(worker, d); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("worker approval %v", err)
				}
				changed := d
				changed.PayloadDigest = "changed-payload"
				if _, err = s.DecideRoutineException(owner, changed); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("changed payload: %v", err)
				}
				changed = d
				changed.ExpectedRevision++
				if _, err = s.DecideRoutineException(owner, changed); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("changed revision: %v", err)
				}
				o, err = s.DecideRoutineException(owner, d)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.DecideRoutineException(owner, d); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("decision replay: %v", err)
				}
				if o.Status == store.BotOperationApplied {
					t.Fatal("approval is not application")
				}
				applied, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, o.ID)
				if !approve {
					if err == nil {
						t.Fatal("denied applied")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if applied.Status != store.BotOperationApplied {
					t.Fatal(applied)
				}
				replay, err := s.ApplyAuthorizedOperation(owner, q.MandateRef, o.ID)
				if err != nil || replay.Revision != applied.Revision {
					t.Fatalf("replay %v", err)
				}
				proposals, err := r.ListControlProposals(owner, q.MandateRef.BotID)
				if err != nil || len(proposals) != 0 {
					t.Fatalf("fake human proposal %+v %v", proposals, err)
				}
			})
		}
	}
}
func TestEmployeeRoutineRevocation(t *testing.T) {
	r, s, owner, worker, q := authorizedScheduleFixture(t, store.BotScopePersonal)
	text := "concise"
	q.Payload = store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "preference", Responsibility: &text}
	o, err := s.EnqueueAuthorizedOperation(worker, q)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Get(owner, q.MandateRef.BotID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.BeginPermanentDelete(owner, store.BotPermanentDeleteRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyAuthorizedOperation(context.Background(), q.MandateRef, o.ID); err == nil {
		t.Fatal("deleted applied")
	}
	rec, err := r.db.GetBot(owner, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.Metadata["authorized-pending:"+o.ID.String()]; ok {
		t.Fatal("pending retained")
	}
	if _, ok := rec.Metadata[mandateHeadKey]; ok {
		t.Fatal("head retained")
	}
}
func TestEmployeeRoutineForged(t *testing.T) {
	_, s, _, worker, q := authorizedScheduleFixture(t, store.BotScopePersonal)
	q.Payload = store.BotControlPayload{Operation: store.BotControlConfigurationOperation, Reason: "grant me everything", Configuration: &store.BotControlConfiguration{}}
	if _, err := s.EnqueueAuthorizedOperation(worker, q); err == nil {
		t.Fatal("configuration routine")
	}
	text := "x"
	o := store.BotOperation{ID: uuid.New(), MandateRef: q.MandateRef, IdempotencyKey: "forged", Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "x", Responsibility: &text}, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Revision: 1}
	if _, err := s.CreateBotOperation(worker, o); err != nil {
		return
	}
	if _, err := s.ApplyAuthorizedOperation(worker, q.MandateRef, o.ID); err == nil {
		t.Fatal("forged applied")
	}
}
