package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Session holds the schema definition for the Session entity.
type Session struct {
	ent.Schema
}

func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			NotEmpty(),
		field.UUID("user_id", uuid.UUID{}).
			Optional().
			Nillable(),
		field.String("title").
			Default(""),
		field.Int("message_count").
			Default(0),
		field.String("parent_id").
			Optional().
			Nillable(),
		field.String("fleet_key").
			Default(""),
		field.String("fleet_name").
			Default(""),
		field.Int("issue_number").
			Default(0),
		field.String("repo").
			Default(""),
		field.String("workspace_dir").
			Default(""),
		field.String("provider_name").
			Optional().
			Nillable(),
		field.String("model_name").
			Optional().
			Nillable(),
		field.JSON("metadata", map[string]any{}).
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Annotations(&entsql.Annotation{
				DefaultExprs: map[string]string{
					dialect.Postgres: "now()",
					dialect.SQLite:   "(datetime('now'))",
				},
			}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			Annotations(&entsql.Annotation{
				DefaultExprs: map[string]string{
					dialect.Postgres: "now()",
					dialect.SQLite:   "(datetime('now'))",
				},
			}),
	}
}

func (Session) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("events", SessionEvent.Type),
		edge.To("cache_diagnostics", CacheDiagnostic.Type),
	}
}

func (Session) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("updated_at"),
	}
}

func (Session) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("sessions"),
	}
}
