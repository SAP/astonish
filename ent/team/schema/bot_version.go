package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// BotVersion records immutable executable configuration, never secret values.
type BotVersion struct{ ent.Schema }

func (BotVersion) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("bot_id", uuid.UUID{}).Immutable(),
		field.Int64("version").Positive().Immutable(),
		field.Int("schema_version").Default(1).Immutable(),
		field.JSON("definition", map[string]any{}).Immutable(),
		field.String("content_hash").NotEmpty().Immutable(),
		field.UUID("created_by", uuid.UUID{}).Immutable(),
		field.String("change_reason").Default("").Immutable(),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
	}
}
func (BotVersion) Edges() []ent.Edge {
	return []ent.Edge{edge.From("bot", Bot.Type).Ref("versions").Field("bot_id").Unique().Required().Immutable()}
}
func (BotVersion) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "version").Unique()}
}
func (BotVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_versions")}
}
