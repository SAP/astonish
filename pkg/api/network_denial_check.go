// Package api — network_denial_check.go provides an HTTP endpoint for the
// frontend to poll for network denials on a running sandbox session.
//
// GET /api/studio/sessions/{id}/network-denials
//
// The frontend polls this endpoint after receiving a tool_result that indicates
// an error. If pending draft policy proposals exist, they are returned for
// the user to approve or deny.
//
// Denial detection and PolicyAllow auto-approve/pre-seed live in
// pkg/sandbox/netpolicy; this file wraps those helpers for ChatRunner.

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// NetworkDenialCheckHandler returns pending network denial proposals for a sandbox
// associated with the given session.
func NetworkDenialCheckHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["id"]
	if sessionID == "" {
		http.Error(w, "session ID required", http.StatusBadRequest)
		return
	}

	sandboxName, err := sandboxNameForSession(r, sessionID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"denials":      []any{},
			"sandbox_name": "",
		})
		return
	}

	gateway, cleanup, err := gatewayClientForRequest(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("gateway client error: %v", err), http.StatusInternalServerError)
		return
	}
	if cleanup != nil {
		defer cleanup()
	}

	resp, err := gateway.GetDraftPolicy(r.Context(), sandboxName, "pending")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"denials":      []any{},
			"sandbox_name": sandboxName,
			"error":        err.Error(),
		})
		return
	}

	denials := make([]openshell.DenialInfo, 0, len(resp.Chunks))
	for _, ch := range resp.Chunks {
		denials = append(denials, openshell.DenialInfo{
			ChunkID:        ch.ID,
			Host:           ch.Host,
			Port:           ch.Port,
			Binary:         ch.Binary,
			Rationale:      ch.Rationale,
			SecurityNotes:  ch.SecurityNotes,
			BroaderPattern: openshell.SuggestBroaderPattern(ch.Host),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"denials":      denials,
		"sandbox_name": sandboxName,
	})
}

// sandboxNameForSession resolves the OpenShell sandbox name for a chat session.
func sandboxNameForSession(r *http.Request, sessionID string) (string, error) {
	registry := buildPGSessionRegistry(r.Context())
	if registry != nil {
		if name := registry.GetContainerName(sessionID); name != "" {
			return name, nil
		}
		slog.Debug("sandbox name not found in PG registry, using deterministic fallback",
			"session_id", sessionID)
	} else {
		localReg, err := sandbox.NewSessionRegistry()
		if err == nil {
			if name := localReg.GetContainerName(sessionID); name != "" {
				return name, nil
			}
		}
		slog.Debug("sandbox name not found in local registry, using deterministic fallback",
			"session_id", sessionID, "error", err)
	}
	return openshell.SandboxName(sessionID), nil
}

func looksLikeNetworkDenial(resp map[string]any) bool {
	return netpolicy.LooksLikeNetworkDenial(resp)
}

func extractDenialsFromOutput(stdout string) []map[string]any {
	return netpolicy.ExtractDenialsFromOutput(stdout)
}

func isNetworkTool(name string) bool {
	return netpolicy.IsNetworkTool(name)
}

func looksLikeNetworkToolDenial(resp map[string]any) bool {
	return netpolicy.LooksLikeNetworkToolDenial(resp)
}

func extractDenialFromToolError(resp map[string]any, fallbackURL string) []map[string]any {
	return netpolicy.ExtractDenialFromToolError(resp, fallbackURL)
}

func hostPortFromURL(rawURL string) (string, int) {
	return netpolicy.HostPortFromURL(rawURL)
}

// filterDenialsByPolicy auto-approves PolicyAllow denials and returns unknowns for UI.
func (cr *ChatRunner) filterDenialsByPolicy(denials []map[string]any) []map[string]any {
	nps := store.NetworkPolicyStoresFromContext(cr.ctx)
	ep := netpolicy.LoadFromStores(cr.ctx, nps)
	return netpolicy.ApplyPolicyAllowDenials(cr.ctx, cr.gatewayConfig, cr.SessionID, ep, denials)
}

func (cr *ChatRunner) preSeedNetworkPolicy() {
	if netpolicy.SessionIsSeeded(cr.SessionID) {
		return
	}
	nps := store.NetworkPolicyStoresFromContext(cr.ctx)
	if err := netpolicy.PreSeedFromStores(cr.ctx, cr.gatewayConfig, cr.SessionID, nps); err != nil {
		return
	}
	netpolicy.MarkSessionSeeded(cr.SessionID)
}
