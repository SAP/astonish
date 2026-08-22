package entstore

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "modernc.org/sqlite"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
)

func TestTeamDocsCRUDAndOrdering(t *testing.T) {
	db, err := sql.Open("sqlite", "file:teamdocs?mode=memory&cache=shared&_fk=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := teament.NewClient(teament.Driver(drv))
	if err := client.Schema.Create(context.Background(), schema.WithForeignKeys(true)); err != nil {
		t.Fatal(err)
	}
	testDocsCRUD(t, &teamDocsStore{client: client})
}
func testDocsCRUD(t *testing.T, s store.DocsStore) {
	t.Helper()
	ctx := context.Background()
	d := &store.DeckManifest{Slug: "same", Title: "Deck", SchemaVersion: 1}
	if err := s.CreateDeck(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, v := range []*store.SlideContent{{DeckID: d.ID, Position: 1, Content: "two", SchemaVersion: 1}, {DeckID: d.ID, Position: 0, Content: "one", SchemaVersion: 1}} {
		if err := s.UpsertSlide(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	slides, err := s.ListSlides(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(slides) != 2 || slides[0].Content != "one" {
		t.Fatalf("unexpected order: %#v", slides)
	}
	if err := s.ReorderSlides(ctx, d.ID, []string{slides[1].ID, slides[0].ID}); err != nil {
		t.Fatalf("reorder slides: %v", err)
	}
	reordered, err := s.ListSlides(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reordered[0].ID != slides[1].ID || reordered[1].ID != slides[0].ID {
		t.Fatalf("unexpected reordered slides: %#v", reordered)
	}
	if err := s.ReorderSlides(ctx, d.ID, []string{slides[0].ID}); err != store.ErrDocsNotFound {
		t.Fatalf("partial reorder error = %v", err)
	}
	afterFailedReorder, err := s.ListSlides(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailedReorder[0].ID != slides[1].ID || afterFailedReorder[1].ID != slides[0].ID {
		t.Fatalf("failed reorder changed order: %#v", afterFailedReorder)
	}
	if err := s.DeleteDeck(ctx, d.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDeck(ctx, d.Slug); err != store.ErrDocsNotFound {
		t.Fatalf("got %v", err)
	}
}
