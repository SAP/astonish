package entstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/SAP/astonish/pkg/store"
)

func TestApplySQLiteExtrasRejectsInconsistentFTSIndex(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE memories (
		id TEXT PRIMARY KEY,
		chunk_text TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create memories table: %v", err)
	}
	s := &Store{dialect: DialectSQLite}
	if err := s.applySQLiteExtras(ctx, ScopeTeam, db); err != nil {
		t.Fatalf("initial extras: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER trg_memories_fts_insert"); err != nil {
		t.Fatalf("drop insert trigger: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO memories (id, chunk_text) VALUES (?, ?)", uuid.NewString(), "unindexed marker"); err != nil {
		t.Fatalf("insert unindexed memory: %v", err)
	}
	if err := s.applySQLiteExtras(ctx, ScopeTeam, db); err == nil {
		t.Fatal("inconsistent FTS index was accepted")
	}
}

func TestSQLiteTenantOpenRepairsMissingFTSIndexes(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	cfg := Config{
		DSN:     "file:" + filepath.Join(dataDir, "platform.db"),
		DataDir: dataDir,
	}

	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := s.ProvisionOrg(ctx, uuid.NewString(), "acme"); err != nil {
		t.Fatalf("ProvisionOrg() error = %v", err)
	}
	org, err := s.ForOrg("acme")
	if err != nil {
		t.Fatalf("ForOrg() error = %v", err)
	}
	userID := uuid.NewString()
	if err := org.ProvisionTeam(ctx, "engineering"); err != nil {
		t.Fatalf("ProvisionTeam() error = %v", err)
	}
	if err := org.ProvisionPersonalSchema(ctx, userID); err != nil {
		t.Fatalf("ProvisionPersonalSchema() error = %v", err)
	}

	entries := []struct {
		name   string
		memory store.MemoryStore
		entry  store.MemoryEntry
	}{
		{name: "org", memory: org.OrgMemories(), entry: store.MemoryEntry{Content: "org repair marker", CreatedBy: userID}},
		{name: "team", memory: org.ForTeam("engineering").Memories(), entry: store.MemoryEntry{Content: "team repair marker", CreatedBy: userID}},
		{name: "personal", memory: org.ForUser(userID).Memories(), entry: store.MemoryEntry{Content: "personal repair marker", CreatedBy: userID}},
	}
	for _, entry := range entries {
		if err := entry.memory.Add(ctx, entry.entry); err != nil {
			t.Fatalf("add %s memory: %v", entry.name, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	dbs := []struct {
		path     string
		ftsTable string
		triggers []string
	}{
		{
			path:     filepath.Join(dataDir, "orgs", "acme", "org.db"),
			ftsTable: "org_memories_fts",
			triggers: []string{"trg_org_memories_fts_insert", "trg_org_memories_fts_delete", "trg_org_memories_fts_update"},
		},
		{
			path:     filepath.Join(dataDir, "orgs", "acme", "teams", "engineering.db"),
			ftsTable: "memories_fts",
			triggers: []string{"trg_memories_fts_insert", "trg_memories_fts_delete", "trg_memories_fts_update"},
		},
		{
			path:     filepath.Join(dataDir, "orgs", "acme", "personal", userID+".db"),
			ftsTable: "memories_fts",
			triggers: []string{"trg_memories_fts_insert", "trg_memories_fts_delete", "trg_memories_fts_update"},
		},
	}
	for _, item := range dbs {
		db, err := sql.Open("sqlite", item.path)
		if err != nil {
			t.Fatalf("open %s: %v", item.path, err)
		}
		for _, trigger := range item.triggers {
			if _, err := db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+trigger); err != nil {
				db.Close()
				t.Fatalf("drop trigger %s: %v", trigger, err)
			}
		}
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+item.ftsTable); err != nil {
			db.Close()
			t.Fatalf("drop FTS table %s: %v", item.ftsTable, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close %s: %v", item.path, err)
		}
	}

	s, err = New(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s.Close()
	org, err = s.ForOrg("acme")
	if err != nil {
		t.Fatalf("reopen org: %v", err)
	}

	searches := []struct {
		name   string
		memory store.MemoryStore
		query  string
	}{
		{name: "org", memory: org.OrgMemories(), query: `What about "org" (repair)?`},
		{name: "team", memory: org.ForTeam("engineering").Memories(), query: `What about "team" (repair)?`},
		{name: "personal", memory: org.ForUser(userID).Memories(), query: `What about "personal" (repair)?`},
	}
	for _, search := range searches {
		prepared, ok := search.memory.(store.PreparedMemoryStore)
		if !ok {
			t.Fatalf("%s memory store does not support prepared search", search.name)
		}
		results, err := prepared.SearchPrepared(ctx, store.PreparedMemoryQuery{KeywordQuery: search.query}, 10, 0, "")
		if err != nil {
			t.Fatalf("search repaired %s FTS index: %v", search.name, err)
		}
		if len(results) != 1 || results[0].Snippet == "" {
			t.Fatalf("search repaired %s FTS index returned %#v", search.name, results)
		}
	}
}
