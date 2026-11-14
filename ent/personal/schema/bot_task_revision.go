package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"time"
)

// Snapshot data is immutable archival/recovery material, not live instructions.
// Mutable publication state belongs to this envelope; only committed revisions
// may become the task head. data uses the existing precision-protecting payload.
type BotTaskRevision struct{ ent.Schema }

func (BotTaskRevision) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(), field.UUID("bot_id", uuid.UUID{}).Immutable(), field.UUID("task_id", uuid.UUID{}).Immutable(),
		field.Int64("revision").Immutable(), field.String("digest").NotEmpty().Immutable(), field.Int64("size_bytes").Immutable(),
		field.Int64("parent_revision").Default(0).Immutable(), field.String("parent_digest").Default("").Immutable(),
		field.Enum("publication_state").Values("prepared", "files_ready", "committed", "held", "aborted").Default("prepared"),
		field.String("authority_constraint_ref").NotEmpty().Immutable(), field.String("workspace_receipt_ref").Default(""),
		field.JSON("data", map[string]any{}).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
	}
}
func (BotTaskRevision) Edges() []ent.Edge {
	return []ent.Edge{edge.To("bot", Bot.Type).Field("bot_id").Unique().Required().Immutable(), edge.To("task", BotTask.Type).Field("task_id").Unique().Required().Immutable()}
}
func (BotTaskRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "task_id", "revision").Unique(), index.Fields("bot_id", "publication_state")}
}
func (BotTaskRevision) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_task_revisions")}
}
