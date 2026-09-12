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

// OAuthSigningKey stores public JWK metadata and an encrypted private-key blob.
type OAuthSigningKey struct{ ent.Schema }

func (OAuthSigningKey) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("key_id").NotEmpty(),
		field.String("algorithm").NotEmpty(),
		field.JSON("public_jwk", map[string]any{}),
		field.Bytes("encrypted_private_key").NotEmpty(),
		field.Enum("status").Values("active", "retired", "revoked").Default("active"),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(&entsql.Annotation{DefaultExprs: map[string]string{dialect.Postgres: "now()", dialect.SQLite: "(datetime('now'))"}}),
		field.Time("not_after").Optional().Nillable(),
	}
}
func (OAuthSigningKey) Indexes() []ent.Index {
	return []ent.Index{index.Fields("key_id").Unique(), index.Fields("status")}
}
func (OAuthSigningKey) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Table("oauth_signing_keys")}
}
