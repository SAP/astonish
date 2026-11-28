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

// BotSkill is operating knowledge private to one Bot, not the shared skill catalogue.
type BotSkill struct{ ent.Schema }

func (BotSkill) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.String("key").NotEmpty().Immutable(), field.Int64("revision").Default(1),
		field.Enum("status").Values("draft", "active", "archived").Default("draft"),
		field.JSON("data", map[string]any{}),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(botTimeDefault()),
	}
}
func (BotSkill) Edges() []ent.Edge {
	return []ent.Edge{edge.To("bot", Bot.Type).Field("bot_id").Unique().Required().Immutable()}
}
func (BotSkill) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "key").Unique(), index.Fields("bot_id", "status")}
}
func (BotSkill) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_skills")}
}
