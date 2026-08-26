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

type Deck struct{ ent.Schema }

func (Deck) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("slug").NotEmpty(),
		field.String("title").NotEmpty(),
		field.String("description").Default(""),
		field.Int("schema_version").Default(1),
		field.JSON("theme", map[string]string{}).Optional(),
		field.JSON("assets", map[string]string{}).Optional(),
		// template_model holds the lossless imported-template IR as a raw JSON
		// blob (themes.TemplateModel). Stored as text to avoid coupling the Ent
		// schema to the slides IR types; empty for all decks except high-fidelity
		// imported templates (schema_version=3). Additive optional column.
		field.Text("template_model").Optional(),
		field.Bool("thumbnail_ready").Default(false),
		// session_id links a deck to the chat session that created it. Empty
		// means the deck is saved/permanent; non-empty means session-scoped
		// (invisible in Slides view, cascade-deleted with the session).
		field.String("session_id").Default(""),
		// version tracks the current version number (bumps on override-save).
		field.Int("version").Default(1),
		// source_slug links an enhance-copy to the saved deck it was cloned
		// from, so the Save dialog can offer "Override Original".
		field.String("source_slug").Default(""),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
	}
}

func (Deck) Edges() []ent.Edge    { return []ent.Edge{edge.To("slides", Slide.Type)} }
func (Deck) Indexes() []ent.Index {
	return []ent.Index{index.Fields("slug").Unique(), index.Fields("session_id")}
}
