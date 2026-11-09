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

type continuationFailedCommitDB struct{ botDraftDB }

func (d continuationFailedCommitDB) Transaction(ctx context.Context, f func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		if err := f(tx); err != nil {
			return err
		}
		return errors.New("injected SQL rollback")
	})
}

func TestInteractiveContinuationQueuedRestartAndReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, _, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			v := store.DefaultBotVersion()
			v.Charter = "Continue safely"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Continue", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "continue"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			ctx = store.WithBotMessageAdmission(ctx, registry)
			receipt, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "turn", Instructions: "Hi"})
			if err != nil {
				t.Fatal(err)
			}
			id := execution.BotRunIdentity{BotID: b.ID, RunID: receipt.Run.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			q := store.BotContinueInteractiveRequest{BotID: b.ID, RunID: receipt.Run.ID, ExpectedRevision: b.Revision, IdempotencyKey: "retry"}
			if _, err = repo.ContinueInteractive(ctx, q); err == nil {
				t.Fatal("renewed live authority")
			}
			registry.Cancel(id)
			if err = repo.interactiveAnswerAuthority(ctx, receipt.Run); err == nil {
				t.Fatal("answer recreated lost authority")
			}
			blocked, err := repo.RequireInteractiveContinuation(ctx, b.ID, receipt.Run.ID)
			if err != nil || blocked.Status != store.RunInterrupted || blocked.ContinuationReason != store.BotContinuationRequired {
				t.Fatalf("not durably blocked: %+v %v", blocked, err)
			}
			saved, err := repo.db.ReadRun(ctx, b.ID, receipt.Run.ID)
			if err != nil || saved.Run.ContinuationReason != store.BotContinuationRequired || !safeInteractiveQueue(*saved) {
				t.Fatalf("lost durable reason: %+v %v", saved, err)
			}
			if _, err := repo.RequireInteractiveContinuation(ctx, b.ID, receipt.Run.ID); err != nil {
				t.Fatal(err)
			}
			originalDB := repo.db
			repo.db = continuationFailedCommitDB{originalDB}
			if _, err = repo.ContinueInteractive(ctx, q); err == nil {
				t.Fatal("expected rollback")
			}
			if _, err = registry.Resolve(id); err == nil {
				t.Fatal("rollback left live authority")
			}
			repo.db = originalDB
			continued, err := repo.ContinueInteractive(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if continued.Status != store.RunQueued {
				t.Fatal(continued.Status)
			}
			registry.Cancel(id)
			if _, err = repo.ContinueInteractive(ctx, q); err != nil {
				t.Fatal(err)
			}
			if _, err = registry.Resolve(id); err == nil {
				t.Fatal("duplicate renewed authority")
			}
			current, err := repo.Get(ctx, b.ID)
			if err != nil || current.Status != store.BotDraft {
				t.Fatalf("activated: %v %v", current, err)
			}
		})
	}
}
