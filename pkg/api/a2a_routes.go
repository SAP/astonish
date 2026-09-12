package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/a2aserver"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/gorilla/mux"
)

var (
	a2aServiceMu sync.RWMutex
	a2aService   *a2aserver.Service
)

// SetA2AService supplies the endpoint-owned A2A service at composition time.
func SetA2AService(service *a2aserver.Service) {
	a2aServiceMu.Lock()
	a2aService = service
	a2aServiceMu.Unlock()
}
func getA2AService() *a2aserver.Service {
	a2aServiceMu.RLock()
	defer a2aServiceMu.RUnlock()
	return a2aService
}

// RegisterA2ARoutes registers public discovery and OAuth-protected protocol routes.
func RegisterA2ARoutes(router *mux.Router, dependencies ...any) {
	var validator BearerPrincipalValidator
	var resolveTenant A2ATenantResolver
	var tenantMW func(http.Handler) http.Handler
	if len(dependencies) > 0 {
		validator, _ = dependencies[0].(BearerPrincipalValidator)
	}
	if len(dependencies) > 1 {
		resolveTenant, _ = dependencies[1].(A2ATenantResolver)
	}
	if len(dependencies) > 2 {
		tenantMW, _ = dependencies[2].(func(http.Handler) http.Handler)
	}
	router.HandleFunc("/.well-known/agent-card.json", AgentCardHandler).Methods(http.MethodGet)
	if validator == nil {
		return
	}
	var handler http.Handler = http.HandlerFunc(A2AHandler)
	var stream http.Handler = http.HandlerFunc(A2AStreamHandler)
	if tenantMW != nil {
		handler = tenantMW(handler)
		stream = tenantMW(stream)
	}
	router.Handle("/api/a2a", a2aBearerMiddleware(validator, resolveTenant, handler)).Methods(http.MethodPost)
	router.Handle("/api/a2a/stream", a2aBearerMiddleware(validator, resolveTenant, stream)).Methods(http.MethodPost)
}

func AgentCardHandler(w http.ResponseWriter, _ *http.Request) {
	service := getA2AService()
	if service == nil {
		http.Error(w, "A2A service unavailable", http.StatusServiceUnavailable)
		return
	}
	card := a2a.BuildAgentCard(a2a.AgentCardConfig{Name: "Astonish", Description: "AI agent platform with multi-tool capabilities", BaseURL: service.BaseURL(), Version: "1.0.0", AuthMethods: []string{"bearer"}}, nil)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(card)
}

func A2AHandler(w http.ResponseWriter, r *http.Request) {
	service, principal, ok := authorizedA2AService(w, r)
	if !ok {
		return
	}
	req, ok := readA2ARequest(w, r)
	if !ok {
		return
	}
	identity := a2aIdentity(principal)
	switch req.Method {
	case "message/send":
		var params a2a.SendMessageParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
			return
		}
		task, err := service.SendMessage(r.Context(), identity, params)
		if errors.Is(err, a2aserver.ErrActiveTaskLimit) {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeRateLimited, "A2A active task limit reached")
			return
		}
		if err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, err.Error())
			return
		}
		writeJSONRPCResult(w, req.ID, task)
	case "tasks/get":
		var params a2a.GetTaskParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
			return
		}
		task, err := service.GetTask(identity, params.TaskID)
		if err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, "Task not found")
			return
		}
		writeJSONRPCResult(w, req.ID, task)
	case "tasks/cancel":
		var params a2a.CancelTaskParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
			return
		}
		if err := service.CancelTask(identity, params.TaskID); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, "Task not found")
			return
		}
		writeJSONRPCResult(w, req.ID, map[string]string{"status": "canceled"})
	case "pushNotification/set", "pushNotification/get", "pushNotification/delete":
		handlePushNotification(w, r, service, identity, req)
	default:
		writeJSONRPCError(w, req.ID, a2a.ErrCodeMethodNotFound, fmt.Sprintf("Unknown method: %s", req.Method))
	}
}

func A2AStreamHandler(w http.ResponseWriter, r *http.Request) {
	service, principal, ok := authorizedA2AService(w, r)
	if !ok {
		return
	}
	req, ok := readA2ARequest(w, r)
	if !ok {
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, "Streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	task, err := service.SendMessage(r.Context(), a2aIdentity(principal), params)
	if errors.Is(err, a2aserver.ErrActiveTaskLimit) {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeRateLimited, "A2A active task limit reached")
		return
	}
	resp := a2a.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: task}
	if err != nil {
		resp.Result = nil
		resp.Error = &a2a.JSONRPCError{Code: a2a.ErrCodeInternal, Message: err.Error()}
	}
	data, _ := json.Marshal(resp)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func authorizedA2AService(w http.ResponseWriter, r *http.Request) (*a2aserver.Service, execution.Principal, bool) {
	service := getA2AService()
	if service == nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeInternal, "A2A service unavailable")
		return nil, execution.Principal{}, false
	}
	principal, ok := execution.PrincipalFromContext(r.Context())
	if !ok {
		writeJSONRPCError(w, nil, a2a.ErrCodeAuthRequired, "Authentication required")
		return nil, execution.Principal{}, false
	}
	return service, principal, true
}
func readA2ARequest(w http.ResponseWriter, r *http.Request) (a2a.JSONRPCRequest, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONRPCError(w, nil, a2a.ErrCodeParseError, "Failed to read request body")
		return a2a.JSONRPCRequest{}, false
	}
	var req a2a.JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidRequest, "Invalid JSON-RPC request")
		return a2a.JSONRPCRequest{}, false
	}
	return req, true
}
func a2aAgentID(p execution.Principal) string {
	if p.Actor != "" && p.Subject != "" {
		return p.Actor + ":" + p.Subject
	}
	if p.Subject != "" {
		return p.Subject
	}
	return p.ClientID
}
func a2aIdentity(p execution.Principal) a2aserver.Identity {
	return a2aserver.Identity{AgentID: a2aAgentID(p), UserID: p.Subject, OrgID: p.OrgSlug, TeamID: p.TeamSlug}
}

func handlePushNotification(w http.ResponseWriter, r *http.Request, service *a2aserver.Service, identity a2aserver.Identity, req a2a.JSONRPCRequest) {
	var params a2a.GetTaskParams
	if req.Method == "pushNotification/set" {
		var set a2a.SetPushNotificationParams
		if err := json.Unmarshal(req.Params, &set); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
			return
		}
		if _, err := service.GetTask(identity, set.TaskID); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, "Task not found")
			return
		}
		if err := service.PushNotifier().ValidatePushURL(r.Context(), set.Config.URL); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid push URL: "+err.Error())
			return
		}
		if err := service.TaskStore().SetPushConfig(set.TaskID, set.Config); err != nil {
			writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, err.Error())
			return
		}
		writeJSONRPCResult(w, req.ID, set.Config)
		return
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInvalidParams, "Invalid params: "+err.Error())
		return
	}
	if _, err := service.GetTask(identity, params.TaskID); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeTaskNotFound, "Task not found")
		return
	}
	if req.Method == "pushNotification/get" {
		writeJSONRPCResult(w, req.ID, service.TaskStore().GetPushConfig(params.TaskID))
		return
	}
	if err := service.TaskStore().DeletePushConfig(params.TaskID); err != nil {
		writeJSONRPCError(w, req.ID, a2a.ErrCodeInternal, err.Error())
		return
	}
	writeJSONRPCResult(w, req.ID, map[string]string{"status": "deleted"})
}
func writeJSONRPCResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a2a.JSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}
func writeJSONRPCError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	if code == a2a.ErrCodeAuthRequired {
		w.WriteHeader(http.StatusUnauthorized)
	}
	if err := json.NewEncoder(w).Encode(a2a.JSONRPCResponse{JSONRPC: "2.0", ID: id, Error: &a2a.JSONRPCError{Code: code, Message: message}}); err != nil {
		log.Printf("[a2a] write response: %v", err)
	}
}
