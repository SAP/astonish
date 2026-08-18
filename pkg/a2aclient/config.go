package a2aclient

import (
	"time"

	"github.com/SAP/astonish/pkg/a2a"
)

// A2AAgentConfig holds configuration for connecting to a single remote A2A agent.
type A2AAgentConfig struct {
	Name            string            `json:"name" yaml:"name"`
	URL             string            `json:"url" yaml:"url"`                                               // Base URL of remote agent
	CredentialName  string            `json:"credential_name,omitempty" yaml:"credential_name,omitempty"`   // Reference to credential in store
	AuthType        string            `json:"auth_type,omitempty" yaml:"auth_type,omitempty"`               // bearer, api_key, oauth
	Enabled         *bool             `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Headers         map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`                   // Additional headers
	RefreshInterval time.Duration     `json:"-" yaml:"-"`                                                   // How often to re-fetch agent card
	Timeout         time.Duration     `json:"-" yaml:"-"`                                                   // Per-request timeout
	CachedCard      *a2a.AgentCard    `json:"-" yaml:"-"`                                                   // Cached agent card (not serialized)
}

// IsEnabled returns true if the agent is enabled (defaults to true if not set).
func (c *A2AAgentConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// A2AClientConfig holds configuration for all remote A2A agent connections.
type A2AClientConfig struct {
	Agents map[string]A2AAgentConfig `json:"agents" yaml:"agents"`
}
