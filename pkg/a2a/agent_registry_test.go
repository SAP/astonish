package a2a

import "testing"

func TestInMemoryAgentRegistry_RegisterAndLookup(t *testing.T) {
	reg := NewInMemoryAgentRegistry()

	apiKey, err := reg.Register(RegisteredAgent{
		Name:         "TestAgent",
		LinkedUserID: "user-123",
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if apiKey == "" {
		t.Fatal("expected non-empty API key")
	}

	// Lookup by API key
	agent, err := reg.GetByAPIKey(apiKey)
	if err != nil {
		t.Fatalf("GetByAPIKey failed: %v", err)
	}
	if agent.Name != "TestAgent" {
		t.Fatalf("expected name 'TestAgent', got %q", agent.Name)
	}
	if agent.LinkedUserID != "user-123" {
		t.Fatalf("expected linked user 'user-123', got %q", agent.LinkedUserID)
	}
}

func TestInMemoryAgentRegistry_InvalidKey(t *testing.T) {
	reg := NewInMemoryAgentRegistry()
	_, _ = reg.Register(RegisteredAgent{Name: "Agent1"})

	_, err := reg.GetByAPIKey("invalid-key")
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestInMemoryAgentRegistry_RotateKey(t *testing.T) {
	reg := NewInMemoryAgentRegistry()
	oldKey, _ := reg.Register(RegisteredAgent{Name: "Agent1"})

	agent, _ := reg.GetByAPIKey(oldKey)
	newKey, err := reg.RotateKey(agent.ID)
	if err != nil {
		t.Fatalf("RotateKey failed: %v", err)
	}

	// Old key should no longer work
	_, err = reg.GetByAPIKey(oldKey)
	if err == nil {
		t.Fatal("old key should be invalid after rotation")
	}

	// New key should work
	_, err = reg.GetByAPIKey(newKey)
	if err != nil {
		t.Fatalf("new key should be valid: %v", err)
	}
}

func TestInMemoryAgentRegistry_Delete(t *testing.T) {
	reg := NewInMemoryAgentRegistry()
	apiKey, _ := reg.Register(RegisteredAgent{Name: "Agent1"})
	agent, _ := reg.GetByAPIKey(apiKey)

	if err := reg.Delete(agent.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err := reg.GetByAPIKey(apiKey)
	if err == nil {
		t.Fatal("expected error after agent deleted")
	}
}

func TestInMemoryAgentRegistry_List(t *testing.T) {
	reg := NewInMemoryAgentRegistry()
	_, _ = reg.Register(RegisteredAgent{Name: "Agent1"})
	_, _ = reg.Register(RegisteredAgent{Name: "Agent2"})

	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(list))
	}
}

func TestInMemoryAgentRegistry_RequiresName(t *testing.T) {
	reg := NewInMemoryAgentRegistry()
	_, err := reg.Register(RegisteredAgent{})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}
