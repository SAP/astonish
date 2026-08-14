package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
	a2achan "github.com/SAP/astonish/pkg/channels/a2a"
	"github.com/gorilla/mux"
)

func setupA2ATestChannel(t *testing.T) (*a2achan.A2AChannel, string) {
	t.Helper()
	store := a2a.NewInMemoryTaskStore(1 * time.Hour)
	t.Cleanup(store.Close)
	reg := a2a.NewInMemoryAgentRegistry()

	ch := a2achan.New(&a2achan.Config{
		TaskStore:     store,
		AgentRegistry: reg,
		BaseURL:       "http://localhost:9393",
	}, nil)

	// Register a test agent
	apiKey, err := reg.Register(a2a.RegisteredAgent{
		Name:                     "TestAgent",
		LinkedUserID:             "test-user",
		AllowIdentityPropagation: true,
	})
	if err != nil {
		t.Fatalf("Failed to register test agent: %v", err)
	}

	// Set the global channel
	SetA2AChannel(ch)
	t.Cleanup(func() { SetA2AChannel(nil) })

	return ch, apiKey
}

func TestA2AAgentCardHandler(t *testing.T) {
	_, _ = setupA2ATestChannel(t)

	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	w := httptest.NewRecorder()

	AgentCardHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var card a2a.AgentCard
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatalf("failed to parse agent card: %v", err)
	}

	if card.Name != "Astonish" {
		t.Fatalf("expected name 'Astonish', got %q", card.Name)
	}
	if card.URL != "http://localhost:9393/api/a2a" {
		t.Fatalf("expected URL with /api/a2a, got %q", card.URL)
	}
}

func TestA2AAgentCardHandler_NotConfigured(t *testing.T) {
	SetA2AChannel(nil)

	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	w := httptest.NewRecorder()

	AgentCardHandler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestA2AHandler_MessageSend(t *testing.T) {
	ch, apiKey := setupA2ATestChannel(t)

	// Start the channel with a handler that responds
	_ = ch.Start(context.Background(), func(ctx context.Context, msg channels.InboundMessage) error {
		return nil
	})

	// Build JSON-RPC request
	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Hello"}},
		},
		Configuration: &a2a.TaskConfig{
			ReturnImmediately: true, // Use async mode for simpler test
		},
	}
	paramsJSON, _ := json.Marshal(params)
	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
		Params:  paramsJSON,
	}
	body, _ := json.Marshal(rpcReq)

	// Create router with auth middleware
	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

func TestA2AHandler_InvalidAuth(t *testing.T) {
	_, _ = setupA2ATestChannel(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("X-API-Key", "invalid-key")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for invalid auth")
	}
	if resp.Error.Code != a2a.ErrCodeForbidden {
		t.Fatalf("expected error code %d, got %d", a2a.ErrCodeForbidden, resp.Error.Code)
	}
}

func TestA2AHandler_NoAuth(t *testing.T) {
	_, _ = setupA2ATestChannel(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "message/send",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	// No auth header
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for missing auth")
	}
	if resp.Error.Code != a2a.ErrCodeAuthRequired {
		t.Fatalf("expected error code %d, got %d", a2a.ErrCodeAuthRequired, resp.Error.Code)
	}
}

func TestA2AHandler_UnknownMethod(t *testing.T) {
	_, apiKey := setupA2ATestChannel(t)

	rpcReq := a2a.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "unknown/method",
	}
	body, _ := json.Marshal(rpcReq)

	router := mux.NewRouter()
	sub := router.PathPrefix("/api/a2a").Subrouter()
	sub.Use(A2AAuthMiddleware)
	sub.HandleFunc("", A2AHandler).Methods("POST")

	req := httptest.NewRequest("POST", "/api/a2a", bytes.NewReader(body))
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	var resp a2a.JSONRPCResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != a2a.ErrCodeMethodNotFound {
		t.Fatalf("expected error code %d, got %d", a2a.ErrCodeMethodNotFound, resp.Error.Code)
	}
}

func TestA2AAdminRegisterAndList(t *testing.T) {
	_, _ = setupA2ATestChannel(t)

	// Register a new agent
	regReq := A2AAdminRegisterRequest{
		Name:         "NewAgent",
		LinkedUserID: "user-456",
	}
	body, _ := json.Marshal(regReq)

	req := httptest.NewRequest("POST", "/api/admin/a2a/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	A2AAdminRegisterAgentHandler(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var regResp A2AAdminRegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("failed to parse register response: %v", err)
	}
	if regResp.APIKey == "" {
		t.Fatal("expected non-empty API key")
	}
	if regResp.Agent == nil || regResp.Agent.Name != "NewAgent" {
		t.Fatal("expected agent with name 'NewAgent'")
	}

	// List agents
	req = httptest.NewRequest("GET", "/api/admin/a2a/agents", nil)
	w = httptest.NewRecorder()

	A2AAdminListAgentsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestA2AAuthExemptPaths(t *testing.T) {
	tests := []struct {
		path   string
		exempt bool
	}{
		{"/.well-known/agent-card.json", true},
		{"/api/a2a", true},
		{"/api/a2a/stream", true},
		{"/api/admin/a2a/agents", false},
	}
	for _, tt := range tests {
		got := isAuthExemptPath(tt.path)
		if got != tt.exempt {
			t.Errorf("isAuthExemptPath(%q) = %v, want %v", tt.path, got, tt.exempt)
		}
	}
}
