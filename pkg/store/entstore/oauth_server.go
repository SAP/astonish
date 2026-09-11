package entstore

import (
	"context"
	"fmt"
	"time"

	platforment "github.com/SAP/astonish/ent/platform"
	"github.com/SAP/astonish/ent/platform/oauthauthorization"
	"github.com/SAP/astonish/ent/platform/oauthclient"
	"github.com/SAP/astonish/ent/platform/oauthconsent"
	"github.com/SAP/astonish/ent/platform/oauthsigningkey"
	"github.com/SAP/astonish/ent/platform/oauthtoken"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// OAuthServer returns the platform-scoped OAuth persistence boundary.
func (s *Store) OAuthServer() store.OAuthServerStore {
	return &oauthServerStore{client: s.platformClient}
}

type oauthServerStore struct{ client *platforment.Client }

func (s *oauthServerStore) CreateOAuthClient(ctx context.Context, client store.OAuthClient) error {
	create := s.client.OAuthClient.Create().SetClientID(client.ClientID).SetName(client.Name).
		SetClientType(oauthclient.ClientType(client.ClientType)).SetRedirectUris(client.RedirectURIs).
		SetGrantTypes(client.GrantTypes).SetResources(client.Resources).SetScopes(client.Scopes).SetActive(client.Active)
	if client.ID != "" {
		id, err := uuid.Parse(client.ID)
		if err != nil {
			return fmt.Errorf("parse OAuth client ID: %w", err)
		}
		create.SetID(id)
	}
	ownerID, err := uuid.Parse(client.OwnerUserID)
	if err != nil {
		return fmt.Errorf("parse OAuth client owner user ID: %w", err)
	}
	orgID, err := uuid.Parse(client.OrgID)
	if err != nil {
		return fmt.Errorf("parse OAuth client org ID: %w", err)
	}
	create.SetOwnerUserID(ownerID).SetOrgID(orgID).SetTeamID(client.TeamID)
	if client.SecretHash != "" {
		create.SetSecretHash(client.SecretHash)
	}
	if !client.CreatedAt.IsZero() {
		create.SetCreatedAt(client.CreatedAt)
	}
	if !client.UpdatedAt.IsZero() {
		create.SetUpdatedAt(client.UpdatedAt)
	}
	_, err = create.Save(ctx)
	return err
}

func (s *oauthServerStore) GetOAuthClient(ctx context.Context, clientID string) (*store.OAuthClient, error) {
	row, err := s.client.OAuthClient.Query().Where(oauthclient.ClientIDEQ(clientID)).Only(ctx)
	if platforment.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return oauthClientFromEnt(row), nil
}

func (s *oauthServerStore) ListOAuthClients(ctx context.Context) ([]store.OAuthClient, error) {
	rows, err := s.client.OAuthClient.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	clients := make([]store.OAuthClient, 0, len(rows))
	for _, row := range rows {
		clients = append(clients, *oauthClientFromEnt(row))
	}
	return clients, nil
}

func (s *oauthServerStore) ListOAuthClientsForOwner(ctx context.Context, ownerUserID string) ([]store.OAuthClient, error) {
	ownerID, err := uuid.Parse(ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("parse OAuth client owner user ID: %w", err)
	}
	rows, err := s.client.OAuthClient.Query().Where(oauthclient.OwnerUserIDEQ(ownerID)).All(ctx)
	if err != nil {
		return nil, err
	}
	clients := make([]store.OAuthClient, 0, len(rows))
	for _, row := range rows {
		clients = append(clients, *oauthClientFromEnt(row))
	}
	return clients, nil
}

func (s *oauthServerStore) UpdateOAuthClient(ctx context.Context, client store.OAuthClient) error {
	update := s.client.OAuthClient.Update().Where(oauthclient.ClientIDEQ(client.ClientID)).
		SetName(client.Name).SetClientType(oauthclient.ClientType(client.ClientType)).
		SetRedirectUris(client.RedirectURIs).SetGrantTypes(client.GrantTypes).
		SetResources(client.Resources).SetScopes(client.Scopes).SetActive(client.Active)
	if client.SecretHash != "" {
		update.SetSecretHash(client.SecretHash)
	}
	_, err := update.Save(ctx)
	return err
}

func (s *oauthServerStore) DeleteOAuthClient(ctx context.Context, clientID string) error {
	if _, err := s.client.OAuthToken.Update().Where(oauthtoken.ClientIDEQ(clientID), oauthtoken.RevokedAtIsNil()).SetRevokedAt(time.Now().UTC()).Save(ctx); err != nil {
		return err
	}
	_, err := s.client.OAuthClient.Delete().Where(oauthclient.ClientIDEQ(clientID)).Exec(ctx)
	return err
}

func (s *oauthServerStore) SaveOAuthAuthorization(ctx context.Context, authorization store.OAuthAuthorization) error {
	create := s.client.OAuthAuthorization.Create().SetID(authorization.CodeHash).SetClientID(authorization.ClientID).
		SetRedirectURI(authorization.RedirectURI).SetCodeChallenge(authorization.CodeChallenge).
		SetCodeChallengeMethod(authorization.CodeChallengeMethod).SetSubject(authorization.Subject).
		SetOrgID(authorization.OrgID).SetScopes(authorization.Scopes).SetResources(authorization.Resources).SetExpiresAt(authorization.ExpiresAt)
	if authorization.Nonce != "" {
		create.SetNonce(authorization.Nonce)
	}
	if authorization.Actor != "" {
		create.SetActor(authorization.Actor)
	}
	if authorization.TeamID != "" {
		create.SetTeamID(authorization.TeamID)
	}
	if !authorization.CreatedAt.IsZero() {
		create.SetCreatedAt(authorization.CreatedAt)
	}
	_, err := create.Save(ctx)
	return err
}

func (s *oauthServerStore) ConsumeOAuthAuthorization(ctx context.Context, codeHash string, now time.Time) (*store.OAuthAuthorization, error) {
	row, err := s.client.OAuthAuthorization.Query().Where(oauthauthorization.IDEQ(codeHash), oauthauthorization.ExpiresAtGT(now)).Only(ctx)
	if platforment.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	updated, err := s.client.OAuthAuthorization.Update().
		Where(oauthauthorization.IDEQ(codeHash), oauthauthorization.ConsumedAtIsNil(), oauthauthorization.ExpiresAtGT(now)).
		SetConsumedAt(now).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if updated == 0 {
		return nil, nil
	}
	out := oauthAuthorizationFromEnt(row)
	out.ConsumedAt = now
	return out, nil
}

func (s *oauthServerStore) SaveOAuthToken(ctx context.Context, token store.OAuthToken) error {
	create := s.client.OAuthToken.Create().SetID(token.HandleHash).SetFamilyID(token.FamilyID).SetTokenType(oauthtoken.TokenType(token.TokenType)).SetClientID(token.ClientID).SetOrgID(token.OrgID).SetScopes(token.Scopes).SetResources(token.Resources).SetExpiresAt(token.ExpiresAt).SetReplayDetected(token.ReplayDetected)
	if token.Subject != "" {
		create.SetSubject(token.Subject)
	}
	if token.Actor != "" {
		create.SetActor(token.Actor)
	}
	if token.TeamID != "" {
		create.SetTeamID(token.TeamID)
	}
	if token.ReplacedBy != "" {
		create.SetReplacedBy(token.ReplacedBy)
	}
	if !token.RevokedAt.IsZero() {
		create.SetRevokedAt(token.RevokedAt)
	}
	if !token.CreatedAt.IsZero() {
		create.SetCreatedAt(token.CreatedAt)
	}
	_, err := create.Save(ctx)
	return err
}

func (s *oauthServerStore) GetOAuthRefreshToken(ctx context.Context, handleHash string) (*store.OAuthToken, error) {
	row, err := s.client.OAuthToken.Query().Where(oauthtoken.IDEQ(handleHash), oauthtoken.TokenTypeEQ(oauthtoken.TokenTypeRefresh)).Only(ctx)
	if platforment.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return oauthTokenFromEnt(row), nil
}

func (s *oauthServerStore) ConsumeOAuthRefreshToken(ctx context.Context, handleHash string, now time.Time) (*store.OAuthToken, error) {
	row, err := s.client.OAuthToken.Query().Where(oauthtoken.IDEQ(handleHash), oauthtoken.TokenTypeEQ(oauthtoken.TokenTypeRefresh)).Only(ctx)
	if platforment.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	updated, err := s.client.OAuthToken.Update().
		Where(oauthtoken.IDEQ(handleHash), oauthtoken.TokenTypeEQ(oauthtoken.TokenTypeRefresh), oauthtoken.RevokedAtIsNil(), oauthtoken.ExpiresAtGT(now)).
		SetRevokedAt(now).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if updated == 1 {
		out := oauthTokenFromEnt(row)
		out.RevokedAt = now
		return out, nil
	}

	// A found but unclaimable handle is replay evidence. Revoke the entire
	// family derived from persisted state rather than trusting caller input.
	if _, err := s.client.OAuthToken.Update().Where(oauthtoken.FamilyIDEQ(row.FamilyID)).SetRevokedAt(now).SetReplayDetected(true).Save(ctx); err != nil {
		return nil, err
	}
	out := oauthTokenFromEnt(row)
	out.ReplayDetected = true
	return out, nil
}

func (s *oauthServerStore) RevokeOAuthTokenFamily(ctx context.Context, familyID string, now time.Time) error {
	_, err := s.client.OAuthToken.Update().Where(oauthtoken.FamilyIDEQ(familyID), oauthtoken.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx)
	return err
}

func (s *oauthServerStore) SaveOAuthConsent(ctx context.Context, consent store.OAuthConsent) error {
	userID, err := uuid.Parse(consent.UserID)
	if err != nil {
		return fmt.Errorf("parse consent user ID: %w", err)
	}
	orgID, err := uuid.Parse(consent.OrgID)
	if err != nil {
		return fmt.Errorf("parse consent org ID: %w", err)
	}
	create := s.client.OAuthConsent.Create().SetUserID(userID).SetOrgID(orgID).SetClientID(consent.ClientID).SetScopes(consent.Scopes).SetResources(consent.Resources)
	if !consent.CreatedAt.IsZero() {
		create.SetCreatedAt(consent.CreatedAt)
	}
	if !consent.ExpiresAt.IsZero() {
		create.SetExpiresAt(consent.ExpiresAt)
	}
	if !consent.RevokedAt.IsZero() {
		create.SetRevokedAt(consent.RevokedAt)
	}
	_, err = create.Save(ctx)
	return err
}

func (s *oauthServerStore) RevokeOAuthConsent(ctx context.Context, userID, orgID, clientID string, now time.Time) error {
	user, err := uuid.Parse(userID)
	if err != nil {
		return err
	}
	org, err := uuid.Parse(orgID)
	if err != nil {
		return err
	}
	_, err = s.client.OAuthConsent.Update().Where(oauthconsent.UserIDEQ(user), oauthconsent.OrgIDEQ(org), oauthconsent.ClientIDEQ(clientID), oauthconsent.RevokedAtIsNil()).SetRevokedAt(now).Save(ctx)
	return err
}

func (s *oauthServerStore) SaveOAuthSigningKey(ctx context.Context, key store.OAuthSigningKey) error {
	create := s.client.OAuthSigningKey.Create().SetKeyID(key.KeyID).SetAlgorithm(key.Algorithm).SetPublicJwk(key.PublicJWK).SetEncryptedPrivateKey(key.EncryptedPrivateKey).SetStatus(oauthsigningkey.Status(key.Status))
	if !key.CreatedAt.IsZero() {
		create.SetCreatedAt(key.CreatedAt)
	}
	if !key.NotAfter.IsZero() {
		create.SetNotAfter(key.NotAfter)
	}
	_, err := create.Save(ctx)
	return err
}
func (s *oauthServerStore) ListOAuthSigningKeys(ctx context.Context) ([]store.OAuthSigningKey, error) {
	rows, err := s.client.OAuthSigningKey.Query().Where(oauthsigningkey.StatusNEQ(oauthsigningkey.StatusRevoked)).All(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]store.OAuthSigningKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, oauthSigningKeyFromEnt(row))
	}
	return keys, nil
}

func oauthClientFromEnt(row *platforment.OAuthClient) *store.OAuthClient {
	value := &store.OAuthClient{ID: row.ID.String(), OwnerUserID: row.OwnerUserID.String(), OrgID: row.OrgID.String(), TeamID: row.TeamID, ClientID: row.ClientID, Name: row.Name, ClientType: string(row.ClientType), RedirectURIs: row.RedirectUris, GrantTypes: row.GrantTypes, Resources: row.Resources, Scopes: row.Scopes, Active: row.Active, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.SecretHash != nil {
		value.SecretHash = *row.SecretHash
	}
	return value
}
func oauthAuthorizationFromEnt(row *platforment.OAuthAuthorization) *store.OAuthAuthorization {
	value := &store.OAuthAuthorization{CodeHash: row.ID, ClientID: row.ClientID, RedirectURI: row.RedirectURI, CodeChallenge: row.CodeChallenge, CodeChallengeMethod: row.CodeChallengeMethod, Subject: row.Subject, OrgID: row.OrgID, Scopes: row.Scopes, Resources: row.Resources, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
	if row.Nonce != nil {
		value.Nonce = *row.Nonce
	}
	if row.Actor != nil {
		value.Actor = *row.Actor
	}
	if row.TeamID != nil {
		value.TeamID = *row.TeamID
	}
	if row.ConsumedAt != nil {
		value.ConsumedAt = *row.ConsumedAt
	}
	return value
}
func oauthTokenFromEnt(row *platforment.OAuthToken) *store.OAuthToken {
	value := &store.OAuthToken{HandleHash: row.ID, FamilyID: row.FamilyID, TokenType: string(row.TokenType), ClientID: row.ClientID, OrgID: row.OrgID, Scopes: row.Scopes, Resources: row.Resources, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, ReplayDetected: row.ReplayDetected}
	if row.Subject != nil {
		value.Subject = *row.Subject
	}
	if row.Actor != nil {
		value.Actor = *row.Actor
	}
	if row.TeamID != nil {
		value.TeamID = *row.TeamID
	}
	if row.RevokedAt != nil {
		value.RevokedAt = *row.RevokedAt
	}
	if row.ReplacedBy != nil {
		value.ReplacedBy = *row.ReplacedBy
	}
	return value
}
func oauthSigningKeyFromEnt(row *platforment.OAuthSigningKey) store.OAuthSigningKey {
	value := store.OAuthSigningKey{KeyID: row.KeyID, Algorithm: row.Algorithm, Status: string(row.Status), PublicJWK: row.PublicJwk, EncryptedPrivateKey: append([]byte(nil), row.EncryptedPrivateKey...), CreatedAt: row.CreatedAt}
	if row.NotAfter != nil {
		value.NotAfter = *row.NotAfter
	}
	return value
}

var _ store.OAuthServerStore = (*oauthServerStore)(nil)
