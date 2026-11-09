package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotReviewedConfigurationRevokesOldGrants(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{"body":"review"}`)}
			digest, err := action.Digest()
			if err != nil {
				t.Fatal(err)
			}
			approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: b.ID, RunID: run.ID, Version: b.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "review config"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotConfigurationRequest{BotID: b.ID, ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "new scope", Autonomy: store.DefaultBotVersion().Autonomy, Budget: store.BudgetSpec{MaxRuntimeSeconds: 30}, Delivery: store.DeliveryPolicy{Notify: store.NotifyNever}}
			saved, err := repo.ConfigureReviewed(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			after, err := repo.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Fence != before.Fence || after.Bot.Status != store.BotPaused || saved.Version != b.Version+1 {
				t.Fatal("configuration changed lifecycle")
			}
			a, err := repo.db.ReadApproval(ctx, b.ID, approval.ID)
			if err != nil || a.Status != store.ApprovalRevoked {
				t.Fatalf("grant %+v %v", a, err)
			}
			if _, err := repo.ConfigureReviewed(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatal(err)
			}
			if _, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: saved.Version, Trigger: store.TriggerManual, Instructions: "no", IdempotencyKey: "no"}); err == nil {
				t.Fatal("configuration enabled execution")
			}
		})
	}
}

type failingConversionDB struct{ botDraftDB }
type failingConversionTx struct {
	botDraftDB
	botIntakeDB
}

func (d failingConversionDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(failingConversionTx{tx, tx.(botIntakeDB)}) })
}
func (d failingConversionTx) pauseAdaptive(ctx context.Context, id uuid.UUID, expected time.Time) error {
	if err := d.botIntakeDB.pauseAdaptive(ctx, id, expected); err != nil {
		return err
	}
	return errors.New("injected failure after original pause")
}
func TestBotAdaptiveConversionRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			var id uuid.UUID
			var updated time.Time
			var active func() bool
			switch d := repo.db.(type) {
			case personalBotDB:
				row, err := d.client.ScheduledJob.Create().SetName("original").SetMode("adaptive").SetSchedule("0 9 * * *").SetStatus("active").SetCreatedBy(b.OwnerID).SetPayload(map[string]any{"instructions": "Observe"}).Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
				id = row.ID
				updated = row.UpdatedAt
				active = func() bool {
					row, err := d.client.ScheduledJob.Get(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return row.Status == "active"
				}
			case teamBotDB:
				row, err := d.client.ScheduledJob.Create().SetName("original").SetMode("adaptive").SetSchedule("0 9 * * *").SetStatus("active").SetCreatedBy(b.OwnerID).SetPayload(map[string]any{"instructions": "Observe"}).Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
				id = row.ID
				updated = row.UpdatedAt
				active = func() bool {
					row, err := d.client.ScheduledJob.Get(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return row.Status == "active"
				}
			}
			// The SQLite driver persists timestamps at microsecond precision.
			updated = updated.Truncate(time.Microsecond)
			original := repo.db
			repo.db = failingConversionDB{original}
			q := store.BotConversionRequest{SourceJobID: id, ExpectedUpdatedAt: updated, Name: "replacement", IdempotencyKey: "rollback", Charter: "Observe", Reviewed: true, ReviewedForTeam: true}
			_, err = repo.ConvertAdaptiveJob(ctx, q)
			if err == nil || err.Error() != "injected failure after original pause" {
				t.Fatalf("did not reach rollback injection: %v", err)
			}
			if !active() {
				t.Fatal("original remained paused after rollback")
			}
			key, _ := botHash([]string{b.OwnerID.String(), "rollback"})
			if _, err := original.FindCreate(ctx, key); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("partial replacement survived: %v", err)
			}
			repo.db = original
			saved, err := repo.ConvertAdaptiveJob(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if active() || saved.Status != store.BotDraft {
				t.Fatal("retry failed")
			}
		})
	}
}

func TestBotReviewedDraftRejectsUnreviewedPolicyReplacement(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, _ := botRunFixture(t, scope)
			active, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			v := store.DefaultBotVersion()
			v.Charter = "Observe"
			draft, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Draft policy", OwnerID: active.OwnerID, IdempotencyKey: "reviewed-draft-policy", Version: v})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotConfigurationRequest{BotID: draft.ID, ExpectedRevision: draft.Revision, ExpectedVersion: draft.Version, Reviewed: true, Reason: "Review authority", Autonomy: v.Autonomy, Budget: store.BudgetSpec{MaxRuntimeSeconds: 30}, Delivery: store.DeliveryPolicy{Notify: store.NotifyNever}}
			q.Autonomy.CanPublish = true
			saved, err := repo.ConfigureReviewed(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			current, err := repo.db.ReadVersion(ctx, saved.ID, saved.Version)
			if err != nil {
				t.Fatal(err)
			}
			changed := *current
			changed.Autonomy.CanPublish = false
			_, err = repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: saved.ID, ExpectedRevision: saved.Revision, Version: changed})
			if !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("ordinary edit replaced policy: %v", err)
			}
			changed = *current
			changed.Charter = "New prose only"
			updated, err := repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: saved.ID, ExpectedRevision: saved.Revision, Version: changed})
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != store.BotDraft {
				t.Fatal("prose activated draft")
			}
		})
	}
}
