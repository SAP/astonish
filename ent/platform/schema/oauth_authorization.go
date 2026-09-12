package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// OAuthAuthorization is one short-lived, one-time authorization code.
type OAuthAuthorization struct{ ent.Schema }

func (OAuthAuthorization) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty(),
		field.String("client_id").NotEmpty(),
		field.String("redirect_uri").NotEmpty(),
		field.String("code_challenge").NotEmpty(),
		field.String("code_challenge_method").Default("S256"),
		field.String("nonce").Optional().Nillable(),
		field.String("subject").NotEmpty(),
		field.String("actor").Optional().Nillable(),
		field.String("org_id").NotEmpty(),
		field.String("team_id").Optional().Nillable(),
		field.JSON("scopes", []string{}),
		field.JSON("resources", []string{}),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
		field.Time("expires_at"),
		field.Time("consumed_at").Optional().Nillable(),
	}
}
func (OAuthAuthorization) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("oauth_authorizations")}
}
