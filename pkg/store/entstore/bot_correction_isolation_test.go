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
			other, err := repo.Create(ctx, store.BotCreateRequest{Name: "Other", OwnerID: b.OwnerID, Version: v, IdempotencyKey: "other"})
			if err != nil {
				t.Fatal(err)
			}
			var memories store.MemoryStore
			var public store.BotCorrectionReconciler
			switch d := repo.db.(type) {
			case teamBotDB:
				memories = &teamMemoryStore{client: d.client, dialect: DialectSQLite}
				public = &teamBotStore{client: d.client, content: d.content, auth: repo.auth}
			case personalBotDB:
				memories = &personalMemoryStore{client: d.client, dialect: DialectSQLite}
				public = &personalBotStore{client: d.client, content: d.content, auth: repo.auth}
			}
			otherSvc, otherDisk := diskMemoryFixture(t, repo, other.ID)
			if err := otherSvc.Add(ctx, store.MemoryEntry{Content: "Private fact", SourcePath: "bot:" + b.ID.String(), SessionID: other.HomeSessionID.String(), Metadata: map[string]any{"bot_id": b.ID.String()}}); err != nil {
				t.Fatal(err)
			}
			if err := memories.Add(ctx, store.MemoryEntry{Content: "Private Chat fact", SourcePath: "bot:" + b.ID.String()}); err != nil {
				t.Fatal(err)
			}
			chatRows, err := memories.List(ctx, "", 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			botRows, err := otherDisk.List(ctx, "", 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, targetDisk := diskMemoryFixture(t, repo, b.ID)
			rows := append(botRows, chatRows...)
			for _, row := range rows {

				current, _ := repo.Get(ctx, b.ID)
				q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: current.Revision, SupersededSource: "memory:" + row.ID, Replacement: "Must not write", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Confirmed", Source: "owner-message", ActorID: b.OwnerID, ObservedAt: *now}}
				if err := repo.Correct(ctx, q); err != nil {
					t.Fatal(err)
				}
				if err := public.ReconcileCorrection(ctx, b.ID, q.ID, targetDisk); !errors.Is(err, store.ErrBotForbidden) && !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("cross-Bot/Chat mutation allowed: %v", err)
				}
				if _, err := repo.db.FindActivity(ctx, b.ID, "correction-memory:"+q.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("foreign memory produced receipt: %v", err)
				}
				readStore := memories
				if row.SessionID == other.HomeSessionID.String() {
					readStore = otherDisk
				}
				unchanged, err := readStore.Get(ctx, row.ID)
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
				if err := repo.ReconcileCorrection(ctx, b.ID, q.ID, foreign); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("foreign database accepted: %v", err)
				}
				if err := public.ReconcileCorrection(context.Background(), b.ID, q.ID, memories); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("missing principal: %v", err)
				}
			}
		})
	}
}

func TestBotMemoryCorrectionRepositoryIdentity(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			bots, generic := concreteBotMemoryStores(repo)
			public := bots.(store.BotCorrectionReconciler)
			svc, disk := diskMemoryFixture(t, repo, b.ID)
			if err := svc.Add(ctx, store.MemoryEntry{Content: "original", SessionID: run.SessionID.String()}); err != nil {
				t.Fatal(err)
			}
			rows, err := disk.ListBySession(ctx, run.SessionID.String())
			if err != nil || len(rows) != 1 {
				t.Fatalf("seed: %+v %v", rows, err)
			}
			q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:" + rows[0].ID, Replacement: "replacement", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Confirmed", Source: "owner", ActorID: b.OwnerID, ObservedAt: *now}}
			if err := repo.Correct(ctx, q); err != nil {
				t.Fatal(err)
			}
			valid := disk.(*botDiskMemoryStore)
			wrongBot := *valid
			wrongBot.id = uuid.New()
			wrongOwner := *valid
			wrongOwner.owner = nil
			missingRepo := *valid
			missingRepo.repo = nil
			foreignRepo, _, _, _, _, _ := botRunFixture(t, scope)
			wrongClient := *valid
			wrongClient.repo = foreignRepo // Keep the expected owner marker: the DB must also match.
			wrongCodec := *valid
			copyRepo := *valid.repo
			switch db := copyRepo.db.(type) {
			case personalBotDB:
				db.content = &botContentCodec{}
				copyRepo.db = db
			case teamBotDB:
				db.content = &botContentCodec{}
				copyRepo.db = db
			}
			wrongCodec.repo = &copyRepo
			var typedNil *botDiskMemoryStore
			for name, memory := range map[string]store.MemoryStore{
				"nil": nil, "typed-nil": typedNil, "generic": generic,
				"wrong-bot": &wrongBot, "wrong-owner": &wrongOwner,
				"missing-repo": &missingRepo, "wrong-client": &wrongClient, "wrong-codec": &wrongCodec,
			} {
				t.Run(name, func(t *testing.T) {
					if err := public.ReconcileCorrection(ctx, b.ID, q.ID, memory); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("untrusted memory destination accepted: %v", err)
					}
					if _, err := repo.db.FindActivity(ctx, b.ID, "correction-memory:"+q.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
						t.Fatalf("invalid destination produced receipt: %v", err)
					}
				})
			}
			if err := public.ReconcileCorrection(context.Background(), b.ID, q.ID, disk); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("missing principal accepted: %v", err)
			}
			for range 2 {
				if err := public.ReconcileCorrection(ctx, b.ID, q.ID, disk); err != nil {
					t.Fatal(err)
				}
			}
			replacement, err := disk.Get(ctx, uuid.NewSHA1(q.ID, []byte("memory")).String())
			if err != nil || replacement == nil || replacement.Snippet != q.Replacement {
				t.Fatalf("valid destination: %+v %v", replacement, err)
			}
		})
	}
}

func TestBotMemoryCorrectionOutOfOrder(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, _ := repo.Get(ctx, run.BotID)
			svc, memories := diskMemoryFixture(t, repo, b.ID)
			if err := svc.Add(ctx, store.MemoryEntry{Content: "Thursday", SessionID: run.SessionID.String()}); err != nil {
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
			row, err := memories.Get(ctx, uuid.NewSHA1(newer.ID, []byte("memory")).String())
			if err != nil || row == nil || row.Snippet != "Saturday" {
				t.Fatal("older intent clobbered new correction")
			}
		})
	}
}
