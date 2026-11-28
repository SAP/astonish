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

// SandboxSession holds the schema definition for the SandboxSession entity.
type SandboxSession struct {
	ent.Schema
}

func (SandboxSession) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			NotEmpty(),
		field.String("chat_session_id").
			NotEmpty(),
		field.JSON("bot_home_binding", map[string]any{}).Optional(),
		field.String("backend").
			Default("incus"),
		field.String("container_name").
			Optional().
			Nillable(),
		field.UUID("template_id", uuid.UUID{}),
		field.String("upper_layer_id").
			Optional().
			Nillable(),
		field.Enum("state").
			Values("creating", "running", "evicting", "evicted", "resuming", "terminated").
			Default("creating"),
		field.String("pod_name").
			Optional().
			Nillable(),
		field.String("node_name").
			Optional().
			Nillable(),
		field.JSON("exposed_ports", []any{}).
			Default([]any{}),
		field.String("base_domain").
			Optional().
			Nillable(),
		field.Bool("pinned").
			Default(false),
		field.UUID("created_by", uuid.UUID{}).
			Optional().
			Nillable(),
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
		field.Time("last_active_at").
			Default(time.Now).
			Annotations(&entsql.Annotation{
				DefaultExprs: map[string]string{
					dialect.Postgres: "now()",
					dialect.SQLite:   "(datetime('now'))",
				},
			}),
	}
}

func (SandboxSession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chat_session_id"),
		index.Fields("state", "last_active_at"),
		index.Fields("upper_layer_id"),
		index.Fields("container_name"),
	}
}

func (SandboxSession) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("sandbox_sessions"),
	}
}
