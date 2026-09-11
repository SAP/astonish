package store

import (
	"context"
	"time"
)

// OAuthServerStore is the deliberately narrow persistence contract for the
// authorization server. It avoids widening PlatformStore while keeping every
// OAuth credential lifecycle operation explicit and auditable.
type OAuthServerStore interface {
	CreateOAuthClient(context.Context, OAuthClient) error
	GetOAuthClient(context.Context, string) (*OAuthClient, error)
	ListOAuthClients(context.Context) ([]OAuthClient, error)
	ListOAuthClientsForOwner(context.Context, string) ([]OAuthClient, error)
	UpdateOAuthClient(context.Context, OAuthClient) error
	DeleteOAuthClient(context.Context, string) error
	SaveOAuthAuthorization(context.Context, OAuthAuthorization) error
	ConsumeOAuthAuthorization(context.Context, string, time.Time) (*OAuthAuthorization, error)
	SaveOAuthToken(context.Context, OAuthToken) error
	GetOAuthRefreshToken(context.Context, string) (*OAuthToken, error)
	ConsumeOAuthRefreshToken(context.Context, string, time.Time) (*OAuthToken, error)
	RevokeOAuthTokenFamily(context.Context, string, time.Time) error
	SaveOAuthConsent(context.Context, OAuthConsent) error
	RevokeOAuthConsent(context.Context, string, string, string, time.Time) error
	SaveOAuthSigningKey(context.Context, OAuthSigningKey) error
	ListOAuthSigningKeys(context.Context) ([]OAuthSigningKey, error)
}

type OAuthClient struct {
	ID, OwnerUserID, OrgID, TeamID, ClientID, Name, ClientType, SecretHash string
	RedirectURIs, GrantTypes, Resources, Scopes       []string
	Active                                            bool
	CreatedAt, UpdatedAt                              time.Time
}
type OAuthAuthorization struct {
	CodeHash, ClientID, RedirectURI, CodeChallenge, CodeChallengeMethod, Nonce string
	Subject, Actor, OrgID, TeamID                                              string
	Scopes, Resources                                                          []string
	CreatedAt, ExpiresAt, ConsumedAt                                           time.Time
}
type OAuthToken struct {
	HandleHash, FamilyID, TokenType, ClientID, Subject, Actor, OrgID, TeamID string
	Scopes, Resources                                                        []string
	CreatedAt, ExpiresAt, RevokedAt                                          time.Time
	ReplacedBy                                                               string
	ReplayDetected                                                           bool
}
type OAuthConsent struct {
	UserID, OrgID, ClientID         string
	Scopes, Resources               []string
	CreatedAt, ExpiresAt, RevokedAt time.Time
}
type OAuthSigningKey struct {
	KeyID, Algorithm, Status string
	PublicJWK                map[string]any
	EncryptedPrivateKey      []byte
	CreatedAt, NotAfter      time.Time
}
