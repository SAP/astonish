package entstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/SAP/astonish/pkg/store"
)

func TestPreparedMemoryStoresSeparateQueriesAndFilterCategory(t *testing.T) {
	factories := []struct {
		name        string
		table       string
		fts         string
		insertSQL   string
		populateSQL string
		new         func(*sql.DB) store.PreparedMemoryStore
	}{
		{"personal", "memories", "memories_fts",
			"INSERT INTO memories (id, chunk_text, category, created_by, created_at, embedding) VALUES (?, ?, ?, ?, ?, ?)",
			"INSERT INTO memories_fts(rowid, chunk_text) SELECT rowid, chunk_text FROM memories",
			func(db *sql.DB) store.PreparedMemoryStore {
				return &personalMemoryStore{db: db, dialect: DialectSQLite, vecIndex: newVectorIndex(), table: "memories", ftsTable: "memories_fts"}
			}},
		{"team", "memories", "memories_fts",
			"INSERT INTO memories (id, chunk_text, category, created_by, created_at, embedding) VALUES (?, ?, ?, ?, ?, ?)",
			"INSERT INTO memories_fts(rowid, chunk_text) SELECT rowid, chunk_text FROM memories",
			func(db *sql.DB) store.PreparedMemoryStore {
				return &teamMemoryStore{db: db, dialect: DialectSQLite, vecIndex: newVectorIndex(), table: "memories", ftsTable: "memories_fts"}
			}},
		{"org", "org_memories", "org_memories_fts",
			"INSERT INTO org_memories (id, chunk_text, category, promoted_by, created_at, embedding) VALUES (?, ?, ?, ?, ?, ?)",
			"INSERT INTO org_memories_fts(rowid, chunk_text) SELECT rowid, chunk_text FROM org_memories",
			func(db *sql.DB) store.PreparedMemoryStore {
				return &orgMemoryStore{db: db, dialect: DialectSQLite, vecIndex: newVectorIndex(), table: "org_memories", ftsTable: "org_memories_fts"}
			}},
	}
	for _, tc := range factories {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			creator := "created_by"
			if tc.name == "org" {
				creator = "promoted_by"
			}
			stmts := []string{
				"CREATE TABLE " + tc.table + " (id TEXT PRIMARY KEY, chunk_text TEXT NOT NULL, category TEXT, source_path TEXT, " + creator + " TEXT, session_id TEXT, created_at TEXT, embedding BLOB)",
				"CREATE VIRTUAL TABLE " + tc.fts + " USING fts5(chunk_text, content='" + tc.table + "', content_rowid='rowid')",
			}
			for _, stmt := range stmts {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			id := uuid.NewString()
			if _, err := db.Exec(tc.insertSQL, id, "literal keyword", "allowed", uuid.NewString(), "2026-01-01T00:00:00Z", float32SliceToBytes([]float32{1, 0})); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.populateSQL); err != nil {
				t.Fatal(err)
			}
			results, err := tc.new(db).SearchPrepared(context.Background(), store.PreparedMemoryQuery{SemanticQuery: "different semantic text", KeywordQuery: "literal", Embedding: []float32{1, 0}}, 10, 0, "allowed")
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].ID != id {
				t.Fatalf("results = %#v", results)
			}
			results, err = tc.new(db).SearchPrepared(context.Background(), store.PreparedMemoryQuery{SemanticQuery: "different semantic text", KeywordQuery: "literal", Embedding: []float32{1, 0}}, 10, 0, "other")
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 0 {
				t.Fatalf("category leaked results: %#v", results)
			}
		})
	}
}

func TestPreparedMemoryStoresPropagateSQLiteErrors(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stores := []store.PreparedMemoryStore{
		&personalMemoryStore{db: db, dialect: DialectSQLite, vecIndex: newVectorIndex(), table: "missing", ftsTable: "missing_fts"},
		&teamMemoryStore{db: db, dialect: DialectSQLite, vecIndex: newVectorIndex(), table: "missing", ftsTable: "missing_fts"},
		&orgMemoryStore{db: db, dialect: DialectSQLite, vecIndex: newVectorIndex(), table: "missing", ftsTable: "missing_fts"},
	}
	for i, memoryStore := range stores {
		if _, err := memoryStore.SearchPrepared(context.Background(), store.PreparedMemoryQuery{KeywordQuery: "keyword"}, 10, 0, ""); err == nil {
			t.Fatalf("store %d keyword error was swallowed", i)
		}
		if _, err := memoryStore.SearchPrepared(context.Background(), store.PreparedMemoryQuery{SemanticQuery: "semantic", Embedding: []float32{1}}, 10, 0, ""); err == nil {
			t.Fatalf("store %d vector error was swallowed", i)
		}
	}
}
