package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// OAuthToken stores hashed access/refresh handles and their revocation family.
type OAuthToken struct{ ent.Schema }

func (OAuthToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty(),
		field.String("family_id").NotEmpty(),
		field.Enum("token_type").Values("access", "refresh"),
		field.String("client_id").NotEmpty(),
		field.String("subject").Optional().Nillable(),
		field.String("actor").Optional().Nillable(),
		field.String("org_id").NotEmpty(),
		field.String("team_id").Optional().Nillable(),
		field.JSON("scopes", []string{}),
		field.JSON("resources", []string{}),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
		field.Time("expires_at"),
		field.Time("revoked_at").Optional().Nillable(),
		field.String("replaced_by").Optional().Nillable(),
		field.Bool("replay_detected").Default(false),
	}
}
func (OAuthToken) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("oauth_tokens")}
}
