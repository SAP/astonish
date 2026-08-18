package a2aclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/credentials"
)

// mockResolver implements credentials.CredentialResolver for testing.
type mockResolver struct {
	headerKey   string
	headerValue string
	err         error
}

func (m *mockResolver) Get(name string) *credentials.Credential {
	return nil
}

func (m *mockResolver) Resolve(name string) (string, string, error) {
	return m.headerKey, m.headerValue, m.err
}

func (m *mockResolver) Reload() error {
	return nil
}

func TestFetchAgentCard(t *testing.T) {
	card := a2a.AgentCard{
		Name:        "test-agent",
		Description: "A test agent",
		URL:         "http://example.com",
		Version:     "1.0",
		Skills: []a2a.Skill{
			{ID: "skill1", Name: "Skill One", Description: "First skill"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/agent-card.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test",
		URL:  server.URL,
	}, nil)

	ctx := context.Background()
	result, err := client.FetchAgentCard(ctx)
	if err != nil {
		t.Fatalf("FetchAgentCard failed: %v", err)
	}

	if result.Name != "test-agent" {
		t.Errorf("expected name 'test-agent', got %q", result.Name)
	}
	if result.Description != "A test agent" {
		t.Errorf("expected description 'A test agent', got %q", result.Description)
	}
	if len(result.Skills) != 1 {
		t.Errorf("expected 1 skill, got %d", len(result.Skills))
	}
}

func TestFetchAgentCardError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test",
		URL:  server.URL,
	}, nil)

	ctx := context.Background()
	_, err := client.FetchAgentCard(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSendMessage(t *testing.T) {
	expectedTask := a2a.Task{
		ID:        "task-123",
		ContextID: "ctx-456",
		Status: a2a.TaskStatus{
			State:     a2a.TaskStateCompleted,
			Timestamp: time.Now(),
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}

		var rpcReq a2a.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if rpcReq.Method != "message/send" {
			t.Errorf("expected method 'message/send', got %q", rpcReq.Method)
		}

		resp := a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      rpcReq.ID,
			Result:  expectedTask,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test",
		URL:  server.URL,
	}, nil)

	ctx := context.Background()
	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Hello"}},
		},
	}

	task, err := client.SendMessage(ctx, params)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if task.ID != "task-123" {
		t.Errorf("expected task ID 'task-123', got %q", task.ID)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("expected state 'completed', got %q", task.Status.State)
	}
}

func TestSendMessageRPCError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)

		resp := a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      rpcReq.ID,
			Error: &a2a.JSONRPCError{
				Code:    -32001,
				Message: "task not found",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test",
		URL:  server.URL,
	}, nil)

	ctx := context.Background()
	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Hello"}},
		},
	}

	_, err := client.SendMessage(ctx, params)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAuthHeaderInjection(t *testing.T) {
	var receivedAuth string
	var receivedCustom string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedCustom = r.Header.Get("X-Custom-Header")

		card := a2a.AgentCard{Name: "test"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	}))
	defer server.Close()

	resolver := &mockResolver{
		headerKey:   "Authorization",
		headerValue: "Bearer test-token-123",
	}

	client := NewClient(A2AAgentConfig{
		Name:           "test",
		URL:            server.URL,
		CredentialName: "my-cred",
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
		},
	}, resolver)

	ctx := context.Background()
	_, err := client.FetchAgentCard(ctx)
	if err != nil {
		t.Fatalf("FetchAgentCard failed: %v", err)
	}

	if receivedAuth != "Bearer test-token-123" {
		t.Errorf("expected auth header 'Bearer test-token-123', got %q", receivedAuth)
	}
	if receivedCustom != "custom-value" {
		t.Errorf("expected custom header 'custom-value', got %q", receivedCustom)
	}
}

func TestAuthHeaderInjectionError(t *testing.T) {
	resolver := &mockResolver{
		err: fmt.Errorf("credential expired"),
	}

	client := NewClient(A2AAgentConfig{
		Name:           "test",
		URL:            "http://localhost:9999",
		CredentialName: "bad-cred",
	}, resolver)

	ctx := context.Background()
	_, err := client.FetchAgentCard(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGetTask(t *testing.T) {
	expectedTask := a2a.Task{
		ID:        "task-789",
		ContextID: "ctx-abc",
		Status: a2a.TaskStatus{
			State:     a2a.TaskStateWorking,
			Timestamp: time.Now(),
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)

		if rpcReq.Method != "tasks/get" {
			t.Errorf("expected method 'tasks/get', got %q", rpcReq.Method)
		}

		resp := a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      rpcReq.ID,
			Result:  expectedTask,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test",
		URL:  server.URL,
	}, nil)

	ctx := context.Background()
	task, err := client.GetTask(ctx, "task-789")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	if task.ID != "task-789" {
		t.Errorf("expected task ID 'task-789', got %q", task.ID)
	}
}

func TestCancelTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)

		if rpcReq.Method != "tasks/cancel" {
			t.Errorf("expected method 'tasks/cancel', got %q", rpcReq.Method)
		}

		resp := a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      rpcReq.ID,
			Result:  map[string]any{"status": "canceled"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test",
		URL:  server.URL,
	}, nil)

	ctx := context.Background()
	err := client.CancelTask(ctx, "task-789")
	if err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}
}

func TestNoCredentialStillAppliesHeaders(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-API-Key")
		card := a2a.AgentCard{Name: "test"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test",
		URL:  server.URL,
		Headers: map[string]string{
			"X-API-Key": "my-api-key",
		},
	}, nil)

	ctx := context.Background()
	_, err := client.FetchAgentCard(ctx)
	if err != nil {
		t.Fatalf("FetchAgentCard failed: %v", err)
	}

	if receivedHeader != "my-api-key" {
		t.Errorf("expected header 'my-api-key', got %q", receivedHeader)
	}
}
