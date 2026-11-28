package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestEmployeeRoutinePausePreservesAccounting(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			if err := s.ReserveBotMandateRun(worker, q.MandateRef, q.Lease.RunID); err != nil {
				t.Fatal(err)
			}
			if err := s.ConsumeBotMandateQuota(worker, q.MandateRef, q.Lease.RunID, 1, 1, 0); err != nil {
				t.Fatal(err)
			}
			before, _ := r.db.GetBot(owner, q.MandateRef.BotID)
			want, _ := json.Marshal(before.Metadata[mandateUsageKey])
			b, err := r.Pause(owner, store.BotLifecycleRequest{BotID: before.Bot.ID, ActorID: before.Bot.OwnerID, ExpectedRevision: before.Bot.Revision, Reason: "pause"})
			if err != nil {
				t.Fatal(err)
			}
			after, _ := r.db.GetBot(owner, b.ID)
			got, _ := json.Marshal(after.Metadata[mandateUsageKey])
			if string(want) != string(got) || after.Metadata[mandateHeadKey] == nil {
				t.Fatal("pause reset authority/accounting")
			}
			m, err := s.ReadBotMandate(owner, q.MandateRef)
			if err != nil || m.RevokedAt != nil {
				t.Fatalf("pause revoked %v", err)
			}
			if _, err = s.VerifyBotMandate(owner, q.MandateRef, m.Mandate.Purpose); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("paused execution %v", err)
			}
			if err = s.ConsumeBotMandateQuota(worker, q.MandateRef, q.Lease.RunID, 1, 0, 0); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("paused quota %v", err)
			}
			if err = s.ReleaseBotMandateRun(owner, q.MandateRef, q.Lease.RunID); err != nil {
				t.Fatal(err)
			}
			after, _ = r.db.GetBot(owner, b.ID)
			var usage mandateUsage
			raw, _ := json.Marshal(after.Metadata[mandateUsageKey])
			_ = json.Unmarshal(raw, &usage)
			if usage.Tools != 1 || usage.Tokens != 1 {
				t.Fatal("release refunded usage")
			}
		})
	}
}
func TestEmployeeRoutineExplicitRevokeAndExpiry(t *testing.T) {
	for _, revoke := range []bool{true, false} {
		t.Run(map[bool]string{true: "revoke", false: "expiry"}[revoke], func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, store.BotScopePersonal)
			s.policy = routineExceptionTestPolicy
			text := "concise"
			q.Payload = store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "preference", Responsibility: &text}
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if revoke {
				if err = s.RevokeBotMandate(owner, q.MandateRef); err != nil {
					t.Fatal(err)
				}
				o, err = s.ReadAuthorizedOperation(owner, q.MandateRef, o.ID)
				if err != nil || o.Status != store.BotOperationCancelled {
					t.Fatalf("not cancelled %+v %v", o, err)
				}
				if err = s.RevokeBotMandate(owner, q.MandateRef); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "captured")}); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			s.repo.now = func() time.Time { return now }
			o, err = s.DecideRoutineException(owner, BotRoutineExceptionDecision{MandateRef: q.MandateRef, OperationID: o.ID, ExpectedRevision: o.Revision, PayloadDigest: o.Provenance.PayloadDigest, Approve: true, ExpiresAt: now.Add(time.Second)})
			if err != nil {
				t.Fatal(err)
			}
			if o.Exception == nil {
				t.Fatal("missing exact exception descriptor")
			}
			s.repo.now = func() time.Time { return now.Add(2 * time.Second) }
			if _, err = s.ApplyAuthorizedOperation(owner, q.MandateRef, o.ID); err == nil {
				t.Fatal("expired exception applied")
			}
			if err = s.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 10); err != nil {
				t.Fatal(err)
			}
			o, err = s.ReadAuthorizedOperation(owner, q.MandateRef, o.ID)
			if err != nil || o.Status != store.BotOperationCancelled {
				t.Fatalf("expiry not terminal %+v %v", o, err)
			}
		})
	}
}
func TestEmployeeRoutineDeniedSurvivesRuntimeRestart(t *testing.T) {
	r, s, owner, worker, q := authorizedScheduleFixture(t, store.BotScopePersonal)
	text := "concise"
	q.Payload = store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "first", Responsibility: &text}
	o, err := s.EnqueueAuthorizedOperation(worker, q)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DecideRoutineException(owner, BotRoutineExceptionDecision{MandateRef: q.MandateRef, OperationID: o.ID, ExpectedRevision: o.Revision, PayloadDigest: o.Provenance.PayloadDigest})
	if err != nil {
		t.Fatal(err)
	}
	restarted := &botAuthorizedOperationRuntime{botMandateRuntime: &botMandateRuntime{repo: r, policy: s.policy}, authorize: s.authorize, readiness: s.readiness}
	q.IdempotencyKey = "different"
	q.Payload.Reason = "new narration"
	if _, err = restarted.EnqueueAuthorizedOperation(worker, q); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("denial reasked: %v", err)
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "captured, denied")}); err != nil {
		t.Fatal(err)
	}
	before, err := r.db.GetBot(owner, q.MandateRef.BotID)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err = restarted.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 32); err != nil {
			t.Fatal(err)
		}
	}
	after, err := r.db.GetBot(owner, q.MandateRef.BotID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Metadata["authorized-pending:"+o.ID.String()] != nil || after.Metadata["authorized-retry:"+o.ID.String()] != nil || after.LastSequence != before.LastSequence {
		t.Fatal("denial retained retry work or emitted another receipt")
	}
	denied, err := restarted.ReadAuthorizedOperation(owner, q.MandateRef, o.ID)
	if err != nil || denied.Decision != store.BotOperationDeny || denied.Status != store.BotOperationCancelled || denied.Receipt == nil || denied.Receipt.Reason != "authorization_denied" {
		t.Fatalf("missing denial receipt: %+v %v", denied, err)
	}
}

func TestEmployeeRoutineSuppressionFingerprint(t *testing.T) {
	_, s, owner, _, q := authorizedScheduleFixture(t, store.BotScopePersonal)
	m, err := s.ReadBotMandate(owner, q.MandateRef)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := s.policy(context.Background(), q.MandateRef)
	if err != nil {
		t.Fatal(err)
	}
	text := "concise"
	p := store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "first", Responsibility: &text}
	key := routineSuppressionKey(p, m.Mandate, policy)
	p.Reason = "another narration"
	m.Mandate.Ref.ID = uuid.New()
	if key != routineSuppressionKey(p, m.Mandate, policy) {
		t.Fatal("unrelated message reopened denial")
	}
	policy.Constraints[0].Resources = append(policy.Constraints[0].Resources, RoutineControlResource(p.Operation))
	if key == routineSuppressionKey(p, m.Mandate, policy) {
		t.Fatal("policy change did not reopen")
	}
}
