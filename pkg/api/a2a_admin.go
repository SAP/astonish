package api

import (
	"encoding/json"
	"net/http"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/gorilla/mux"
)

// A2AAdminRegisterRequest is the request body for registering a new A2A agent.
type A2AAdminRegisterRequest struct {
	Name                     string `json:"name"`
	Description              string `json:"description,omitempty"`
	LinkedUserID             string `json:"linked_user_id,omitempty"`
	LinkedOrgSlug            string `json:"linked_org_slug,omitempty"`
	LinkedTeamSlug           string `json:"linked_team_slug,omitempty"`
	AllowIdentityPropagation bool   `json:"allow_identity_propagation"`
	RateLimit                int    `json:"rate_limit,omitempty"`
	MaxConcurrent            int    `json:"max_concurrent,omitempty"`
}

// A2AAdminRegisterResponse is returned after registering an agent.
type A2AAdminRegisterResponse struct {
	Agent  *a2a.RegisteredAgent `json:"agent"`
	APIKey string               `json:"api_key"` // Shown once, never stored in plaintext
}

// A2AAdminListAgentsHandler lists all registered A2A agents.
// GET /api/admin/a2a/agents
func A2AAdminListAgentsHandler(w http.ResponseWriter, r *http.Request) {
	ch := getA2AChannel()
	if ch == nil {
		http.Error(w, "A2A channel not configured", http.StatusServiceUnavailable)
		return
	}

	agents := ch.AgentRegistry().List()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"agents": agents})
}

// A2AAdminRegisterAgentHandler registers a new external A2A agent.
// POST /api/admin/a2a/agents
func A2AAdminRegisterAgentHandler(w http.ResponseWriter, r *http.Request) {
	ch := getA2AChannel()
	if ch == nil {
		http.Error(w, "A2A channel not configured", http.StatusServiceUnavailable)
		return
	}

	var req A2AAdminRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	agent := a2a.RegisteredAgent{
		Name:                     req.Name,
		Description:              req.Description,
		LinkedUserID:             req.LinkedUserID,
		LinkedOrgSlug:            req.LinkedOrgSlug,
		LinkedTeamSlug:           req.LinkedTeamSlug,
		AllowIdentityPropagation: req.AllowIdentityPropagation,
		RateLimit:                req.RateLimit,
		MaxConcurrent:            req.MaxConcurrent,
	}

	apiKey, err := ch.AgentRegistry().Register(agent)
	if err != nil {
		http.Error(w, "Registration failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fetch the registered agent to get the generated ID
	registeredAgent, _ := ch.AgentRegistry().GetByAPIKey(apiKey)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(A2AAdminRegisterResponse{
		Agent:  registeredAgent,
		APIKey: apiKey,
	})
}

// A2AAdminDeleteAgentHandler removes a registered A2A agent.
// DELETE /api/admin/a2a/agents/{id}
func A2AAdminDeleteAgentHandler(w http.ResponseWriter, r *http.Request) {
	ch := getA2AChannel()
	if ch == nil {
		http.Error(w, "A2A channel not configured", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	id := vars["id"]

	if err := ch.AgentRegistry().Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// A2AAdminRotateKeyHandler rotates the API key for a registered agent.
// POST /api/admin/a2a/agents/{id}/rotate-key
func A2AAdminRotateKeyHandler(w http.ResponseWriter, r *http.Request) {
	ch := getA2AChannel()
	if ch == nil {
		http.Error(w, "A2A channel not configured", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	id := vars["id"]

	newKey, err := ch.AgentRegistry().RotateKey(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"api_key": newKey})
}
