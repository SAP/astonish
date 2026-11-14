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

// BotKnowledgeExtraction is an immutable source snapshot and fenced extraction journal.
type BotKnowledgeExtraction struct{ ent.Schema }

func (BotKnowledgeExtraction) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.UUID("run_id", uuid.UUID{}).Immutable(), field.String("outcome_digest").NotEmpty().Immutable(),
		field.Int64("source_sequence").Immutable(), field.Int64("source_version").Immutable(),
		field.Int64("revision").Default(1), field.Int64("fence").Default(0),
		field.String("worker_id").Default(""), field.Time("lease_expires_at").Optional().Nillable(),
		field.Enum("state").Values("pending", "extracting", "prepared", "files_ready", "committed", "no_change", "held", "aborted").Default("pending"),
		field.JSON("data", map[string]any{}),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(botTimeDefault()),
	}
}
func (BotKnowledgeExtraction) Edges() []ent.Edge {
	return []ent.Edge{edge.To("bot", Bot.Type).Field("bot_id").Unique().Required().Immutable()}
}
func (BotKnowledgeExtraction) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("bot_id", "run_id", "outcome_digest", "source_sequence", "source_version").Unique(),
		index.Fields("bot_id", "state"), index.Fields("lease_expires_at"),
	}
}
func (BotKnowledgeExtraction) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_knowledge_extractions")}
}

// BotKnowledgeFileHead is a scoped publication head, not an editable copy of Markdown.
type BotKnowledgeFileHead struct{ ent.Schema }

func (BotKnowledgeFileHead) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.String("path").NotEmpty().Immutable(), field.String("digest").NotEmpty(),
		field.UUID("extraction_id", uuid.UUID{}), field.Int64("revision").Default(1),
	}
}
func (BotKnowledgeFileHead) Edges() []ent.Edge {
	return []ent.Edge{edge.To("bot", Bot.Type).Field("bot_id").Unique().Required().Immutable()}
}
func (BotKnowledgeFileHead) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "path").Unique()}
}
func (BotKnowledgeFileHead) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_knowledge_file_heads")}
}
