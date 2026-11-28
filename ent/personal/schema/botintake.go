package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"time"
)

// BotIntake is owner-private unfinished content, never a runnable Bot.
type BotIntake struct{ ent.Schema }

func (BotIntake) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Immutable(),
		field.UUID("owner_id", uuid.UUID{}).Immutable(),
		field.Int64("revision"),
		field.Bytes("content").Optional(),
		field.Bool("discarded").Default(false),
		field.String("mutation_hash"),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(botTimeDefault()),
		field.Time("updated_at").Default(time.Now).Annotations(botTimeDefault()),
	}
}

func (BotIntake) Indexes() []ent.Index { return []ent.Index{index.Fields("owner_id")} }
