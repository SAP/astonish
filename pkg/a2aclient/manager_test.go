package a2aclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/a2a"
)

func TestManagerInitialize(t *testing.T) {
	card := a2a.AgentCard{
		Name:        "remote-agent",
		Description: "A remote test agent",
		URL:         "http://example.com",
		Skills: []a2a.Skill{
			{ID: "s1", Name: "Skill 1"},
			{ID: "s2", Name: "Skill 2"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(card)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	disabled := false
	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"agent1": {
				Name: "agent1",
				URL:  server.URL,
			},
			"agent2": {
				Name:    "agent2",
				URL:     server.URL,
				Enabled: &disabled,
			},
		},
	}

	mgr := NewManager(cfg)
	ctx := context.Background()
	err := mgr.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// agent1 should be connected, agent2 should be disabled
	client, err := mgr.GetClient("agent1")
	if err != nil {
		t.Fatalf("GetClient('agent1') failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client for agent1")
	}

	_, err = mgr.GetClient("agent2")
	if err == nil {
		t.Fatal("expected error for disabled agent2, got nil")
	}
}

func TestManagerGetAgentCard(t *testing.T) {
	card := a2a.AgentCard{
		Name:        "card-agent",
		Description: "Card test",
		Skills: []a2a.Skill{
			{ID: "cs1", Name: "Card Skill"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	}))
	defer server.Close()

	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"card-agent": {
				Name: "card-agent",
				URL:  server.URL,
			},
		},
	}

	mgr := NewManager(cfg)
	ctx := context.Background()
	mgr.Initialize(ctx)

	result, err := mgr.GetAgentCard("card-agent")
	if err != nil {
		t.Fatalf("GetAgentCard failed: %v", err)
	}
	if result.Name != "card-agent" {
		t.Errorf("expected card name 'card-agent', got %q", result.Name)
	}

	_, err = mgr.GetAgentCard("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent, got nil")
	}
}

func TestManagerListAgents(t *testing.T) {
	card := a2a.AgentCard{
		Name:        "list-agent",
		Description: "List test agent",
		Skills: []a2a.Skill{
			{ID: "ls1", Name: "List Skill 1"},
			{ID: "ls2", Name: "List Skill 2"},
			{ID: "ls3", Name: "List Skill 3"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	}))
	defer server.Close()

	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"agent-a": {
				Name: "agent-a",
				URL:  server.URL,
			},
			"agent-b": {
				Name: "agent-b",
				URL:  server.URL,
			},
		},
	}

	mgr := NewManager(cfg)
	ctx := context.Background()
	mgr.Initialize(ctx)

	agents := mgr.ListAgents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	// Check that all agents are connected with 3 skills
	for _, info := range agents {
		if !info.Connected {
			t.Errorf("expected agent %q to be connected", info.Name)
		}
		if info.SkillCount != 3 {
			t.Errorf("expected 3 skills for %q, got %d", info.Name, info.SkillCount)
		}
		if info.Description != "List test agent" {
			t.Errorf("expected description 'List test agent' for %q, got %q", info.Name, info.Description)
		}
	}
}

func TestManagerRefreshCard(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		card := a2a.AgentCard{
			Name:        "refresh-agent",
			Description: "Refreshed",
			Skills: []a2a.Skill{
				{ID: "rs1", Name: "Refresh Skill"},
			},
		}
		if callCount > 1 {
			card.Skills = append(card.Skills, a2a.Skill{ID: "rs2", Name: "New Skill"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	}))
	defer server.Close()

	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"refresh-agent": {
				Name: "refresh-agent",
				URL:  server.URL,
			},
		},
	}

	mgr := NewManager(cfg)
	ctx := context.Background()
	mgr.Initialize(ctx)

	// Initial card should have 1 skill
	card, err := mgr.GetAgentCard("refresh-agent")
	if err != nil {
		t.Fatalf("GetAgentCard failed: %v", err)
	}
	if len(card.Skills) != 1 {
		t.Errorf("expected 1 skill initially, got %d", len(card.Skills))
	}

	// After refresh, should have 2 skills
	refreshed, err := mgr.RefreshCard(ctx, "refresh-agent")
	if err != nil {
		t.Fatalf("RefreshCard failed: %v", err)
	}
	if len(refreshed.Skills) != 2 {
		t.Errorf("expected 2 skills after refresh, got %d", len(refreshed.Skills))
	}
}

func TestManagerInitializeWithFailedCard(t *testing.T) {
	// Server that returns errors for agent card
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"failing-agent": {
				Name: "failing-agent",
				URL:  server.URL,
			},
		},
	}

	mgr := NewManager(cfg)
	ctx := context.Background()

	// Initialize should succeed even if card fetch fails
	err := mgr.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize should not fail even with card fetch errors: %v", err)
	}

	// Client should still exist
	_, err = mgr.GetClient("failing-agent")
	if err != nil {
		t.Fatalf("GetClient should work even without card: %v", err)
	}

	// But card should not be available
	_, err = mgr.GetAgentCard("failing-agent")
	if err == nil {
		t.Fatal("expected error for missing card, got nil")
	}
}

func TestNewManagerNilConfig(t *testing.T) {
	mgr := NewManager(nil)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}

	agents := mgr.ListAgents()
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestNewManagerFromConfig(t *testing.T) {
	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"test": {Name: "test", URL: "http://localhost"},
		},
	}
	mgr := NewManagerFromConfig(cfg)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
}

func TestManagerSetCredentialResolver(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		card := a2a.AgentCard{Name: "auth-agent"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	}))
	defer server.Close()

	cfg := &A2AClientConfig{
		Agents: map[string]A2AAgentConfig{
			"auth-agent": {
				Name:           "auth-agent",
				URL:            server.URL,
				CredentialName: "my-cred",
			},
		},
	}

	resolver := &mockResolver{
		headerKey:   "Authorization",
		headerValue: "Bearer mgr-token",
	}

	mgr := NewManager(cfg)
	mgr.SetCredentialResolver(resolver)

	ctx := context.Background()
	mgr.Initialize(ctx)

	if receivedAuth != "Bearer mgr-token" {
		t.Errorf("expected auth 'Bearer mgr-token', got %q", receivedAuth)
	}
}

func TestManagerCleanup(t *testing.T) {
	mgr := NewManager(nil)
	// Should not panic
	mgr.Cleanup()
}
