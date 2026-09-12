package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// OAuthConsent records the user's explicit grant for a client and exact request.
type OAuthConsent struct{ ent.Schema }

func (OAuthConsent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("user_id", uuid.UUID{}),
		field.UUID("org_id", uuid.UUID{}),
		field.String("client_id").NotEmpty(),
		field.JSON("scopes", []string{}),
		field.JSON("resources", []string{}),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
		field.Time("expires_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}
func (OAuthConsent) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "org_id", "client_id").Unique()}
}
func (OAuthConsent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("oauth_consents")}
}
