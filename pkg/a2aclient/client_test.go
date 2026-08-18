package a2aclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestV1ProtocolDetection(t *testing.T) {
	// Test isV1 with various version strings
	client := &Client{}

	client.SetProtocolVersion("")
	if client.isV1() {
		t.Error("empty version should not be v1")
	}

	client.SetProtocolVersion("0.3")
	if client.isV1() {
		t.Error("0.3 should not be v1")
	}

	client.SetProtocolVersion("1.0")
	if !client.isV1() {
		t.Error("1.0 should be v1")
	}

	client.SetProtocolVersion("1")
	if !client.isV1() {
		t.Error("1 should be v1")
	}

	client.SetProtocolVersion("1.1")
	if !client.isV1() {
		t.Error("1.1 should be v1")
	}
}

func TestV1SendMessageMethod(t *testing.T) {
	// Set up a test server that captures the request
	var receivedMethod string
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		var rpcReq struct {
			Method string `json:"method"`
		}
		json.Unmarshal(body, &rpcReq)
		receivedMethod = rpcReq.Method

		// Check A2A-Version header
		if r.Header.Get("A2A-Version") != "1.0" {
			t.Errorf("expected A2A-Version header '1.0', got %q", r.Header.Get("A2A-Version"))
		}

		// Return a valid v1.0 response with artifacts
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"task": map[string]any{
					"id":        "task-123",
					"contextId": "ctx-456",
					"status": map[string]any{
						"state":     "TASK_STATE_COMPLETED",
						"timestamp": "2026-01-01T00:00:00Z",
					},
					"artifacts": []map[string]any{
						{
							"artifactId": "art-1",
							"name":       "result",
							"parts": []map[string]any{
								{"data": map[string]any{"count": 3, "devices": []string{"a", "b", "c"}}, "mediaType": "application/json"},
								{"text": "Found 3 devices", "mediaType": "text/plain"},
							},
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		config:     A2AAgentConfig{URL: server.URL},
	}
	client.SetProtocolVersion("1.0")

	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Find devices"}},
			Metadata: map[string]any{
				"skill_id": "find_devices",
			},
		},
	}

	task, err := client.SendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Verify the RPC method used
	if receivedMethod != "SendMessage" {
		t.Errorf("expected method 'SendMessage', got %q", receivedMethod)
	}

	// Verify the body contains v1.0 format (ROLE_USER, messageId present)
	var rpcReq struct {
		Params json.RawMessage `json:"params"`
	}
	json.Unmarshal(receivedBody, &rpcReq)

	var params2 map[string]any
	json.Unmarshal(rpcReq.Params, &params2)

	msg, _ := params2["message"].(map[string]any)
	if msg["role"] != "ROLE_USER" {
		t.Errorf("expected role 'ROLE_USER', got %v", msg["role"])
	}

	// Verify messageId is present (required in v1.0)
	if msg["messageId"] == nil || msg["messageId"] == "" {
		t.Error("expected messageId to be present in v1.0 message")
	}

	// Verify task state was normalized
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("expected normalized state 'completed', got %q", task.Status.State)
	}

	if task.ID != "task-123" {
		t.Errorf("expected task ID 'task-123', got %q", task.ID)
	}

	// Verify artifacts were parsed correctly
	if len(task.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(task.Artifacts))
	}
	if task.Artifacts[0].Name != "result" {
		t.Errorf("expected artifact name 'result', got %q", task.Artifacts[0].Name)
	}
	if len(task.Artifacts[0].Parts) != 2 {
		t.Fatalf("expected 2 parts in artifact, got %d", len(task.Artifacts[0].Parts))
	}
	// First part should be DataPart
	if dp, ok := task.Artifacts[0].Parts[0].(a2a.DataPart); !ok {
		t.Errorf("expected first part to be DataPart, got %T", task.Artifacts[0].Parts[0])
	} else if dp.MimeType != "application/json" {
		t.Errorf("expected mediaType 'application/json', got %q", dp.MimeType)
	}
	// Second part should be TextPart
	if tp, ok := task.Artifacts[0].Parts[1].(a2a.TextPart); !ok {
		t.Errorf("expected second part to be TextPart, got %T", task.Artifacts[0].Parts[1])
	} else if tp.Text != "Found 3 devices" {
		t.Errorf("expected text 'Found 3 devices', got %q", tp.Text)
	}
}

func TestV03SendMessageUnchanged(t *testing.T) {
	// Verify that v0.3 behavior is preserved when protocolVersion is empty
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rpcReq struct {
			Method string `json:"method"`
		}
		json.Unmarshal(body, &rpcReq)
		receivedMethod = rpcReq.Method

		// Should NOT have A2A-Version header
		if v := r.Header.Get("A2A-Version"); v != "" {
			t.Errorf("v0.3 should not send A2A-Version header, got %q", v)
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"id":        "task-789",
				"contextId": "ctx-000",
				"status": map[string]any{
					"state":     "completed",
					"timestamp": "2026-01-01T00:00:00Z",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		config:     A2AAgentConfig{URL: server.URL},
	}
	// No SetProtocolVersion call — defaults to v0.3

	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "hello"}},
		},
	}

	task, err := client.SendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if receivedMethod != "message/send" {
		t.Errorf("expected method 'message/send', got %q", receivedMethod)
	}

	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("expected state 'completed', got %q", task.Status.State)
	}
}

func TestV1NormalizeTaskState(t *testing.T) {
	tests := []struct {
		input    a2a.TaskState
		expected a2a.TaskState
	}{
		{"TASK_STATE_COMPLETED", a2a.TaskStateCompleted},
		{"TASK_STATE_SUBMITTED", a2a.TaskStateSubmitted},
		{"TASK_STATE_WORKING", a2a.TaskStateWorking},
		{"TASK_STATE_FAILED", a2a.TaskStateFailed},
		{"TASK_STATE_CANCELED", a2a.TaskStateCanceled},
		{"TASK_STATE_INPUT_REQUIRED", a2a.TaskStateInputRequired},
		{"TASK_STATE_AUTH_REQUIRED", a2a.TaskStateAuthRequired},
		{"TASK_STATE_REJECTED", a2a.TaskStateRejected},
		// v0.3 states should pass through unchanged
		{a2a.TaskStateCompleted, a2a.TaskStateCompleted},
		{a2a.TaskStateFailed, a2a.TaskStateFailed},
		{"unknown_state", "unknown_state"},
	}

	for _, tt := range tests {
		result := normalizeTaskState(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeTaskState(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestDetectProtocolVersionFromSupportedInterfaces(t *testing.T) {
	// Simulates the SAP Autonomous Operations agent card structure
	card := a2a.AgentCard{
		Name:    "AO Agent",
		Version: "0.1.0", // This is the agent version, NOT protocol version
		SupportedInterfaces: []a2a.AgentInterface{
			{
				URL:             "https://example.com/",
				ProtocolBinding: "JSONRPC",
				ProtocolVersion: "1.0",
			},
		},
	}

	pv := card.DetectProtocolVersion()
	if pv != "1.0" {
		t.Errorf("expected protocolVersion '1.0' from supportedInterfaces, got %q", pv)
	}
}

func TestDetectProtocolVersionTopLevelTakesPrecedence(t *testing.T) {
	card := a2a.AgentCard{
		Name:            "Agent",
		ProtocolVersion: "2.0",
		SupportedInterfaces: []a2a.AgentInterface{
			{ProtocolVersion: "1.0"},
		},
	}

	pv := card.DetectProtocolVersion()
	if pv != "2.0" {
		t.Errorf("expected top-level protocolVersion '2.0' to take precedence, got %q", pv)
	}
}

func TestDetectProtocolVersionEmpty(t *testing.T) {
	card := a2a.AgentCard{
		Name:    "Agent",
		Version: "0.3.0", // agent version, not protocol
	}

	pv := card.DetectProtocolVersion()
	if pv != "" {
		t.Errorf("expected empty protocolVersion, got %q", pv)
	}
}

func TestV1SendMessageWithSupportedInterfaces(t *testing.T) {
	// End-to-end test: agent card has supportedInterfaces with protocolVersion 1.0
	// Verify the manager detects it and the client sends the correct method
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			// Return a card with supportedInterfaces (like SAP AO agent)
			card := map[string]any{
				"name":    "AO Agent",
				"version": "0.1.0",
				"supportedInterfaces": []map[string]any{
					{
						"url":             "https://example.com/",
						"protocolBinding": "JSONRPC",
						"protocolVersion": "1.0",
					},
				},
				"skills": []map[string]any{
					{"id": "find_devices", "name": "Find Devices"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(card)
			return
		}

		// RPC endpoint
		body, _ := io.ReadAll(r.Body)
		var rpcReq struct {
			Method string `json:"method"`
		}
		json.Unmarshal(body, &rpcReq)
		receivedMethod = rpcReq.Method

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"task": map[string]any{
					"id":        "task-001",
					"contextId": "ctx-001",
					"status": map[string]any{
						"state":     "TASK_STATE_COMPLETED",
						"timestamp": "2026-01-01T00:00:00Z",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"ao": {Name: "ao", URL: server.URL},
		},
	}

	mgr := NewManager(cfg)
	err := mgr.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	client, err := mgr.GetClient("ao")
	if err != nil {
		t.Fatalf("GetClient failed: %v", err)
	}

	// Verify protocol version was detected
	if !client.isV1() {
		t.Fatal("expected client to detect v1.0 from supportedInterfaces")
	}

	// Send a message and verify the method used
	params := a2a.SendMessageParams{
		Message: a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.TextPart{Text: "Find devices"}},
		},
	}

	task, err := client.SendMessage(context.Background(), params)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if receivedMethod != "SendMessage" {
		t.Errorf("expected method 'SendMessage', got %q", receivedMethod)
	}

	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("expected normalized state 'completed', got %q", task.Status.State)
	}
}

func TestGetTask_V1_NormalizesState(t *testing.T) {
	// Set up a test server that returns a v1.0 JSON-RPC response for tasks/get
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&rpcReq)

		if rpcReq.Method != "tasks/get" {
			t.Errorf("expected method 'tasks/get', got %q", rpcReq.Method)
		}

		// Return a v1.0 response with task envelope and TASK_STATE_COMPLETED
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"task": map[string]any{
					"id":        "task-123",
					"contextId": "ctx-456",
					"status": map[string]any{
						"state":     "TASK_STATE_COMPLETED",
						"timestamp": "2026-01-01T00:00:00Z",
					},
					"history": []map[string]any{
						{
							"role": "ROLE_AGENT",
							"parts": []map[string]any{
								{"text": "Hello from agent"},
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with v1.0 protocol version
	client := &Client{
		httpClient: server.Client(),
		config:     A2AAgentConfig{URL: server.URL},
	}
	client.SetProtocolVersion("1.0")

	// Call GetTask
	ctx := context.Background()
	task, err := client.GetTask(ctx, "task-123")
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	// Verify task ID
	if task.ID != "task-123" {
		t.Errorf("expected task ID 'task-123', got %q", task.ID)
	}

	// Verify context ID
	if task.ContextID != "ctx-456" {
		t.Errorf("expected context ID 'ctx-456', got %q", task.ContextID)
	}

	// Verify state is normalized from TASK_STATE_COMPLETED to "completed"
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("expected normalized state 'completed', got %q", task.Status.State)
	}

	// Verify history is correctly parsed
	if len(task.History) != 1 {
		t.Fatalf("expected 1 history message, got %d", len(task.History))
	}

	histMsg := task.History[0]
	if histMsg.Role != "ROLE_AGENT" {
		t.Errorf("expected history role 'ROLE_AGENT', got %q", histMsg.Role)
	}

	if len(histMsg.Parts) != 1 {
		t.Fatalf("expected 1 part in history message, got %d", len(histMsg.Parts))
	}

	textPart, ok := histMsg.Parts[0].(a2a.TextPart)
	if !ok {
		t.Fatalf("expected TextPart, got %T", histMsg.Parts[0])
	}

	if textPart.Text != "Hello from agent" {
		t.Errorf("expected text 'Hello from agent', got %q", textPart.Text)
	}
}
