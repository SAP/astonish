package entstore

import (
	"context"
	"errors"
	"testing"

	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotMemoryCorrectionReconcile(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			svc, memories := diskMemoryFixture(t, repo, b.ID)
			if err := svc.Add(ctx, store.MemoryEntry{Content: "Thursday", Category: "delivery", SessionID: run.SessionID.String(), CreatedBy: b.OwnerID.String()}); err != nil {
				t.Fatal(err)
			}
			rows, err := memories.ListBySession(ctx, run.SessionID.String())
			if err != nil || len(rows) != 1 {
				t.Fatalf("memory: %v %v", rows, err)
			}
			q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:" + rows[0].ID, Replacement: "Friday", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Confirmed", Source: "owner-message:1", ActorID: b.OwnerID, ObservedAt: *now}}
			if err := repo.Correct(ctx, q); err != nil {
				t.Fatal(err)
			}
			reconciler, ok := any(repo).(interface {
				ReconcileCorrection(context.Context, store.BotID, uuid.UUID, store.MemoryStore) error
			})
			if !ok {
				t.Fatal("durable corrections have no memory reconciliation path")
			}
			// A durable intent is not authorization: membership is checked again
			// before the first application as well as on completed-receipt replay.
			revoked := &botRepository{auth: repo.auth, db: repo.db}
			revoked.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			if err := revoked.ReconcileCorrection(ctx, b.ID, q.ID, memories); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("pending intent bypassed live membership: %v", err)
			}
			pending, err := memories.Get(ctx, rows[0].ID)
			if err != nil || pending.Snippet != "Thursday" {
				t.Fatalf("revoked member changed memory: %+v %v", pending, err)
			}
			if _, err := repo.db.FindActivity(ctx, b.ID, "correction-memory:"+q.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("revoked member created receipt: %v", err)
			}
			// Fail after the memory mutation but before its receipt can commit.
			failReceipt := true
			switch d := repo.db.(type) {
			case teamBotDB:
				d.client.BotOutbox.Use(func(next teament.Mutator) teament.Mutator {
					return teament.MutateFunc(func(ctx context.Context, m teament.Mutation) (teament.Value, error) {
						if failReceipt {
							return nil, errors.New("receipt unavailable")
						}
						return next.Mutate(ctx, m)
					})
				})
			case personalBotDB:
				d.client.BotOutbox.Use(func(next personalent.Mutator) personalent.Mutator {
					return personalent.MutateFunc(func(ctx context.Context, m personalent.Mutation) (personalent.Value, error) {
						if failReceipt {
							return nil, errors.New("receipt unavailable")
						}
						return next.Mutate(ctx, m)
					})
				})
			}
			if err := reconciler.ReconcileCorrection(ctx, b.ID, q.ID, memories); err == nil {
				t.Fatal("missing injected receipt failure")
			}
			unchanged, err := memories.Get(ctx, rows[0].ID)
			if err != nil || unchanged.Snippet != "Thursday" {
				t.Fatalf("partial memory commit: %+v %v", unchanged, err)
			}
			if _, err := repo.db.FindActivity(ctx, b.ID, "correction-memory:"+q.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("receipt on rollback: %v", err)
			}
			failReceipt = false
			// Fresh repository simulates a worker restart after failure.
			reconciler = &botRepository{auth: repo.auth, db: repo.db, now: repo.now}
			for range 2 {
				if err := reconciler.ReconcileCorrection(ctx, b.ID, q.ID, memories); err != nil {
					t.Fatal(err)
				}
			}
			replacementID := uuid.NewSHA1(q.ID, []byte("memory")).String()
			row, err := memories.Get(ctx, replacementID)
			if err != nil || row == nil || row.Snippet != "Friday" || row.Category != "delivery" || row.SessionID != run.SessionID.String() {
				t.Fatalf("not reconciled with provenance: %+v %v", row, err)
			}
			history, err := memories.Get(ctx, rows[0].ID)
			if err != nil || history == nil || history.Snippet != "Thursday" || history.Attribution == nil || history.Attribution.SupersededBy == "" {
				t.Fatalf("lost original evidence: %+v %v", history, err)
			}
			if old, err := svc.Search(ctx, "Thursday", 10, 0); err != nil || len(old) != 0 {
				t.Fatalf("stale recall: %+v %v", old, err)
			}
			switch d := repo.db.(type) {
			case teamBotDB:
				if n, err := d.client.Memory.Query().Count(ctx); err != nil || n != 0 {
					t.Fatalf("SQL memory rows: %d %v", n, err)
				}
			case personalBotDB:
				if n, err := d.client.Memory.Query().Count(ctx); err != nil || n != 0 {
					t.Fatalf("SQL memory rows: %d %v", n, err)
				}
			}
			// A completed old correction must not overwrite a subsequently reviewed edit.
			review := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision + 1, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Confirmed: true, Reason: "newer owner review", Edits: []store.BotMemoryReviewEdit{{SessionID: row.SessionID, Content: "Saturday", Category: row.Category, Sources: []store.BotMemoryReviewSource{{ID: row.ID, SessionID: row.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(*row)}}}}}
			receipt, err := svc.ApplyReviewed(ctx, review)
			if err != nil {
				t.Fatal(err)
			}
			if err := reconciler.ReconcileCorrection(ctx, b.ID, q.ID, memories); err != nil {
				t.Fatal(err)
			}
			row, _ = memories.Get(ctx, receipt.ReplacementIDs[0])
			if row == nil || row.Snippet != "Saturday" {
				t.Fatal("replay overwrote newer memory")
			}

			saved, _ := repo.Get(ctx, b.ID)
			if saved.Version != b.Version || saved.Revision != b.Revision+2 {
				t.Fatalf("memory changed authority: %+v", saved)
			}
			if err := reconciler.ReconcileCorrection(ctx, b.ID, uuid.New(), memories); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("missing intent: %v", err)
			}
			if got, err := svc.Search(ctx, "Saturday", 10, 0); err != nil || len(got) != 1 {
				t.Fatalf("receipt replay lost current recall: %v %v", got, err)
			}
			if err := revoked.ReconcileCorrection(ctx, b.ID, q.ID, memories); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("receipt bypassed live membership: %v", err)
			}
		})
	}
}
