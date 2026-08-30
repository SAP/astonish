package api

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/store"
)

// PlatformAdminCacheDiagnosticsHandler handles
// GET /api/platform/admin/sessions/{id}/cache-diagnostics.
func PlatformAdminCacheDiagnosticsHandler(w http.ResponseWriter, r *http.Request) {
	_, backend := platformAdminGuard(w, r)
	if backend == nil {
		return
	}

	orgSlug := r.URL.Query().Get("org")
	scope := r.URL.Query().Get("scope")
	userID := r.URL.Query().Get("user")
	teamSlug := r.URL.Query().Get("team")
	if orgSlug == "" || (scope != "personal" && scope != "team") {
		respondError(w, http.StatusBadRequest, "org and scope=personal|team are required")
		return
	}
	if scope == "personal" && userID == "" {
		respondError(w, http.StatusBadRequest, "user is required for personal scope")
		return
	}
	if scope == "team" && teamSlug == "" {
		respondError(w, http.StatusBadRequest, "team is required for team scope")
		return
	}

	org, err := backend.Organizations().GetBySlug(r.Context(), orgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve organization")
		return
	}
	if org == nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}
	orgStore, err := backend.ForOrg(orgSlug)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve organization store")
		return
	}

	var sessions store.SessionStore
	if scope == "personal" {
		user, err := backend.Users().GetByID(r.Context(), userID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to resolve user")
			return
		}
		if user == nil {
			respondError(w, http.StatusNotFound, "session not found")
			return
		}
		memberRole, err := backend.Organizations().GetMemberRole(r.Context(), userID, org.ID)
		if err != nil || memberRole == "" {
			respondError(w, http.StatusNotFound, "session not found")
			return
		}
		sessions = orgStore.ForUser(userID).Sessions()
	} else {
		team, err := orgStore.Teams().GetTeamBySlug(r.Context(), teamSlug)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to resolve team")
			return
		}
		if team == nil {
			respondError(w, http.StatusNotFound, "session not found")
			return
		}
		sessions = orgStore.ForTeam(teamSlug).Sessions()
	}
	if sessions == nil {
		respondError(w, http.StatusInternalServerError, "session store not available")
		return
	}

	sessionID := mux.Vars(r)["id"]
	meta, err := sessions.GetSessionMeta(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to resolve session")
		return
	}
	if meta == nil || (scope == "personal" && meta.UserID != userID) {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	diagnostics, err := sessions.ListCacheDiagnostics(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load cache diagnostics")
		return
	}
	if diagnostics == nil {
		diagnostics = []store.CacheDiagnostic{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessionId":   sessionID,
		"scope":       scope,
		"diagnostics": diagnostics,
	})
}
