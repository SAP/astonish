package entstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Exercise generic stores directly: no principal, Bot context, or required
// attribution may be relied on to keep Bot content out of generic SQL memory.
func TestBotGenericMemoryExclusion(t *testing.T) {
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			// SQLite pragmas are connection-local; pin the in-memory fixture to
			// this connection and enable foreign keys before Ent schema creation.
			if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
				t.Fatal(err)
			}
			drv := entsql.OpenDB(dialect.SQLite, db)
			calls := 0
			embed := func(context.Context, string) ([]float32, error) { calls++; return []float32{1, 0}, nil }
			var memories store.MemoryStore
			var createSession func(string)
			if scope == "personal" {
				client := personalent.NewClient(personalent.Driver(drv))
				if err := client.Schema.Create(ctx); err != nil {
					t.Fatal(err)
				}
				memories = &personalMemoryStore{client: client, db: db, dialect: DialectSQLite, embedFunc: embed, vecIndex: newVectorIndex()}
				createSession = func(id string) {
					if _, err := client.Session.Create().SetID(id).SetUserID(uuid.New()).Save(ctx); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				client := teament.NewClient(teament.Driver(drv))
				if err := client.Schema.Create(ctx); err != nil {
					t.Fatal(err)
				}
				memories = &teamMemoryStore{client: client, db: db, dialect: DialectSQLite, embedFunc: embed, vecIndex: newVectorIndex()}
				createSession = func(id string) {
					if _, err := client.Session.Create().SetID(id).SetUserID(uuid.New()).Save(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Mirror SQLite's indexing side effects, so a rejected write cannot leave
			// an FTS copy even when the generic store is used without the router.
			for _, statement := range []string{
				"CREATE VIRTUAL TABLE memories_fts USING fts5(chunk_text)",
				"CREATE TRIGGER test_memory_insert AFTER INSERT ON memories BEGIN INSERT INTO memories_fts(rowid, chunk_text) VALUES (new.rowid, new.chunk_text); END",
				"CREATE TRIGGER test_memory_update AFTER UPDATE ON memories BEGIN UPDATE memories_fts SET chunk_text = new.chunk_text WHERE rowid = new.rowid; END",
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			botID := uuid.New()
			botSession, chatSession, fleetSession := uuid.NewString(), uuid.NewString(), uuid.NewString()
			for _, sid := range []string{botSession, chatSession, fleetSession} {
				createSession(sid)
			}
			bind := func(sid string) {
				if _, err := db.Exec("UPDATE sessions SET bot_id = ? WHERE id = ?", botID.String(), sid); err != nil {
					t.Fatal(err)
				}
			}
			bind(botSession)
			const sentinel = "BOT_CONTENT_MUST_NOT_ENTER_SQL"
			for _, sid := range []string{botSession, strings.ToUpper(botSession)} {
				for _, attribution := range []*store.MemoryAttribution{nil, {BotID: botID}} {
					err := memories.Add(ctx, store.MemoryEntry{Content: sentinel, SessionID: sid, Attribution: attribution, Metadata: map[string]any{"body": sentinel, "source_records": []any{map[string]any{"body": sentinel}}}})
					if !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("Bot Add = %v", err)
					}
				}
			}
			if calls != 0 {
				t.Fatalf("rejected Add invoked embedder %d times", calls)
			}
			// Ordinary Chat, fleet and sessionless memories still write and update.
			for _, sid := range []string{chatSession, fleetSession, ""} {
				if err := memories.Add(ctx, store.MemoryEntry{Content: "ordinary", SessionID: sid}); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := memories.List(ctx, "", 100, 0)
			if err != nil || len(rows) != 3 {
				t.Fatalf("ordinary rows = %v, %v", rows, err)
			}
			for _, row := range rows {
				if err := memories.Update(ctx, row.ID, "ordinary updated", "fact"); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 6 {
				t.Fatalf("ordinary Add/Update embedder calls = %d, want 6", calls)
			}
			assertOrdinaryRowsUnchanged := func() {
				t.Helper()
				var total, ordinary int
				if err := db.QueryRow("SELECT count(*), count(CASE WHEN chunk_text = 'ordinary updated' AND category = 'fact' AND length(embedding) > 0 THEN 1 END) FROM memories").Scan(&total, &ordinary); err != nil || total != 3 || ordinary != 3 {
					t.Fatalf("ordinary SQL rows: total=%d unchanged=%d, err=%v", total, ordinary, err)
				}
				if err := db.QueryRow("SELECT count(*), count(CASE WHEN chunk_text = 'ordinary updated' THEN 1 END) FROM memories_fts").Scan(&total, &ordinary); err != nil || total != 3 || ordinary != 3 {
					t.Fatalf("ordinary FTS rows: total=%d unchanged=%d, err=%v", total, ordinary, err)
				}
			}
			assertOrdinaryRowsUnchanged()
			before := calls
			// Test both ways a previously ordinary row can become Bot-bound: its
			// session is rebound, or the memory's stored session_id is changed.
			bind(chatSession)
			for _, row := range rows {
				if row.SessionID == "" {
					continue
				}
				if row.SessionID == fleetSession {
					if _, err := db.Exec("UPDATE memories SET session_id = ? WHERE id = ?", botSession, row.ID); err != nil {
						t.Fatal(err)
					}
				}
				if err := memories.Update(ctx, row.ID, sentinel, sentinel); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("Bot Update = %v", err)
				}
			}
			if calls != before {
				t.Fatal("rejected Update invoked embedder")
			}
			assertOrdinaryRowsUnchanged()
			var count int
			if err := db.QueryRow("SELECT count(*) FROM memories WHERE chunk_text LIKE ? OR tsv LIKE ? OR metadata LIKE ? OR category LIKE ?", "%"+sentinel+"%", "%"+sentinel+"%", "%"+sentinel+"%", "%"+sentinel+"%").Scan(&count); err != nil || count != 0 {
				t.Fatalf("SQL sentinel count = %d, %v", count, err)
			}
			if err := db.QueryRow("SELECT count(*) FROM memories_fts WHERE chunk_text LIKE ?", "%"+sentinel+"%").Scan(&count); err != nil || count != 0 {
				t.Fatalf("FTS sentinel count = %d, %v", count, err)
			}
		})
	}
}
