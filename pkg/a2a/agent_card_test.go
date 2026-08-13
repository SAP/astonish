package a2a

import (
	"encoding/json"
	"testing"
)

func TestBuildAgentCard(t *testing.T) {
	skills := []Skill{
		{ID: "code-review", Name: "Code Review", Description: "Reviews code for quality"},
	}
	card := BuildAgentCard(AgentCardConfig{
		Name:        "Astonish",
		Description: "AI agent platform",
		BaseURL:     "https://astonish.example.com",
		Version:     "1.0.0",
		AuthMethods: []string{"bearer", "api_key"},
	}, skills)

	if card.Name != "Astonish" {
		t.Fatalf("expected name 'Astonish', got %q", card.Name)
	}
	if card.URL != "https://astonish.example.com/api/a2a" {
		t.Fatalf("expected URL with /api/a2a suffix, got %q", card.URL)
	}
	if !card.Capabilities.Streaming {
		t.Fatal("expected streaming capability")
	}
	if len(card.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(card.Skills))
	}
	if len(card.SecuritySchemes) != 2 {
		t.Fatalf("expected 2 security schemes, got %d", len(card.SecuritySchemes))
	}

	// Verify it marshals to valid JSON
	_, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("failed to marshal agent card: %v", err)
	}
}

func TestBuildAgentCard_DefaultAuth(t *testing.T) {
	card := BuildAgentCard(AgentCardConfig{
		Name:    "Test",
		BaseURL: "http://localhost:9393",
	}, nil)

	// Should default to both bearer and api_key
	if len(card.SecuritySchemes) != 2 {
		t.Fatalf("expected 2 default security schemes, got %d", len(card.SecuritySchemes))
	}
}
