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

// PersonalSettings holds the schema definition for the PersonalSettings entity.
//
// One row per user. In personal-mode SQLite where session.user_id is often
// NULL, uuid.Nil (all-zeros) is the sentinel for the single local user
// (DECISION-1 in .omo/plans/per-chat-app-model-pin.md).
type PersonalSettings struct {
	ent.Schema
}

func (PersonalSettings) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("user_id", uuid.UUID{}).
			Default(func() uuid.UUID { return uuid.Nil }).
			Immutable(),
		field.String("default_provider").
			Default(""),
		field.String("default_model").
			Default(""),
		// BrandTheme is the user's Studio brand pack preference (nova, aster, …).
		// Empty string means inherit platform default (then product default aster).
		field.String("brand_theme").
			Default(""),
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

func (PersonalSettings) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id").Unique(),
	}
}

func (PersonalSettings) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("personal_settings"),
	}
}
