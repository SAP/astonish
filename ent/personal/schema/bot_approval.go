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

type BotApproval struct{ ent.Schema }

func (BotApproval) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(), field.UUID("run_id", uuid.UUID{}).Immutable(),
		field.Int64("version").Positive().Immutable(), field.String("action_digest").NotEmpty().Immutable(),
		field.Enum("status").Values("pending", "approved", "denied", "expired", "revoked", "consumed").Default("pending"),
		field.JSON("data", map[string]any{}), field.Time("expires_at"),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
	}
}
func (BotApproval) Edges() []ent.Edge {
	return []ent.Edge{edge.From("bot", Bot.Type).Ref("approvals").Field("bot_id").Unique().Required().Immutable(), edge.From("run", BotRun.Type).Ref("approvals").Field("run_id").Unique().Required().Immutable()}
}
func (BotApproval) Indexes() []ent.Index {
	return []ent.Index{index.Fields("run_id", "action_digest").Unique(), index.Fields("bot_id", "status")}
}
func (BotApproval) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_approvals")}
}
