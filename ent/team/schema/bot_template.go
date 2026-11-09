package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
	"time"
)

type BotTemplate struct{ ent.Schema }

func (BotTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("name").NotEmpty().Unique(),
		field.Int64("revision").Default(1),
		field.JSON("definition", map[string]any{}),
		field.UUID("published_by", uuid.UUID{}),
		field.Time("published_at").Default(time.Now).Annotations(botTimeDefault()),
	}
}
func (BotTemplate) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_templates")}
}
