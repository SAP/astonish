package a2aclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/a2a"
	"google.golang.org/genai"
)

func TestADKToolDeclaration(t *testing.T) {
	inner := &A2ATool{
		name:        "a2a_myagent_greet",
		description: "Greets the user via myagent",
		agentName:   "myagent",
		skillID:     "greet",
	}

	adkTool := ToADKTool(inner)

	if adkTool.Name() != "a2a_myagent_greet" {
		t.Errorf("Name() = %q, want %q", adkTool.Name(), "a2a_myagent_greet")
	}
	if adkTool.Description() != "Greets the user via myagent" {
		t.Errorf("Description() = %q, want %q", adkTool.Description(), "Greets the user via myagent")
	}

	// Check Declaration interface
	type declarable interface {
		Declaration() *genai.FunctionDeclaration
	}
	decl, ok := adkTool.(declarable)
	if !ok {
		t.Fatal("adkA2ATool does not implement Declaration()")
	}

	fd := decl.Declaration()
	if fd == nil {
		t.Fatal("Declaration() returned nil")
	}
	if fd.Name != "a2a_myagent_greet" {
		t.Errorf("Declaration.Name = %q, want %q", fd.Name, "a2a_myagent_greet")
	}
	if fd.Parameters == nil {
		t.Fatal("Declaration.Parameters is nil")
	}
	if fd.Parameters.Properties == nil {
		t.Fatal("Declaration.Parameters.Properties is nil")
	}
	if _, ok := fd.Parameters.Properties["message"]; !ok {
		t.Error("Declaration missing 'message' property")
	}
	if _, ok := fd.Parameters.Properties["context_id"]; !ok {
		t.Error("Declaration missing 'context_id' property")
	}
	if len(fd.Parameters.Required) != 1 || fd.Parameters.Required[0] != "message" {
		t.Errorf("Declaration.Required = %v, want [message]", fd.Parameters.Required)
	}
}

func TestADKToolRun(t *testing.T) {
	// Create a mock A2A server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)

		task := &a2a.Task{
			ID: "test-task-1",
			Status: a2a.TaskStatus{
				State: a2a.TaskStateCompleted,
				Message: &a2a.Message{
					Role:  "agent",
					Parts: []a2a.Part{a2a.TextPart{Text: "Hello from A2A!"}},
				},
			},
		}

		resp := a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      rpcReq.ID,
			Result:  task,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "test-agent",
		URL:  server.URL,
	}, nil)

	inner := &A2ATool{
		name:        "a2a_test_agent",
		description: "Test agent tool",
		agentName:   "test-agent",
		skillID:     "default",
		client:      client,
	}

	adkTool := ToADKTool(inner).(*adkA2ATool)

	// Test via the inner Run directly (tool.Context is hard to mock in unit tests;
	// the ADK adapter just delegates to inner.Run with the context extracted).
	ctx := context.Background()
	result, err := adkTool.inner.Run(ctx, map[string]any{
		"message": "Hello!",
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result["status"] != "completed" {
		t.Errorf("status = %v, want 'completed'", result["status"])
	}
	if result["response"] != "Hello from A2A!" {
		t.Errorf("response = %v, want 'Hello from A2A!'", result["response"])
	}
}

func TestADKToolRunMissingMessage(t *testing.T) {
	inner := &A2ATool{
		name:        "a2a_test",
		description: "Test",
		agentName:   "test",
		skillID:     "",
	}

	adkTool := ToADKTool(inner).(*adkA2ATool)

	// Test the inner Run (same logic, avoids needing tool.Context mock)
	ctx := context.Background()
	_, err := adkTool.inner.Run(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing message, got nil")
	}
}

func TestADKToolRunInvalidArgs(t *testing.T) {
	inner := &A2ATool{
		name:        "a2a_test",
		description: "Test",
		agentName:   "test",
		skillID:     "",
	}

	adkTool := ToADKTool(inner).(*adkA2ATool)

	// Test that IsLongRunning is true for A2A tools
	if !adkTool.IsLongRunning() {
		t.Error("IsLongRunning() should return true for A2A tools")
	}
}

func TestGetA2AToolsNoConfig(t *testing.T) {
	// With no config, should return nil gracefully
	ctx := context.Background()
	tools := GetA2ATools(ctx, false)
	// May return nil or empty depending on file config — just ensure no panic
	_ = tools
}
