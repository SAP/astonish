package oauthserver

import "fmt"

const (
	// ScopeToolExecute grants access to the protected MCP endpoint.
	ScopeToolExecute = "tool:execute"
	// ScopeA2A grants access to the inbound A2A protocol endpoint.
	ScopeA2A = "a2a"
	// ScopeChat grants access to the conversational chat capability.
	ScopeChat = "chat"
)

// ScopeDefinition is a capability administrators can grant to an OAuth client.
type ScopeDefinition struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var capabilityScopes = []ScopeDefinition{
	{Value: ScopeToolExecute, Label: "MCP access", Description: "Allows access to the protected MCP tool endpoint."},
	{Value: ScopeA2A, Label: "A2A access", Description: "Allows access to the inbound A2A protocol endpoint."},
	{Value: ScopeChat, Label: "Chat access", Description: "Allows conversational access to Astonish chat."},
}

// ScopeDefinitions returns the supported client-grant catalog in display and
// token-claim order.
func ScopeDefinitions() []ScopeDefinition {
	return append([]ScopeDefinition(nil), capabilityScopes...)
}

func normalizeClientScopes(scopes []string) ([]string, error) {
	selected := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if !isCapabilityScope(scope) {
			return nil, fmt.Errorf("unsupported OAuth permission %q", scope)
		}
		selected[scope] = true
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("at least one OAuth permission is required")
	}
	out := make([]string, 0, len(selected))
	for _, definition := range capabilityScopes {
		if selected[definition.Value] {
			out = append(out, definition.Value)
		}
	}
	return out, nil
}

func isCapabilityScope(scope string) bool {
	for _, definition := range capabilityScopes {
		if definition.Value == scope {
			return true
		}
	}
	return false
}
