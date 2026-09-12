package oauthserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/SAP/astonish/pkg/store"
)

// Subject is the canonical platform identity established by existing built-in
// or federated login. External IdPs remain authentication sources; tokens from
// this server are Astonish-issued and audience-bound.
type Subject struct {
	ID, OrgID, TeamID, Actor string
}

// SessionValidator verifies the existing platform login presented at /authorize.
type SessionValidator func(context.Context, *http.Request) (Subject, error)

type Server struct {
	config   Config
	store    store.OAuthServerStore
	validate SessionValidator
	signing  *signingKey
}

func New(config Config, oauthStore store.OAuthServerStore, validate SessionValidator) (*Server, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if oauthStore == nil || validate == nil {
		return nil, errors.New("oauth store and platform session validator are required")
	}
	key, err := loadOrCreateSigningKey(context.Background(), oauthStore)
	if err != nil {
		return nil, fmt.Errorf("initialize signing key: %w", err)
	}
	return &Server{config: config, store: oauthStore, validate: validate, signing: key}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.discovery)
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("/oauth/jwks", s.jwks)
	mux.HandleFunc("/oauth/authorize", s.authorize)
	mux.HandleFunc("/oauth/token", s.token)
	mux.HandleFunc("/oauth/revoke", s.revoke)
	mux.HandleFunc("/oauth/introspect", s.introspect)
	return mux
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"issuer": s.config.Issuer, "authorization_endpoint": s.config.Issuer + "/oauth/authorize",
		"token_endpoint": s.config.Issuer + "/oauth/token", "jwks_uri": s.config.Issuer + "/oauth/jwks",
		"revocation_endpoint": s.config.Issuer + "/oauth/revoke", "introspection_endpoint": s.config.Issuer + "/oauth/introspect",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{GrantAuthorizationCode, GrantRefreshToken, GrantClientCredentials},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"scopes_supported":                      []string{"openid", "offline_access", ScopeToolExecute, ScopeA2A, ScopeChat},
	})
}

func (s *Server) protectedResource(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"resource": s.config.Resource, "authorization_servers": []string{s.config.Issuer}})
}

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{"keys": []map[string]any{s.signing.publicJWK()}})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") == "" || q.Get("redirect_uri") == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "authorization code parameters are required")
		return
	}
	client, err := s.client(r.Context(), q.Get("client_id"))
	redirectURI := ""
	if client != nil {
		for _, registeredURI := range client.RedirectURIs {
			if registeredURI == q.Get("redirect_uri") {
				redirectURI = registeredURI
				break
			}
		}
	}
	if err != nil || client == nil || !client.Active || !contains(client.GrantTypes, GrantAuthorizationCode) || redirectURI == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "unknown client or redirect URI")
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "S256 PKCE is required")
		return
	}
	subject, err := s.validate(r.Context(), r)
	if err != nil {
		// Validation above has bound this request to an active client, exact redirect
		// URI, and S256 challenge. Studio authenticates the human (including optional
		// upstream SSO) and then resumes this same-origin authorization request.
		login, loginErr := url.Parse(s.config.LoginPath)
		if loginErr != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "oauth login continuation is unavailable")
			return
		}
		loginQuery := login.Query()
		loginQuery.Set("oauth_continue", r.URL.RequestURI())
		login.RawQuery = loginQuery.Encode()
		http.Redirect(w, r, login.String(), http.StatusFound)
		return
	}
	if subject.ID == "" || subject.OrgID == "" || (!isChromeExtensionClient(client) && client.OrgID != "" && subject.OrgID != client.OrgID) {
		oauthError(w, http.StatusForbidden, "access_denied", "subject is not authorized for this client")
		return
	}
	// The client registration is the immutable tenant selection. The browser
	// session proves the subject belongs to the assigned organization; it must not
	// be allowed to select or omit the team embedded in the resulting token.
	orgID, teamID := client.OrgID, client.TeamID
	if isChromeExtensionClient(client) {
		orgID, teamID = subject.OrgID, subject.TeamID
	}
	scopes, ok := permittedSubset(strings.Fields(q.Get("scope")), authorizationScopes(client.Scopes))
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_scope", "requested scope is not granted")
		return
	}
	resources := allowed(q["resource"], client.Resources)
	code, err := randomHandle()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue code")
		return
	}
	if err := s.store.SaveOAuthAuthorization(r.Context(), store.OAuthAuthorization{CodeHash: hash(code), ClientID: client.ClientID, RedirectURI: redirectURI, CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: "S256", Nonce: q.Get("nonce"), Subject: subject.ID, Actor: subject.Actor, OrgID: orgID, TeamID: teamID, Scopes: scopes, Resources: resources, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(s.config.CodeTTL)}); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not save code")
		return
	}
	redirect, _ := url.Parse(redirectURI)
	values := redirect.Query()
	values.Set("code", code)
	values.Set("state", q.Get("state"))
	redirect.RawQuery = values.Encode()
	// redirectURI is selected from the client's registered redirect URI list by
	// exact match above; it is not an arbitrary request destination.
	// #nosec G710 -- exact registered redirect URI allowlist enforced above.
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	switch r.Form.Get("grant_type") {
	case GrantAuthorizationCode:
		s.exchangeCode(w, r)
	case GrantRefreshToken:
		s.refreshToken(w, r)
	case GrantClientCredentials:
		s.clientCredentials(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant type is not supported")
	}
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateClient(w, r, false)
	if !ok {
		return
	}
	code := r.Form.Get("code")
	auth, err := s.store.ConsumeOAuthAuthorization(r.Context(), hash(code), time.Now().UTC())
	if err != nil || auth == nil || auth.ClientID != client.ClientID || auth.RedirectURI != r.Form.Get("redirect_uri") || pkceS256(r.Form.Get("code_verifier")) != auth.CodeChallenge {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid")
		return
	}
	s.writeTokens(w, r.Context(), client, auth.Subject, auth.Actor, auth.OrgID, auth.TeamID, auth.Scopes, auth.Resources, contains(auth.Scopes, "offline_access"), "")
}

func (s *Server) refreshToken(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateClient(w, r, false)
	if !ok {
		return
	}
	now := time.Now().UTC()
	refresh := r.Form.Get("refresh_token")
	if refresh == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	token, err := s.store.ConsumeOAuthRefreshToken(r.Context(), hash(refresh), now)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "refresh token lookup failed")
		return
	}
	if token == nil || token.ReplayDetected || token.ClientID != client.ClientID {
		// A consumed handle is indistinguishable from an unknown handle at this
		// narrow store boundary. Revoke only a proven family, never an attacker-
		// supplied family identifier.
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid")
		return
	}
	requestedScopes := strings.Fields(r.Form.Get("scope"))
	if len(requestedScopes) > 0 {
		var ok bool
		requestedScopes, ok = permittedSubset(requestedScopes, token.Scopes)
		if !ok {
			oauthError(w, http.StatusBadRequest, "invalid_scope", "requested scope is not granted")
			return
		}
	} else {
		requestedScopes = token.Scopes
	}
	s.writeTokens(w, r.Context(), client, token.Subject, token.Actor, token.OrgID, token.TeamID, requestedScopes, token.Resources, true, token.FamilyID)
}

func (s *Server) clientCredentials(w http.ResponseWriter, r *http.Request) {
	client, ok := s.authenticateClient(w, r, true)
	if !ok {
		return
	}
	scopes, ok := permittedSubset(strings.Fields(r.Form.Get("scope")), client.Scopes)
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_scope", "requested scope is not granted")
		return
	}
	resources := allowed(r.Form["resource"], client.Resources)
	if client.OwnerUserID == "" || client.OrgID == "" || client.TeamID == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client is missing required tenant context")
		return
	}
	s.writeTokens(w, r.Context(), client, "", "", client.OrgID, client.TeamID, scopes, resources, false, "")
}

func (s *Server) authenticateClient(w http.ResponseWriter, r *http.Request, requireSecret bool) (*store.OAuthClient, bool) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	client, err := s.client(r.Context(), id)
	if err != nil || client == nil || !client.Active {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return nil, false
	}
	if requireSecret && client.ClientType != "confidential" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "confidential client required")
		return nil, false
	}
	if client.ClientType == "confidential" && (client.SecretHash == "" || bcrypt.CompareHashAndPassword([]byte(client.SecretHash), []byte(secret)) != nil) {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return nil, false
	}
	return client, true
}

func (s *Server) writeTokens(w http.ResponseWriter, ctx context.Context, client *store.OAuthClient, subject, actor, orgID, teamID string, scopes, resources []string, refresh bool, familyID string) {
	now := time.Now().UTC()
	exp := now.Add(s.config.AccessTokenTTL)
	// MCP clients are permitted to omit the optional RFC 8707 resource parameter.
	// In that case, issue for this server's advertised protected resource rather
	// than the client ID so the bearer is accepted at the MCP endpoint.
	claims := jwt.MapClaims{"iss": s.config.Issuer, "aud": audience(resources, s.config.Resource), "exp": exp.Unix(), "iat": now.Unix(), "client_id": client.ClientID, "scope": strings.Join(scopes, " "), "org_id": orgID}
	if subject != "" {
		claims["sub"] = subject
	}
	if actor != "" {
		claims["act"] = map[string]string{"sub": actor}
	}
	if teamID != "" {
		claims["team_id"] = teamID
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	accessToken.Header["kid"] = s.signing.id
	access, err := accessToken.SignedString(s.signing.key)
	if err != nil {
		oauthError(w, 500, "server_error", "token signing failed")
		return
	}
	response := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(s.config.AccessTokenTTL.Seconds()), "scope": strings.Join(scopes, " ")}
	if refresh {
		handle, err := randomHandle()
		if err != nil {
			oauthError(w, 500, "server_error", "refresh token failed")
			return
		}
		if familyID == "" {
			familyID, _ = randomHandle()
		}
		if familyID == "" {
			oauthError(w, http.StatusInternalServerError, "server_error", "refresh token family failed")
			return
		}
		if err := s.store.SaveOAuthToken(ctx, store.OAuthToken{HandleHash: hash(handle), FamilyID: familyID, TokenType: "refresh", ClientID: client.ClientID, Subject: subject, Actor: actor, OrgID: orgID, TeamID: teamID, Scopes: scopes, Resources: resources, CreatedAt: now, ExpiresAt: now.Add(s.config.RefreshTokenTTL)}); err != nil {
			oauthError(w, 500, "server_error", "refresh token storage failed")
			return
		}
		response["refresh_token"] = handle
	}
	respondJSON(w, http.StatusOK, response)
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid revocation request")
		return
	}
	client, ok := s.authenticateClient(w, r, false)
	if !ok {
		return
	}
	token, err := s.store.GetOAuthRefreshToken(r.Context(), hash(r.Form.Get("token")))
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token lookup failed")
		return
	}
	// RFC 7009 deliberately returns success for unknown tokens. A client may
	// only revoke a credential it owns; foreign handles are indistinguishable.
	if token != nil && token.ClientID == client.ClientID {
		if err := s.store.RevokeOAuthTokenFamily(r.Context(), token.FamilyID, time.Now().UTC()); err != nil {
			oauthError(w, http.StatusInternalServerError, "server_error", "token revocation failed")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) introspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid introspection request")
		return
	}
	client, ok := s.authenticateClient(w, r, true)
	if !ok {
		return
	}
	token, err := s.store.GetOAuthRefreshToken(r.Context(), hash(r.Form.Get("token")))
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "token lookup failed")
		return
	}
	now := time.Now().UTC()
	if token == nil || token.ClientID != client.ClientID || !token.RevokedAt.IsZero() || !token.ExpiresAt.After(now) {
		respondJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	response := map[string]any{"active": true, "client_id": token.ClientID, "scope": strings.Join(token.Scopes, " "), "exp": token.ExpiresAt.Unix(), "iat": token.CreatedAt.Unix(), "token_type": "refresh_token", "org_id": token.OrgID}
	if token.Subject != "" {
		response["sub"] = token.Subject
	}
	if token.Actor != "" {
		response["act"] = map[string]string{"sub": token.Actor}
	}
	if token.TeamID != "" {
		response["team_id"] = token.TeamID
	}
	respondJSON(w, http.StatusOK, response)
}

func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func oauthError(w http.ResponseWriter, status int, code, description string) {
	respondJSON(w, status, map[string]string{"error": code, "error_description": description})
}
func randomHandle() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func hash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func pkceS256(v string) string { return hash(v) }
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func allowed(requested, permitted []string) []string {
	if len(requested) == 0 {
		return nil
	}
	out := make([]string, 0, len(requested))
	for _, value := range requested {
		if contains(permitted, value) {
			out = append(out, value)
		}
	}
	return out
}
func authorizationScopes(clientScopes []string) []string {
	// openid and offline_access are protocol scopes for interactive authorization
	// code clients. They do not grant an Astonish capability and therefore are
	// intentionally not configurable in the client-permission selector.
	out := make([]string, 0, len(clientScopes)+2)
	out = append(out, "openid", "offline_access")
	out = append(out, clientScopes...)
	return out
}

func permittedSubset(requested, permitted []string) ([]string, bool) {
	if len(requested) == 0 {
		return nil, true
	}
	selected := make(map[string]bool, len(requested))
	for _, value := range requested {
		if !contains(permitted, value) {
			return nil, false
		}
		selected[value] = true
	}
	out := make([]string, 0, len(selected))
	for _, value := range permitted {
		if selected[value] {
			out = append(out, value)
		}
	}
	return out, true
}
func audience(resources []string, fallback string) []string {
	if len(resources) > 0 {
		return resources
	}
	return []string{fallback}
}
