package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
)

// platformAuthSettingsResponse is returned by GET /api/platform/admin/auth-settings.
// It represents the effective auth policy (DB override merged with config defaults).
type platformAuthSettingsResponse struct {
	AllowRegistration        bool   `json:"allow_registration"`
	RequireEmailVerification bool   `json:"require_email_verification"`
	DevEnvironment           bool   `json:"dev_environment"`
	AuthMode                 string `json:"auth_mode"`
	SingleUserEmail          string `json:"single_user_email,omitempty"`
}

// platformAuthSettingsRequest is accepted by PUT /api/platform/admin/auth-settings.
type platformAuthSettingsRequest struct {
	AllowRegistration        *bool  `json:"allow_registration"`
	RequireEmailVerification *bool  `json:"require_email_verification"`
	DevEnvironment           *bool  `json:"dev_environment"`
	AuthMode                 string `json:"auth_mode,omitempty"`
	SingleUserEmail          string `json:"single_user_email,omitempty"`
}

// PlatformAdminGetAuthSettingsHandler handles GET /api/platform/admin/auth-settings.
// Returns the effective auth settings (DB values merged with YAML defaults).
// Requires superadmin.
func PlatformAdminGetAuthSettingsHandler(w http.ResponseWriter, r *http.Request) {
	_, backend := platformAdminGuard(w, r)
	if backend == nil {
		return
	}

	settings, err := backend.PlatformSettings().Get(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load platform settings")
		return
	}

	// Build effective values: DB override > YAML config default.
	pa := getPlatformAuth()
	resp := platformAuthSettingsResponse{
		AllowRegistration:        effectiveAllowRegistration(settings, pa),
		RequireEmailVerification: effectiveRequireEmailVerification(settings, pa),
		DevEnvironment:           effectiveDevEnvironment(settings),
	}
	if pa != nil {
		resp.AuthMode = pa.authCfg.EffectiveAuthMode(pa.storeCfg.Backend)
		resp.SingleUserEmail = pa.authCfg.SingleUserEmail
		if resp.SingleUserEmail == "" {
			resp.SingleUserEmail = config.SingleUserEmail
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

// PlatformAdminSaveAuthSettingsHandler handles PUT /api/platform/admin/auth-settings.
// Persists auth policy settings into the platform_settings table.
// Requires superadmin.
func PlatformAdminSaveAuthSettingsHandler(w http.ResponseWriter, r *http.Request) {
	_, backend := platformAdminGuard(w, r)
	if backend == nil {
		return
	}

	pa := getPlatformAuth()
	if pa == nil {
		respondError(w, http.StatusInternalServerError, "platform auth is unavailable")
		return
	}
	backendName := pa.storeCfg.Backend

	var req platformAuthSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()
	var pendingConfig *config.AppConfig

	// Load existing platform settings (preserves providers, channels, etc.).
	settings, err := backend.PlatformSettings().Get(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load platform settings")
		return
	}

	// Initialize auth sub-struct if nil.
	if settings.Auth == nil {
		settings.Auth = &store.PlatformAuthSettings{}
	}

	// Apply requested changes.
	if req.AllowRegistration != nil {
		settings.Auth.AllowRegistration = req.AllowRegistration
	}
	if req.RequireEmailVerification != nil {
		settings.Auth.RequireEmailVerification = req.RequireEmailVerification
	}
	if req.DevEnvironment != nil {
		settings.Auth.DevEnvironment = req.DevEnvironment
	}

	if req.AuthMode != "" || req.SingleUserEmail != "" {
		if req.AuthMode == "" {
			req.AuthMode = config.AuthModeNone
		}
		if err := config.ValidateAuthMode(backendName, req.AuthMode); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.AuthMode == config.AuthModeNone && req.SingleUserEmail == "" {
			respondError(w, http.StatusBadRequest, "single_user_email is required when enabling no-login mode")
			return
		}
		cfg, err := config.LoadAppConfig()
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to load app config")
			return
		}
		cfg.Storage.Auth.Mode = req.AuthMode
		if req.AuthMode == config.AuthModeNone {
			user, err := backend.Users().GetByEmail(ctx, req.SingleUserEmail)
			if err != nil || user == nil {
				respondError(w, http.StatusBadRequest, "selected single user was not found")
				return
			}
			if user.Status != "active" {
				respondError(w, http.StatusBadRequest, "selected single user is not active")
				return
			}
			if user.PlatformRole != "superadmin" {
				respondError(w, http.StatusBadRequest, "no-login mode requires a superadmin user")
				return
			}
			cfg.Storage.Auth.SingleUserEmail = user.Email
		} else {
			cfg.Storage.Auth.SingleUserEmail = ""
		}
		pendingConfig = cfg
	}

	if err := backend.PlatformSettings().Save(ctx, settings); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save auth settings")
		return
	}
	if pendingConfig != nil {
		if err := config.SaveAppConfig(pendingConfig); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to save auth mode")
			return
		}
		// The running server intentionally keeps its current auth policy. The
		// listener binding is fixed at startup, so applying only part of a mode
		// change here would create a misleading half-applied state. Restarting
		// Astonish loads the persisted policy and applies it atomically.
	}

	// Return the persisted mode when it was changed. The running process keeps
	// serving with its current policy until restart.
	resp := platformAuthSettingsResponse{
		AllowRegistration:        effectiveAllowRegistration(settings, pa),
		RequireEmailVerification: effectiveRequireEmailVerification(settings, pa),
		DevEnvironment:           effectiveDevEnvironment(settings),
	}
	responseAuth := pa.authCfg
	if pendingConfig != nil {
		responseAuth = pendingConfig.Storage.Auth
	}
	resp.AuthMode = responseAuth.EffectiveAuthMode(pa.storeCfg.Backend)
	resp.SingleUserEmail = responseAuth.SingleUserEmail
	if resp.SingleUserEmail == "" {
		resp.SingleUserEmail = config.SingleUserEmail
	}
	respondJSON(w, http.StatusOK, resp)
}

// effectiveAllowRegistration resolves the allow_registration value:
// DB override (if non-nil) > YAML config > true (default).
func effectiveAllowRegistration(settings *store.PlatformSettings, pa *PlatformAuth) bool {
	if settings != nil && settings.Auth != nil && settings.Auth.AllowRegistration != nil {
		return *settings.Auth.AllowRegistration
	}
	if pa != nil {
		return pa.authCfg.IsRegistrationAllowed()
	}
	return true
}

// effectiveRequireEmailVerification resolves the require_email_verification value:
// DB override (if non-nil) > YAML config > true (default).
func effectiveRequireEmailVerification(settings *store.PlatformSettings, pa *PlatformAuth) bool {
	if settings != nil && settings.Auth != nil && settings.Auth.RequireEmailVerification != nil {
		return *settings.Auth.RequireEmailVerification
	}
	if pa != nil {
		return pa.authCfg.IsEmailVerificationRequired()
	}
	return true
}

// effectiveDevEnvironment resolves the dev_environment value:
// DB override (if non-nil) > false (default, production assumed).
func effectiveDevEnvironment(settings *store.PlatformSettings) bool {
	if settings != nil && settings.Auth != nil && settings.Auth.DevEnvironment != nil {
		return *settings.Auth.DevEnvironment
	}
	return false
}

// isDevEnvironment loads platform settings and returns the effective
// DevEnvironment flag. Returns false on any error (fail-safe to production).
// Use this helper at email send sites to populate mailer struct fields.
func isDevEnvironment(ctx context.Context, ps store.PlatformSettingsStore) bool {
	if ps == nil {
		return false
	}
	settings, err := ps.Get(ctx)
	if err != nil {
		return false
	}
	return effectiveDevEnvironment(settings)
}
