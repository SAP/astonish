package api

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/store"
)

type mcpNetworkGrantRequest struct {
	Host string `json:"host"`
	Port uint32 `json:"port"`
}

type mcpNetworkGrantResponse struct {
	Approved bool   `json:"approved"`
	Host     string `json:"host"`
	Port     uint32 `json:"port"`
}

// MCPNetworkGrantHandler persists an allow rule for an MCP server's outbound
// dependency. MCP discovery uses disposable sandboxes, so the grant is durable
// network policy rather than a live per-session approval. The next retry creates
// a fresh discovery sandbox and pre-seeds this persisted allow rule.
func MCPNetworkGrantHandler(w http.ResponseWriter, r *http.Request) {
	_ = mux.Vars(r)["serverName"]
	scope := r.URL.Query().Get("scope")

	svc := RequirePlatformServices(w, r)
	if svc == nil {
		return
	}
	targetStore := resolveNetworkPolicyStoreForWrite(w, r, svc, scope)
	if targetStore == nil {
		return
	}

	var req mcpNetworkGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Host == "" {
		respondError(w, http.StatusBadRequest, "Host is required")
		return
	}
	if req.Port == 0 {
		req.Port = 443
	}

	rule := &store.NetworkPolicyRule{
		Host:   req.Host,
		Port:   req.Port,
		Action: store.NetworkPolicyAllow,
	}
	if user := GetPlatformUser(r); user != nil {
		rule.CreatedBy = user.ID
	}
	if err := targetStore.Save(r.Context(), rule); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save network policy: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, mcpNetworkGrantResponse{
		Approved: true,
		Host:     req.Host,
		Port:     req.Port,
	})
}
