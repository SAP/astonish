package entstore

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	storepkg "github.com/SAP/astonish/pkg/store"
)

func TestOAuthServerLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: "file:" + filepath.Join(t.TempDir(), "platform.db")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Close()
	if err := s.platformClient.Schema.Create(ctx); err != nil {
		t.Fatalf("create platform schema: %v", err)
	}
	store := s.OAuthServer()
	orgID, userID := uuid.NewString(), uuid.NewString()
	client := "mcp-client"
	if err := store.CreateOAuthClient(ctx, storepkg.OAuthClient{OwnerUserID: userID, ClientID: client, Name: "MCP", ClientType: "confidential", SecretHash: "argon2id-hash", OrgID: orgID, TeamID: "team", RedirectURIs: []string{"https://example.test/callback"}, GrantTypes: []string{"authorization_code"}, Resources: []string{"https://astonish.test"}, Scopes: []string{"chat", "tool:execute"}, Active: true}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	storedClient, err := store.GetOAuthClient(ctx, client)
	if err != nil || storedClient == nil {
		t.Fatalf("get client = %v, %v", storedClient, err)
	}
	if storedClient.SecretHash != "argon2id-hash" {
		t.Fatalf("secret hash = %q", storedClient.SecretHash)
	}
	now := time.Now().UTC()
	code := "hashed-code"
	if err := store.SaveOAuthAuthorization(ctx, storepkg.OAuthAuthorization{CodeHash: code, ClientID: client, RedirectURI: "https://example.test/callback", CodeChallenge: "challenge", CodeChallengeMethod: "S256", Subject: userID, OrgID: orgID, Scopes: []string{"chat"}, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("save authorization: %v", err)
	}
	if consumed, err := store.ConsumeOAuthAuthorization(ctx, code, now); err != nil || consumed == nil {
		t.Fatalf("consume authorization = %v, %v", consumed, err)
	}
	if consumed, err := store.ConsumeOAuthAuthorization(ctx, code, now); err != nil || consumed != nil {
		t.Fatalf("reconsume authorization = %v, %v", consumed, err)
	}
	refresh := "hashed-refresh"
	family := "refresh-family"
	if err := store.SaveOAuthToken(ctx, storepkg.OAuthToken{HandleHash: refresh, FamilyID: family, TokenType: "refresh", ClientID: client, Subject: userID, OrgID: orgID, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("save refresh: %v", err)
	}
	if consumed, err := store.ConsumeOAuthRefreshToken(ctx, refresh, now); err != nil || consumed == nil {
		t.Fatalf("consume refresh = %v, %v", consumed, err)
	}
	if consumed, err := store.ConsumeOAuthRefreshToken(ctx, refresh, now); err != nil || consumed == nil || !consumed.ReplayDetected {
		t.Fatalf("replay refresh = %v, %v", consumed, err)
	}
	if err := store.SaveOAuthToken(ctx, storepkg.OAuthToken{HandleHash: "delete-refresh", FamilyID: "delete-family", TokenType: "refresh", ClientID: client, Subject: userID, OrgID: orgID, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("save deletable refresh: %v", err)
	}
	if err := store.DeleteOAuthClient(ctx, client); err != nil {
		t.Fatalf("delete client: %v", err)
	}
	if deleted, err := store.GetOAuthClient(ctx, client); err != nil || deleted != nil {
		t.Fatalf("get deleted client = %v, %v", deleted, err)
	}
	if revoked, err := store.ConsumeOAuthRefreshToken(ctx, "delete-refresh", now); err != nil || revoked == nil || !revoked.ReplayDetected {
		t.Fatalf("deleted client refresh token = %v, %v", revoked, err)
	}
	if err := store.SaveOAuthConsent(ctx, storepkg.OAuthConsent{UserID: userID, OrgID: orgID, ClientID: client, Scopes: []string{"chat"}}); err != nil {
		t.Fatalf("save consent: %v", err)
	}
	if err := store.RevokeOAuthConsent(ctx, userID, orgID, client, now); err != nil {
		t.Fatalf("revoke consent: %v", err)
	}
	if err := store.SaveOAuthSigningKey(ctx, storepkg.OAuthSigningKey{KeyID: "key-1", Algorithm: "RS256", Status: "active", PublicJWK: map[string]any{"kty": "RSA"}, EncryptedPrivateKey: []byte("encrypted")}); err != nil {
		t.Fatalf("save key: %v", err)
	}
	keys, err := store.ListOAuthSigningKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys = %v, %v", keys, err)
	}
}

func TestOAuthServerAtomicAuthorizationConsumption(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: "file:" + filepath.Join(t.TempDir(), "atomic.db") + "?_busy_timeout=5000"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.platformClient.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	oauth := s.OAuthServer()
	now := time.Now().UTC()
	if err := oauth.SaveOAuthAuthorization(ctx, storepkg.OAuthAuthorization{CodeHash: "one-code", ClientID: "client", RedirectURI: "https://example.test", CodeChallenge: "c", CodeChallengeMethod: "S256", Subject: "user", OrgID: "org", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	var successes atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			row, err := oauth.ConsumeOAuthAuthorization(ctx, "one-code", now)
			if err != nil {
				t.Errorf("consume: %v", err)
				return
			}
			if row != nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful consumers = %d, want 1", got)
	}

	if err := oauth.SaveOAuthToken(ctx, storepkg.OAuthToken{HandleHash: "one-refresh", FamilyID: "one-family", TokenType: "refresh", ClientID: "client", OrgID: "org", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	successes.Store(0)
	start = make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			row, err := oauth.ConsumeOAuthRefreshToken(ctx, "one-refresh", now)
			if err != nil {
				t.Errorf("consume refresh: %v", err)
				return
			}
			if row != nil && !row.ReplayDetected {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful refresh consumers = %d, want 1", got)
	}
}

func TestOAuthServerCleanup(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{DSN: "file:" + filepath.Join(t.TempDir(), "cleanup.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.platformClient.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oauth := s.OAuthServer()
	for _, authorization := range []storepkg.OAuthAuthorization{
		{CodeHash: "expired", ClientID: "c", RedirectURI: "https://example.test", CodeChallenge: "c", CodeChallengeMethod: "S256", Subject: "u", OrgID: "o", ExpiresAt: now.Add(-24 * time.Hour)},
		{CodeHash: "active", ClientID: "c", RedirectURI: "https://example.test", CodeChallenge: "c", CodeChallengeMethod: "S256", Subject: "u", OrgID: "o", ExpiresAt: now.Add(time.Hour)},
	} {
		if err := oauth.SaveOAuthAuthorization(ctx, authorization); err != nil {
			t.Fatal(err)
		}
	}
	if err := oauth.SaveOAuthToken(ctx, storepkg.OAuthToken{HandleHash: "expired-token", FamilyID: "f", TokenType: "refresh", ClientID: "c", OrgID: "o", ExpiresAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := oauth.SaveOAuthToken(ctx, storepkg.OAuthToken{HandleHash: "active-token", FamilyID: "f2", TokenType: "refresh", ClientID: "c", OrgID: "o", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := oauth.SaveOAuthConsent(ctx, storepkg.OAuthConsent{UserID: uuid.NewString(), OrgID: uuid.NewString(), ClientID: "expired-consent", ExpiresAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := oauth.SaveOAuthConsent(ctx, storepkg.OAuthConsent{UserID: uuid.NewString(), OrgID: uuid.NewString(), ClientID: "active-consent", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := oauth.SaveOAuthSigningKey(ctx, storepkg.OAuthSigningKey{KeyID: "revoked-key", Algorithm: "RS256", Status: "revoked", PublicJWK: map[string]any{"kty": "RSA"}, EncryptedPrivateKey: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := oauth.SaveOAuthSigningKey(ctx, storepkg.OAuthSigningKey{KeyID: "active-key", Algorithm: "RS256", Status: "active", PublicJWK: map[string]any{"kty": "RSA"}, EncryptedPrivateKey: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	if err := s.CleanupExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.platformClient.OAuthAuthorization.Query().Count(ctx); n != 1 {
		t.Fatalf("authorizations remaining = %d", n)
	}
	if n, _ := s.platformClient.OAuthToken.Query().Count(ctx); n != 1 {
		t.Fatalf("tokens remaining = %d", n)
	}
	if n, _ := s.platformClient.OAuthConsent.Query().Count(ctx); n != 1 {
		t.Fatalf("consents remaining = %d", n)
	}
	if n, _ := s.platformClient.OAuthSigningKey.Query().Count(ctx); n != 1 {
		t.Fatalf("keys remaining = %d", n)
	}
}
