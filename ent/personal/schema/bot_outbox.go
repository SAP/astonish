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

type BotOutbox struct{ ent.Schema }

func (BotOutbox) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.Int64("sequence").Positive().Immutable(), field.String("kind").NotEmpty().Immutable(),
		field.String("deduplication_key").NotEmpty().Immutable(), field.JSON("payload", map[string]any{}),
		field.Time("available_at"), field.Time("delivered_at").Optional().Nillable(),
		field.String("worker_id").Default(""), field.Int64("fence").Default(0), field.Time("lease_expires_at").Optional().Nillable(),
		field.Int("attempts").Default(0), field.String("last_error").Default(""),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
	}
}
func (BotOutbox) Edges() []ent.Edge {
	return []ent.Edge{edge.From("bot", Bot.Type).Ref("outbox").Field("bot_id").Unique().Required().Immutable()}
}
func (BotOutbox) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "sequence").Unique(), index.Fields("bot_id", "deduplication_key").Unique(), index.Fields("available_at", "delivered_at")}
}
func (BotOutbox) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_outbox")}
}
