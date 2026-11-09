package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotMemoryCorrectionIsolation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, _ := repo.Get(ctx, run.BotID)
			v := store.DefaultBotVersion()
			v.Charter = "Other Bot"
			other, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Other", OwnerID: b.OwnerID, Version: v, IdempotencyKey: "other"})
			if err != nil {
				t.Fatal(err)
			}
			var memories store.MemoryStore
			var public store.BotCorrectionReconciler
			switch d := repo.db.(type) {
			case teamBotDB:
				memories = &teamMemoryStore{client: d.client, dialect: DialectSQLite}
				public = &teamBotStore{client: d.client, auth: repo.auth}
			case personalBotDB:
				memories = &personalMemoryStore{client: d.client, dialect: DialectSQLite}
				public = &personalBotStore{client: d.client, auth: repo.auth}
			}
			for _, session := range []string{other.HomeSessionID.String(), ""} {
				if err := memories.Add(ctx, store.MemoryEntry{Content: "Private fact", SourcePath: "bot:" + b.ID.String(), SessionID: session, CreatedBy: b.OwnerID.String(), Metadata: map[string]any{"bot_id": b.ID.String()}}); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := memories.List(ctx, "", 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				current, _ := repo.Get(ctx, b.ID)
				q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: current.Revision, SupersededSource: "memory:" + row.ID, Replacement: "Must not write", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Confirmed", Source: "owner-message", ActorID: b.OwnerID, ObservedAt: *now}}
				if err := repo.Correct(ctx, q); err != nil {
					t.Fatal(err)
				}
				if err := public.ReconcileCorrection(ctx, b.ID, q.ID, memories); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("cross-Bot/Chat mutation allowed: %v", err)
				}
				if _, err := repo.db.FindActivity(ctx, b.ID, "correction-memory:"+q.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("foreign memory produced receipt: %v", err)
				}
				unchanged, err := memories.Get(ctx, row.ID)
				if err != nil || unchanged.Snippet != row.Snippet {
					t.Fatalf("foreign memory changed: %+v %v", unchanged, err)
				}
				// A different database (even the same scope) cannot be passed as the destination.
				otherRepo, _, _, _, _, _ := botRunFixture(t, scope)
				var foreign store.MemoryStore
				switch d := otherRepo.db.(type) {
				case teamBotDB:
					foreign = &teamMemoryStore{client: d.client}
				case personalBotDB:
					foreign = &personalMemoryStore{client: d.client}
				}
				if err := public.ReconcileCorrection(ctx, b.ID, q.ID, foreign); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("foreign database accepted: %v", err)
				}
				if err := public.ReconcileCorrection(context.Background(), b.ID, q.ID, memories); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("missing principal: %v", err)
				}
			}
		})
	}
}

func TestBotMemoryCorrectionOutOfOrder(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, _ := repo.Get(ctx, run.BotID)
			var memories store.MemoryStore
			switch d := repo.db.(type) {
			case teamBotDB:
				memories = &teamMemoryStore{client: d.client, dialect: DialectSQLite}
			case personalBotDB:
				memories = &personalMemoryStore{client: d.client, dialect: DialectSQLite}
			}
			if err := memories.Add(ctx, store.MemoryEntry{Content: "Thursday", SessionID: run.SessionID.String()}); err != nil {
				t.Fatal(err)
			}
			rows, err := memories.ListBySession(ctx, run.SessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:" + rows[0].ID, Replacement: "Friday", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Confirmed", Source: "owner-message", ActorID: b.OwnerID, ObservedAt: *now}}
			if err := repo.Correct(ctx, q); err != nil {
				t.Fatal(err)
			}
			newer := q
			newer.ID = uuid.New()
			newer.ExpectedRevision++
			newer.Replacement = "Saturday"
			// UUID spelling must not let an older intent evade source matching.
			newer.SupersededSource = "memory:" + strings.ToUpper(rows[0].ID)
			if err := repo.Correct(ctx, newer); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- repo.ReconcileCorrection(ctx, b.ID, newer.ID, memories) }()
			}
			wg.Wait()
			close(errs)
			success := 0
			for err := range errs {
				if err == nil {
					success++
				}
			}
			if success == 0 {
				t.Fatal("neither concurrent reconciliation committed")
			}
			// A transaction conflict/busy loser may retry safely.
			if err := repo.ReconcileCorrection(ctx, b.ID, newer.ID, memories); err != nil {
				t.Fatal(err)
			}
			activities, err := repo.ListActivity(ctx, b.ID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, a := range activities {
				var payload struct {
					Applied bool `json:"memory_applied"`
				}
				if err := json.Unmarshal(a.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Applied {
					receipts++
				}
			}
			if receipts != 1 {
				t.Fatalf("duplicate receipts: %d", receipts)
			}
			if err := repo.ReconcileCorrection(ctx, b.ID, q.ID, memories); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("older correction applied: %v", err)
			}
			row, _ := memories.Get(ctx, rows[0].ID)
			if row.Snippet != "Saturday" {
				t.Fatal("older intent clobbered new correction")
			}
		})
	}
}
