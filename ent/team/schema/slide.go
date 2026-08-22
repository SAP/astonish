package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type Slide struct{ ent.Schema }

func (Slide) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Int("position").NonNegative(),
		field.String("title").Default(""),
		field.Text("content"),
		field.Text("notes").Default(""),
		field.Int("schema_version").Default(1),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
	}
}

func (Slide) Edges() []ent.Edge {
	return []ent.Edge{edge.From("deck", Deck.Type).Ref("slides").Unique().Required()}
}
func (Slide) Indexes() []ent.Index {
	return []ent.Index{index.Edges("deck").Fields("position").Unique()}
}
