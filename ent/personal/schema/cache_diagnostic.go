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
)

// CacheDiagnostic holds one secret-safe model request fingerprint.
type CacheDiagnostic struct {
	ent.Schema
}

func (CacheDiagnostic) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("session_id").NotEmpty(),
		field.Int("round"),
		field.Bool("cache_stable_path"),
		field.String("system_hash").MaxLen(128),
		field.Bool("system_changed"),
		field.Bool("system_changed_session"),
		field.String("tool_hash").MaxLen(128),
		field.Int("tool_count"),
		field.Bool("tools_changed"),
		field.Bool("tools_changed_session"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Annotations(&entsql.Annotation{DefaultExprs: map[string]string{
				dialect.Postgres: "now()",
				dialect.SQLite:   "(datetime('now'))",
			}}),
	}
}

func (CacheDiagnostic) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("session", Session.Type).
			Ref("cache_diagnostics").
			Field("session_id").
			Required().
			Unique().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (CacheDiagnostic) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_id", "id"),
	}
}

func (CacheDiagnostic) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("cache_diagnostics")}
}
