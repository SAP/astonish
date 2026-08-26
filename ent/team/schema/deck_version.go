package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// DeckVersion stores a historical snapshot of a deck at a given version number.
// Up to 5 versions are kept per deck; oldest are pruned on save.
type DeckVersion struct{ ent.Schema }

func (DeckVersion) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("deck_slug").NotEmpty(),
		field.Int("version").Default(1),
		field.String("title").NotEmpty(),
		// snapshot holds the full deck state as JSON: {theme, assets, slides[]}.
		field.Text("snapshot").Default(""),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
	}
}

func (DeckVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("deck_slug", "version").Unique(),
		index.Fields("deck_slug"),
	}
}
