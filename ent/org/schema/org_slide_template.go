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

// OrgSlideTemplate is a PPTX-imported slide template visible to every team in the org.
type OrgSlideTemplate struct {
	ent.Schema
}

func (OrgSlideTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		field.String("name").
			NotEmpty(),
		field.String("label").
			Default(""),
		field.String("description").
			Default(""),
		field.Int("schema_version").
			Default(2),
		field.String("skin").
			Default(""),
		field.JSON("tokens", map[string]string{}).
			Optional(),
		field.JSON("assets", map[string]string{}).
			Optional(),
		field.JSON("palettes", []any{}).
			Optional(),
		field.JSON("archetypes", []any{}).
			Optional(),
		field.Text("template_model").
			Optional(),
		field.String("created_by").
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

func (OrgSlideTemplate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}

func (OrgSlideTemplate) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Table("org_slide_templates"),
	}
}
