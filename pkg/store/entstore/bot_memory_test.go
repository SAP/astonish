package entstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotMemoryAttributedUpdateGuard(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, b, _ := botMemorySearchFixture(t, scope, "vector")
			botMemoryAttributedUpdateContract(t, bots, memories, ctx, b, scope)
		})
	}
}

func botMemoryAttributedUpdateContract(t *testing.T, bots store.BotStore, memories store.MemoryStore, ctx context.Context, b *store.Bot, scope store.BotScope) {
	t.Helper()
	svc, err := store.NewBotMemoryService(bots, memories, b.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	attr := &store.MemoryAttribution{BotID: b.ID, Source: "receipt:observed", ObservedAt: now, Status: store.MemoryKnowledgeVerified, Evidence: &store.BotEvidence{Kind: store.BotEvidenceObservation, Source: "receipt:observed", Summary: "Original observation", ObservedAt: now, ActorID: b.OwnerID}}
	if err := svc.Add(ctx, store.MemoryEntry{SessionID: b.HomeSessionID.String(), Content: "observed original", Category: "fact", SourcePath: "receipt:observed", Attribution: attr, Metadata: map[string]any{"custom": "preserved"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.List(ctx, "fact", 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("seed: %+v %v", rows, err)
	}
	before := rows[0]
	if err := svc.Update(ctx, before.ID, "unobserved replacement", "changed"); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("attributed content edit must require reviewed replacement: %v", err)
	}
	after, err := svc.Get(ctx, before.ID)
	if err != nil || !reflect.DeepEqual(*after, before) {
		t.Fatalf("rejected update changed original: %+v %v", after, err)
	}
	// The backend must not trust an omitted Previous attribution. Entry metadata
	// cannot clear or replace persisted evidence, including on category-only edits.
	stripped := before
	stripped.Attribution = nil
	backend := bots.(store.BotMemoryMutationStore)
	forged := before
	forged.Snippet = "bypass"
	for _, previous := range []store.MemorySearchResult{stripped, forged} {
		if err := backend.MutateBotMemory(ctx, memories, b.ID, store.BotMemoryMutation{Kind: store.BotMemoryUpdate, Previous: previous, Entry: store.MemoryEntry{Content: "bypass"}}); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("forged previous bypass: %v", err)
		}
	}
	for _, category := range []string{"reviewed", ""} {
		if err := svc.Update(ctx, before.ID, before.Snippet, category); err != nil {
			t.Fatal(err)
		}
		after, err = svc.Get(ctx, before.ID)
		expected := before
		expected.Category = category
		if err != nil || !reflect.DeepEqual(*after, expected) {
			t.Fatalf("category edit lost evidence/scope: %+v %v", after, err)
		}
	}
	if err := backend.MutateBotMemory(ctx, memories, b.ID, store.BotMemoryMutation{Kind: store.BotMemoryUpdate, Previous: *after, Entry: store.MemoryEntry{Content: before.Snippet, Attribution: &store.MemoryAttribution{}, Metadata: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	after, err = svc.Get(ctx, before.ID)
	if err != nil || !reflect.DeepEqual(after.Attribution, before.Attribution) || after.SessionID != before.SessionID {
		t.Fatalf("Entry stripped evidence/scope: %+v %v", after, err)
	}
	var metadata map[string]any
	switch m := memories.(type) {
	case *personalMemoryStore:
		row, err := m.client.Memory.Get(ctx, uuid.MustParse(before.ID))
		if err != nil {
			t.Fatal(err)
		}
		metadata = row.Metadata
	case *teamMemoryStore:
		row, err := m.client.Memory.Get(ctx, uuid.MustParse(before.ID))
		if err != nil {
			t.Fatal(err)
		}
		metadata = row.Metadata
	}
	if metadata["custom"] != "preserved" {
		t.Fatal("category edit lost metadata")
	}
	saved, err := bots.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.ApplyReviewed(ctx, store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: saved.Revision, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Reason: "reviewed replacement", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: before.SessionID, Content: "reviewed replacement", Sources: []store.BotMemoryReviewSource{{ID: before.ID, SessionID: before.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(*after)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := svc.Get(ctx, before.ID)
	if err != nil || original.Snippet != before.Snippet || original.Attribution.Status != store.MemoryKnowledgeVerified || !reflect.DeepEqual(original.Attribution.Evidence, before.Attribution.Evidence) || original.Attribution.SupersededBy == "" {
		t.Fatalf("review lost original evidence: %+v %v", original, err)
	}
	replacement, err := svc.Get(ctx, receipt.ReplacementIDs[0])
	if err != nil || replacement.Snippet != "reviewed replacement" || replacement.Attribution.Status != store.MemoryKnowledgeInferred || replacement.Attribution.Evidence != nil {
		t.Fatalf("replacement inherited evidence: %+v %v", replacement, err)
	}
	if err := svc.Update(ctx, replacement.ID, "inferred changed", ""); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("inferred attribution bypass: %v", err)
	}
	if err := svc.Update(ctx, original.ID, "superseded changed", ""); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("superseded attribution bypass: %v", err)
	}
	if err := svc.Add(ctx, store.MemoryEntry{SessionID: before.SessionID, Content: "ordinary", Category: "legacy"}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := svc.List(ctx, "legacy", 10, 0)
	if err != nil || len(ordinary) != 1 {
		t.Fatalf("ordinary seed: %v %v", ordinary, err)
	}
	if err := svc.Update(ctx, ordinary[0].ID, "ordinary changed", "changed"); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.Get(ctx, ordinary[0].ID)
	if err != nil || updated.Snippet != "ordinary changed" || updated.Category != "changed" || updated.Attribution != nil {
		t.Fatalf("ordinary edit changed contract: %+v %v", updated, err)
	}
}

func TestBotMemoryRevokedAtCommit(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		for _, op := range []string{"add", "update", "delete"} {
			t.Run(string(scope)+"/"+op, func(t *testing.T) {
				_, bots, memories, ctx, a, _ := botMemorySearchFixture(t, scope, "vector")
				role := "member"
				check := func(context.Context, string) (string, error) { return role, nil }
				switch b := bots.(type) {
				case *teamBotStore:
					b.auth.check = check
				case *personalBotStore:
					b.auth.check = check
				}
				svc, _ := store.NewBotMemoryService(bots, memories, a.ID, scope)
				if err := svc.Add(ctx, store.MemoryEntry{Content: "original", SessionID: a.HomeSessionID.String()}); err != nil {
					t.Fatal(err)
				}
				rows, _ := svc.List(ctx, "", 10, 0)
				// Revoke after the Ent memory statement succeeds, but before commit.
				hook := func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						value, err := next.Mutate(ctx, mutation)
						role = "revoked"
						return value, err
					})
				}
				switch m := memories.(type) {
				case *teamMemoryStore:
					m.client.Memory.Use(hook)
				case *personalMemoryStore:
					m.client.Memory.Use(hook)
				}
				var err error
				switch op {
				case "add":
					err = svc.Add(ctx, store.MemoryEntry{Content: "denied", SessionID: a.HomeSessionID.String()})
				case "update":
					err = svc.Update(ctx, rows[0].ID, "denied", "")
				case "delete":
					err = svc.Delete(ctx, rows[0].ID)
				}
				if !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("commit guard: %v", err)
				}
				persisted, err := memories.ListBySession(ctx, a.HomeSessionID.String())
				if err != nil || len(persisted) != 1 || persisted[0].Snippet != "original" {
					t.Fatalf("rollback: %+v %v", persisted, err)
				}
				var index *vectorIndex
				switch m := memories.(type) {
				case *teamMemoryStore:
					index = m.vecIndex
				case *personalMemoryStore:
					index = m.vecIndex
				}
				cached := index.search([]float32{1, 0}, 10, 0.9)
				if len(cached) != 1 || cached[0].ID != rows[0].ID {
					t.Fatalf("rolled-back mutation changed cache: %+v", cached)
				}
			})
		}
	}
}

func TestBotMemoryMismatchedMutationStore(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, _, ctx, a, _ := botMemorySearchFixture(t, scope, "vector")
			_, _, other, _, _, _ := botMemorySearchFixture(t, scope, "vector")
			svc, _ := store.NewBotMemoryService(bots, other, a.ID, scope)
			if err := svc.Add(ctx, store.MemoryEntry{Content: "denied", SessionID: a.HomeSessionID.String()}); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("cross-store mutation: %v", err)
			}
		})
	}
}

func TestBotMemoryRevokedDuringEmbedding(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		for _, roleAfter := range []string{"viewer", "revoked"} {
			for _, op := range []string{"add", "update"} {
				t.Run(string(scope)+"/"+roleAfter+"/"+op, func(t *testing.T) {
					_, bots, memories, ctx, a, _ := botMemorySearchFixture(t, scope, "vector")
					var role atomic.Value
					role.Store("member")
					check := func(context.Context, string) (string, error) { return role.Load().(string), nil }
					switch b := bots.(type) {
					case *teamBotStore:
						b.auth.check = check
					case *personalBotStore:
						b.auth.check = check
					}
					svc, _ := store.NewBotMemoryService(bots, memories, a.ID, scope)
					if err := svc.Add(ctx, store.MemoryEntry{Content: "original", SessionID: a.HomeSessionID.String()}); err != nil {
						t.Fatal(err)
					}
					rows, _ := svc.List(ctx, "", 10, 0)
					entered, release := make(chan struct{}), make(chan struct{})
					embed := func(context.Context, string) ([]float32, error) {
						close(entered)
						<-release
						return []float32{0, 1}, nil
					}
					switch m := memories.(type) {
					case *teamMemoryStore:
						m.embedFunc = embed
					case *personalMemoryStore:
						m.embedFunc = embed
					}
					done := make(chan error, 1)
					go func() {
						if op == "add" {
							done <- svc.Add(ctx, store.MemoryEntry{Content: "unauthorized", SessionID: a.HomeSessionID.String()})
						} else {
							done <- svc.Update(ctx, rows[0].ID, "unauthorized", "")
						}
					}()
					select {
					case <-entered:
					case <-time.After(5 * time.Second):
						t.Fatal("embedding not reached")
					}
					role.Store(roleAfter)
					close(release)
					if err := <-done; !errors.Is(err, store.ErrBotForbidden) {
						t.Errorf("revoked mutation: %v", err)
					}
					persisted, err := memories.ListBySession(ctx, a.HomeSessionID.String())
					if err != nil || len(persisted) != 1 || persisted[0].Snippet != "original" {
						t.Fatalf("unauthorized persistence: %+v %v", persisted, err)
					}
				})
			}
		}
	}
}

func TestBotMemoryReattributedDuringEmbedding(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			_, bots, memories, ctx, a, b := botMemorySearchFixture(t, scope, "vector")
			svc, _ := store.NewBotMemoryService(bots, memories, a.ID, scope)
			if err := svc.Add(ctx, store.MemoryEntry{Content: "original", SessionID: a.HomeSessionID.String()}); err != nil {
				t.Fatal(err)
			}
			rows, _ := svc.List(ctx, "", 10, 0)
			entered, release := make(chan struct{}), make(chan struct{})
			embed := func(context.Context, string) ([]float32, error) {
				close(entered)
				<-release
				return []float32{0, 1}, nil
			}
			switch m := memories.(type) {
			case *teamMemoryStore:
				m.embedFunc = embed
			case *personalMemoryStore:
				m.embedFunc = embed
			}
			done := make(chan error, 1)
			go func() { done <- svc.Update(ctx, rows[0].ID, "stolen", "") }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("embedding not reached")
			}
			id := uuid.MustParse(rows[0].ID)
			var err error
			switch m := memories.(type) {
			case *teamMemoryStore:
				err = m.client.Memory.UpdateOneID(id).SetSessionID(b.HomeSessionID).Exec(ctx)
			case *personalMemoryStore:
				err = m.client.Memory.UpdateOneID(id).SetSessionID(b.HomeSessionID).Exec(ctx)
			}
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, store.ErrBotConflict) && !errors.Is(err, store.ErrBotForbidden) {
				t.Errorf("reattributed mutation: %v", err)
			}
			// A delete carrying the service's earlier authorized snapshot must
			// also fail at its constrained write, without an embedding step.
			err = bots.(store.BotMemoryMutationStore).MutateBotMemory(ctx, memories, a.ID, store.BotMemoryMutation{Kind: store.BotMemoryDelete, Previous: rows[0]})
			if !errors.Is(err, store.ErrBotConflict) && !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("reattributed delete: %v", err)
			}
			row, err := memories.Get(ctx, rows[0].ID)
			if err != nil || row.Snippet != "original" || row.SessionID != b.HomeSessionID.String() {
				t.Fatalf("stolen memory: %+v %v", row, err)
			}
		})
	}
}

func concreteBotMemoryStores(repo *botRepository) (store.BotStore, store.MemoryStore) {
	switch d := repo.db.(type) {
	case teamBotDB:
		return &teamBotStore{client: d.client, auth: repo.auth}, &teamMemoryStore{client: d.client, dialect: DialectSQLite}
	case personalBotDB:
		return &personalBotStore{client: d.client, auth: repo.auth}, &personalMemoryStore{client: d.client, dialect: DialectSQLite}
	}
	panic("unknown fixture scope")
}

func TestBotMemoryConcreteCRUDAndAuthorization(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, _, _ := botRunFixture(t, scope)
			role := "member"
			repo.auth.check = func(context.Context, string) (string, error) { return role, nil }
			bots, memories := concreteBotMemoryStores(repo)
			svc, err := store.NewBotMemoryService(bots, memories, run.BotID, scope)
			if err != nil {
				t.Fatal(err)
			}
			b, err := bots.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			for _, sid := range []string{b.HomeSessionID.String(), run.SessionID.String()} {
				if err := svc.Add(ctx, store.MemoryEntry{Content: "owner memory", SessionID: sid, Category: "fact", SourcePath: "message:1"}); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := svc.List(ctx, "fact", 1, 1)
			if err != nil || len(rows) != 1 || rows[0].CreatedBy != b.OwnerID.String() || rows[0].Scope != string(scope) || rows[0].Path != "message:1" {
				t.Fatalf("list: %+v %v", rows, err)
			}
			id := rows[0].ID
			if err := svc.Update(ctx, id, "updated", "fact"); err != nil {
				t.Fatal(err)
			}
			got, err := svc.Get(ctx, id)
			if err != nil || got.Snippet != "updated" {
				t.Fatalf("get: %+v %v", got, err)
			}
			for _, deniedCtx := range []context.Context{context.Background(), worker} {
				if err := svc.Update(deniedCtx, id, "unauthorized", "fact"); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("identity: %v", err)
				}
			}
			if err := svc.Add(ctx, store.MemoryEntry{Content: "forged actor", SessionID: run.SessionID.String(), CreatedBy: uuid.NewString()}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("provenance: %v", err)
			}
			role = "viewer"
			if _, err := svc.Get(ctx, id); err != nil {
				t.Fatalf("viewer read: %v", err)
			}
			for _, op := range []func() error{
				func() error {
					return svc.Add(ctx, store.MemoryEntry{Content: "viewer", SessionID: run.SessionID.String()})
				},
				func() error { return svc.Update(ctx, id, "viewer", "") },
				func() error { return svc.Delete(ctx, id) },
			} {
				if err := op(); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("viewer write: %v", err)
				}
			}
			role = "revoked"
			if _, err := svc.List(ctx, "", 10, 0); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked read: %v", err)
			}
			if err := svc.Delete(ctx, id); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked write: %v", err)
			}
			role = "member"
			if scope == store.BotScopeTeam {
				p, _ := execution.PrincipalFromContext(ctx)
				p.Subject = uuid.NewString()
				memberCtx, err := execution.WithPrincipal(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Get(memberCtx, id); err != nil {
					t.Fatal(err)
				}
				if err := svc.Update(memberCtx, id, "nonowner", ""); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("read member elevated: %v", err)
				}
			}
			if err := svc.Delete(ctx, id); err != nil {
				t.Fatal(err)
			}
			if got, err := svc.Get(ctx, id); err != nil || got != nil {
				t.Fatalf("delete: %+v %v", got, err)
			}
		})
	}
}

func botMemorySearchFixture(t *testing.T, scope store.BotScope, mode string) (*sql.DB, store.BotStore, store.MemoryStore, context.Context, *store.Bot, *store.Bot) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := execution.PrincipalFromContext(ctx)
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: p.Subject, scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	var bots store.BotStore
	var memories store.MemoryStore
	var embed store.EmbedFunc
	fts := ""
	if mode != "fts" {
		embed = func(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }
	}
	if mode != "vector" {
		fts = "memories_fts"
	}
	driver := entsql.OpenDB(dialect.SQLite, db)
	if scope == store.BotScopeTeam {
		client := teament.NewClient(teament.Driver(driver))
		if err := client.Schema.Create(ctx); err != nil {
			t.Fatal(err)
		}
		bots = &teamBotStore{client: client, auth: auth}
		memories = &teamMemoryStore{client: client, db: db, dialect: DialectSQLite, table: "memories", ftsTable: fts, embedFunc: embed, vecIndex: newVectorIndex()}
	} else {
		client := personalent.NewClient(personalent.Driver(driver))
		if err := client.Schema.Create(ctx); err != nil {
			t.Fatal(err)
		}
		bots = &personalBotStore{client: client, auth: auth}
		memories = &personalMemoryStore{client: client, db: db, dialect: DialectSQLite, table: "memories", ftsTable: fts, embedFunc: embed, vecIndex: newVectorIndex()}
	}
	if fts != "" {
		for _, stmt := range []string{
			"CREATE VIRTUAL TABLE memories_fts USING fts5(chunk_text, content='memories', content_rowid='rowid')",
			"CREATE TRIGGER memory_fts_insert AFTER INSERT ON memories BEGIN INSERT INTO memories_fts(rowid,chunk_text) VALUES (new.rowid,new.chunk_text); END",
		} {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	v := store.DefaultBotVersion()
	v.Charter = "Observe"
	a, err := bots.CreateDraft(ctx, store.BotCreateRequest{Name: "A", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := bots.CreateDraft(ctx, store.BotCreateRequest{Name: "B", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "b"})
	if err != nil {
		t.Fatal(err)
	}
	return db, bots, memories, ctx, a, b
}

func TestBotMemoryPrefilterBeforeTopK(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		for _, mode := range []string{"vector", "fts", "hybrid"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				db, bots, memories, ctx, a, b := botMemorySearchFixture(t, scope, mode)
				svc, err := store.NewBotMemoryService(bots, memories, a.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
				// Earlier, stronger, same-category other-Bot and unlinked distractors;
				// these swamp both the old 2*K search pool and ordinary list pagination.
				for i := 0; i < 20; i++ {
					sid := b.HomeSessionID.String()
					if i%2 == 0 {
						sid = uuid.NewString()
					}
					if err := memories.Add(ctx, store.MemoryEntry{Content: "needle", Category: "fact", SessionID: sid, Embedding: []float32{1, 0}}); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < 5; i++ {
					if err := svc.Add(ctx, store.MemoryEntry{Content: "needle", Category: "distractor", SessionID: a.HomeSessionID.String(), Embedding: []float32{1, 0}}); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < 2; i++ {
					if err := svc.Add(ctx, store.MemoryEntry{Content: fmt.Sprintf("needle allowed %d %s", i, strings.Repeat("padding ", 30)), Category: "fact", SessionID: a.HomeSessionID.String(), Embedding: []float32{0.8, 0.6}, SourcePath: "source:owned"}); err != nil {
						t.Fatal(err)
					}
				}
				// Restart-style adapter: intentionally forget the shared in-memory vector
				// cache. Search must use authoritative persisted, scoped embeddings.
				switch m := memories.(type) {
				case *teamMemoryStore:
					m.vecIndex = newVectorIndex()
				case *personalMemoryStore:
					m.vecIndex = newVectorIndex()
				}
				rows, err := svc.SearchByCategory(ctx, "needle", 1, 0.3, "fact")
				if err != nil || len(rows) != 1 || rows[0].SessionID != a.HomeSessionID.String() || !strings.Contains(rows[0].Snippet, "allowed") || rows[0].CreatedBy != a.OwnerID.String() || rows[0].Path != "source:owned" || rows[0].Score <= 0 {
					t.Fatalf("topK lost to distractors: %+v %v", rows, err)
				}
				rows, err = svc.SearchByCategory(ctx, "needle", 1, 0, "missing' OR 1=1 --")
				if err != nil || len(rows) != 0 {
					t.Fatalf("empty category widened: %+v %v", rows, err)
				}
				rows, err = svc.List(ctx, "fact", 1, 1)
				if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Snippet, "allowed") {
					t.Fatalf("pagination before isolation: %+v %v", rows, err)
				}
				backend := memories.(store.BotMemoryPrefilterStore)
				if rows, err := backend.SearchBySessions(ctx, []string{uuid.NewString()}, "needle", 1, 0, ""); err != nil || len(rows) != 0 {
					t.Fatalf("unknown session widened: %+v %v", rows, err)
				}
				if rows, err := backend.ListBySessions(ctx, []string{uuid.NewString()}, "", 1, 0); err != nil || len(rows) != 0 {
					t.Fatalf("unknown session list widened: %+v %v", rows, err)
				}
				for _, ids := range [][]string{nil, {}} {
					rows, err := backend.SearchBySessions(ctx, ids, "needle", 1, 0, "")
					if err != nil || len(rows) != 0 {
						t.Fatalf("empty search widened: %+v %v", rows, err)
					}
					rows, err = backend.ListBySessions(ctx, ids, "", 1, 0)
					if err != nil || len(rows) != 0 {
						t.Fatalf("empty list widened: %+v %v", rows, err)
					}
				}
				foreign, err := memories.ListBySession(ctx, b.HomeSessionID.String())
				if err != nil || len(foreign) == 0 {
					t.Fatal(err)
				}
				if _, err := svc.Get(ctx, foreign[0].ID); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("cross-Bot get: %v", err)
				}
				if err := svc.Update(ctx, foreign[0].ID, "stolen", ""); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("cross-Bot update: %v", err)
				}
				if err := svc.Delete(ctx, foreign[0].ID); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("cross-Bot delete: %v", err)
				}
				if err := svc.Add(ctx, store.MemoryEntry{Content: "crossBot", SessionID: b.HomeSessionID.String()}); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("cross-Bot add: %v", err)
				}
				// Broken FTS cannot fall back to a global search (or quietly empty results).
				if mode != "vector" {
					if _, err := db.ExecContext(ctx, "DROP TABLE memories_fts"); err != nil {
						t.Fatal(err)
					}
					if _, err := svc.Search(ctx, "needle", 1, 0); err == nil {
						t.Fatal("FTS error swallowed")
					}
				}
			})
		}
	}
}

func TestBotMemoryPrefilterInvalidAndUnavailable(t *testing.T) {
	for _, backend := range []store.BotMemoryPrefilterStore{&teamMemoryStore{}, &personalMemoryStore{}} {
		if _, err := backend.SearchBySessions(context.Background(), []string{"bad"}, "query", 1, 0, ""); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("invalid sessions: %v", err)
		}
		if _, err := backend.ListBySessions(context.Background(), []string{uuid.Nil.String()}, "", 1, 0); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("zero session: %v", err)
		}
		if _, err := backend.SearchBySessions(context.Background(), []string{uuid.NewString()}, "query", 1, 0, ""); !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatalf("unavailable search: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := backend.SearchBySessions(ctx, nil, "query", 1, 0, ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	}
}

func TestBotMemorySQLScope(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
		clause, args := botMemorySQLScope(dialect, ids, "fact' OR 1=1 --")
		expected := "session_id IN (?,?) AND category = ?"
		if dialect == DialectPostgres {
			expected = "session_id IN ($1,$2) AND category = $3"
		}
		if clause != expected || len(args) != 3 || args[0] != ids[0].String() || args[2] != "fact' OR 1=1 --" {
			t.Fatalf("unsafe scope: %s %v", clause, args)
		}
	}
}
