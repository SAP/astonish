package a2a

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

var (
	cardInputModes  = []string{"text/plain", "application/json"}
	cardOutputModes = []string{"text/plain", "text/markdown", "application/json"}
)

// ToolCapability describes a tool without exposing its implementation details.
type ToolCapability struct {
	Name        string
	Description string
	Source      string
}

// RemoteAgentCapability describes an enabled remote agent and its cached skills.
type RemoteAgentCapability struct {
	Name        string
	Description string
	Skills      []Skill
}

// CapabilitySet is the effective capability input used to build card skills.
type CapabilitySet struct {
	Tools        []ToolCapability
	RemoteAgents []RemoteAgentCapability
}

// SkillsFromCapabilities converts effective capabilities into stable card skills.
func SkillsFromCapabilities(capabilities CapabilitySet) []Skill {
	internalTools := make(map[string]ToolCapability)
	mcpTools := make(map[string]map[string]ToolCapability)
	mcpNames := make(map[string]string)
	remoteAgents := make(map[string]RemoteAgentCapability)

	for _, capability := range capabilities.Tools {
		capability.Name = strings.TrimSpace(capability.Name)
		if capability.Name == "" {
			continue
		}
		source := strings.TrimSpace(capability.Source)
		if source == "" || strings.EqualFold(source, "internal") {
			internalTools[capability.Name] = capability
			continue
		}
		key := strings.ToLower(source)
		if mcpTools[key] == nil {
			mcpTools[key] = make(map[string]ToolCapability)
			mcpNames[key] = source
		}
		mcpTools[key][capability.Name] = capability
	}

	for _, capability := range capabilities.RemoteAgents {
		capability.Name = strings.TrimSpace(capability.Name)
		if capability.Name == "" {
			continue
		}
		remoteAgents[strings.ToLower(capability.Name)] = capability
	}

	skills := make([]Skill, 0, 1+len(mcpTools)+len(remoteAgents))
	if len(internalTools) > 0 {
		names := sortedCapabilityNames(internalTools)
		skills = append(skills, newCardSkill(
			"astonish-core",
			"Astonish Assistant",
			fmt.Sprintf("Uses built-in tools for: %s.", strings.Join(names, ", ")),
			[]string{"assistant", "tools"},
			names,
		))
	}

	for key, tools := range mcpTools {
		source := mcpNames[key]
		names := sortedCapabilityNames(tools)
		skills = append(skills, newCardSkill(
			"mcp-"+capabilitySlug(source),
			source,
			fmt.Sprintf("Uses the %s MCP integration for: %s.", source, strings.Join(names, ", ")),
			[]string{"mcp", source},
			names,
		))
	}

	for _, capability := range remoteAgents {
		examples := make([]string, 0)
		for _, skill := range capability.Skills {
			examples = append(examples, skill.Examples...)
		}
		description := strings.TrimSpace(capability.Description)
		if description == "" {
			description = "Delegates tasks to the connected " + capability.Name + " agent."
		}
		skills = append(skills, newCardSkill(
			"a2a-"+capabilitySlug(capability.Name),
			capability.Name,
			description,
			[]string{"a2a"},
			deduplicateStrings(examples),
		))
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].ID < skills[j].ID })
	return skills
}

// SummarizeCapabilities returns a safe summary of an effective capability set.
func SummarizeCapabilities(capabilities CapabilitySet) string {
	internal := 0
	mcpSources := make(map[string]struct{})
	remoteAgents := make(map[string]struct{})
	for _, capability := range capabilities.Tools {
		if strings.TrimSpace(capability.Name) == "" {
			continue
		}
		source := strings.TrimSpace(capability.Source)
		if source == "" || strings.EqualFold(source, "internal") {
			internal++
		} else {
			mcpSources[strings.ToLower(source)] = struct{}{}
		}
	}
	for _, capability := range capabilities.RemoteAgents {
		if name := strings.TrimSpace(capability.Name); name != "" {
			remoteAgents[strings.ToLower(name)] = struct{}{}
		}
	}
	return fmt.Sprintf("Assistant with %d built-in tools, %d MCP integrations, and %d connected agents.", internal, len(mcpSources), len(remoteAgents))
}

// BuildAuthenticatedAgentCard builds the tenant-specific card returned after authentication.
func BuildAuthenticatedAgentCard(cfg AgentCardConfig, skills []Skill, description string) *AgentCard {
	card := BuildAgentCard(cfg, skills)
	if description = strings.TrimSpace(description); description != "" {
		card.Description = description
	}
	card.Capabilities.SupportsAuthenticatedExtendedCard = true
	return card
}

func newCardSkill(id, name, description string, tags, examples []string) Skill {
	return Skill{
		ID:          id,
		Name:        name,
		Description: description,
		Tags:        deduplicateStrings(tags),
		Examples:    deduplicateStrings(examples),
		InputModes:  append([]string(nil), cardInputModes...),
		OutputModes: append([]string(nil), cardOutputModes...),
	}
}

func sortedCapabilityNames(capabilities map[string]ToolCapability) []string {
	names := make([]string, 0, len(capabilities))
	for name := range capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func deduplicateStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func capabilitySlug(value string) string {
	var slug strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if separator && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(r)
			separator = false
			continue
		}
		separator = true
	}
	if slug.Len() == 0 {
		return "capability"
	}
	return slug.String()
}

// AgentCardConfig holds configuration for building an Agent Card.
type AgentCardConfig struct {
	Name        string
	Description string
	BaseURL     string
	Version     string
	AuthMethods []string // e.g., ["bearer", "api_key"]
}

// BuildAgentCard constructs an Agent Card from configuration and skills.
func BuildAgentCard(cfg AgentCardConfig, skills []Skill) *AgentCard {
	card := &AgentCard{
		Name:            cfg.Name,
		Description:     cfg.Description,
		URL:             cfg.BaseURL + "/api/a2a",
		Version:         cfg.Version,
		ProtocolVersion: "1.0",
		SupportedInterfaces: []AgentInterface{
			{
				URL:             cfg.BaseURL + "/api/a2a",
				ProtocolBinding: "JSONRPC",
				ProtocolVersion: "1.0",
			},
		},
		Provider: &AgentProvider{
			Organization: "Astonish",
			URL:          cfg.BaseURL,
		},
		Capabilities: &AgentCapabilities{
			Streaming:                         true,
			PushNotifications:                 true,
			StateTransitionHistory:            true,
			SupportsAuthenticatedExtendedCard: true,
		},
		DefaultInputModes:  []string{"text/plain", "application/json"},
		DefaultOutputModes: []string{"text/plain", "text/markdown", "application/json"},
		Skills:             skills,
	}

	// Build security schemes from configured auth methods
	card.SecuritySchemes = make(map[string]SecurityScheme)
	card.Security = make([]map[string][]string, 0)

	for _, method := range cfg.AuthMethods {
		switch method {
		case "bearer":
			card.SecuritySchemes["bearerAuth"] = SecurityScheme{
				Type:   "http",
				Scheme: "bearer",
			}
			card.Security = append(card.Security, map[string][]string{"bearerAuth": {}})
		case "api_key":
			card.SecuritySchemes["apiKeyAuth"] = SecurityScheme{
				Type: "apiKey",
				In:   "header",
				Name: "X-API-Key",
			}
			card.Security = append(card.Security, map[string][]string{"apiKeyAuth": {}})
		}
	}

	if len(card.SecuritySchemes) == 0 {
		// Default: both bearer and API key
		card.SecuritySchemes["bearerAuth"] = SecurityScheme{Type: "http", Scheme: "bearer"}
		card.SecuritySchemes["apiKeyAuth"] = SecurityScheme{Type: "apiKey", In: "header", Name: "X-API-Key"}
		card.Security = []map[string][]string{
			{"bearerAuth": {}},
			{"apiKeyAuth": {}},
		}
	}

	return card
}
