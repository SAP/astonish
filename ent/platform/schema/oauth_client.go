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

// OAuthClient stores an Astonish OAuth client. Client secrets are stored only as
// verifier hashes; public clients leave secret_hash nil.
type OAuthClient struct{ ent.Schema }

func (OAuthClient) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		// Legacy clients created before ownership existed remain unassigned after
		// automatic migration. New client creation supplies a real owner and scope.
		field.UUID("owner_user_id", uuid.UUID{}).Optional().Immutable(),
		// OrgID and TeamID are the explicit execution boundary for new clients.
		field.UUID("org_id", uuid.UUID{}).Optional(),
		field.String("team_id").Optional(),
		field.String("client_id").NotEmpty(),
		field.String("name").NotEmpty(),
		field.Enum("client_type").Values("public", "confidential").Default("public"),
		field.String("secret_hash").Optional().Nillable(),
		field.JSON("redirect_uris", []string{}),
		field.JSON("grant_types", []string{}),
		field.JSON("resources", []string{}),
		field.JSON("scopes", []string{}),
		field.Bool("active").Default(true),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
	}
}
func (OAuthClient) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("client_id").Unique(),
		index.Fields("owner_user_id"),
		index.Fields("org_id"),
	}
}
func (OAuthClient) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("oauth_clients")}
}
