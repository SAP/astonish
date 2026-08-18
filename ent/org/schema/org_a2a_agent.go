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

// OrgA2AAgent holds the schema definition for the OrgA2AAgent entity.
type OrgA2AAgent struct {
	ent.Schema
}

func (OrgA2AAgent) Fields() []ent.Field {
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

func (OrgA2AAgent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}

func (OrgA2AAgent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("org_a2a_agents"),
	}
}
