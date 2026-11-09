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

// ScheduledJob holds the schema definition for the ScheduledJob entity.
type ScheduledJob struct {
	ent.Schema
}

func (ScheduledJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		field.String("name").
			NotEmpty(),
		field.String("schedule").
			NotEmpty(),
		field.Enum("mode").
			Values("routine", "adaptive", "fleet_poll", "bot_trigger").
			Default("routine"),
		field.JSON("payload", map[string]any{}).
			Default(map[string]any{}),
		field.Enum("status").
			Values("active", "paused", "completed", "failed").
			Default("active"),
		field.Time("last_run_at").
			Optional().
			Nillable(),
		field.Time("next_run_at").
			Optional().
			Nillable(),
		field.String("last_status").
			Default("pending"),
		field.String("last_error").
			Default(""),
		field.Int("consecutive_failures").
			Default(0),
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
	}
}

func (ScheduledJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("next_run_at"),
	}
}

func (ScheduledJob) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("scheduled_jobs"),
	}
}
