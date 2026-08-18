package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// A2aAgent holds the schema definition for the A2aAgent entity.
type A2aAgent struct {
	ent.Schema
}

func (A2aAgent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		field.String("name").
			NotEmpty(),
		field.String("url").
			NotEmpty(),
		field.String("credential_name").
			Optional().
			Nillable(),
		field.String("auth_type").
			Default("bearer"),
		field.Bool("enabled").
			Default(true),
		field.JSON("headers", map[string]string{}).
			Optional().
			Default(map[string]string{}),
		field.String("timeout").
			Optional().
			Nillable(),
		field.JSON("cached_card", []byte(nil)).
			Optional(),
		field.JSON("cached_skills", []any{}).
			Optional(),
		field.UUID("created_by", uuid.UUID{}),
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

func (A2aAgent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}

func (A2aAgent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("a2a_agents"),
	}
}
