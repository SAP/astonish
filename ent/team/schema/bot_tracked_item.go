package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type BotTrackedItem struct{ ent.Schema }

func (BotTrackedItem) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("goal_id", uuid.UUID{}).Immutable(),
		field.Int64("revision").Default(1),
		field.Enum("status").Values("open", "blocked", "completed", "cancelled").Default("open"),
		field.Time("due_at").Optional().Nillable(),
		field.JSON("data", map[string]any{}),
	}
}
func (BotTrackedItem) Edges() []ent.Edge {
	return []ent.Edge{edge.From("goal", BotGoal.Type).Ref("tracked_items").Field("goal_id").Unique().Required().Immutable()}
}
func (BotTrackedItem) Indexes() []ent.Index { return []ent.Index{index.Fields("goal_id", "status")} }
func (BotTrackedItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_tracked_items")}
}
