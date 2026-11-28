package entstore

import (
	"context"
	"database/sql"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/backup"
	"path/filepath"
	"testing"
)

func TestBotKnowledgeExtractionMigrateV4(t *testing.T) {
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			c := context.Background()
			db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "knowledge.db")+"?_pragma=foreign_keys(1)&_fk=1")
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			driver := entsql.OpenDB(dialect.SQLite, db)
			var create func() error
			if scope == "personal" {
				client := personalent.NewClient(personalent.Driver(driver))
				create = func() error { return client.Schema.Create(c) }
			} else {
				client := teament.NewClient(teament.Driver(driver))
				create = func() error { return client.Schema.Create(c) }
			}
			if e = create(); e != nil {
				t.Fatal(e)
			}
			if _, e = db.ExecContext(c, "DROP TABLE bot_knowledge_file_heads; DROP TABLE bot_knowledge_extractions; CREATE TABLE bot_schema_version(id TEXT PRIMARY KEY,version INTEGER NOT NULL); INSERT INTO bot_schema_version VALUES('bots',3)"); e != nil {
				t.Fatal(e)
			}
			s := backup.Scope{Kind: scope, OrgSlug: "org", TeamSlug: "team", UserID: "owner"}
			archive, e := schemaVersionForScope(c, "sqlite", db, s)
			if e != nil {
				t.Fatal(e)
			}
			manifest := backup.Manifest{Entries: []backup.Entry{{Scope: s, Entity: "bot_tasks"}}}
			for i := 0; i < 2; i++ {
				if e = migrateBotSchema(c, db, DialectSQLite, create); e != nil {
					t.Fatal(e)
				}
			}
			ok, e := preKnowledgeBackupCompatible(c, db, DialectSQLite, manifest, s, archive)
			if e != nil || !ok {
				t.Fatalf("exact marker3 archive %v %v", ok, e)
			}
			manifest.Entries = append(manifest.Entries, backup.Entry{Scope: s, Entity: "bot_knowledge_extractions"})
			ok, e = preKnowledgeBackupCompatible(c, db, DialectSQLite, manifest, s, archive)
			if e != nil || ok {
				t.Fatalf("excluded table accepted %v %v", ok, e)
			}
			var marker int
			if e = db.QueryRowContext(c, "SELECT version FROM bot_schema_version").Scan(&marker); e != nil || marker != botMigrationVersion {
				t.Fatalf("marker %d %v", marker, e)
			}
		})
	}
}
