package api

import "testing"

func TestResolveAgentCardURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "base URL without trailing slash",
			input:    "https://example.com",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "base URL with trailing slash",
			input:    "https://example.com/",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "base URL with path",
			input:    "https://example.com/agents/myagent",
			expected: "https://example.com/agents/myagent/.well-known/agent-card.json",
		},
		{
			name:     "base URL with path and trailing slash",
			input:    "https://example.com/agents/myagent/",
			expected: "https://example.com/agents/myagent/.well-known/agent-card.json",
		},
		{
			name:     "already contains well-known path",
			input:    "https://example.com/.well-known/agent-card.json",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "already contains well-known path with trailing slash",
			input:    "https://example.com/.well-known/agent-card.json/",
			expected: "https://example.com/.well-known/agent-card.json",
		},
		{
			name:     "well-known path under a subpath",
			input:    "https://example.com/v1/.well-known/agent-card.json",
			expected: "https://example.com/v1/.well-known/agent-card.json",
		},
		{
			name:     "multiple trailing slashes",
			input:    "https://example.com///",
			expected: "https://example.com/.well-known/agent-card.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAgentCardURL(tt.input)
			if got != tt.expected {
				t.Errorf("resolveAgentCardURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
