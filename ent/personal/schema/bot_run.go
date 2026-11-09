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

type BotRun struct{ ent.Schema }

func (BotRun) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.Int64("version").Positive(), field.String("session_id").NotEmpty().Unique().Immutable(),
		field.String("idempotency_key").NotEmpty().Immutable(), field.String("request_hash").NotEmpty().Immutable(),
		field.Enum("status").Values("queued", "running", "waiting_approval", "interrupted", "succeeded", "partial", "failed", "skipped", "cancelled").Default("queued"),
		field.Int64("fence").Default(0), field.String("worker_id").Default(""),
		field.Time("lease_expires_at").Optional().Nillable(), field.JSON("data", map[string]any{}),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(botTimeDefault()),
	}
}
func (BotRun) Edges() []ent.Edge {
	return []ent.Edge{edge.From("bot", Bot.Type).Ref("runs").Field("bot_id").Unique().Required().Immutable(), edge.To("approvals", BotApproval.Type)}
}
func (BotRun) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "idempotency_key").Unique(), index.Fields("bot_id", "status"), index.Fields("lease_expires_at")}
}
func (BotRun) Annotations() []schema.Annotation { return []schema.Annotation{entsql.Table("bot_runs")} }
