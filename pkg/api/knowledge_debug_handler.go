package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/store"
)

// StudioKnowledgeDebugHandler returns knowledge and tool injection debug data
// for a specific invocation within a session.
//
// GET /api/studio/sessions/{id}/knowledge-debug?invocationId=X
//
// The knowledge/tool tracking events are emitted as system events (with empty
// InvocationID) whose StateDelta contains `_knowledge_injection` or
// `_tool_injection` keys. They appear in the event stream immediately before
// the first event carrying the target invocationId. This handler scans the
// transcript, accumulating the latest tracking events, and captures them when
// the target invocation is first encountered.
func StudioKnowledgeDebugHandler(w http.ResponseWriter, r *http.Request) {
	if !IsPlatformAdmin(GetPlatformUser(r)) {
		respondError(w, http.StatusForbidden, "platform superadmin access required")
		return
	}
	sessionID := mux.Vars(r)["id"]
	invocationID := strings.TrimSpace(r.URL.Query().Get("invocationId"))
	if invocationID == "" {
		respondError(w, http.StatusBadRequest, "invocationId is required")
		return
	}
	svc := store.FromRequest(r)
	if svc == nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}
	sessions := resolveSessionStore(svc, sessionID)
	if sessions == nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	userID := effectiveUserID(r)
	events, err := sessions.ReadTranscriptEvents(r.Context(), studioChatAppName, userID, sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read session events")
		return
	}

	// Scan events to find knowledge/tool injection data for the target invocation.
	//
	// Tracking events carry _knowledge_injection / _tool_injection in StateDelta.
	// They may have the same InvocationID as the model response (when yielded from
	// the same Run call) or an empty InvocationID (file-based transcript). The scan
	// accumulates tracking data from ALL events and captures when the invocation
	// boundary is crossed OR when tracking data is found on the invocation itself.
	var knowledgeData map[string]any
	var toolData map[string]any
	var lastKnowledge map[string]any
	var lastTool map[string]any
	found := false

	for _, evt := range events {
		if evt.Actions.StateDelta != nil {
			if ki, ok := evt.Actions.StateDelta["_knowledge_injection"]; ok {
				if m, ok := ki.(map[string]any); ok {
					lastKnowledge = m
				}
			}
			if ti, ok := evt.Actions.StateDelta["_tool_injection"]; ok {
				if m, ok := ti.(map[string]any); ok {
					lastTool = m
				}
			}
		}
		// When we hit the target invocation, capture the accumulated tracking data.
		// Continue scanning (don't break) so we also pick up tracking events that
		// share the same invocationId as the model response events.
		if evt.InvocationID == invocationID && !found {
			knowledgeData = lastKnowledge
			toolData = lastTool
			found = true
		}
	}

	// If we found the invocation but knowledgeData is still nil, the tracking
	// event may have been AFTER the first matching invocation event (same ID).
	// Use whatever was accumulated during the full scan.
	if found && knowledgeData == nil && lastKnowledge != nil {
		knowledgeData = lastKnowledge
	}
	if found && toolData == nil && lastTool != nil {
		toolData = lastTool
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessionId":    sessionID,
		"invocationId": invocationID,
		"knowledge":    knowledgeData,
		"tools":        toolData,
	})
}

// PlatformAdminKnowledgeDebugHandler handles
// GET /api/platform/admin/sessions/{id}/knowledge-debug.
// It mirrors the studio handler but uses the platform admin session resolution
// path (org + scope + user/team), consistent with PlatformAdminCacheDiagnosticsHandler.
func PlatformAdminKnowledgeDebugHandler(w http.ResponseWriter, r *http.Request) {
	_, backend := platformAdminGuard(w, r)
	if backend == nil {
		return
	}

	orgSlug := r.URL.Query().Get("org")
	scope := r.URL.Query().Get("scope")
	userID := r.URL.Query().Get("user")
	teamSlug := r.URL.Query().Get("team")
	invocationID := strings.TrimSpace(r.URL.Query().Get("invocationId"))

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
	if invocationID == "" {
		respondError(w, http.StatusBadRequest, "invocationId is required")
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

	events, err := sessions.ReadTranscriptEvents(r.Context(), studioChatAppName, userID, sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read session events")
		return
	}

	var knowledgeData map[string]any
	var toolData map[string]any
	var lastKnowledge map[string]any
	var lastTool map[string]any
	found := false

	for _, evt := range events {
		if evt.Actions.StateDelta != nil {
			if ki, ok := evt.Actions.StateDelta["_knowledge_injection"]; ok {
				if m, ok := ki.(map[string]any); ok {
					lastKnowledge = m
				}
			}
			if ti, ok := evt.Actions.StateDelta["_tool_injection"]; ok {
				if m, ok := ti.(map[string]any); ok {
					lastTool = m
				}
			}
		}
		if evt.InvocationID == invocationID && !found {
			knowledgeData = lastKnowledge
			toolData = lastTool
			found = true
		}
	}

	// If we found the invocation but data is still nil, the tracking event may
	// have been after the first matching invocation event (same InvocationID).
	if found && knowledgeData == nil && lastKnowledge != nil {
		knowledgeData = lastKnowledge
	}
	if found && toolData == nil && lastTool != nil {
		toolData = lastTool
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessionId":    sessionID,
		"invocationId": invocationID,
		"knowledge":    knowledgeData,
		"tools":        toolData,
	})
}
