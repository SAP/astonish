package api

import (
	"net/http"
)

// Legacy A2A admin endpoints — deprecated.
// Agent management is now handled via TrustedIssuers and AllowedAgents
// in platform channel settings (PlatformA2AConfig).
// These stubs return 501 Not Implemented to signal callers to migrate.

// A2AAdminListAgentsHandler is deprecated — use platform channel settings.
// GET /api/admin/a2a/agents
func A2AAdminListAgentsHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Legacy agent registry removed. Use platform channel settings for trusted issuers and allowed agents.", http.StatusNotImplemented)
}

// A2AAdminRegisterAgentHandler is deprecated — use platform channel settings.
// POST /api/admin/a2a/agents
func A2AAdminRegisterAgentHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Legacy agent registry removed. Use platform channel settings for trusted issuers and allowed agents.", http.StatusNotImplemented)
}

// A2AAdminDeleteAgentHandler is deprecated — use platform channel settings.
// DELETE /api/admin/a2a/agents/{id}
func A2AAdminDeleteAgentHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Legacy agent registry removed. Use platform channel settings for trusted issuers and allowed agents.", http.StatusNotImplemented)
}

// A2AAdminRotateKeyHandler is deprecated — use platform channel settings.
// POST /api/admin/a2a/agents/{id}/rotate-key
func A2AAdminRotateKeyHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Legacy agent registry removed. Use platform channel settings for trusted issuers and allowed agents.", http.StatusNotImplemented)
}
