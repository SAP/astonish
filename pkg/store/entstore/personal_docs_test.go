package entstore

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "modernc.org/sqlite"

	personalent "github.com/SAP/astonish/ent/personal"
)

func TestPersonalDocsCRUDAndOrdering(t *testing.T) {
	db, err := sql.Open("sqlite", "file:personaldocs?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := personalent.NewClient(personalent.Driver(drv))
	if err := client.Schema.Create(context.Background(), schema.WithForeignKeys(true)); err != nil {
		t.Fatal(err)
	}
	testDocsCRUD(t, &personalDocsStore{client: client})
}
