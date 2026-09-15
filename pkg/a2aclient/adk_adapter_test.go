package a2aclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
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

func TestGetA2AToolsFromStoresUsesContextCredentialResolver(t *testing.T) {
	const credentialName = "device-agent"
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(a2a.AgentCard{
			Name: "devices",
			Skills: []a2a.Skill{{
				ID:          "devices-per-site",
				Name:        "Devices per site",
				Description: "Lists devices grouped by site",
			}},
		})
	}))
	defer server.Close()

	teamAgents := &stubA2AAgentStore{agents: []store.A2AAgent{{
		Name:           "devices",
		URL:            server.URL,
		CredentialName: credentialName,
	}}}
	creds := &stubA2ACredentialStore{credentials: map[string]*store.Credential{
		credentialName: {Type: store.CredBearer, Token: "tenant-token"},
	}}
	ctx := store.WithCredentialStore(context.Background(), creds)

	tools := GetA2AToolsFromStores(ctx, &store.A2AAgentStores{Team: teamAgents})
	if receivedAuth != "Bearer tenant-token" {
		t.Fatalf("agent card Authorization = %q, want tenant credential", receivedAuth)
	}
	if len(tools) != 1 || tools[0].Name() != "a2a_devices_devices_per_site" {
		t.Fatalf("tools = %v, want devices-per-site A2A tool", toolNames(tools))
	}
}

type stubA2AAgentStore struct {
	agents []store.A2AAgent
}

func (s *stubA2AAgentStore) List(context.Context) ([]store.A2AAgent, error) {
	return s.agents, nil
}
func (s *stubA2AAgentStore) Get(context.Context, string) (*store.A2AAgent, error) {
	return nil, nil
}
func (s *stubA2AAgentStore) Save(context.Context, *store.A2AAgent) error { return nil }
func (s *stubA2AAgentStore) Delete(context.Context, string) error        { return nil }
func (s *stubA2AAgentStore) UpdateCachedCard(context.Context, string, json.RawMessage, json.RawMessage) error {
	return nil
}

type stubA2ACredentialStore struct {
	credentials map[string]*store.Credential
}

func (s *stubA2ACredentialStore) Get(_ context.Context, name string) *store.Credential {
	return s.credentials[name]
}
func (s *stubA2ACredentialStore) Set(context.Context, string, *store.Credential) error {
	return nil
}
func (s *stubA2ACredentialStore) Remove(context.Context, string) error { return nil }
func (s *stubA2ACredentialStore) List(context.Context) map[string]store.CredentialType {
	return nil
}
func (s *stubA2ACredentialStore) Count(context.Context) int { return len(s.credentials) }
func (s *stubA2ACredentialStore) Resolve(_ context.Context, name string) (string, string, error) {
	return store.ResolveCredentialHeader(name, s.credentials[name], nil)
}
func (s *stubA2ACredentialStore) InvalidateToken(context.Context, string) {}
func (s *stubA2ACredentialStore) SetSecret(context.Context, string, string) error {
	return nil
}
func (s *stubA2ACredentialStore) SetSecretBatch(context.Context, map[string]string) error {
	return nil
}
func (s *stubA2ACredentialStore) GetSecret(context.Context, string) string { return "" }
func (s *stubA2ACredentialStore) RemoveSecret(context.Context, string) error {
	return nil
}
func (s *stubA2ACredentialStore) HasSecrets(context.Context) bool      { return false }
func (s *stubA2ACredentialStore) SecretCount(context.Context) int      { return 0 }
func (s *stubA2ACredentialStore) ListSecrets(context.Context) []string { return nil }
func (s *stubA2ACredentialStore) Reload(context.Context) error         { return nil }

func toolNames(tools []tool.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		names = append(names, candidate.Name())
	}
	return names
}

func TestGetA2AToolsNoConfig(t *testing.T) {
	// With no config, should return nil gracefully
	ctx := context.Background()
	tools := GetA2ATools(ctx, false)
	// May return nil or empty depending on file config — just ensure no panic
	_ = tools
}
