package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/mux"
	"golang.org/x/oauth2"

	"github.com/SAP/astonish/pkg/store"
)

// --------------------------------------------------------------------------
// Device session management — interface + in-memory implementation
// --------------------------------------------------------------------------

// ssoPageBaseCSS is the shared CSS foundation for all server-rendered SSO pages.
// It uses prefers-color-scheme to match the user's OS theme preference, and
// mirrors the Astonish Studio design tokens (Inter font, colors, radiuses).
const ssoPageBaseCSS = `
:root {
  --bg: #fafbfe; --surface: #ffffff; --text: #0b1222;
  --muted: #6b7280; --border: #e5e8f0; --accent: #7c3aed;
  --shadow: 0 8px 28px rgba(15, 23, 42, 0.1);
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0b1222; --surface: #0f172a; --text: #f6f7fb;
    --muted: #9ca3af; --border: rgba(255,255,255,0.08); --accent: #8d7ae0;
    --shadow: 0 10px 32px rgba(0, 0, 0, 0.42);
  }
}
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
body {
  font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
  background: var(--bg); color: var(--text);
  display: flex; align-items: center; justify-content: center;
  min-height: 100vh; padding: 24px;
}
.card {
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 16px; padding: 48px; max-width: 420px; width: 100%;
  text-align: center; box-shadow: var(--shadow);
}
.muted { color: var(--muted); font-size: 14px; line-height: 1.6; }
`

// deviceSessionStatus represents the state of an SSO device flow.
type deviceSessionStatus string

const (
	deviceStatusPending  deviceSessionStatus = "pending"
	deviceStatusComplete deviceSessionStatus = "complete"
	deviceStatusFailed   deviceSessionStatus = "failed"
)

// deviceSession holds state for a single CLI SSO login attempt.
type deviceSession struct {
	DeviceCode string
	State      string // OIDC state parameter (binds browser flow to this session)
	Nonce      string // OIDC nonce for ID token validation
	ProviderID string // Which OIDC provider to use
	ClientType string // "web" for Studio UI, "cli" (or empty) for CLI device flow

	Status       deviceSessionStatus
	ErrorMessage string

	// Set on successful completion
	AccessToken    string
	RefreshToken   string
	ExpiresIn      int
	User           authUserResponse
	Org            authOrgResponse
	TeamSlug       string
	AvailableOrgs  []authOrgOption
	AvailableTeams []authTeamOption

	CreatedAt time.Time
}

// DeviceSessionBackend abstracts device session storage for SSO flows.
// In personal mode (or when PG is unavailable), the in-memory implementation is used.
// In platform mode, the PG-backed implementation enables stateless horizontal scaling.
type DeviceSessionBackend interface {
	Create(ctx context.Context, sess *deviceSession) error
	GetByCode(ctx context.Context, code string) *deviceSession
	GetByState(ctx context.Context, state string) *deviceSession
	Complete(ctx context.Context, code string, sess *deviceSession) error
}

// --------------------------------------------------------------------------
// In-memory device session store (default, personal mode)
// --------------------------------------------------------------------------

// memoryDeviceSessionStore is a thread-safe in-memory store for device sessions.
// Sessions expire after 10 minutes.
type memoryDeviceSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*deviceSession // keyed by device_code
	byState  map[string]string         // state -> device_code mapping
}

const deviceSessionTTL = 10 * time.Minute

func newMemoryDeviceSessionStore() *memoryDeviceSessionStore {
	s := &memoryDeviceSessionStore{
		sessions: make(map[string]*deviceSession),
		byState:  make(map[string]string),
	}
	// Start background cleanup
	go s.cleanup()
	return s
}

func (s *memoryDeviceSessionStore) Create(_ context.Context, sess *deviceSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.DeviceCode] = sess
	s.byState[sess.State] = sess.DeviceCode
	return nil
}

func (s *memoryDeviceSessionStore) GetByCode(_ context.Context, code string) *deviceSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess := s.sessions[code]
	if sess != nil && time.Since(sess.CreatedAt) > deviceSessionTTL {
		return nil
	}
	return sess
}

func (s *memoryDeviceSessionStore) GetByState(_ context.Context, state string) *deviceSession {
	s.mu.RLock()
	code, ok := s.byState[state]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.GetByCode(context.Background(), code)
}

func (s *memoryDeviceSessionStore) Complete(_ context.Context, code string, sess *deviceSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[code] = sess
	return nil
}

func (s *memoryDeviceSessionStore) cleanup() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for code, sess := range s.sessions {
			if now.Sub(sess.CreatedAt) > deviceSessionTTL {
				delete(s.byState, sess.State)
				delete(s.sessions, code)
			}
		}
		s.mu.Unlock()
	}
}

// --------------------------------------------------------------------------
// SSOHandler manages OIDC SSO authentication flows
// --------------------------------------------------------------------------

// SSOHandler manages the SSO/OIDC endpoints.
type SSOHandler struct {
	pa             *PlatformAuth
	deviceSessions DeviceSessionBackend
}

// NewSSOHandler creates a new SSO handler.
// If pgPool is non-nil, uses PG-backed device sessions for stateless scaling.
// Otherwise falls back to in-memory store.
func NewSSOHandler(pa *PlatformAuth) *SSOHandler {
	return &SSOHandler{
		pa:             pa,
		deviceSessions: newMemoryDeviceSessionStore(),
	}
}

// NewSSOHandlerWithPG creates a new SSO handler with PG-backed device sessions.
func NewSSOHandlerWithPG(pa *PlatformAuth, backend DeviceSessionBackend) *SSOHandler {
	return &SSOHandler{
		pa:             pa,
		deviceSessions: backend,
	}
}

// RegisterSSORoutes registers the SSO-related endpoints.
// These are under /api/auth/sso/* and are bypassed by PlatformAuthMiddleware.
func RegisterSSORoutes(router *mux.Router, sso *SSOHandler) {
	// Device flow endpoints (CLI)
	router.HandleFunc("/api/auth/sso/init", sso.handleInit).Methods("POST")
	router.HandleFunc("/api/auth/sso/poll", sso.handlePoll).Methods("POST")

	// Browser flow endpoints (redirect chain)
	router.HandleFunc("/api/auth/sso/verify/{device_code}", sso.handleVerify).Methods("GET")
	router.HandleFunc("/api/auth/sso/callback", sso.handleCallback).Methods("GET")

	// Discovery endpoint: lists available SSO providers (for login UI)
	router.HandleFunc("/api/auth/sso/providers", sso.handleListProviders).Methods("GET")
}

// --------------------------------------------------------------------------
// POST /api/auth/sso/init — CLI initiates device flow
// --------------------------------------------------------------------------

type ssoInitRequest struct {
	ProviderID string `json:"provider_id,omitempty"` // Optional: specific provider. If empty, use the first enabled one.
	ClientType string `json:"client_type,omitempty"` // "web" for Studio UI, empty/"cli" for CLI device flow.
}

type ssoInitResponse struct {
	DeviceCode string `json:"device_code"`
	VerifyURL  string `json:"verify_url"`
	ExpiresIn  int    `json:"expires_in"`
	Interval   int    `json:"interval"` // polling interval in seconds
}

func (h *SSOHandler) handleInit(w http.ResponseWriter, r *http.Request) {
	var req ssoInitRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	pgStore := getPlatformBackend()
	if pgStore == nil {
		respondError(w, http.StatusInternalServerError, "platform store not available")
		return
	}

	// Find the OIDC provider
	ctx := r.Context()
	var provider *store.OIDCProvider

	if req.ProviderID != "" {
		var err error
		provider, err = pgStore.OIDCProviders().GetByID(ctx, req.ProviderID)
		if err != nil || !provider.Enabled {
			respondError(w, http.StatusBadRequest, "OIDC provider not found or disabled")
			return
		}
	} else {
		// Use first enabled provider
		providers, err := pgStore.OIDCProviders().ListEnabled(ctx, "")
		if err != nil || len(providers) == 0 {
			respondError(w, http.StatusNotFound, "no OIDC providers configured")
			return
		}
		provider = providers[0]
	}

	// Generate device code and OIDC state/nonce
	deviceCode := generateSecureToken(32)
	state := generateSecureToken(24)
	nonce := generateSecureToken(24)

	// Create device session
	continueURI, err := oauthContinueURI(r.URL.Query().Get("oauth_continue"))
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if continueURI != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     oauthContinueCookieName(deviceCode),
			Value:    base64.RawURLEncoding.EncodeToString([]byte(continueURI)),
			Path:     "/api/auth/sso/callback",
			MaxAge:   int(deviceSessionTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   isSecureRequest(r),
		})
	}
	sess := &deviceSession{
		DeviceCode: deviceCode,
		State:      state,
		Nonce:      nonce,
		ProviderID: provider.ID,
		ClientType: req.ClientType,
		Status:     deviceStatusPending,
		CreatedAt:  time.Now(),
	}
	if err := h.deviceSessions.Create(ctx, sess); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create device session")
		return
	}

	// Build the verify URL (the URL the user opens in their browser)
	scheme := "https"
	if r.TLS == nil && (strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1")) {
		scheme = "http"
	}
	// X-Forwarded-Proto can only upgrade (http→https), never downgrade.
	// This prevents misconfigured proxy chains from producing http:// URLs
	// for external domains (common with TLS termination at an outer proxy).
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		scheme = "https"
	}
	verifyURL := fmt.Sprintf("%s://%s/api/auth/sso/verify/%s", scheme, r.Host, deviceCode)

	respondJSON(w, http.StatusOK, ssoInitResponse{
		DeviceCode: deviceCode,
		VerifyURL:  verifyURL,
		ExpiresIn:  int(deviceSessionTTL.Seconds()),
		Interval:   2,
	})
}

// --------------------------------------------------------------------------
// GET /api/auth/sso/verify/{device_code} — Browser visits, redirects to IdP
// --------------------------------------------------------------------------

func (h *SSOHandler) handleVerify(w http.ResponseWriter, r *http.Request) {
	deviceCode := mux.Vars(r)["device_code"]

	sess := h.deviceSessions.GetByCode(r.Context(), deviceCode)
	if sess == nil {
		respondError(w, http.StatusBadRequest, "Invalid or expired device code. Please restart the login process.")
		return
	}
	if sess.Status != deviceStatusPending {
		respondError(w, http.StatusBadRequest, "This login session has already been used.")
		return
	}

	// Load the OIDC provider
	pgStore := getPlatformBackend()
	if pgStore == nil {
		respondError(w, http.StatusInternalServerError, "Platform store not available")
		return
	}

	provider, err := pgStore.OIDCProviders().GetByID(r.Context(), sess.ProviderID)
	if err != nil || !provider.Enabled {
		respondError(w, http.StatusInternalServerError, "OIDC provider not available")
		return
	}

	// Build the OIDC authorization URL
	oauth2Cfg := h.buildOAuth2Config(r, provider)
	authURL := oauth2Cfg.AuthCodeURL(
		sess.State,
		oauth2.SetAuthURLParam("nonce", sess.Nonce),
		oauth2.S256ChallengeOption(sess.DeviceCode), // Use device code as PKCE code verifier
	)

	http.Redirect(w, r, authURL, http.StatusFound)
}

// --------------------------------------------------------------------------
// GET /api/auth/sso/callback — IdP redirects here after authentication
// --------------------------------------------------------------------------

func (h *SSOHandler) handleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract state and code from IdP response
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	idpError := r.URL.Query().Get("error")

	if idpError != "" {
		desc := r.URL.Query().Get("error_description")
		h.renderCallbackError(w, fmt.Sprintf("Identity provider error: %s — %s", idpError, desc))
		return
	}

	if state == "" || code == "" {
		h.renderCallbackError(w, "Missing state or code parameter from identity provider")
		return
	}

	// Look up device session by state
	sess := h.deviceSessions.GetByState(ctx, state)
	if sess == nil {
		h.renderCallbackError(w, "Invalid or expired login session. Please restart the login process.")
		return
	}

	// Load OIDC provider
	pgStore := getPlatformBackend()
	if pgStore == nil {
		h.failDeviceSession(sess, "platform store not available")
		h.renderCallbackError(w, "Internal server error")
		return
	}

	provider, err := pgStore.OIDCProviders().GetByID(ctx, sess.ProviderID)
	if err != nil {
		h.failDeviceSession(sess, "OIDC provider not found")
		h.renderCallbackError(w, "OIDC provider configuration error")
		return
	}

	// Exchange authorization code for tokens
	oauth2Cfg := h.buildOAuth2Config(r, provider)
	token, err := oauth2Cfg.Exchange(ctx, code,
		oauth2.VerifierOption(sess.DeviceCode), // PKCE verifier
	)
	if err != nil {
		slog.Error("OIDC token exchange failed", "error", err)
		h.failDeviceSession(sess, "token exchange failed")
		h.renderCallbackError(w, "Failed to exchange authorization code. Please try again.")
		return
	}

	// Extract and verify the ID token
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		h.failDeviceSession(sess, "no id_token in response")
		h.renderCallbackError(w, "Identity provider did not return an ID token")
		return
	}

	// Create OIDC verifier
	oidcProvider, err := h.discoverOIDCProvider(provider)
	if err != nil {
		slog.Error("failed to create OIDC provider", "issuer", provider.IssuerURL, "error", err)
		h.failDeviceSession(sess, "OIDC discovery failed")
		h.renderCallbackError(w, "Failed to verify identity provider. Please contact your administrator.")
		return
	}

	verifier := oidcProvider.Verifier(&oidc.Config{
		ClientID: provider.ClientID,
	})

	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		slog.Error("ID token verification failed", "error", err)
		h.failDeviceSession(sess, "ID token verification failed")
		h.renderCallbackError(w, "Identity verification failed. Please try again.")
		return
	}

	// Verify nonce
	if idToken.Nonce != sess.Nonce {
		h.failDeviceSession(sess, "nonce mismatch")
		h.renderCallbackError(w, "Security verification failed (nonce mismatch). Please try again.")
		return
	}

	// Extract claims from ID token
	var claims struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		GivenName     string `json:"given_name"`
		FamilyName    string `json:"family_name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		slog.Error("failed to parse ID token claims", "error", err)
		h.failDeviceSession(sess, "failed to parse claims")
		h.renderCallbackError(w, "Failed to read identity information")
		return
	}

	// --- User Linking (Phase 4 logic, implemented inline) ---
	issuer := provider.IssuerURL
	subject := claims.Subject
	email := strings.ToLower(strings.TrimSpace(claims.Email))

	if subject == "" {
		h.failDeviceSession(sess, "ID token has no subject claim")
		h.renderCallbackError(w, "Identity provider returned no user identifier")
		return
	}

	// Step 1: Try to find user by OIDC subject
	user, err := pgStore.Users().GetByOIDC(ctx, issuer, subject)
	if err != nil {
		slog.Error("OIDC user lookup failed", "error", err, "issuer", issuer, "subject", subject)
		h.failDeviceSession(sess, "user lookup failed")
		h.renderCallbackError(w, "An internal error occurred. Please try again.")
		return
	}
	if user == nil && email != "" {
		// Step 2: Fall back to email match and auto-link.
		// Only allow auto-linking if the IdP has verified the email address.
		// Without this check, an attacker with an unverified email on the IdP
		// could hijack an existing account by triggering auto-link.
		if !claims.EmailVerified {
			slog.Warn("OIDC auto-link blocked: email not verified by IdP",
				"email", email,
				"issuer", issuer,
				"subject", subject,
			)
			h.failDeviceSession(sess, "email not verified")
			h.renderCallbackError(w, "Your email address has not been verified by the identity provider. Please verify your email and try again.")
			return
		}
		user, err = pgStore.Users().GetByEmail(ctx, email)
		if err == nil && user != nil {
			// Auto-link: set OIDC subject/issuer on the user
			slog.Info("auto-linking OIDC identity to existing user",
				"user_id", user.ID,
				"email", email,
				"issuer", issuer,
				"subject", subject,
			)
			user.OIDCSubject = subject
			user.OIDCIssuer = issuer
			if updateErr := pgStore.Users().Update(ctx, user); updateErr != nil {
				slog.Error("failed to auto-link OIDC identity", "error", updateErr)
				// Non-fatal: continue with the user we found
			}
		}
	}

	// Step 3: If still no user found, reject
	if user == nil {
		h.failDeviceSession(sess, "user not provisioned")
		h.renderCallbackError(w, fmt.Sprintf(
			"No account found for %s. Please contact your administrator to provision your account.",
			email,
		))
		return
	}

	// Check account status
	if user.Status != "active" {
		h.failDeviceSession(sess, "account suspended")
		h.renderCallbackError(w, "Your account is suspended. Please contact your administrator.")
		return
	}

	// Update display name from IdP if we don't have one
	if user.DisplayName == "" || user.DisplayName == strings.Split(user.Email, "@")[0] {
		if claims.Name != "" {
			user.DisplayName = claims.Name
		} else if claims.GivenName != "" || claims.FamilyName != "" {
			user.DisplayName = strings.TrimSpace(claims.GivenName + " " + claims.FamilyName)
		}
	}

	// Update last login
	user.LastLoginAt = time.Now()
	_ = pgStore.Users().Update(ctx, user)

	// --- Resolve org and team (same logic as password login) ---
	orgs, err := pgStore.Organizations().GetUserOrgs(ctx, user.ID)
	if err != nil || len(orgs) == 0 {
		h.failDeviceSession(sess, "no organization membership")
		h.renderCallbackError(w, "Your account has no organization membership. Please contact your administrator.")
		return
	}

	// Use first org (CLI will prompt if multiple)
	membership := orgs[0]
	org, err := pgStore.Organizations().GetByID(ctx, membership.OrgID)
	if err != nil {
		h.failDeviceSession(sess, "failed to load organization")
		h.renderCallbackError(w, "Failed to load your organization")
		return
	}

	// Resolve team
	teamSlug := h.pa.resolveDefaultTeam(ctx, user.ID, org)

	// Validate team membership (critical: no user without a team can log in)
	if !h.pa.validateTeamMembership(ctx, user.ID, org, teamSlug) {
		h.failDeviceSession(sess, "no team membership")
		h.renderCallbackError(w, "Your account has no team assignment. Please contact your administrator.")
		return
	}

	// --- Issue Astonish tokens ---
	accessToken, err := h.pa.jwt.IssueAccessToken(
		user.ID, user.Email, user.DisplayName,
		org.Slug, teamSlug, membership.Role, user.PlatformRole,
	)
	if err != nil {
		h.failDeviceSession(sess, "failed to issue access token")
		h.renderCallbackError(w, "Internal error issuing tokens")
		return
	}

	refreshToken, err := h.pa.jwt.IssueRefreshToken(user.ID, org.Slug, teamSlug)
	if err != nil {
		h.failDeviceSession(sess, "failed to issue refresh token")
		h.renderCallbackError(w, "Internal error issuing tokens")
		return
	}

	// Store refresh token hash for revocation
	tokenHash := hashRefreshToken(refreshToken)
	loginSession := &store.LoginSession{
		TokenHash: tokenHash,
		UserID:    user.ID,
		OrgID:     org.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(h.pa.jwt.RefreshTokenTTL()),
		UserAgent: r.UserAgent(),
		IPAddress: clientIP(r),
	}
	if err := pgStore.LoginSessions().Create(ctx, loginSession); err != nil {
		slog.Error("failed to persist login session during OIDC callback", "error", err, "user_id", user.ID)
		h.failDeviceSession(sess, "failed to create session")
		h.renderCallbackError(w, "Internal error creating session")
		return
	}

	// Build available orgs/teams for CLI selection
	var availableOrgs []authOrgOption
	for _, m := range orgs {
		o, oErr := pgStore.Organizations().GetByID(ctx, m.OrgID)
		if oErr != nil {
			continue
		}
		availableOrgs = append(availableOrgs, authOrgOption{
			ID:   o.ID,
			Name: o.Name,
			Slug: o.Slug,
			Role: m.Role,
		})
	}
	availableTeams := h.pa.listUserTeams(ctx, user.ID, org)

	// Mark device session as complete
	sess.Status = deviceStatusComplete
	sess.AccessToken = accessToken
	sess.RefreshToken = refreshToken
	sess.ExpiresIn = int(h.pa.jwt.AccessTokenTTL().Seconds())
	sess.User = authUserResponse{
		ID:           user.ID,
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		Role:         membership.Role,
		PlatformRole: user.PlatformRole,
	}
	sess.Org = authOrgResponse{
		ID:   org.ID,
		Name: org.Name,
		Slug: org.Slug,
	}
	sess.TeamSlug = teamSlug
	sess.AvailableOrgs = availableOrgs
	sess.AvailableTeams = availableTeams
	_ = h.deviceSessions.Complete(ctx, sess.DeviceCode, sess)

	// Web UI flow: set session cookies and resume the OAuth authorization request
	// when this browser login originated from an extension authorization flow.
	if sess.ClientType == "web" {
		setAccessTokenCookie(w, r, accessToken, h.pa.jwt.AccessTokenTTL())
		setRefreshTokenCookie(w, r, refreshToken, h.pa.jwt.RefreshTokenTTL())
		if continueURI, err := oauthContinueCookie(r, sess.DeviceCode); err != nil {
			h.renderCallbackError(w, "Invalid OAuth login continuation")
			return
		} else if continueURI != "" {
			continueURL, err := url.Parse(continueURI)
			if err != nil {
				h.renderCallbackError(w, "Invalid OAuth login continuation")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: oauthContinueCookieName(sess.DeviceCode), Value: "", Path: "/api/auth/sso/callback", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isSecureRequest(r)})
			// #nosec G710 -- oauthContinueURI already requires a same-origin /oauth/authorize request URI.
			http.Redirect(w, r, continueURL.String(), http.StatusFound)
			return
		}
		h.renderSSOBounce(w)
		return
	}

	// CLI flow: render success page (user closes tab, CLI polls for tokens)
	h.renderCallbackSuccess(w, user.DisplayName, org.Name)
}

// --------------------------------------------------------------------------
// POST /api/auth/sso/poll — CLI polls for completion
// --------------------------------------------------------------------------

type ssoPollRequest struct {
	DeviceCode string `json:"device_code"`
}

func (h *SSOHandler) handlePoll(w http.ResponseWriter, r *http.Request) {
	var req ssoPollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceCode == "" {
		respondError(w, http.StatusBadRequest, "device_code is required")
		return
	}

	sess := h.deviceSessions.GetByCode(r.Context(), req.DeviceCode)
	if sess == nil {
		respondError(w, http.StatusGone, "device session expired or not found")
		return
	}

	switch sess.Status {
	case deviceStatusPending:
		respondJSON(w, http.StatusAccepted, map[string]any{
			"status": "pending",
		})

	case deviceStatusFailed:
		respondJSON(w, http.StatusOK, map[string]any{
			"status": "failed",
			"error":  sess.ErrorMessage,
		})

	case deviceStatusComplete:
		respondJSON(w, http.StatusOK, map[string]any{
			"status":          "complete",
			"access_token":    sess.AccessToken,
			"refresh_token":   sess.RefreshToken,
			"expires_in":      sess.ExpiresIn,
			"user":            sess.User,
			"org":             sess.Org,
			"team":            sess.TeamSlug,
			"available_orgs":  sess.AvailableOrgs,
			"available_teams": sess.AvailableTeams,
		})
	}
}

// --------------------------------------------------------------------------
// GET /api/auth/sso/providers — Lists available SSO providers for UI
// --------------------------------------------------------------------------

func (h *SSOHandler) handleListProviders(w http.ResponseWriter, r *http.Request) {
	pgStore := getPlatformBackend()
	if pgStore == nil {
		// Platform store not initialized — return 503 so frontend knows to retry
		respondError(w, http.StatusServiceUnavailable, "platform not ready")
		return
	}

	providers, err := pgStore.OIDCProviders().ListEnabled(r.Context(), "")
	if err != nil {
		slog.Warn("failed to list SSO providers", "error", err)
		respondError(w, http.StatusServiceUnavailable, "temporarily unable to load SSO providers")
		return
	}

	if len(providers) == 0 {
		respondJSON(w, http.StatusOK, map[string]any{"providers": []any{}})
		return
	}

	type providerInfo struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var result []providerInfo
	for _, p := range providers {
		name := p.Name
		if name == "" {
			name = p.IssuerURL
		}
		result = append(result, providerInfo{ID: p.ID, Name: name})
	}

	respondJSON(w, http.StatusOK, map[string]any{"providers": result})
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// buildOAuth2Config builds an OAuth2 config for the given OIDC provider.
func (h *SSOHandler) buildOAuth2Config(r *http.Request, provider *store.OIDCProvider) *oauth2.Config {
	// Build callback URL
	scheme := "https"
	if r.TLS == nil && (strings.HasPrefix(r.Host, "localhost") || strings.HasPrefix(r.Host, "127.0.0.1")) {
		scheme = "http"
	}
	// X-Forwarded-Proto can only upgrade (http→https), never downgrade.
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		scheme = "https"
	}
	redirectURL := fmt.Sprintf("%s://%s/api/auth/sso/callback", scheme, r.Host)

	// Discover OIDC endpoints
	oidcProvider, err := h.discoverOIDCProvider(provider)

	var endpoint oauth2.Endpoint
	if err != nil {
		// Fallback: construct manually (shouldn't happen in production)
		slog.Warn("OIDC discovery failed, using manual endpoint construction", "issuer", provider.IssuerURL, "error", err)
		baseURL := provider.IssuerURL
		if provider.DiscoveryURL != "" {
			baseURL = provider.DiscoveryURL
		}
		endpoint = oauth2.Endpoint{
			AuthURL:  baseURL + "/oauth2/authorize",
			TokenURL: baseURL + "/oauth2/token",
		}
	} else {
		endpoint = oidcProvider.Endpoint()
	}

	return &oauth2.Config{
		ClientID:     provider.ClientID,
		ClientSecret: provider.ClientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     endpoint,
		Scopes:       provider.Scopes,
	}
}

// discoverOIDCProvider performs OIDC discovery for the given provider.
// It handles providers where the discovery URL differs from the issuer URL
// (e.g., SAP BTP XSUAA where issuer is "{base}/oauth/token" but discovery
// is at "{base}/.well-known/openid-configuration").
func (h *SSOHandler) discoverOIDCProvider(provider *store.OIDCProvider) (*oidc.Provider, error) {
	ctx := context.Background()

	if provider.DiscoveryURL != "" && provider.DiscoveryURL != provider.IssuerURL {
		// Use InsecureIssuerURLContext to decouple discovery URL from issuer validation.
		// This tells go-oidc: fetch .well-known from DiscoveryURL, but accept IssuerURL
		// as the issuer claim in ID tokens.
		ctx = oidc.InsecureIssuerURLContext(ctx, provider.IssuerURL)
		return oidc.NewProvider(ctx, provider.DiscoveryURL)
	}

	return oidc.NewProvider(ctx, provider.IssuerURL)
}

const oauthContinueCookiePrefix = "astonish_oauth_continue_"

func oauthContinueCookieName(deviceCode string) string {
	return oauthContinueCookiePrefix + deviceCode
}

func oauthContinueCookie(r *http.Request, deviceCode string) (string, error) {
	cookie, err := r.Cookie(oauthContinueCookieName(deviceCode))
	if err == http.ErrNoCookie {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return "", err
	}
	return oauthContinueURI(string(decoded))
}

func oauthContinueURI(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	continueURL, err := url.Parse(raw)
	if err != nil || continueURL.IsAbs() || continueURL.Host != "" || !strings.HasPrefix(continueURL.Path, "/oauth/authorize") {
		return "", fmt.Errorf("invalid OAuth login continuation")
	}
	return continueURL.RequestURI(), nil
}

// failDeviceSession marks a device session as failed.
func (h *SSOHandler) failDeviceSession(sess *deviceSession, msg string) {
	sess.Status = deviceStatusFailed
	sess.ErrorMessage = msg
	_ = h.deviceSessions.Complete(context.Background(), sess.DeviceCode, sess)
}

// renderSSOBounce renders a tiny HTML page that performs a same-origin redirect to "/".
// This is used for web UI SSO login: the IdP redirects to /callback (cross-origin),
// the server sets cookies, and this page triggers a same-origin navigation so that
// SameSite=Strict cookies are included in subsequent requests.
// Uses <meta http-equiv="refresh"> instead of inline <script> to avoid CSP issues.
func (h *SSOHandler) renderSSOBounce(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="0;url=/">
<title>Astonish — Logging in</title>
<style>
`+ssoPageBaseCSS+`
.spinner {
  width: 24px; height: 24px;
  border: 3px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%; animation: spin 0.8s linear infinite;
  margin: 0 auto 16px;
}
@keyframes spin { to { transform: rotate(360deg); } }
</style>
</head>
<body>
<div class="card">
<div class="spinner"></div>
<p class="muted">Logging in…</p>
</div>
</body>
</html>`)
}

// renderCallbackSuccess renders an HTML page shown to the user after successful SSO login.
func (h *SSOHandler) renderCallbackSuccess(w http.ResponseWriter, displayName, orgName string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Astonish — Login Successful</title>
<style>
`+ssoPageBaseCSS+`
.icon {
  width: 48px; height: 48px; border-radius: 12px;
  background: linear-gradient(135deg, #a855f7 0%, #7c3aed 100%);
  display: flex; align-items: center; justify-content: center;
  margin: 0 auto 20px; font-size: 20px;
}
h1 { font-size: 20px; font-weight: 600; color: var(--text); margin-bottom: 8px; }
.detail { font-size: 14px; color: var(--muted); margin-top: 4px; line-height: 1.6; }
.detail strong { color: var(--text); font-weight: 500; }
.hint { font-size: 13px; color: var(--muted); margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--border); }
</style>
</head>
<body>
<div class="card">
<div class="icon"><svg width="24" height="24" fill="none" viewBox="0 0 24 24"><path d="M5 13l4 4L19 7" stroke="white" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/></svg></div>
<h1>Login Successful</h1>`)
	fmt.Fprintf(w, `
<p class="detail">Welcome, <strong>%s</strong></p>
<p class="detail">Organization: <strong>%s</strong></p>`, html.EscapeString(displayName), html.EscapeString(orgName))
	fmt.Fprint(w, `
<p class="hint">You can close this browser tab and return to your terminal.</p>
</div>
</body>
</html>`)
}

// renderCallbackError renders an HTML error page shown to the user on SSO failure.
func (h *SSOHandler) renderCallbackError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Astonish — Login Failed</title>
<style>
`+ssoPageBaseCSS+`
.icon {
  width: 48px; height: 48px; border-radius: 12px;
  background: rgba(239, 68, 68, 0.1);
  display: flex; align-items: center; justify-content: center;
  margin: 0 auto 20px; font-size: 20px;
}
h1 { font-size: 20px; font-weight: 600; color: var(--text); margin-bottom: 8px; }
.error-box {
  font-size: 14px; color: #ef4444; background: rgba(239, 68, 68, 0.08);
  border: 1px solid rgba(239, 68, 68, 0.2); border-radius: 10px;
  padding: 14px 16px; margin-top: 20px; text-align: left; line-height: 1.5;
}
.hint { font-size: 13px; color: var(--muted); margin-top: 24px; }
</style>
</head>
<body>
<div class="card">
<div class="icon"><svg width="24" height="24" fill="none" viewBox="0 0 24 24"><path d="M6 18L18 6M6 6l12 12" stroke="#ef4444" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"/></svg></div>
<h1>Login Failed</h1>`)
	fmt.Fprintf(w, `
<div class="error-box">%s</div>`, html.EscapeString(message))
	fmt.Fprint(w, `
<p class="hint">Please close this tab and try again, or contact your administrator.</p>
</div>
</body>
</html>`)
}

// generateSecureToken generates a cryptographically secure random hex string.
func generateSecureToken(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		// Fallback (should never happen)
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// --------------------------------------------------------------------------
// Registration of SSO routes (called from launcher)
// --------------------------------------------------------------------------

// platformSSOHandler is the singleton SSO handler for the platform.
var platformSSOHandler *SSOHandler

// SetPlatformSSOHandler stores the SSO handler singleton.
func SetPlatformSSOHandler(h *SSOHandler) {
	platformSSOHandler = h
}

// GetPlatformSSOHandler returns the SSO handler singleton.
func GetPlatformSSOHandler() *SSOHandler {
	return platformSSOHandler
}
