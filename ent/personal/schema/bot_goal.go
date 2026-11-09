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

type BotGoal struct{ ent.Schema }

func (BotGoal) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.Int64("revision").Default(1), field.String("title").NotEmpty(),
		field.Enum("status").Values("active", "completed").Default("active"),
		field.Time("due_at").Optional().Nillable(), field.JSON("data", map[string]any{}),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(botTimeDefault()),
	}
}
func (BotGoal) Edges() []ent.Edge {
	return []ent.Edge{edge.From("bot", Bot.Type).Ref("goals").Field("bot_id").Unique().Required().Immutable(), edge.To("tracked_items", BotTrackedItem.Type)}
}
func (BotGoal) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "status"), index.Fields("due_at")}
}
func (BotGoal) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_goals")}
}
