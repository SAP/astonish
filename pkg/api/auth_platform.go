package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/mailer"
	"github.com/SAP/astonish/pkg/store"
)

// orgResolver is the subset of PGStore used for resolving org data stores.
// Extracted as an interface to allow unit testing of team access checks.
type orgResolver interface {
	ForOrg(orgSlug string) (store.OrgDataStore, error)
}

// PlatformAuth manages authentication for platform (multi-tenant) mode.
// It handles user registration, login, JWT token lifecycle, and automated
// org/team provisioning for the first user.
type PlatformAuth struct {
	jwt         *JWTIssuer
	authCfg     config.PlatformAuthConfig
	pgStore     store.PlatformBackend
	storeCfg    config.StorageConfig
	noAuthMode  bool
	orgResolver orgResolver // defaults to pgStore; override in tests
	linkCodes   store.LinkCodeStore
}

// NewPlatformAuth creates a new platform auth manager.
func NewPlatformAuth(authCfg config.PlatformAuthConfig, backend store.PlatformBackend, storeCfg config.StorageConfig) *PlatformAuth {
	jwtSecret := authCfg.GetJWTSecret()
	return &PlatformAuth{
		jwt:         NewJWTIssuer(jwtSecret, authCfg.GetAccessTokenTTL(), authCfg.GetRefreshTokenTTL()),
		authCfg:     authCfg,
		pgStore:     backend,
		storeCfg:    storeCfg,
		noAuthMode:  authCfg.IsNoAuth(storeCfg.Backend),
		orgResolver: backend,
	}
}

// IsNoAuth reports whether this platform uses the automatically authenticated local user.
func (pa *PlatformAuth) IsNoAuth() bool {
	return pa.noAuthMode
}

// SetLinkCodeStoreForAuth sets the link code store for registration email verification.
func (pa *PlatformAuth) SetLinkCodeStoreForAuth(lcs store.LinkCodeStore) {
	pa.linkCodes = lcs
}

// JWTIssuer returns the JWT issuer for use by middleware.
func (pa *PlatformAuth) JWTIssuer() *JWTIssuer {
	return pa.jwt
}

// RegisterPlatformAuthRoutes registers platform auth endpoints.
// These routes are always accessible (not behind auth middleware).
func RegisterPlatformAuthRoutes(router *mux.Router, pa *PlatformAuth) {
	router.HandleFunc("/api/auth/register", pa.handleRegister).Methods("POST")
	router.HandleFunc("/api/auth/login", pa.handleLogin).Methods("POST")
	router.HandleFunc("/api/auth/refresh", pa.handleRefresh).Methods("POST")
	router.HandleFunc("/api/auth/logout", pa.handleLogout).Methods("POST")
	router.HandleFunc("/api/auth/me", pa.handleMe).Methods("GET")
	router.HandleFunc("/api/auth/setup-status", pa.handleSetupStatus).Methods("GET")
	router.HandleFunc("/api/auth/verify-email", pa.handleVerifyEmail).Methods("POST")
	router.HandleFunc("/api/auth/resend-verification", pa.handleResendVerification).Methods("POST")
}

// --- Handler: POST /api/auth/register ---

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type authResponse struct {
	User           authUserResponse `json:"user"`
	Org            authOrgResponse  `json:"org"`
	AccessToken    string           `json:"access_token,omitempty"`
	RefreshToken   string           `json:"refresh_token,omitempty"`
	ExpiresIn      int              `json:"expires_in"`
	TeamSlug       string           `json:"team,omitempty"`
	AvailableOrgs  []authOrgOption  `json:"available_orgs,omitempty"`
	AvailableTeams []authTeamOption `json:"available_teams,omitempty"`
}

type authOrgOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

type authTeamOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type authUserResponse struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	Role         string `json:"role"`
	PlatformRole string `json:"platform_role,omitempty"`
}

type authOrgResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (pa *PlatformAuth) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate input
	if err := validateEmail(req.Email); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePassword(req.Password); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = strings.Split(req.Email, "@")[0]
	}

	// Check if this is the first user (org bootstrap) — always allowed regardless
	// of registration policy, since someone must be able to set up the platform.
	ctx := r.Context()
	orgCount, _ := pa.pgStore.Organizations().Count(ctx)
	isFirstUser := orgCount == 0

	// Check if registration is allowed (DB override > YAML config).
	// The first-user bootstrap is exempt — the platform must be initializable.
	if !isFirstUser && !pa.isRegistrationEffectivelyAllowed(ctx) {
		respondError(w, http.StatusForbidden, "registration is disabled; contact your administrator for an invitation")
		return
	}

	// Check if email is already taken
	existing, _ := pa.pgStore.Users().GetByEmail(ctx, req.Email)
	if existing != nil {
		respondError(w, http.StatusConflict, "email already registered")
		return
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to process password")
		return
	}

	// Determine initial status:
	// - First user: always "active" (bootstrap, no one to verify against)
	// - Subsequent users: "pending_verification" if email verification is required
	initialStatus := "active"
	if !isFirstUser && pa.isEmailVerificationEffectivelyRequired(ctx) {
		initialStatus = "pending_verification"
	}

	// Create user
	user := &store.User{
		ID:           uuid.New().String(),
		Email:        strings.ToLower(strings.TrimSpace(req.Email)),
		DisplayName:  strings.TrimSpace(req.DisplayName),
		PasswordHash: string(hash),
		Status:       initialStatus,
		CreatedAt:    time.Now(),
	}

	if err := pa.pgStore.Users().Create(ctx, user); err != nil {
		slog.Error("failed to create user", "email", user.Email, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// First user: auto-provision org + team (bootstrap flow, unchanged)
	if isFirstUser {
		org, role, err := pa.provisionFirstOrg(ctx, user)
		if err != nil {
			slog.Error("failed to provision org for first user", "user", user.ID, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to provision organization")
			return
		}
		// Issue tokens and respond — first user gets full access immediately
		pa.issueTokensAndRespond(w, r, user, org, role)
		return
	}

	// Non-first user: do NOT auto-assign to any org or team.
	// Team admins will add them later via POST /api/teams/{slug}/members.

	if pa.isEmailVerificationEffectivelyRequired(ctx) {
		// Send verification code
		if err := pa.sendRegistrationVerificationCode(ctx, user); err != nil {
			slog.Error("failed to send verification code", "email", user.Email, "error", err)
			// Non-fatal: user is created, they can request resend
		}
		respondJSON(w, http.StatusCreated, map[string]any{
			"requires_verification": true,
			"email":                 user.Email,
			"message":               "Please check your email for a verification code.",
		})
		return
	}

	// Email verification not required: account is active but has no org/team.
	// Send platform welcome email.
	mailer.SendAsync(ctx, mailer.Welcome{
		Recipient:      user.Email,
		DisplayName:    user.DisplayName,
		AppURL:         resolveAppURL(r),
		DevEnvironment: isDevEnvironment(ctx, pa.pgStore.PlatformSettings()),
	})

	respondJSON(w, http.StatusCreated, map[string]any{
		"requires_verification": false,
		"no_team":               true,
		"email":                 user.Email,
		"message":               "Account created. A team administrator must add you to a team before you can access resources.",
	})
}

// --- Handler: POST /api/auth/login ---

type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	ClientType string `json:"client_type,omitempty"` // "cli" to receive tokens in response body
	Org        string `json:"org,omitempty"`         // Optional: org slug to scope token to
	Team       string `json:"team,omitempty"`        // Optional: team slug to scope token to
}

func (pa *PlatformAuth) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		respondError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	ctx := r.Context()

	// Find user by email
	user, err := pa.pgStore.Users().GetByEmail(ctx, strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil || user == nil {
		respondError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	// Check account status
	if user.Status == "pending_verification" {
		respondJSON(w, http.StatusForbidden, map[string]any{
			"error":                 "email_not_verified",
			"message":               "Please verify your email address before logging in.",
			"requires_verification": true,
			"email":                 user.Email,
		})
		return
	}
	if user.Status != "active" {
		respondError(w, http.StatusForbidden, "account is suspended")
		return
	}

	// Verify password
	if user.PasswordHash == "" {
		// OIDC-only user, no password set
		respondError(w, http.StatusUnauthorized, "this account uses external login (OIDC)")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		respondError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	// Update last login time
	user.LastLoginAt = time.Now()
	_ = pa.pgStore.Users().Update(ctx, user)

	// Get user's org memberships
	orgs, err := pa.pgStore.Organizations().GetUserOrgs(ctx, user.ID)
	if err != nil || len(orgs) == 0 {
		// User is active but has no org/team — they need a team admin to add them
		respondJSON(w, http.StatusOK, map[string]any{
			"error":   "no_team_membership",
			"message": "Your account is active but you have not been added to any team yet. Please contact a team administrator.",
			"user": map[string]any{
				"id":           user.ID,
				"email":        user.Email,
				"display_name": user.DisplayName,
			},
		})
		return
	}

	// Resolve which org to use
	var membership *store.OrgMembership
	if req.Org != "" {
		// User specified an org — find it in their memberships
		for _, m := range orgs {
			if m.OrgSlug == req.Org {
				membership = m
				break
			}
		}
		if membership == nil {
			respondError(w, http.StatusForbidden, fmt.Sprintf("you are not a member of organization %q", req.Org))
			return
		}
	} else {
		// Default to first org
		membership = orgs[0]
	}

	org, err := pa.pgStore.Organizations().GetByID(ctx, membership.OrgID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load organization")
		return
	}

	// Resolve team
	teamSlug := ""
	if req.Team != "" {
		// User specified a team — validate membership
		teamSlug = req.Team
		if !pa.validateTeamMembership(ctx, user.ID, org, teamSlug) {
			respondError(w, http.StatusForbidden, fmt.Sprintf("you are not a member of team %q", req.Team))
			return
		}
	} else {
		teamSlug = pa.resolveDefaultTeam(ctx, user.ID, org)
	}

	// Build available orgs list for CLI selection
	var availableOrgs []authOrgOption
	if req.ClientType == "cli" {
		for _, m := range orgs {
			o, oErr := pa.pgStore.Organizations().GetByID(ctx, m.OrgID)
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
	}

	// Build available teams list for CLI selection
	var availableTeams []authTeamOption
	if req.ClientType == "cli" {
		availableTeams = pa.listUserTeams(ctx, user.ID, org)
	}

	pa.issueTokensAndRespondWithContext(w, r, user, org, membership.Role, teamSlug, availableOrgs, availableTeams, req.ClientType)
}

// --- Handler: POST /api/auth/refresh ---

func (pa *PlatformAuth) handleRefresh(w http.ResponseWriter, r *http.Request) {
	// Read refresh token from cookie or request body (CLI clients).
	var refreshTokenStr string
	var isCLI bool

	// Try request body first (for CLI clients that can't use cookies)
	type refreshRequest struct {
		RefreshToken string `json:"refresh_token"`
	}
	var bodyReq refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&bodyReq); err == nil && bodyReq.RefreshToken != "" {
		refreshTokenStr = bodyReq.RefreshToken
		isCLI = true
	}

	// Fall back to cookie (browser clients)
	if refreshTokenStr == "" {
		cookie, err := r.Cookie("astonish_refresh")
		if err != nil {
			respondError(w, http.StatusUnauthorized, "no refresh token")
			return
		}
		refreshTokenStr = cookie.Value
	}

	// Validate the refresh token
	claims, err := pa.jwt.ValidateRefreshToken(refreshTokenStr)
	if err != nil {
		if errors.Is(err, ErrTokenExpired) {
			// Clear the expired cookie
			clearAuthCookies(w, r)
			respondError(w, http.StatusUnauthorized, "refresh token expired; please login again")
			return
		}
		respondError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	ctx := r.Context()

	// Verify the refresh token hasn't been revoked (e.g., by logout or admin action).
	// The token hash must still exist in login_sessions and not be expired.
	// Graceful degrade: if no row exists (legacy token issued before revocation tracking),
	// allow the refresh and self-heal by inserting the hash after org resolution.
	tokenHash := hashRefreshToken(refreshTokenStr)
	needsSelfHeal := false
	if _, err := pa.pgStore.LoginSessions().Validate(ctx, tokenHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Legacy token — no session row. Allow refresh but schedule self-heal.
			slog.Info("refresh token not in login_sessions; will self-heal", "user_id", claims.UserID)
			needsSelfHeal = true
		} else {
			// DB error — degrade gracefully, log and continue (don't lock user out)
			slog.Warn("login_sessions.Validate error during refresh; allowing", "error", err)
		}
	}

	// Verify user still exists and is active
	user, err := pa.pgStore.Users().GetByID(ctx, claims.UserID)
	if err != nil || user == nil || user.Status != "active" {
		clearAuthCookies(w, r)
		respondError(w, http.StatusUnauthorized, "account not found or suspended")
		return
	}

	// Get current org membership (may have changed since token was issued)
	orgs, err := pa.pgStore.Organizations().GetUserOrgs(ctx, user.ID)
	if err != nil || len(orgs) == 0 {
		clearAuthCookies(w, r)
		respondError(w, http.StatusUnauthorized, "no organization membership")
		return
	}

	// Find the org from the refresh token claims, or default to first
	var membership *store.OrgMembership
	for _, m := range orgs {
		if m.OrgSlug == claims.OrgSlug {
			membership = m
			break
		}
	}
	if membership == nil {
		membership = orgs[0]
	}

	org, err := pa.pgStore.Organizations().GetByID(ctx, membership.OrgID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load organization")
		return
	}

	// Get user's team — preserve from refresh token if set, otherwise resolve from DB
	teamSlug := claims.DefaultTeamSlug
	if teamSlug == "" {
		teamSlug = pa.resolveDefaultTeam(ctx, user.ID, org)
	}

	// Self-heal: insert the token hash into login_sessions so future refreshes are tracked.
	// This handles legacy tokens issued before revocation tracking was added.
	if needsSelfHeal {
		loginSession := &store.LoginSession{
			TokenHash: tokenHash,
			UserID:    user.ID,
			OrgID:     org.ID,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(pa.jwt.RefreshTokenTTL()),
			UserAgent: r.UserAgent(),
			IPAddress: clientIP(r),
		}
		if err := pa.pgStore.LoginSessions().Create(ctx, loginSession); err != nil {
			slog.Warn("failed to self-heal login session", "error", err)
		} else {
			slog.Info("self-healed login session", "user_id", user.ID)
		}
	}

	// Issue new access token
	accessToken, err := pa.jwt.IssueAccessToken(
		user.ID, user.Email, user.DisplayName,
		org.Slug, teamSlug, membership.Role, user.PlatformRole,
	)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	// Issue new refresh token (rotation) for CLI clients
	var newRefreshToken string
	if isCLI {
		newRefreshToken, err = pa.jwt.IssueRefreshToken(user.ID, org.Slug, teamSlug)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to issue refresh token")
			return
		}
		// Delete the old refresh token from login_sessions (prevents reuse)
		_ = pa.pgStore.LoginSessions().Delete(ctx, tokenHash)
		// Store new refresh token hash for revocation support
		newTokenHash := hashRefreshToken(newRefreshToken)
		loginSession := &store.LoginSession{
			TokenHash: newTokenHash,
			UserID:    user.ID,
			OrgID:     org.ID,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(pa.jwt.RefreshTokenTTL()),
			UserAgent: r.UserAgent(),
			IPAddress: clientIP(r),
		}
		_ = pa.pgStore.LoginSessions().Create(ctx, loginSession)
	}

	setAccessTokenCookie(w, r, accessToken, pa.jwt.AccessTokenTTL())

	resp := authResponse{
		User: authUserResponse{
			ID:           user.ID,
			Email:        user.Email,
			DisplayName:  user.DisplayName,
			Role:         membership.Role,
			PlatformRole: user.PlatformRole,
		},
		Org: authOrgResponse{
			ID:   org.ID,
			Name: org.Name,
			Slug: org.Slug,
		},
		ExpiresIn: int(pa.jwt.AccessTokenTTL().Seconds()),
	}

	// Include tokens in body for CLI clients
	if isCLI {
		resp.AccessToken = accessToken
		resp.RefreshToken = newRefreshToken
	}

	respondJSON(w, http.StatusOK, resp)
}

// --- Handler: POST /api/auth/logout ---

func (pa *PlatformAuth) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Revoke the refresh token server-side so it can no longer be used.
	// Read the token from cookie (browser) or body (CLI).
	var refreshTokenStr string
	type logoutRequest struct {
		RefreshToken string `json:"refresh_token"`
	}
	var bodyReq logoutRequest
	if err := json.NewDecoder(r.Body).Decode(&bodyReq); err == nil && bodyReq.RefreshToken != "" {
		refreshTokenStr = bodyReq.RefreshToken
	}
	if refreshTokenStr == "" {
		if cookie, err := r.Cookie("astonish_refresh"); err == nil {
			refreshTokenStr = cookie.Value
		}
	}

	// Delete the refresh token from login_sessions (server-side revocation)
	if refreshTokenStr != "" {
		tokenHash := hashRefreshToken(refreshTokenStr)
		if err := pa.pgStore.LoginSessions().Delete(r.Context(), tokenHash); err != nil {
			slog.Debug("logout: failed to delete login session (may already be gone)",
				"error", err)
		}
	}

	clearAuthCookies(w, r)
	respondJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// --- Handler: GET /api/auth/me ---

func (pa *PlatformAuth) handleMe(w http.ResponseWriter, r *http.Request) {
	if pa.IsNoAuth() {
		pa.respondSingleUser(w, r)
		return
	}
	// Read access token from Authorization header (CLI) or cookie (browser)
	var tokenStr string
	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		cookie, err := r.Cookie("astonish_access")
		if err != nil {
			respondError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		tokenStr = cookie.Value
	}

	claims, err := pa.jwt.ValidateAccessToken(tokenStr)
	if err != nil {
		respondError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}

	// Look up the org to return its full details (name, id).
	orgResp := authOrgResponse{Slug: claims.OrgSlug}
	if org, err := pa.pgStore.Organizations().GetBySlug(r.Context(), claims.OrgSlug); err == nil && org != nil {
		orgResp.ID = org.ID
		orgResp.Name = org.Name
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"user": authUserResponse{
			ID:           claims.UserID,
			Email:        claims.Email,
			DisplayName:  claims.DisplayName,
			Role:         claims.Role,
			PlatformRole: claims.PlatformRole,
		},
		"org":  orgResp,
		"team": claims.DefaultTeamSlug,
	})
}

// --- Handler: GET /api/auth/setup-status ---
// Returns whether the platform has been set up (has at least one org).
// Used by the frontend to decide whether to show register or login.

func (pa *PlatformAuth) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if pa.IsNoAuth() {
		respondJSON(w, http.StatusOK, map[string]any{
			"initialized":        true,
			"allow_registration": false,
			"auth_mode":          config.AuthModeNone,
		})
		return
	}
	ctx := r.Context()
	count, err := pa.pgStore.Organizations().Count(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to check setup status")
		return
	}

	resp := map[string]any{
		"initialized":        count > 0,
		"allow_registration": pa.isRegistrationEffectivelyAllowed(ctx),
		"auth_mode":          pa.authCfg.EffectiveAuthMode(pa.storeCfg.Backend),
	}

	respondJSON(w, http.StatusOK, resp)
}

func (pa *PlatformAuth) respondSingleUser(w http.ResponseWriter, r *http.Request) {
	claims, err := pa.singleUserClaims(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve local user")
		return
	}
	org, err := pa.pgStore.Organizations().GetBySlug(r.Context(), claims.OrgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve local organization")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"user": authUserResponse{
			ID:           claims.UserID,
			Email:        claims.Email,
			DisplayName:  claims.DisplayName,
			Role:         claims.Role,
			PlatformRole: claims.PlatformRole,
		},
		"org":  authOrgResponse{ID: org.ID, Name: org.Name, Slug: org.Slug},
		"team": claims.DefaultTeamSlug,
	})
}

func (pa *PlatformAuth) singleUserClaims(ctx context.Context) (*PlatformClaims, error) {
	identity := pa.authCfg.SingleUserEmail
	if identity == "" {
		identity = config.SingleUserEmail
	}
	user, err := pa.pgStore.Users().GetByEmail(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("get single user %q: %w", identity, err)
	}
	if user == nil {
		return nil, fmt.Errorf("get single user %q: user not found", identity)
	}
	if user.Status != "active" {
		return nil, fmt.Errorf("get single user %q: user is not active", identity)
	}
	memberships, err := pa.pgStore.Organizations().GetUserOrgs(ctx, user.ID)
	if err != nil || len(memberships) == 0 {
		return nil, fmt.Errorf("get single user organization: %w", err)
	}
	membership := memberships[0]
	orgStore, err := pa.orgResolver.ForOrg(membership.OrgSlug)
	if err != nil {
		return nil, fmt.Errorf("get local organization store: %w", err)
	}
	teams, err := orgStore.Teams().ListTeamsForUser(ctx, user.ID)
	if err != nil || len(teams) == 0 {
		return nil, fmt.Errorf("get local user team: %w", err)
	}
	return &PlatformClaims{
		UserID:          user.ID,
		Email:           user.Email,
		DisplayName:     user.DisplayName,
		OrgSlug:         membership.OrgSlug,
		Role:            membership.Role,
		PlatformRole:    user.PlatformRole,
		DefaultTeamSlug: teams[0].Slug,
	}, nil
}

// --- Effective auth policy (DB override > YAML config) ---

// isRegistrationEffectivelyAllowed checks platform_settings.auth.allow_registration first,
// falling back to the YAML config value. This allows superadmins to toggle registration
// at runtime from the Platform Admin UI without restarting the daemon.
// The first-user bootstrap flow is always allowed regardless of this setting.
func (pa *PlatformAuth) isRegistrationEffectivelyAllowed(ctx context.Context) bool {
	settings, err := pa.pgStore.PlatformSettings().Get(ctx)
	if err == nil && settings != nil && settings.Auth != nil && settings.Auth.AllowRegistration != nil {
		return *settings.Auth.AllowRegistration
	}
	return pa.authCfg.IsRegistrationAllowed()
}

// isEmailVerificationEffectivelyRequired checks platform_settings.auth.require_email_verification
// first, falling back to the YAML config value.
func (pa *PlatformAuth) isEmailVerificationEffectivelyRequired(ctx context.Context) bool {
	settings, err := pa.pgStore.PlatformSettings().Get(ctx)
	if err == nil && settings != nil && settings.Auth != nil && settings.Auth.RequireEmailVerification != nil {
		return *settings.Auth.RequireEmailVerification
	}
	return pa.authCfg.IsEmailVerificationRequired()
}

// --- Internal helpers ---

// provisionFirstOrg creates the default org, team, and makes the user an owner.
func (pa *PlatformAuth) provisionFirstOrg(ctx context.Context, user *store.User) (*store.Organization, string, error) {
	orgSlug := pa.authCfg.GetDefaultOrgSlug()
	orgName := pa.authCfg.GetDefaultOrgName()
	dbName := config.OrgDBName(pa.pgStore.InstanceSuffix(), orgSlug)

	org := &store.Organization{
		ID:        uuid.New().String(),
		Name:      orgName,
		Slug:      orgSlug,
		DBName:    dbName,
		Status:    "active",
		CreatedAt: time.Now(),
	}

	// Create org record in platform DB
	if err := pa.pgStore.Organizations().Create(ctx, org); err != nil {
		return nil, "", fmt.Errorf("failed to create org: %w", err)
	}

	// Provision org database (CREATE DATABASE + run migrations)
	if err := pa.pgStore.ProvisionOrg(ctx, org.ID, orgSlug); err != nil {
		return nil, "", fmt.Errorf("failed to provision org database: %w", err)
	}

	// Make user the org owner
	role := "owner"
	if err := pa.pgStore.Organizations().AddMember(ctx, user.ID, org.ID, role); err != nil {
		return nil, "", fmt.Errorf("failed to add owner to org: %w", err)
	}

	// First user is automatically promoted to platform superadmin
	if err := pa.pgStore.Users().SetPlatformRole(ctx, user.ID, "superadmin"); err != nil {
		slog.Warn("failed to set platform superadmin role on first user", "error", err)
	} else {
		user.PlatformRole = "superadmin"
	}

	// Create default team within the org
	orgDataStore, err := pa.pgStore.ForOrg(orgSlug)
	if err != nil {
		slog.Warn("failed to get org data store for default team creation", "error", err)
		return org, role, nil
	}

	defaultTeam := &store.Team{
		ID:         uuid.New().String(),
		Name:       "General",
		Slug:       "general",
		SchemaName: "team_general",
		CreatedAt:  time.Now(),
	}
	if err := orgDataStore.Teams().CreateTeam(ctx, defaultTeam); err != nil {
		slog.Warn("failed to create default team", "error", err)
		return org, role, nil
	}

	// Provision team schema
	if err := orgDataStore.ProvisionTeam(ctx, "general"); err != nil {
		slog.Warn("failed to provision default team schema", "error", err)
		return org, role, nil
	}

	// Add user to default team as admin
	if err := orgDataStore.Teams().AddMember(ctx, &store.TeamMembership{
		UserID:   user.ID,
		TeamID:   defaultTeam.ID,
		Role:     "admin",
		JoinedAt: time.Now(),
	}); err != nil {
		slog.Warn("failed to add user to default team", "error", err)
	}

	// Provision personal schema for the user
	if err := orgDataStore.ProvisionPersonalSchema(ctx, user.ID); err != nil {
		slog.Warn("failed to provision personal schema", "error", err)
	}

	slog.Info("first-user setup complete",
		"user", user.Email,
		"org", orgSlug,
		"team", "general",
	)

	return org, role, nil
}

// resolveDefaultTeam finds the user's default team slug within an org.
func (pa *PlatformAuth) resolveDefaultTeam(ctx context.Context, userID string, org *store.Organization) string {
	orgDataStore, err := pa.pgStore.ForOrg(org.Slug)
	if err != nil {
		return "general"
	}

	memberships, err := orgDataStore.Teams().GetUserTeams(ctx, userID)
	if err != nil || len(memberships) == 0 {
		return "general"
	}

	// Find the team slug for the first membership
	team, err := orgDataStore.Teams().GetTeam(ctx, memberships[0].TeamID)
	if err != nil || team == nil {
		return "general"
	}
	return team.Slug
}

// validateTeamMembership checks if the user is a member of the specified team in the org.
func (pa *PlatformAuth) validateTeamMembership(ctx context.Context, userID string, org *store.Organization, teamSlug string) bool {
	orgDataStore, err := pa.pgStore.ForOrg(org.Slug)
	if err != nil {
		return false
	}

	memberships, err := orgDataStore.Teams().GetUserTeams(ctx, userID)
	if err != nil {
		return false
	}

	for _, m := range memberships {
		team, err := orgDataStore.Teams().GetTeam(ctx, m.TeamID)
		if err != nil || team == nil {
			continue
		}
		if team.Slug == teamSlug {
			return true
		}
	}
	return false
}

// listUserTeams returns the teams the user belongs to in the given org.
func (pa *PlatformAuth) listUserTeams(ctx context.Context, userID string, org *store.Organization) []authTeamOption {
	orgDataStore, err := pa.pgStore.ForOrg(org.Slug)
	if err != nil {
		return nil
	}

	memberships, err := orgDataStore.Teams().GetUserTeams(ctx, userID)
	if err != nil || len(memberships) == 0 {
		return nil
	}

	var teams []authTeamOption
	for _, m := range memberships {
		team, err := orgDataStore.Teams().GetTeam(ctx, m.TeamID)
		if err != nil || team == nil {
			continue
		}
		teams = append(teams, authTeamOption{
			ID:   team.ID,
			Name: team.Name,
			Slug: team.Slug,
		})
	}
	return teams
}

// issueTokensAndRespondWithContext creates JWT tokens scoped to a specific team,
// includes available orgs/teams in the response for CLI clients.
func (pa *PlatformAuth) issueTokensAndRespondWithContext(w http.ResponseWriter, r *http.Request, user *store.User, org *store.Organization, role, teamSlug string, availableOrgs []authOrgOption, availableTeams []authTeamOption, clientType string) {
	ctx := r.Context()

	// Issue access token
	accessToken, err := pa.jwt.IssueAccessToken(
		user.ID, user.Email, user.DisplayName,
		org.Slug, teamSlug, role, user.PlatformRole,
	)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to issue access token")
		return
	}

	// Issue refresh token
	refreshToken, err := pa.jwt.IssueRefreshToken(user.ID, org.Slug, teamSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to issue refresh token")
		return
	}

	// Store refresh token hash in login_sessions for revocation support
	tokenHash := hashRefreshToken(refreshToken)
	loginSession := &store.LoginSession{
		TokenHash: tokenHash,
		UserID:    user.ID,
		OrgID:     org.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(pa.jwt.RefreshTokenTTL()),
		UserAgent: r.UserAgent(),
		IPAddress: clientIP(r),
	}
	if err := pa.pgStore.LoginSessions().Create(ctx, loginSession); err != nil {
		slog.Error("failed to persist login session during login", "error", err, "user_id", user.ID)
		respondError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	// Set cookies
	setAccessTokenCookie(w, r, accessToken, pa.jwt.AccessTokenTTL())
	setRefreshTokenCookie(w, r, refreshToken, pa.jwt.RefreshTokenTTL())

	resp := authResponse{
		User: authUserResponse{
			ID:           user.ID,
			Email:        user.Email,
			DisplayName:  user.DisplayName,
			Role:         role,
			PlatformRole: user.PlatformRole,
		},
		Org: authOrgResponse{
			ID:   org.ID,
			Name: org.Name,
			Slug: org.Slug,
		},
		TeamSlug:       teamSlug,
		ExpiresIn:      int(pa.jwt.AccessTokenTTL().Seconds()),
		AvailableOrgs:  availableOrgs,
		AvailableTeams: availableTeams,
	}

	// Include tokens in body for CLI clients (they can't use cookies)
	if clientType == "cli" {
		resp.AccessToken = accessToken
		resp.RefreshToken = refreshToken
	}

	respondJSON(w, http.StatusOK, resp)
}

// issueTokensAndRespond creates JWT tokens, sets cookies, and sends the auth response.
// When clientType is "cli", tokens are included in the response body (for CLI clients
// that cannot use cookies).
func (pa *PlatformAuth) issueTokensAndRespond(w http.ResponseWriter, r *http.Request, user *store.User, org *store.Organization, role string, clientType ...string) {
	ctx := r.Context()
	teamSlug := pa.resolveDefaultTeam(ctx, user.ID, org)

	// Issue access token
	accessToken, err := pa.jwt.IssueAccessToken(
		user.ID, user.Email, user.DisplayName,
		org.Slug, teamSlug, role, user.PlatformRole,
	)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to issue access token")
		return
	}

	// Issue refresh token
	refreshToken, err := pa.jwt.IssueRefreshToken(user.ID, org.Slug, teamSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to issue refresh token")
		return
	}

	// Store refresh token hash in login_sessions for revocation support
	tokenHash := hashRefreshToken(refreshToken)
	loginSession := &store.LoginSession{
		TokenHash: tokenHash,
		UserID:    user.ID,
		OrgID:     org.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(pa.jwt.RefreshTokenTTL()),
		UserAgent: r.UserAgent(),
		IPAddress: clientIP(r),
	}
	if err := pa.pgStore.LoginSessions().Create(ctx, loginSession); err != nil {
		slog.Error("failed to persist login session during register", "error", err, "user_id", user.ID)
		respondError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	// Set cookies
	setAccessTokenCookie(w, r, accessToken, pa.jwt.AccessTokenTTL())
	setRefreshTokenCookie(w, r, refreshToken, pa.jwt.RefreshTokenTTL())

	resp := authResponse{
		User: authUserResponse{
			ID:           user.ID,
			Email:        user.Email,
			DisplayName:  user.DisplayName,
			Role:         role,
			PlatformRole: user.PlatformRole,
		},
		Org: authOrgResponse{
			ID:   org.ID,
			Name: org.Name,
			Slug: org.Slug,
		},
		ExpiresIn: int(pa.jwt.AccessTokenTTL().Seconds()),
	}

	// Include tokens in body for CLI clients (they can't use cookies)
	isCLI := len(clientType) > 0 && clientType[0] == "cli"
	if isCLI {
		resp.AccessToken = accessToken
		resp.RefreshToken = refreshToken
	}

	respondJSON(w, http.StatusOK, resp)
}

// --- Cookie helpers ---

const (
	accessCookieName  = "astonish_access"
	refreshCookieName = "astonish_refresh"
)

func setAccessTokenCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isSecureRequest(r),
	})
}

func setRefreshTokenCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     "/api/auth/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isSecureRequest(r),
	})
}

// isSecureRequest returns true if the request was made over HTTPS,
// either directly (r.TLS != nil) or via a reverse proxy (X-Forwarded-Proto).
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

func clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     "/api/auth/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// --- Handler: POST /api/auth/verify-email ---

type verifyEmailRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func (pa *PlatformAuth) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req verifyEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Code == "" {
		respondError(w, http.StatusBadRequest, "email and code are required")
		return
	}

	ctx := r.Context()

	// Find the user
	user, err := pa.pgStore.Users().GetByEmail(ctx, strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil || user == nil {
		respondError(w, http.StatusNotFound, "user not found")
		return
	}

	if user.Status != "pending_verification" {
		respondError(w, http.StatusBadRequest, "account is not pending verification")
		return
	}

	if pa.linkCodes == nil {
		respondError(w, http.StatusInternalServerError, "verification service unavailable")
		return
	}

	// Consume the code (Consume already checks expiry)
	linkCode, err := pa.linkCodes.Consume(ctx, strings.TrimSpace(req.Code))
	if err != nil || linkCode == nil {
		respondError(w, http.StatusBadRequest, "invalid or expired verification code")
		return
	}

	// Validate the code belongs to this user and is for registration
	if linkCode.UserID != user.ID || linkCode.Channel != "registration" {
		respondError(w, http.StatusBadRequest, "invalid or expired verification code")
		return
	}

	// Promote user status to active
	user.Status = "active"
	if err := pa.pgStore.Users().Update(ctx, user); err != nil {
		slog.Error("failed to activate user after verification", "user", user.ID, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to activate account")
		return
	}

	slog.Info("user email verified", "user", user.Email)

	// Send platform welcome email now that the account is active.
	mailer.SendAsync(ctx, mailer.Welcome{
		Recipient:      user.Email,
		DisplayName:    user.DisplayName,
		AppURL:         resolveAppURL(r),
		DevEnvironment: isDevEnvironment(ctx, pa.pgStore.PlatformSettings()),
	})

	respondJSON(w, http.StatusOK, map[string]any{
		"verified": true,
		"email":    user.Email,
		"message":  "Email verified successfully. A team administrator must add you to a team before you can access resources.",
	})
}

// --- Handler: POST /api/auth/resend-verification ---

type resendVerificationRequest struct {
	Email string `json:"email"`
}

func (pa *PlatformAuth) handleResendVerification(w http.ResponseWriter, r *http.Request) {
	var req resendVerificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" {
		respondError(w, http.StatusBadRequest, "email is required")
		return
	}

	ctx := r.Context()

	// Use a generic success message to avoid revealing whether the email exists
	successMsg := "If an account with that email exists and is pending verification, a new code has been sent."

	user, err := pa.pgStore.Users().GetByEmail(ctx, strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil || user == nil {
		respondJSON(w, http.StatusOK, map[string]any{"message": successMsg})
		return
	}

	if user.Status != "pending_verification" {
		respondJSON(w, http.StatusOK, map[string]any{"message": successMsg})
		return
	}

	if err := pa.sendRegistrationVerificationCode(ctx, user); err != nil {
		slog.Error("failed to resend verification code", "email", user.Email, "error", err)
	}

	respondJSON(w, http.StatusOK, map[string]any{"message": successMsg})
}

// --- Email verification helpers ---

const registrationCodeTTL = 10 * time.Minute

// sendRegistrationVerificationCode generates a 6-digit code, stores it, and emails it.
func (pa *PlatformAuth) sendRegistrationVerificationCode(ctx context.Context, user *store.User) error {
	if pa.linkCodes == nil {
		return fmt.Errorf("link code store not configured")
	}

	code := generateRegistrationCode()

	// Store code with "registration" channel and 10-minute TTL
	if err := pa.linkCodes.GenerateWithTTL(ctx, code, user.ID, user.Email, "registration", registrationCodeTTL); err != nil {
		return fmt.Errorf("failed to store verification code: %w", err)
	}

	// Send email
	if !mailer.IsConfigured() {
		slog.Warn("mailer not configured; verification code cannot be sent",
			"email", user.Email, "code", code)
		return fmt.Errorf("mailer not configured")
	}

	mailer.SendAsync(ctx, mailer.RegistrationVerification{
		Recipient:      user.Email,
		DisplayName:    user.DisplayName,
		Code:           code,
		ExpiryMin:      10,
		DevEnvironment: isDevEnvironment(ctx, pa.pgStore.PlatformSettings()),
	})
	return nil
}

// generateRegistrationCode creates a 6-digit numeric code.
func generateRegistrationCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(999999))
	return fmt.Sprintf("%06d", n.Int64())
}

// --- Validation helpers ---

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func validateEmail(email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return errors.New("email is required")
	}
	if !emailRegex.MatchString(email) {
		return errors.New("invalid email format")
	}
	if len(email) > 254 {
		return errors.New("email too long")
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(password) > 128 {
		return errors.New("password too long")
	}
	return nil
}

func hashRefreshToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func clientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return strings.TrimPrefix(strings.TrimSuffix(ip, "]"), "[")
}
