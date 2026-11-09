package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotMandateQuotaBoundaries(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, bots, base, legacy, _ := runtimeFixture(t, scope)
			ctx := mandateOwnerContext(t, base)
			runtime, err := NewBotMandateRuntime(bots, mandateTestPolicy)
			if err != nil {
				t.Fatal(err)
			}
			rt := runtime.(*botMandateRuntime)
			now := time.Now().UTC()
			rt.repo.now = func() time.Time { return now }
			// Use a longer source TTL so advancing across midnight isolates daily accounting.
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, e := mandateTestPolicy(c, ref)
				p.TTL = 48 * time.Hour
				return p, e
			}
			ctx = store.WithBotMandateAdmission(ctx, policy)
			admit := func(key string) *store.BotMessageReceipt {
				t.Helper()
				r, e := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: legacy.BotID, Version: 1, Instructions: "read", IdempotencyKey: key})
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			first, second, third := admit("one"), admit("two"), admit("three")
			ref := *first.Run.MandateRef
			c := context.Background()
			if err = rt.ReserveBotMandateRun(c, ref, legacy.ID); err == nil {
				t.Fatal("unpinned legacy run reserved")
			}
			if err = rt.ReserveBotMandateRun(c, ref, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = rt.ReserveBotMandateRun(c, ref, first.Run.ID); err != nil {
				t.Fatal("idempotent reserve", err)
			}
			if err = rt.ReserveBotMandateRun(c, *second.Run.MandateRef, second.Run.ID); err == nil {
				t.Fatal("concurrency bypass")
			}
			for _, charge := range [][3]int64{{-1, 0, 0}, {0, -1, 0}, {0, 0, -1}, {math.MaxInt64, 0, 0}, {0, math.MaxInt64, 0}, {0, 0, 1}} {
				if err = rt.ConsumeBotMandateQuota(c, ref, first.Run.ID, charge[0], charge[1], charge[2]); err == nil {
					t.Fatalf("invalid charge accepted: %v", charge)
				}
			}
			if err = rt.ConsumeBotMandateQuota(c, ref, first.Run.ID, 1, 6, 0); err != nil {
				t.Fatal(err)
			}
			if err = rt.ReleaseBotMandateRun(c, ref, first.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = rt.ReleaseBotMandateRun(c, ref, first.Run.ID); err != nil {
				t.Fatal("idempotent release", err)
			}
			if err = rt.ReserveBotMandateRun(c, *second.Run.MandateRef, second.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = rt.ConsumeBotMandateQuota(c, *second.Run.MandateRef, second.Run.ID, 0, 10, 0); err == nil {
				t.Fatal("aggregate token limit ignored")
			}
			if err = rt.RevokeBotMandate(c, *second.Run.MandateRef); err == nil {
				t.Fatal("runtime context revoked owner mandate")
			}
			if err = rt.RevokeBotMandate(ctx, *second.Run.MandateRef); err != nil {
				t.Fatal(err)
			}
			if err = rt.RevokeBotMandate(ctx, *second.Run.MandateRef); err != nil {
				t.Fatal("idempotent revoke", err)
			}
			// A revoked mandate and unavailable current policy must not strand its reservation.
			rt.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
				return store.BotMandatePolicy{}, store.ErrBotForbidden
			}
			membership := rt.repo.auth.check
			rt.repo.auth.check = func(context.Context, string) (string, error) { return "", store.ErrBotForbidden }
			if err = rt.ConsumeBotMandateQuota(c, *second.Run.MandateRef, second.Run.ID, 0, 1, 0); err == nil {
				t.Fatal("removed member consumed quota")
			}
			wrong := *second.Run.MandateRef
			wrong.OwnerID = uuid.NewString()
			if err = rt.ReleaseBotMandateRun(c, wrong, second.Run.ID); err == nil {
				t.Fatal("cleanup accepted another owner")
			}
			if err = rt.ReleaseBotMandateRun(c, *second.Run.MandateRef, legacy.ID); err == nil {
				t.Fatal("cleanup accepted unpinned run")
			}
			if err = rt.ReleaseBotMandateRun(c, *second.Run.MandateRef, second.Run.ID); err != nil {
				t.Fatal("revoked cleanup", err)
			}
			rt.repo.auth.check = membership
			rt.policy = mandateTestPolicy
			if err = rt.ReserveBotMandateRun(c, *third.Run.MandateRef, third.Run.ID); err == nil {
				t.Fatal("daily run limit ignored")
			}
			now = now.Add(24 * time.Hour)
			if err = rt.ReserveBotMandateRun(c, *third.Run.MandateRef, third.Run.ID); err != nil {
				t.Fatal("daily rollover", err)
			}
			if err = rt.ConsumeBotMandateQuota(c, *third.Run.MandateRef, third.Run.ID, 0, 10, 0); err == nil {
				t.Fatal("aggregate reset at midnight")
			}
		})
	}
}

func TestBotMandateOperationPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, bot, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
			ctx = mandateOwnerContext(t, ctx)
			bots, _ := reopen()
			admitted, err := bots.(store.BotMessageStore).AdmitMessage(ctx, store.BotMessageRequest{BotID: bot, Version: 1, Instructions: "read", IdempotencyKey: "source"})
			if err != nil {
				t.Fatal(err)
			}
			ref := *admitted.Run.MandateRef
			rt, _ := NewBotMandateRuntime(bots, mandateTestPolicy)
			ops := rt.(store.BotOperationStore)
			text := "read files"
			op := store.BotOperation{ID: uuid.New(), MandateRef: ref, IdempotencyKey: "operation", Revision: 1, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "owner request", Responsibility: &text}}
			created, err := ops.CreateBotOperation(context.Background(), op)
			if err != nil {
				t.Fatal(err)
			}
			*created.Payload.Responsibility = "mutated result"
			duplicate, err := ops.CreateBotOperation(context.Background(), op)
			if err != nil || *duplicate.Payload.Responsibility != text {
				t.Fatalf("duplicate/alias: %+v %v", duplicate, err)
			}
			bad := op
			bad.Decision = store.BotOperationDeny
			if _, err = ops.CreateBotOperation(context.Background(), bad); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("different replay: %v", err)
			}
			receipt := store.BotOperationReceipt{Status: store.BotOperationApplying}
			if _, err = ops.TransitionBotOperation(context.Background(), ref, op.ID, 1, store.BotOperationApplying, receipt); err != nil {
				t.Fatal(err)
			}
			if _, err = ops.TransitionBotOperation(context.Background(), ref, op.ID, 1, store.BotOperationApplying, receipt); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale CAS: %v", err)
			}
			bots, _ = reopen()
			rt, _ = NewBotMandateRuntime(bots, mandateTestPolicy)
			ops = rt.(store.BotOperationStore)
			saved, err := ops.ReadBotOperation(context.Background(), ref, op.ID)
			if err != nil || saved.Revision != 2 || saved.Status != store.BotOperationApplying {
				t.Fatalf("reopen: %+v %v", saved, err)
			}
			if _, err = ops.TransitionBotOperation(context.Background(), ref, op.ID, 2, store.BotOperationApplied, store.BotOperationReceipt{Status: store.BotOperationApplied}); err == nil {
				t.Fatal("unobserved apply accepted")
			}
			if err = rt.RevokeBotMandate(ctx, ref); err != nil {
				t.Fatal(err)
			}
			if _, err = ops.TransitionBotOperation(context.Background(), ref, op.ID, 2, store.BotOperationApplied, store.BotOperationReceipt{Status: store.BotOperationApplied, ObservedAt: time.Now(), Postconditions: []store.BotOperationPostcondition{{Resource: "bot/responsibility", Revision: 2, Verified: true}}}); err == nil {
				t.Fatal("revoked operation transitioned")
			}
		})
	}
}

func TestBotMandateProtectedActivity(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, run, lease, _ := botRunFixture(t, scope)
			ref := store.BotMandateRef{ID: uuid.New(), BotID: run.BotID, OwnerID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Revision: 1}
			payload, _ := json.Marshal(store.BotMandateRecord{Mandate: store.BotMandate{Ref: ref}})
			if _, err := repo.AppendActivity(worker, lease, store.BotActivity{BotID: run.BotID, RunID: &run.ID, Kind: mandateRecordKind, Payload: payload}); err == nil {
				t.Fatal("reserved kind accepted")
			}
			// Even a valid progress event containing a forged record has no reserved key.
			if _, err := repo.AppendActivity(worker, lease, store.BotActivity{BotID: run.BotID, RunID: &run.ID, Kind: store.BotActivityRun, Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if _, err := readMandate(worker, repo.db, ref); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("forged progress became authority: %v", err)
			}
			before, err := repo.db.GetBot(worker, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Metadata[mandateHeadKey] != nil {
				t.Fatal("forged head")
			}
		})
	}
}
