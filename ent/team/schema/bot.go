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

// Bot is a scoped aggregate root. Definition snapshots are immutable BotVersions.
type Bot struct{ ent.Schema }

func (Bot) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("owner_id", uuid.UUID{}).Immutable(),
		field.String("name").NotEmpty(),
		field.Enum("status").Values("draft", "active", "paused", "stopped", "deleted").Default("draft"),
		field.Int64("revision").Default(1),
		field.Int64("version").Default(1),
		field.Int("schema_version").Default(1),
		field.String("home_session_id").NotEmpty().Unique().Immutable(),
		field.JSON("home_binding", map[string]any{}).Optional(),
		field.String("create_key").NotEmpty().Unique().Immutable(),
		field.Int64("last_seq").Default(0),
		field.Int64("fence").Default(0),
		field.UUID("active_run_id", uuid.UUID{}).Optional().Nillable(),
		field.Time("lease_expires_at").Optional().Nillable(),
		field.JSON("data", map[string]any{}).Default(map[string]any{}),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("purge_after").Optional().Nillable(),
		field.Bool("cleanup_complete").Default(false),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(botTimeDefault()),
	}
}
func (Bot) Edges() []ent.Edge {
	return []ent.Edge{edge.To("versions", BotVersion.Type), edge.To("goals", BotGoal.Type), edge.To("runs", BotRun.Type), edge.To("approvals", BotApproval.Type), edge.To("outbox", BotOutbox.Type)}
}
func (Bot) Indexes() []ent.Index {
	return []ent.Index{index.Fields("status"), index.Fields("owner_id"), index.Fields("purge_after")}
}
func (Bot) Annotations() []schema.Annotation { return []schema.Annotation{entsql.Table("bots")} }
func botTimeDefault() *entsql.Annotation {
	return &entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}
}
