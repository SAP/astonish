package a2a

import (
	"encoding/json"
	"reflect"
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

func TestBuildAgentCard_AdvertisesJSONRPCInterface(t *testing.T) {
	card := BuildAgentCard(AgentCardConfig{
		Name:    "Astonish",
		BaseURL: "https://astonish.example.com",
		Version: "1.0.0",
	}, nil)

	if card.ProtocolVersion != "1.0" {
		t.Fatalf("expected protocolVersion 1.0, got %q", card.ProtocolVersion)
	}
	if len(card.SupportedInterfaces) != 1 {
		t.Fatalf("expected exactly one supported interface, got %d", len(card.SupportedInterfaces))
	}
	iface := card.SupportedInterfaces[0]
	if iface.URL != "https://astonish.example.com/api/a2a" {
		t.Fatalf("interface URL = %q, want /api/a2a endpoint", iface.URL)
	}
	if iface.ProtocolBinding != "JSONRPC" {
		t.Fatalf("interface protocolBinding = %q, want JSONRPC", iface.ProtocolBinding)
	}
	if iface.ProtocolVersion != "1.0" {
		t.Fatalf("interface protocolVersion = %q, want 1.0", iface.ProtocolVersion)
	}

	// A v1.0 client selecting a transport must find a compatible interface.
	selected, err := card.SelectCompatibleInterface()
	if err != nil {
		t.Fatalf("SelectCompatibleInterface returned error: %v", err)
	}
	if selected.ProtocolBinding != "JSONRPC" {
		t.Fatalf("selected binding = %q, want JSONRPC", selected.ProtocolBinding)
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

func TestSkillsFromCapabilities(t *testing.T) {
	capabilities := CapabilitySet{
		Tools: []ToolCapability{
			{Name: "read_file", Description: "Reads files", Source: "internal"},
			{Name: "read_file", Description: "Reads files", Source: "internal"},
			{Name: "send_email", Description: "Sends email", Source: "Email Server"},
			{Name: "search_mail", Description: "Searches email", Source: "Email Server"},
			{Name: "query_data", Description: "Queries data", Source: "Analytics"},
		},
		RemoteAgents: []RemoteAgentCapability{
			{
				Name:        "Inventory Agent",
				Description: "Lists inventory by site.",
				Skills: []Skill{
					{Examples: []string{"List devices per site.", "List devices per site."}},
				},
			},
		},
	}

	skills := SkillsFromCapabilities(capabilities)
	ids := make([]string, len(skills))
	for i, skill := range skills {
		ids[i] = skill.ID
		if !reflect.DeepEqual(skill.InputModes, []string{"text/plain", "application/json"}) {
			t.Fatalf("skill %q input modes = %#v", skill.ID, skill.InputModes)
		}
		if !reflect.DeepEqual(skill.OutputModes, []string{"text/plain", "text/markdown", "application/json"}) {
			t.Fatalf("skill %q output modes = %#v", skill.ID, skill.OutputModes)
		}
	}
	wantIDs := []string{"a2a-inventory-agent", "astonish-core", "mcp-analytics", "mcp-email-server"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("skill IDs = %#v, want %#v", ids, wantIDs)
	}
	if got := skills[0].Examples; !reflect.DeepEqual(got, []string{"List devices per site."}) {
		t.Fatalf("remote examples = %#v", got)
	}

	empty := SkillsFromCapabilities(CapabilitySet{})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty skills = %#v, want non-nil empty slice", empty)
	}
}

func TestBuildAuthenticatedAgentCard(t *testing.T) {
	skills := []Skill{{ID: "astonish-core", Name: "Astonish Assistant"}}
	card := BuildAuthenticatedAgentCard(AgentCardConfig{
		Name:        "Astonish",
		Description: "generic",
		BaseURL:     "https://astonish.example.com",
		AuthMethods: []string{"bearer"},
	}, skills, "Assistant with 2 built-in tools, 1 MCP integration, and 1 connected agent.")

	if card.Description == "generic" {
		t.Fatal("expected authenticated description override")
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != "astonish-core" {
		t.Fatalf("authenticated skills = %#v", card.Skills)
	}
	if !card.Capabilities.SupportsAuthenticatedExtendedCard {
		t.Fatal("expected supportsAuthenticatedExtendedCard")
	}
}
