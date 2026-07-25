package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// Product default brand pack for fresh installs and unresolved cascade.
const defaultBrandTheme = "aster"

// shippedBrandThemes is the server allowlist (must stay in sync with web brandTheme.ts shipped packs).
var shippedBrandThemes = map[string]bool{
	"nova":  true,
	"aster": true,
}

// NormalizeBrandTheme returns theme if shipped, otherwise empty string.
func NormalizeBrandTheme(theme string) string {
	t := strings.TrimSpace(strings.ToLower(theme))
	if shippedBrandThemes[t] {
		return t
	}
	return ""
}

// ResolveBrandTheme cascades user → platform → product default (aster).
func ResolveBrandTheme(userTheme, platformDefault string) string {
	if t := NormalizeBrandTheme(userTheme); t != "" {
		return t
	}
	if t := NormalizeBrandTheme(platformDefault); t != "" {
		return t
	}
	return defaultBrandTheme
}

func platformDefaultBrandTheme(ctxSettings *store.PlatformSettings) string {
	if ctxSettings == nil {
		return ""
	}
	return NormalizeBrandTheme(ctxSettings.DefaultBrandTheme)
}

// GetPublicBrandThemeHandler handles GET /api/brand-theme (auth-exempt).
// Returns the platform default (or product default) for login / pre-auth paint.
func GetPublicBrandThemeHandler(w http.ResponseWriter, r *http.Request) {
	platformDefault := ""
	if backend := getPlatformBackend(); backend != nil {
		if settings, err := backend.PlatformSettings().Get(r.Context()); err == nil {
			platformDefault = platformDefaultBrandTheme(settings)
		}
	}
	theme := ResolveBrandTheme("", platformDefault)
	source := "default"
	if platformDefault != "" && theme == platformDefault {
		source = "platform"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"brand_theme": theme,
		"source":      source,
	})
}

type userBrandThemePayload struct {
	// BrandTheme is the user's preferred pack, or empty to inherit platform default.
	BrandTheme string `json:"brand_theme"`
	// Effective is the resolved pack after cascade (GET only).
	Effective string `json:"effective,omitempty"`
	// Source is "user", "platform", or "default" (GET only).
	Source string `json:"source,omitempty"`
	// PlatformDefault is the instance default (GET only).
	PlatformDefault string `json:"platform_default,omitempty"`
}

// GetUserBrandThemeHandler handles GET /api/user-settings/brand-theme.
func GetUserBrandThemeHandler(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil || svc.PersonalSettings == nil {
		http.Error(w, "personal settings store unavailable", http.StatusServiceUnavailable)
		return
	}

	settings, err := svc.PersonalSettings.Get(r.Context())
	if err != nil {
		http.Error(w, "failed to load user settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	platformDefault := ""
	if backend := getPlatformBackend(); backend != nil {
		if ps, err := backend.PlatformSettings().Get(r.Context()); err == nil {
			platformDefault = platformDefaultBrandTheme(ps)
		}
	}

	userPref := NormalizeBrandTheme(settings.BrandTheme)
	effective := ResolveBrandTheme(settings.BrandTheme, platformDefault)
	source := "default"
	if userPref != "" && effective == userPref {
		source = "user"
	} else if platformDefault != "" && effective == platformDefault {
		source = "platform"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userBrandThemePayload{
		BrandTheme:      settings.BrandTheme, // raw stored (may be empty = inherit)
		Effective:       effective,
		Source:          source,
		PlatformDefault: platformDefault,
	})
}

// PatchUserBrandThemeHandler handles PATCH /api/user-settings/brand-theme.
// Empty brand_theme clears the preference (inherit platform).
func PatchUserBrandThemeHandler(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil || svc.PersonalSettings == nil {
		http.Error(w, "personal settings store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body userBrandThemePayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Allow empty (inherit) or a shipped pack.
	next := strings.TrimSpace(body.BrandTheme)
	if next != "" {
		if NormalizeBrandTheme(next) == "" {
			http.Error(w, "unknown or unshipped brand theme", http.StatusBadRequest)
			return
		}
		next = NormalizeBrandTheme(next)
	}

	existing, err := svc.PersonalSettings.Get(r.Context())
	if err != nil {
		http.Error(w, "failed to load user settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	existing.BrandTheme = next
	if err := svc.PersonalSettings.Save(r.Context(), existing); err != nil {
		http.Error(w, "failed to save user settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	platformDefault := ""
	if backend := getPlatformBackend(); backend != nil {
		if ps, err := backend.PlatformSettings().Get(r.Context()); err == nil {
			platformDefault = platformDefaultBrandTheme(ps)
		}
	}
	effective := ResolveBrandTheme(next, platformDefault)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userBrandThemePayload{
		BrandTheme:      next,
		Effective:       effective,
		PlatformDefault: platformDefault,
	})
}

type platformBrandThemePayload struct {
	DefaultBrandTheme string `json:"default_brand_theme"`
}

// GetPlatformBrandThemeHandler handles GET /api/platform/admin/brand-theme (superadmin).
func GetPlatformBrandThemeHandler(w http.ResponseWriter, r *http.Request) {
	_, backend := platformAdminGuard(w, r)
	if backend == nil {
		return
	}
	settings, err := backend.PlatformSettings().Get(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load platform settings")
		return
	}
	theme := platformDefaultBrandTheme(settings)
	if theme == "" {
		theme = defaultBrandTheme
	}
	respondJSON(w, http.StatusOK, platformBrandThemePayload{DefaultBrandTheme: theme})
}

// PatchPlatformBrandThemeHandler handles PATCH /api/platform/admin/brand-theme (superadmin).
func PatchPlatformBrandThemeHandler(w http.ResponseWriter, r *http.Request) {
	_, backend := platformAdminGuard(w, r)
	if backend == nil {
		return
	}

	var body platformBrandThemePayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	next := NormalizeBrandTheme(body.DefaultBrandTheme)
	if next == "" && strings.TrimSpace(body.DefaultBrandTheme) != "" {
		respondError(w, http.StatusBadRequest, "unknown or unshipped brand theme")
		return
	}
	// Empty body → product default aster stored explicitly for clarity.
	if next == "" {
		next = defaultBrandTheme
	}

	ctx := r.Context()
	settings, err := backend.PlatformSettings().Get(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load platform settings")
		return
	}
	settings.DefaultBrandTheme = next
	if err := backend.PlatformSettings().Save(ctx, settings); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save platform settings")
		return
	}
	respondJSON(w, http.StatusOK, platformBrandThemePayload{DefaultBrandTheme: next})
}
