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

type BotSkillRevision struct{ ent.Schema }

func (BotSkillRevision) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(), field.UUID("bot_id", uuid.UUID{}).Immutable(), field.UUID("skill_id", uuid.UUID{}).Immutable(),
		field.Int64("revision").Immutable(), field.String("digest").NotEmpty().Immutable(), field.Int64("size_bytes").Immutable(),
		field.JSON("data", map[string]any{}).Immutable(), field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
	}
}
func (BotSkillRevision) Edges() []ent.Edge {
	return []ent.Edge{edge.To("bot", Bot.Type).Field("bot_id").Unique().Required().Immutable(), edge.To("skill", BotSkill.Type).Field("skill_id").Unique().Required().Immutable()}
}
func (BotSkillRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bot_id", "skill_id", "revision").Unique()}
}
func (BotSkillRevision) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("bot_skill_revisions")}
}
