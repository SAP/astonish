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

// BotTask owns registry identity/head, never an independently editable TASK.md.
type BotTask struct{ ent.Schema }

func (BotTask) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.String("key").NotEmpty().Immutable(), field.String("home_path").NotEmpty().Immutable(),
		field.String("title").NotEmpty(), field.Int64("revision").Default(1),
		field.Enum("status").Values("draft", "ready", "archived").Default("draft"),
		field.Int64("head_revision").Default(0), field.String("head_digest").Default(""),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(botTimeDefault()),
	}
}
func (BotTask) Edges() []ent.Edge {
	return []ent.Edge{edge.To("bot", Bot.Type).Field("bot_id").Unique().Required().Immutable()}
}
func (BotTask) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "key").Unique(), index.Fields("bot_id", "home_path").Unique(), index.Fields("bot_id", "status")}
}
func (BotTask) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_tasks")}
}
