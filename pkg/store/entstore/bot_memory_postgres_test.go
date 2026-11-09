//go:build integration

package entstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"entgo.io/ent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestIntegrationPostgresBotMemoryAttributedUpdateGuard(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPostgresBotServicesFixture(t, scope, 100, 100)
			b, err := f.bots.Get(f.owner, f.run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			botMemoryAttributedUpdateContract(t, f.bots, f.memories, f.owner, b, scope)
			// A successful nested Ent UpdateOne must not commit the outer SQL
			// transaction: force a later failure and verify complete rollback.
			svc, err := store.NewBotMemoryService(f.bots, f.memories, b.ID, scope)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := svc.List(f.owner, "", 100, 0)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := f.bots.Get(f.owner, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			var source store.MemorySearchResult
			for _, row := range rows {
				if row.Snippet == "reviewed replacement" {
					source = row
				}
			}
			if source.ID == "" {
				t.Fatal("missing reviewed source")
			}
			failure := errors.New("after nested update")
			hook := func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					value, err := next.Mutate(ctx, mutation)
					if err == nil && mutation.Op() == ent.OpUpdateOne {
						return nil, failure
					}
					return value, err
				})
			}
			switch m := f.memories.(type) {
			case *personalMemoryStore:
				m.client.Memory.Use(hook)
			case *teamMemoryStore:
				m.client.Memory.Use(hook)
			}
			_, err = svc.ApplyReviewed(f.owner, store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: saved.Revision, Kind: store.BotMemoryReviewDocument, Document: "MEMORY.md", Reason: "rollback test", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: source.SessionID, Content: "must roll back", Sources: []store.BotMemoryReviewSource{{ID: source.ID, SessionID: source.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(source)}}}}})
			if !errors.Is(err, failure) {
				t.Fatalf("nested update failure: %v", err)
			}
			after, err := svc.List(f.owner, "", 100, 0)
			if err != nil || !reflect.DeepEqual(after, rows) {
				t.Fatalf("nested update committed prematurely: %+v %v", after, err)
			}
			afterBot, err := f.bots.Get(f.owner, b.ID)
			if err != nil || afterBot.Revision != saved.Revision {
				t.Fatalf("rollback changed revision: %+v %v", afterBot, err)
			}
		})
	}
}

// Use the production PostgreSQL router and both concrete memory adapters. The
// shared fixture provisions disposable databases, real owners, Bots and sessions.
// Missing infrastructure is a failure, never a silently skipped acceptance test.
func TestIntegrationPostgresBotMemoryPrefilterBeforeTopK(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DSN") == "" {
		t.Fatal("ASTONISH_TEST_DSN is required: use disposable PostgreSQL, never production")
	}
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		for _, mode := range []string{"vector", "fts", "hybrid"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newPostgresBotServicesFixture(t, scope, 100, 100)
				a, err := f.bots.Get(f.owner, f.run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				v := store.DefaultBotVersion()
				v.Charter = "Unrelated Bot must not consume another Bot's top-K"
				b, err := f.bots.CreateDraft(f.owner, store.BotCreateRequest{Name: "Distractor", OwnerID: a.OwnerID, Version: v, IdempotencyKey: "memory-distractor"})
				if err != nil {
					t.Fatal(err)
				}
				vector := func(x, y float32) []float32 {
					result := make([]float32, 384)
					result[0], result[1] = x, y
					return result
				}
				query := "needle"
				if mode == "vector" {
					// No keyword matches: success must come from persisted pgvector.
					query = "semanticprobe"
				}
				embedCalls := 0
				seeding := true
				var embed store.EmbedFunc = func(_ context.Context, text string) ([]float32, error) {
					if seeding {
						if text == "needle allowed" {
							return vector(0.8, 0.6), nil
						}
						return vector(1, 0), nil
					}
					embedCalls++
					if text != query {
						return nil, fmt.Errorf("unexpected embedding request %q", text)
					}
					return vector(1, 0), nil
				}
				var db *sql.DB
				switch m := f.memories.(type) {
				case *teamMemoryStore:
					db, m.embedFunc, m.vecIndex = m.db, embed, newVectorIndex()
				case *personalMemoryStore:
					db, m.embedFunc, m.vecIndex = m.db, embed, newVectorIndex()
				default:
					t.Fatalf("not a concrete PostgreSQL memory store: %T", f.memories)
				}
				svc, err := store.NewBotMemoryService(f.bots, f.memories, a.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
				other, err := store.NewBotMemoryService(f.bots, f.memories, b.ID, scope)
				if err != nil {
					t.Fatal(err)
				}
				// More than 2*K stronger same-category distractors defeat post-filtering
				// of an unrestricted candidate pool. Repetition raises actual ts_rank,
				// unlike padding, which PostgreSQL's default ts_rank does not penalize.
				for i := 0; i < 24; i++ {
					if err := other.Add(f.owner, store.MemoryEntry{Content: strings.Repeat("needle ", 32) + fmt.Sprint(i), Category: "fact", SessionID: b.HomeSessionID.String(), Embedding: vector(1, 0)}); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < 8; i++ {
					if err := svc.Add(f.owner, store.MemoryEntry{Content: strings.Repeat("needle ", 32), Category: "distractor", SessionID: a.HomeSessionID.String(), Embedding: vector(1, 0)}); err != nil {
						t.Fatal(err)
					}
				}
				allowed := map[string]bool{a.HomeSessionID.String(): true, f.run.SessionID.String(): true}
				for sid := range allowed {
					if err := svc.Add(f.owner, store.MemoryEntry{Content: "needle allowed", Category: "fact", SessionID: sid, Embedding: vector(0.8, 0.6), SourcePath: "source:owned"}); err != nil {
						t.Fatal(err)
					}
				}

				seeding = false
				if mode == "fts" {
					switch m := f.memories.(type) {
					case *teamMemoryStore:
						m.embedFunc = nil
					case *personalMemoryStore:
						m.embedFunc = nil
					}
				}

				// Assert the fixture really swamps BOTH global candidate lists; this
				// is independent SQL evidence, not an assumption about ranking.
				var strongerVector, strongerFTS, keywordMatches int
				err = db.QueryRowContext(f.owner, `SELECT
					count(*) FILTER (WHERE session_id = $1 AND (1 - (embedding <=> $3::vector)) >
						(SELECT max(1 - (embedding <=> $3::vector)) FROM memories WHERE session_id = $2 AND category = 'fact')),
					count(*) FILTER (WHERE session_id = $1 AND ts_rank(tsv, websearch_to_tsquery('english', 'needle')) >
						(SELECT max(ts_rank(tsv, websearch_to_tsquery('english', 'needle'))) FROM memories WHERE session_id = $2 AND category = 'fact')),
					count(*) FILTER (WHERE tsv @@ websearch_to_tsquery('english', $4))
					FROM memories WHERE category = 'fact'`, b.HomeSessionID, a.HomeSessionID, float32SliceToPGVectorString(vector(1, 0)), query).Scan(&strongerVector, &strongerFTS, &keywordMatches)
				if err != nil || strongerVector != 24 || strongerFTS != 24 {
					t.Fatalf("distractors not stronger: vector=%d FTS=%d err=%v", strongerVector, strongerFTS, err)
				}
				if (mode == "vector" && keywordMatches != 0) || (mode != "vector" && keywordMatches != 26) {
					t.Fatalf("mode %s has unexpected keyword matches: %d", mode, keywordMatches)
				}
				for _, k := range []int{1, 2} {
					rows, err := svc.SearchByCategory(f.owner, query, k, 0.3, "fact")
					if err != nil || len(rows) != k {
						t.Fatalf("top-%d lost to cross-Bot/category distractors: %+v %v", k, rows, err)
					}
					seen := make(map[string]bool)
					for _, row := range rows {
						if !allowed[row.SessionID] || seen[row.SessionID] || row.Snippet != "needle allowed" || row.Category != "fact" || row.CreatedBy != a.OwnerID.String() || row.Path != "source:owned" || row.Scope != string(scope) || row.Score <= 0 {
							t.Fatalf("incorrect scoped result: %+v", row)
						}
						seen[row.SessionID] = true
					}
				}
				if (mode == "fts" && embedCalls != 0) || (mode != "fts" && embedCalls != 2) {
					t.Fatalf("mode %s embedding calls=%d", mode, embedCalls)
				}
				// Hybrid must also work with its semantic branch removed: prove the
				// concrete PG FTS path contributes an independently searchable pool.
				if mode == "hybrid" {
					switch m := f.memories.(type) {
					case *teamMemoryStore:
						m.embedFunc = nil
					case *personalMemoryStore:
						m.embedFunc = nil
					}
					rows, err := svc.SearchByCategory(f.owner, query, 2, 0.3, "fact")
					if err != nil || len(rows) != 2 {
						t.Fatalf("hybrid keyword branch missing: %+v %v", rows, err)
					}
				}
				if rows, err := svc.SearchByCategory(f.owner, query, 1, 0, "missing' OR 1=1 --"); err != nil || len(rows) != 0 {
					t.Fatalf("category widened scope: %+v %v", rows, err)
				}
				backend := f.memories.(store.BotMemoryPrefilterStore)
				for _, sessions := range [][]string{nil, {uuid.NewString()}} {
					if rows, err := backend.SearchBySessions(f.owner, sessions, query, 1, 0, ""); err != nil || len(rows) != 0 {
						t.Fatalf("empty/unknown sessions widened: %+v %v", rows, err)
					}
				}
				if _, err := backend.SearchBySessions(f.owner, []string{"invalid' OR 1=1 --"}, query, 1, 0, ""); !errors.Is(err, store.ErrBotInvalid) {
					t.Fatalf("malformed session accepted: %v", err)
				}
				if _, err := svc.Search(context.Background(), query, 1, 0); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("unauthenticated search accepted: %v", err)
				}
				t.Logf("live PostgreSQL %s/%s: 24 stronger vector and FTS cross-Bot distractors; scoped top-1/top-2 preserved home/run memories", scope, mode)
			})
		}
	}
}
