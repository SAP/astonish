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
)

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"a2a_agent_skill", "a2a_agent_skill"},
		{"a2a_My Agent_Skill-1", "a2a_my_agent_skill_1"},
		{"a2a_agent.name_skill.id", "a2a_agent_name_skill_id"},
		{"a2a_agent--name__skill", "a2a_agent_name_skill"},
		{"A2A_UPPER_CASE", "a2a_upper_case"},
		{"a2a_special!@#$chars", "a2a_special_chars"},
		{"trailing___", "trailing"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeToolName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeToolName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateToolsWithSkills(t *testing.T) {
	card := &a2a.AgentCard{
		Name:        "test-agent",
		Description: "Test Agent Description",
		Skills: []a2a.Skill{
			{ID: "code-review", Name: "Code Review", Description: "Reviews code"},
			{ID: "summarize", Name: "Summarize", Description: "Summarizes text"},
		},
	}

	tools := GenerateTools("myagent", card, nil)

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Check first tool
	if tools[0].Name() != "a2a_myagent_code_review" {
		t.Errorf("expected tool name 'a2a_myagent_code_review', got %q", tools[0].Name())
	}
	if tools[0].Description() != "Reviews code (via Test Agent Description)" {
		t.Errorf("unexpected description: %q", tools[0].Description())
	}

	// Check second tool
	if tools[1].Name() != "a2a_myagent_summarize" {
		t.Errorf("expected tool name 'a2a_myagent_summarize', got %q", tools[1].Name())
	}
}

func TestGenerateToolsNoSkills(t *testing.T) {
	card := &a2a.AgentCard{
		Name:        "generic-agent",
		Description: "A generic agent",
		Skills:      nil,
	}

	tools := GenerateTools("generic", card, nil)

	if len(tools) != 1 {
		t.Fatalf("expected 1 generic tool, got %d", len(tools))
	}

	if tools[0].Name() != "a2a_generic" {
		t.Errorf("expected tool name 'a2a_generic', got %q", tools[0].Name())
	}
	if tools[0].Description() != "A generic agent" {
		t.Errorf("expected description 'A generic agent', got %q", tools[0].Description())
	}
}

func TestGenerateToolsNilCard(t *testing.T) {
	tools := GenerateTools("agent", nil, nil)
	if tools != nil {
		t.Errorf("expected nil tools for nil card, got %v", tools)
	}
}

func TestGenerateToolsNoDescription(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "nodesc-agent",
		Skills: []a2a.Skill{
			{ID: "skill1", Name: "My Skill", Description: ""},
		},
	}

	tools := GenerateTools("nodesc", card, nil)

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	// When skill has no description, should use name
	if tools[0].Description() != "My Skill" {
		t.Errorf("expected description 'My Skill', got %q", tools[0].Description())
	}
}

func TestGenerateToolsGenericNoDescription(t *testing.T) {
	card := &a2a.AgentCard{
		Name:   "plain-agent",
		Skills: nil,
	}

	tools := GenerateTools("plain", card, nil)

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	if tools[0].Description() != "Send a message to the plain agent" {
		t.Errorf("unexpected description: %q", tools[0].Description())
	}
}

func TestToolRun(t *testing.T) {
	expectedTask := a2a.Task{
		ID:        "run-task-1",
		ContextID: "ctx-run",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
			Message: &a2a.Message{
				Role:  "agent",
				Parts: []a2a.Part{a2a.TextPart{Text: "Hello from agent"}},
			},
			Timestamp: time.Now(),
		},
		Artifacts: []a2a.Artifact{
			{
				Name:        "result",
				Description: "The result",
				Index:       0,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)

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
		Name: "tool-test",
		URL:  server.URL,
	}, nil)

	card := &a2a.AgentCard{
		Name: "tool-agent",
		Skills: []a2a.Skill{
			{ID: "greet", Name: "Greet", Description: "Greets the user"},
		},
	}

	tools := GenerateTools("toolagent", card, client)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	ctx := context.Background()
	result, err := tools[0].Run(ctx, map[string]any{
		"message": "Hello!",
	})
	if err != nil {
		t.Fatalf("Tool.Run failed: %v", err)
	}

	if result["status"] != "completed" {
		t.Errorf("expected status 'completed', got %v", result["status"])
	}
	if result["response"] != "Hello from agent" {
		t.Errorf("expected response 'Hello from agent', got %v", result["response"])
	}
	if result["task_id"] != "run-task-1" {
		t.Errorf("expected task_id 'run-task-1', got %v", result["task_id"])
	}

	artifacts, ok := result["artifacts"].([]any)
	if !ok {
		t.Fatalf("expected artifacts to be []any, got %T", result["artifacts"])
	}
	if len(artifacts) != 1 {
		t.Errorf("expected 1 artifact, got %d", len(artifacts))
	}
}

func TestToolRunWithContextID(t *testing.T) {
	var receivedParams a2a.SendMessageParams

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)

		json.Unmarshal(rpcReq.Params, &receivedParams)

		task := a2a.Task{
			ID:     "ctx-task",
			Status: a2a.TaskStatus{State: a2a.TaskStateCompleted, Timestamp: time.Now()},
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
		Name: "ctx-test",
		URL:  server.URL,
	}, nil)

	tool := &A2ATool{
		name:      "a2a_test_skill",
		agentName: "test",
		skillID:   "skill1",
		client:    client,
	}

	ctx := context.Background()
	_, err := tool.Run(ctx, map[string]any{
		"message":    "Continue conversation",
		"context_id": "my-context-123",
	})
	if err != nil {
		t.Fatalf("Tool.Run failed: %v", err)
	}

	if receivedParams.Configuration == nil {
		t.Fatal("expected configuration to be set")
	}
	if receivedParams.Configuration.ContextID != "my-context-123" {
		t.Errorf("expected context_id 'my-context-123', got %q", receivedParams.Configuration.ContextID)
	}
}

func TestToolRunMissingMessage(t *testing.T) {
	tool := &A2ATool{
		name:      "a2a_test",
		agentName: "test",
		client:    nil,
	}

	ctx := context.Background()
	_, err := tool.Run(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing message, got nil")
	}
}

func TestToolRunEmptyMessage(t *testing.T) {
	tool := &A2ATool{
		name:      "a2a_test",
		agentName: "test",
		client:    nil,
	}

	ctx := context.Background()
	_, err := tool.Run(ctx, map[string]any{
		"message": "",
	})
	if err == nil {
		t.Fatal("expected error for empty message, got nil")
	}
}

func TestGenerateToolsSpecialCharacters(t *testing.T) {
	card := &a2a.AgentCard{
		Name: "Special Agent!",
		Skills: []a2a.Skill{
			{ID: "my-skill.v2", Name: "My Skill v2", Description: "A skill"},
		},
	}

	tools := GenerateTools("Special Agent!", card, nil)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	// Name should be sanitized
	expected := "a2a_special_agent_my_skill_v2"
	if tools[0].Name() != expected {
		t.Errorf("expected tool name %q, got %q", expected, tools[0].Name())
	}
}

func TestToolRunStreamingEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a streaming request
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)
		if rpcReq.Method != "message/stream" {
			t.Errorf("expected method 'message/stream', got %q", rpcReq.Method)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("expected Accept 'text/event-stream', got %q", r.Header.Get("Accept"))
		}

		// Send SSE events
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// Working status
		workingData, _ := json.Marshal(map[string]any{
			"result": a2a.TaskStatusUpdateEvent{
				TaskID: "stream-task-1",
				Status: a2a.TaskStatus{
					State:     a2a.TaskStateWorking,
					Timestamp: time.Now(),
				},
			},
		})
		fmt.Fprintf(w, "event: status_update\ndata: %s\n\n", workingData)
		flusher.Flush()

		// Completed status with message
		completedData, _ := json.Marshal(map[string]any{
			"result": a2a.TaskStatusUpdateEvent{
				TaskID: "stream-task-1",
				Status: a2a.TaskStatus{
					State: a2a.TaskStateCompleted,
					Message: &a2a.Message{
						Role:  "agent",
						Parts: []a2a.Part{a2a.TextPart{Text: "streamed response"}},
					},
					Timestamp: time.Now(),
				},
			},
		})
		fmt.Fprintf(w, "event: status_update\ndata: %s\n\n", completedData)
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "stream-test",
		URL:  server.URL,
	}, nil)

	tool := &A2ATool{
		name:      "a2a_stream_test",
		agentName: "stream-test",
		skillID:   "skill1",
		client:    client,
		streaming: true,
	}

	result, err := tool.Run(context.Background(), map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result["status"] != "completed" {
		t.Errorf("expected status 'completed', got %v", result["status"])
	}
	if result["response"] != "streamed response" {
		t.Errorf("expected response 'streamed response', got %v", result["response"])
	}
	if result["task_id"] != "stream-task-1" {
		t.Errorf("expected task_id 'stream-task-1', got %v", result["task_id"])
	}
}

func TestToolRunStreamingDisabled(t *testing.T) {
	var receivedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq a2a.JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)
		receivedMethod = rpcReq.Method

		resp := a2a.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      rpcReq.ID,
			Result: a2a.Task{
				ID:     "nonstream-task",
				Status: a2a.TaskStatus{State: a2a.TaskStateCompleted, Timestamp: time.Now()},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(A2AAgentConfig{
		Name: "nonstream-test",
		URL:  server.URL,
	}, nil)

	tool := &A2ATool{
		name:      "a2a_nonstream_test",
		agentName: "nonstream-test",
		skillID:   "skill1",
		client:    client,
		streaming: false,
	}

	_, err := tool.Run(context.Background(), map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if receivedMethod != "message/send" {
		t.Errorf("expected method 'message/send', got %q", receivedMethod)
	}
}

func TestGenerateToolsStreamingFromCard(t *testing.T) {
	tests := []struct {
		name         string
		capabilities *a2a.AgentCapabilities
		wantStream   bool
	}{
		{"streaming true", &a2a.AgentCapabilities{Streaming: true}, true},
		{"streaming false", &a2a.AgentCapabilities{Streaming: false}, false},
		{"nil capabilities", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := &a2a.AgentCard{
				Name:         "test-agent",
				Capabilities: tt.capabilities,
				Skills: []a2a.Skill{
					{ID: "s1", Name: "Skill", Description: "A skill"},
				},
			}
			tools := GenerateTools("test", card, nil)
			if len(tools) != 1 {
				t.Fatalf("expected 1 tool, got %d", len(tools))
			}
			if tools[0].streaming != tt.wantStream {
				t.Errorf("streaming = %v, want %v", tools[0].streaming, tt.wantStream)
			}
		})
	}
}

func TestCollectStreamResultWithArtifacts(t *testing.T) {
	ch := make(chan StreamEvent, 3)
	ch <- StreamEvent{
		Type: "status_update",
		StatusUpdate: &a2a.TaskStatusUpdateEvent{
			TaskID: "art-task",
			Status: a2a.TaskStatus{State: a2a.TaskStateWorking},
		},
	}
	ch <- StreamEvent{
		Type: "artifact_update",
		ArtifactUpdate: &a2a.TaskArtifactUpdateEvent{
			TaskID:   "art-task",
			Artifact: a2a.Artifact{Name: "result", Description: "The result", Index: 0},
		},
	}
	ch <- StreamEvent{
		Type: "status_update",
		StatusUpdate: &a2a.TaskStatusUpdateEvent{
			TaskID: "art-task",
			Status: a2a.TaskStatus{
				State:   a2a.TaskStateCompleted,
				Message: &a2a.Message{Role: "agent", Parts: []a2a.Part{a2a.TextPart{Text: "done"}}},
			},
		},
	}
	close(ch)

	result, err := collectStreamResult(ch)
	if err != nil {
		t.Fatalf("collectStreamResult failed: %v", err)
	}
	if result["status"] != "completed" {
		t.Errorf("status = %v, want completed", result["status"])
	}
	if result["response"] != "done" {
		t.Errorf("response = %v, want done", result["response"])
	}
	if result["task_id"] != "art-task" {
		t.Errorf("task_id = %v, want art-task", result["task_id"])
	}
	artifacts, ok := result["artifacts"].([]any)
	if !ok || len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %v", result["artifacts"])
	}
}

func TestCollectStreamResultWithError(t *testing.T) {
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Error: fmt.Errorf("stream failed")}
	close(ch)

	result, err := collectStreamResult(ch)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if result["status"] != "error" {
		t.Errorf("status = %v, want error", result["status"])
	}
}
