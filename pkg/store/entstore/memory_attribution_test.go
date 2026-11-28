package entstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func attributionContract(t *testing.T, bots store.BotStore, _ store.MemoryStore, ctx context.Context, a, b *store.Bot, scope store.BotScope, runID uuid.UUID, sessionID string) string {
	t.Helper()
	// Bot services and their direct read view use the authoritative disk route.
	// Generic Chat memory remains excluded rather than becoming a legacy fallback.
	memories, err := bots.(store.BotMemoryStoreProvider).BotMemoryStore(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := store.NewBotMemoryService(bots, memories, a.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := bots.SaveGoal(ctx, store.BotGoalUpdate{BotID: a.ID, Goal: store.BotGoal{BotID: a.ID, Title: "Observe", CompletionCriteria: "Receipt"}})
	if err != nil {
		t.Fatal(err)
	}
	otherGoal, err := bots.SaveGoal(ctx, store.BotGoalUpdate{BotID: a.ID, Goal: store.BotGoal{BotID: a.ID, Title: "Other", CompletionCriteria: "Receipt"}})
	if err != nil {
		t.Fatal(err)
	}
	foreignGoal, err := bots.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Foreign", CompletionCriteria: "Receipt"}})
	if err != nil {
		t.Fatal(err)
	}
	attr := store.MemoryAttribution{BotID: a.ID, GoalID: goal.ID, RunID: runID, Source: "receipt:source", ObservedAt: time.Now().UTC(), Status: store.MemoryKnowledgeInferred}
	for _, mutate := range []func(*store.MemoryAttribution){
		func(a *store.MemoryAttribution) { a.BotID = b.ID },
		func(a *store.MemoryAttribution) { a.GoalID = foreignGoal.ID },
		func(a *store.MemoryAttribution) { a.RunID = uuid.New() },
	} {
		bad := attr
		mutate(&bad)
		if err := svc.Add(ctx, store.MemoryEntry{Content: "denied", SessionID: sessionID, Attribution: &bad}); err == nil {
			t.Fatal("foreign attribution accepted")
		}
		if err := memories.Add(ctx, store.MemoryEntry{Content: "denied", SessionID: sessionID, Attribution: &bad}); err == nil {
			t.Fatal("direct store accepted foreign attribution")
		}
	}
	vector := func(x, y float32) []float32 { v := make([]float32, 384); v[0], v[1] = x, y; return v }
	for i := 0; i < 12; i++ {
		distractor := attr
		distractor.GoalID = otherGoal.ID
		if err := svc.Add(ctx, store.MemoryEntry{Content: strings.Repeat("needle ", 32), Category: "fact", SessionID: sessionID, Attribution: &distractor, Embedding: vector(1, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Add(ctx, store.MemoryEntry{Content: "needle legacy", Category: "fact", SessionID: sessionID, Embedding: vector(1, 0)}); err != nil {
		t.Fatal(err)
	}
	entry := store.MemoryEntry{Content: "needle allowed", Category: "fact", SourcePath: "file:source", SessionID: sessionID, Attribution: &attr, Embedding: vector(.8, .6), Metadata: map[string]any{"custom": "preserved"}}
	if err := svc.Add(ctx, entry); err != nil {
		t.Fatal(err)
	}
	filter := store.MemoryAttributionFilter{BotID: a.ID, GoalID: goal.ID, RunID: runID}
	rows, err := svc.SearchAttributed(ctx, filter, "needle", 1, 0, "fact")
	if err != nil || len(rows) != 1 || rows[0].Snippet != "needle allowed" || !reflect.DeepEqual(rows[0].Attribution, &attr) {
		t.Fatalf("attributed topK: %+v %v", rows, err)
	}
	id := rows[0].ID
	ordinary, err := memories.Search(ctx, "needle", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range ordinary {
		if row.ID == id {
			found = true
			if !reflect.DeepEqual(row.Attribution, &attr) {
				t.Fatal("ordinary search lost attribution")
			}
		}
	}
	if !found {
		t.Fatal("ordinary search missing attributed row")
	}
	superseded := attr
	superseded.SupersededBy = "correction:previous"
	if err := svc.Add(ctx, store.MemoryEntry{Content: "needle superseded", Category: "history", SessionID: sessionID, Attribution: &superseded, Embedding: vector(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if rows, err := svc.SearchAttributed(ctx, filter, "needle", 1, 0, "history"); err != nil || len(rows) != 0 {
		t.Fatalf("superseded visible by default: %+v %v", rows, err)
	}
	history := filter
	history.IncludeSuperseded = true
	if rows, err := svc.SearchAttributed(ctx, history, "needle", 1, 0, "history"); err != nil || len(rows) != 1 || rows[0].Attribution.SupersededBy != "correction:previous" {
		t.Fatalf("history unavailable: %+v %v", rows, err)
	}
	verified := attr
	verified.Status = store.MemoryKnowledgeVerified
	verified.Evidence = &store.BotEvidence{Kind: store.BotEvidenceObservation, Source: "receipt:observed", Summary: "Observed", ObservedAt: attr.ObservedAt, ActorID: a.OwnerID}
	if err := svc.Add(ctx, store.MemoryEntry{Content: "needle verified", Category: "evidence", SessionID: sessionID, Attribution: &verified, Embedding: vector(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if rows, err := svc.SearchAttributed(ctx, filter, "needle", 1, 0, "evidence"); err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Attribution, &verified) {
		t.Fatalf("verified evidence lost: %+v %v", rows, err)
	}
	got, err := svc.Get(ctx, id)
	if err != nil || !reflect.DeepEqual(got.Attribution, &attr) {
		t.Fatalf("get attribution: %+v %v", got, err)
	}
	listed, err := svc.ListBySession(ctx, sessionID)
	if err != nil || len(listed) != 16 {
		t.Fatalf("legacy compatibility: %d %v", len(listed), err)
	}
	for _, row := range listed {
		if row.Snippet == "needle legacy" && row.Attribution != nil {
			t.Fatal("legacy upgraded")
		}
	}
	if _, err := svc.SearchAttributed(ctx, store.MemoryAttributionFilter{BotID: b.ID}, "needle", 1, 0, ""); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("foreign Bot filter accepted: %v", err)
	}
	// A disk view never leaves this Bot's catalogue. Foreign goal/run filters
	// may return no matches, but must never widen retrieval to another Bot.
	for _, f := range []store.MemoryAttributionFilter{{BotID: a.ID, GoalID: foreignGoal.ID}, {BotID: a.ID, RunID: uuid.New()}} {
		if rows, err := svc.SearchAttributed(ctx, f, "needle", 1, 0, ""); err != nil || len(rows) != 0 {
			t.Fatalf("foreign goal/run filter widened retrieval: %+v %v", rows, err)
		}
	}
	if rows, err := memories.(store.AttributedMemoryStore).SearchAttributed(ctx, nil, filter, "needle", 1, 0, ""); err != nil || len(rows) != 0 {
		t.Fatalf("empty scope widened: %+v %v", rows, err)
	}
	if err := svc.Update(ctx, id, "needle changed", "fact"); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("attributed content update accepted: %v", err)
	}
	if err := svc.Update(ctx, id, "needle allowed", "recategorized"); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get(ctx, id)
	if err != nil || got.Snippet != "needle allowed" || got.Category != "recategorized" || !reflect.DeepEqual(got.Attribution, &attr) {
		t.Fatalf("category update lost attribution: %+v %v", got, err)
	}
	// Restore the fixture category for both fresh-adapter search contracts.
	if err := svc.Update(ctx, id, "needle allowed", "fact"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMemoryAttributionSQLiteReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"vector", "fts", "hybrid"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				db, bots, memories, ctx, a, b := botMemorySearchFixture(t, scope, mode)
				// Use production dimensionality for shared SQLite/PostgreSQL contract.
				embed := func(context.Context, string) ([]float32, error) { v := make([]float32, 384); v[0] = 1; return v, nil }
				switch m := memories.(type) {
				case *teamMemoryStore:
					if mode != "fts" {
						m.embedFunc = embed
					}
				case *personalMemoryStore:
					if mode != "fts" {
						m.embedFunc = embed
					}
				}
				id := attributionContract(t, bots, memories, ctx, a, b, scope, uuid.Nil, a.HomeSessionID.String())
				disk, err := bots.(store.BotMemoryStoreProvider).BotMemoryStore(a.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := memories.Get(ctx, id); err != nil || got != nil {
					t.Fatalf("Bot memory leaked into generic Chat: %+v %v", got, err)
				}
				original, err := disk.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				var seq int
				var name, path string
				if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				reopened.SetMaxOpenConns(1)
				driver := entsql.OpenDB(dialect.SQLite, reopened)
				switch old := memories.(type) {
				case *teamMemoryStore:
					client := teament.NewClient(teament.Driver(driver))
					memories = &teamMemoryStore{client: client, db: reopened, dialect: DialectSQLite, table: "memories", ftsTable: old.ftsTable, embedFunc: old.embedFunc}
					bots = &teamBotStore{client: client, content: bots.(*teamBotStore).content, auth: bots.(*teamBotStore).auth}
				case *personalMemoryStore:
					client := personalent.NewClient(personalent.Driver(driver))
					memories = &personalMemoryStore{client: client, db: reopened, dialect: DialectSQLite, table: "memories", ftsTable: old.ftsTable, embedFunc: old.embedFunc}
					bots = &personalBotStore{client: client, content: bots.(*personalBotStore).content, auth: bots.(*personalBotStore).auth}
				}
				svc, _ := store.NewBotMemoryService(bots, memories, a.ID, scope)
				got, err := svc.Get(ctx, id)
				if err != nil || !reflect.DeepEqual(got.Attribution, original.Attribution) {
					t.Fatalf("reopen: %+v %v", got, err)
				}
				rows, err := svc.SearchAttributed(ctx, store.MemoryAttributionFilter{BotID: a.ID, GoalID: got.Attribution.GoalID}, "needle", 1, 0, "fact")
				if err != nil || len(rows) != 1 || rows[0].ID != id {
					t.Fatalf("reopen search: %+v %v", rows, err)
				}
				// Tamper the disk attribution, not the deliberately empty SQL table.
				disk, err = bots.(store.BotMemoryStoreProvider).BotMemoryStore(a.ID)
				if err != nil {
					t.Fatal(err)
				}
				memoryDB := disk.(*botDiskMemoryStore).repo.db
				if err := changeBotDiskMemory(ctx, memoryDB, a.ID, *got, func(entry *botDiskMemoryEntry) error {
					entry.Row.Attribution.BotID = b.ID
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Get(ctx, id); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("corrupt attribution treated as legacy: %v", err)
				}
				if _, err := svc.SearchAttributed(ctx, store.MemoryAttributionFilter{BotID: a.ID, GoalID: original.Attribution.GoalID}, "needle", 1, 0, "fact"); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("corrupt catalogue did not fail closed: %v", err)
				}
			})
		}
	}
}

func TestMemoryAttributionCorrection(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, _, run, _, now := botRunFixture(t, scope)
			b, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			svc, memories := diskMemoryFixture(t, repo, b.ID)
			a := &store.MemoryAttribution{BotID: b.ID, RunID: run.ID, Source: "inference:date", ObservedAt: *now, Status: store.MemoryKnowledgeInferred}
			if err := svc.Add(ctx, store.MemoryEntry{Content: "Thursday", SessionID: run.SessionID.String(), Attribution: a, Metadata: map[string]any{"custom": "kept"}}); err != nil {
				t.Fatal(err)
			}
			rows, err := memories.ListBySession(ctx, run.SessionID.String())
			if err != nil || len(rows) != 1 {
				t.Fatalf("seed: %+v %v", rows, err)
			}
			q := store.BotCorrection{ID: uuid.New(), BotID: b.ID, ExpectedRevision: b.Revision, SupersededSource: "memory:" + rows[0].ID, Replacement: "Friday", Evidence: store.BotEvidence{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Date", Source: "owner:1", ActorID: b.OwnerID, ObservedAt: *now}}
			if err := repo.Correct(ctx, q); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := repo.ReconcileCorrection(ctx, b.ID, q.ID, memories); err != nil {
					t.Fatal(err)
				}
			}
			got, err := memories.Get(ctx, rows[0].ID)
			if err != nil || got.Attribution == nil || got.Attribution.SupersededBy != "superseded:correction:"+q.ID.String() || got.Attribution.Status != store.MemoryKnowledgeInferred || got.Attribution.Source != a.Source || got.Snippet != "Thursday" {
				t.Fatalf("correction provenance: %+v %v", got, err)
			}
			replacement, err := memories.Get(ctx, uuid.NewSHA1(q.ID, []byte("memory")).String())
			if err != nil || replacement == nil || replacement.Snippet != "Friday" || replacement.Attribution == nil || replacement.Attribution.Status != store.MemoryKnowledgeVerified || replacement.Attribution.Source != "correction:"+q.ID.String() || !reflect.DeepEqual(replacement.Attribution.Evidence, &q.Evidence) {
				t.Fatalf("replacement provenance: %+v %v", replacement, err)
			}
			catalogue, err := readBotDiskMemory(ctx, repo.db, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			for _, entry := range catalogue.Entries {
				if entry.Row.ID == got.ID {
					metadata = entry.Metadata
				}
			}
			if metadata["custom"] != "kept" {
				t.Fatal("lost unrelated metadata")
			}
		})
	}
}
