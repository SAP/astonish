package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/SAP/astonish/pkg/a2a"
	a2achan "github.com/SAP/astonish/pkg/channels/a2a"
	"github.com/gorilla/mux"
)

// --- Package-level state for A2A (set by daemon during startup) ---

var (
	a2aChannelMu sync.RWMutex
	a2aChannel   *a2achan.A2AChannel
)

// SetA2AChannel sets the A2A channel adapter reference for the HTTP handlers.
func SetA2AChannel(ch *a2achan.A2AChannel) {
	a2aChannelMu.Lock()
	defer a2aChannelMu.Unlock()
	a2aChannel = ch
}

func getA2AChannel() *a2achan.A2AChannel {
	a2aChannelMu.RLock()
	defer a2aChannelMu.RUnlock()
	return a2aChannel
}

// RegisterA2ARoutes registers A2A protocol endpoints on the router.
// Called from RegisterRoutes in handlers.go.
func RegisterA2ARoutes(router *mux.Router) {
	// Agent Card discovery — public, no auth required
	router.HandleFunc("/.well-known/agent-card.json", AgentCardHandler).Methods("GET")

	// A2A JSON-RPC endpoint — requires A2A auth (JWT Bearer validation)
	a2aRouter := router.PathPrefix("/api/a2a").Subrouter()
	a2aRouter.Use(A2AAuthMiddleware)
	a2aRouter.HandleFunc("", A2AHandler).Methods("POST")
	a2aRouter.HandleFunc("/stream", A2AStreamHandler).Methods("POST")

	// Admin endpoints for managing registered agents (legacy — will be replaced with
	// trusted issuer/allowed agent admin in a later phase)
	router.HandleFunc("/api/admin/a2a/agents", A2AAdminListAgentsHandler).Methods("GET")
	router.HandleFunc("/api/admin/a2a/agents", A2AAdminRegisterAgentHandler).Methods("POST")
	router.HandleFunc("/api/admin/a2a/agents/{id}", A2AAdminDeleteAgentHandler).Methods("DELETE")
	router.HandleFunc("/api/admin/a2a/agents/{id}/rotate-key", A2AAdminRotateKeyHandler).Methods("POST")
}

// AgentCardHandler serves the A2A Agent Card discovery document.
// GET /.well-known/agent-card.json
func AgentCardHandler(w http.ResponseWriter, r *http.Request) {
	ch := getA2AChannel()
	if ch == nil {
		http.Error(w, "A2A channel not configured", http.StatusServiceUnavailable)
		return
	}

	card := a2a.BuildAgentCard(a2a.AgentCardConfig{
		Name:        "Astonish",
		Description: "AI agent platform with multi-tool capabilities",
		BaseURL:     ch.BaseURL(),
		Version:     "1.0.0",
		AuthMethods: []string{"bearer", "api_key"},
	}, nil) // Skills populated dynamically in future

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(card)
}

// A2AHandler handles the main A2A JSON-RPC endpoint.
// POST /api/a2a
func A2AHandler(w http.ResponseWriter, r *http.Request) {
	ch := getA2AChannel()
	if ch == nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeInternal, "A2A channel not configured")
		return
	}

	claims := A2AClaimsFromContext(r.Context())
	if claims == nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeAuthRequired, "Authentication required")
		return
	}

	// Read and parse JSON-RPC request
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeParseError, "Failed to read request body")
		return
	}

	var req a2a.JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeParseError, "Invalid JSON")
		return
	}

	if req.JSONRPC != "2.0" {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidRequest, "Invalid JSON-RPC version")
		return
	}

	// Bridge: construct a RegisteredAgent from claims for backward compatibility
	// with the channel adapter (until phase 5 updates the adapter signature).
	agent := agentFromClaims(claims)

	// Dispatch by method
	switch req.Method {
	case "message/send":
		handleMessageSend(w, r, ch, agent, req)
	case "tasks/get":
		handleTasksGet(w, ch, agent, req)
	case "tasks/cancel":
		handleTasksCancel(w, ch, agent, req)
	case "pushNotification/set":
		handlePushNotificationSet(w, ch, agent, req)
	case "pushNotification/get":
		handlePushNotificationGet(w, ch, agent, req)
	case "pushNotification/delete":
		handlePushNotificationDelete(w, ch, agent, req)
	default:
		writeJSONRPCError(w, req.ID, a2a.ErrCodeMethodNotFound, fmt.Sprintf("Unknown method: %s", req.Method))
	}
}

func handleMessageSend(w http.ResponseWriter, r *http.Request, ch *a2achan.A2AChannel, agent *a2a.RegisteredAgent, req a2a.JSONRPCRequest) {
	var params a2a.SendMessageParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}

	task, err := ch.HandleSendMessage(r.Context(), agent, params)
	if err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, err.Error())
		return
	}

	writeJSONRPCResult(w, req.ID, task)
}

func handleTasksGet(w http.ResponseWriter, ch *a2achan.A2AChannel, agent *a2a.RegisteredAgent, req a2a.JSONRPCRequest) {
	var params a2a.GetTaskParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}

	task, err := ch.HandleGetTask(agent, params.TaskID)
	if err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, err.Error())
		return
	}

	writeJSONRPCResult(w, req.ID, task)
}

func handleTasksCancel(w http.ResponseWriter, ch *a2achan.A2AChannel, agent *a2a.RegisteredAgent, req a2a.JSONRPCRequest) {
	var params a2a.CancelTaskParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}

	if err := ch.HandleCancelTask(agent, params.TaskID); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, err.Error())
		return
	}

	writeJSONRPCResult(w, req.ID, map[string]string{"status": "canceled"})
}

func handlePushNotificationSet(w http.ResponseWriter, ch *a2achan.A2AChannel, agent *a2a.RegisteredAgent, req a2a.JSONRPCRequest) {
	var params a2a.SetPushNotificationParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}

	// Verify agent owns the task
	task, err := ch.TaskStore().Get(params.TaskID)
	if err != nil || task.AgentID != agent.ID {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, "Task not found")
		return
	}

	if err := ch.TaskStore().SetPushConfig(params.TaskID, params.Config); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, err.Error())
		return
	}

	writeJSONRPCResult(w, req.ID, params.Config)
}

func handlePushNotificationGet(w http.ResponseWriter, ch *a2achan.A2AChannel, agent *a2a.RegisteredAgent, req a2a.JSONRPCRequest) {
	var params a2a.GetTaskParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}

	// Verify agent owns the task
	task, err := ch.TaskStore().Get(params.TaskID)
	if err != nil || task.AgentID != agent.ID {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, "Task not found")
		return
	}

	cfg := ch.TaskStore().GetPushConfig(params.TaskID)
	writeJSONRPCResult(w, req.ID, cfg)
}

func handlePushNotificationDelete(w http.ResponseWriter, ch *a2achan.A2AChannel, agent *a2a.RegisteredAgent, req a2a.JSONRPCRequest) {
	var params a2a.GetTaskParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}

	// Verify agent owns the task
	task, err := ch.TaskStore().Get(params.TaskID)
	if err != nil || task.AgentID != agent.ID {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, "Task not found")
		return
	}

	if err := ch.TaskStore().DeletePushConfig(params.TaskID); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, err.Error())
		return
	}

	writeJSONRPCResult(w, req.ID, map[string]string{"status": "deleted"})
}

// A2AStreamHandler handles the streaming A2A endpoint.
// POST /api/a2a/stream
func A2AStreamHandler(w http.ResponseWriter, r *http.Request) {
	ch := getA2AChannel()
	if ch == nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeInternal, "A2A channel not configured")
		return
	}

	claims := A2AClaimsFromContext(r.Context())
	if claims == nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeAuthRequired, "Authentication required")
		return
	}

	// Read and parse JSON-RPC request
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeParseError, "Failed to read request body")
		return
	}

	var req a2a.JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeParseError, "Invalid JSON")
		return
	}

	if req.Method != "message/stream" {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeMethodNotFound, "Stream endpoint only supports message/stream")
		return
	}

	var params a2a.SendMessageParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}

	// Set up SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, "Streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Bridge: construct a RegisteredAgent from claims
	agent := agentFromClaims(claims)

	// Process the message (same as sync but we stream events)
	task, err := ch.HandleSendMessage(r.Context(), agent, params)
	if err != nil {
		// Write error as SSE event
		errResp := a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &a2a.JSONRPCError{Code: a2a.ErrCodeInternal, Message: err.Error()},
		}
		data, _ := json.Marshal(errResp)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		return
	}

	// Send final status update
	resp := a2a.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  task,
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// agentFromClaims constructs a RegisteredAgent from validated JWT claims.
// This is a bridge function for backward compatibility with the channel adapter
// until the adapter is updated to accept claims directly (phase 5).
func agentFromClaims(claims *a2a.A2ATokenClaims) *a2a.RegisteredAgent {
	// Derive agent ID for task ownership scoping.
	// For delegated tokens (actor present): use composite key so one service
	// acting for multiple users cannot see other users' tasks via tasks/get.
	// For direct user tokens: use the user identifier alone.
	var agentID string
	if claims.ActorIdentifier != "" {
		agentID = claims.ActorIdentifier + ":" + claims.UserIdentifier
	} else {
		agentID = claims.UserIdentifier
	}
	return &a2a.RegisteredAgent{
		ID:                       agentID,
		Name:                     claims.ActorIdentifier,
		LinkedUserID:             claims.UserIdentifier,
		LinkedOrgSlug:            claims.OrgID,
		LinkedTeamSlug:           "",
		AllowIdentityPropagation: false, // explicit: identity comes from JWT, never from message metadata
	}
}

// --- JSON-RPC response helpers ---

func writeJSONRPCResult(w http.ResponseWriter, id any, result any) {
	resp := a2a.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("[a2a] Failed to write response: %v", err)
	}
}

func writeJSONRPCError(w http.ResponseWriter, id any, code int, message string) {
	resp := a2a.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &a2a.JSONRPCError{Code: code, Message: message},
	}
	w.Header().Set("Content-Type", "application/json")
	// Use appropriate HTTP status based on error code
	status := http.StatusOK // JSON-RPC errors still use 200 per spec
	if code == a2a.ErrCodeAuthRequired {
		status = http.StatusUnauthorized
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("[a2a] Failed to write error response: %v", err)
	}
}
