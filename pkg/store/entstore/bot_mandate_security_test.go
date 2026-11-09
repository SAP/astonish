package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"testing"
	"time"
)

func mandateOwnerContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	p, _ := execution.PrincipalFromContext(ctx)
	ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return store.WithBotMandateAdmission(store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(20)), mandateTestPolicy)
}
func TestBotMandateReopenQuotaAndRevocation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, bot, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
			ctx = mandateOwnerContext(t, ctx)
			bots, _ := reopen()
			q := store.BotMessageRequest{BotID: bot, Version: 1, Instructions: "read", IdempotencyKey: "mandate-one"}
			receipt, err := bots.(store.BotMessageStore).AdmitMessage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			ref := *receipt.Run.MandateRef
			rt, err := NewBotMandateRuntime(bots, mandateTestPolicy)
			if err != nil {
				t.Fatal(err)
			}
			if err = rt.ReserveBotMandateRun(context.Background(), ref, receipt.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = rt.ConsumeBotMandateQuota(context.Background(), ref, receipt.Run.ID, 2, 10, 0); err != nil {
				t.Fatal(err)
			}
			bots, _ = reopen()
			rt, _ = NewBotMandateRuntime(bots, mandateTestPolicy)
			again, err := bots.(store.BotMessageStore).AdmitMessage(ctx, q)
			if err != nil || again.Run.MandateRef == nil || *again.Run.MandateRef != ref {
				t.Fatalf("reopened pin: %v", err)
			}
			if _, err = rt.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err != nil {
				t.Fatal(err)
			}
			if err = rt.ConsumeBotMandateQuota(context.Background(), ref, receipt.Run.ID, 1, 0, 0); err == nil {
				t.Fatal("quota lost on reopen")
			}
			if err = rt.ReleaseBotMandateRun(context.Background(), ref, receipt.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = rt.ReserveBotMandateRun(context.Background(), ref, receipt.Run.ID); err == nil {
				t.Fatal("released reservation reused")
			}
			q.IdempotencyKey = "mandate-two"
			second, err := bots.(store.BotMessageStore).AdmitMessage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			ref2 := *second.Run.MandateRef
			if err = rt.ReserveBotMandateRun(context.Background(), ref2, second.Run.ID); err != nil {
				t.Fatal(err)
			}
			if err = rt.ConsumeBotMandateQuota(context.Background(), ref2, second.Run.ID, 2, 1, 0); err == nil {
				t.Fatal("aggregate bypass through new mandate")
			}
			if err = rt.ConsumeBotMandateQuota(context.Background(), ref2, second.Run.ID, 1, 5, 0); err != nil {
				t.Fatal(err)
			}
			if err = rt.RevokeBotMandate(ctx, ref2); err != nil {
				t.Fatal(err)
			}
			bots, _ = reopen()
			rt, _ = NewBotMandateRuntime(bots, mandateTestPolicy)
			if _, err = rt.VerifyBotMandate(context.Background(), ref2, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("revocation lost on reopen")
			}
			if err = rt.ConsumeBotMandateQuota(context.Background(), ref2, second.Run.ID, 0, 0, 0); err == nil {
				t.Fatal("revoked quota accepted")
			}
		})
	}
}
func TestBotMandateSourceAndCurrentPolicy(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, bots, base, run, _ := runtimeFixture(t, scope)
			q := store.BotMessageRequest{BotID: run.BotID, Version: 1, Instructions: "read", IdempotencyKey: "source"}
			// A claimed user principal without verified authentication never grants authority.
			forged := store.WithBotMandateAdmission(base, mandateTestPolicy)
			if _, err := repo.AdmitMessage(forged, q); err == nil {
				t.Fatal("unverified caller admitted")
			}
			p, _ := execution.PrincipalFromContext(base)
			p.Authentication = execution.AuthMethodOAuth
			p.Scopes = []string{string(execution.CapabilityChat)}
			oauth, err := execution.WithPrincipal(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			oauth = mandateOwnerContext(t, oauth)
			if _, err = repo.AdmitMessage(oauth, q); err == nil {
				t.Fatal("OAuth delegated owner issued")
			}
			// Local single-user login carries explicit loopback evidence, not a
			// fabricated local principal or an OAuth token.
			local, localErr := execution.WithAuthenticationEvidence(base, execution.AuthenticationEvidence{Method: execution.AuthMethodLocalOS, LocalLoopback: true, ExpiresAt: time.Now().Add(time.Hour)})
			if localErr != nil {
				t.Fatal(localErr)
			}
			local = store.WithBotMandateAdmission(store.WithBotMessageAdmission(local, execution.NewBotTurnAuthorizationRegistry(2)), mandateTestPolicy)
			localRequest := q
			localRequest.IdempotencyKey = "local-source"
			if admitted, err := repo.AdmitMessage(local, localRequest); err != nil || admitted.Run.MandateRef == nil {
				t.Fatalf("verified loopback owner: %v", err)
			}
			ctx := mandateOwnerContext(t, base)
			receipt, err := repo.AdmitMessage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			ref := *receipt.Run.MandateRef
			rt, _ := NewBotMandateRuntime(bots, mandateTestPolicy)
			for _, mutate := range []func(*store.BotMandateRef){func(r *store.BotMandateRef) { r.OwnerID = uuid.NewString() }, func(r *store.BotMandateRef) { r.OrgSlug = "other" }, func(r *store.BotMandateRef) { r.TeamSlug = "other" }, func(r *store.BotMandateRef) { r.Revision++ }} {
				bad := ref
				mutate(&bad)
				if _, err = rt.VerifyBotMandate(context.Background(), bad, execution.BotMandatePurposeTask); err == nil {
					t.Fatal("wrong binding")
				}
			}
			if _, err = rt.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeSchedule); err == nil {
				t.Fatal("task promoted to schedule")
			}
			changed, _ := NewBotMandateRuntime(bots, func(c context.Context, r store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, _ := mandateTestPolicy(c, r)
				p.Constraints[0].Resources = []string{"other"}
				return p, nil
			})
			if _, err = changed.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("catalog removal ignored")
			}
			concrete := rt.(*botMandateRuntime)
			concrete.repo.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			if _, err = rt.VerifyBotMandate(context.Background(), ref, execution.BotMandatePurposeTask); err == nil {
				t.Fatal("viewer authority survived")
			}
		})
	}
}

type mandateFailDB struct{ botDraftDB }

func (d mandateFailDB) Transaction(ctx context.Context, f func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return f(mandateFailTx{tx}) })
}

type mandateFailTx struct{ botDraftDB }

func (d mandateFailTx) appendAdmittedMessage(ctx context.Context, b store.Bot, e *session.Event) (int64, error) {
	return d.botDraftDB.(botMessageDB).appendAdmittedMessage(ctx, b, e)
}
func (d mandateFailTx) saveMandateMetadata(context.Context, *botStoredRecord) error {
	return errors.New("injected metadata failure")
}
func TestBotMandateRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, base, run, _ := runtimeFixture(t, scope)
			ctx := mandateOwnerContext(t, base)
			before, err := repo.db.GetBot(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			failing := *repo
			failing.db = mandateFailDB{repo.db}
			q := store.BotMessageRequest{BotID: run.BotID, Version: 1, Instructions: "read", IdempotencyKey: "failure"}
			if _, err = failing.AdmitMessage(ctx, q); err == nil {
				t.Fatal("injected failure passed")
			}
			after, err := repo.db.GetBot(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if before.LastSequence != after.LastSequence || after.Metadata[mandateHeadKey] != nil {
				t.Fatal("authority side effects survived rollback")
			}
			if _, err = repo.db.FindRun(ctx, run.BotID, q.IdempotencyKey); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("run survived: %v", err)
			}
			// The same transaction boundary protects quota reservations.
			admitted, err := repo.AdmitMessage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			ref := *admitted.Run.MandateRef
			failingRuntime := &botMandateRuntime{repo: &failing, policy: mandateTestPolicy}
			if err = failingRuntime.ReserveBotMandateRun(context.Background(), ref, admitted.Run.ID); err == nil {
				t.Fatal("injected quota failure passed")
			}
			if _, err = repo.db.FindActivity(ctx, run.BotID, "mandate-reservation:"+admitted.Run.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("reservation survived rollback: %v", err)
			}
			goodRuntime := &botMandateRuntime{repo: repo, policy: mandateTestPolicy}
			if err = goodRuntime.ReserveBotMandateRun(context.Background(), ref, admitted.Run.ID); err != nil {
				t.Fatal("failed reservation consumed quota", err)
			}
			// Public JSON never serializes or accepts the private pin.
			data, _ := json.Marshal(store.BotRun{MandateRef: &store.BotMandateRef{ID: uuid.New()}})
			var decoded store.BotRun
			_ = json.Unmarshal(data, &decoded)
			if decoded.MandateRef != nil {
				t.Fatal("public pin leaked")
			}
		})
	}
}
