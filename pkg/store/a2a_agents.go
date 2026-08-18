package store

import (
	"context"
	"encoding/json"
	"time"
)

// A2AAgent represents a remote A2A agent connection stored in the database.
// This is the multi-tenant equivalent of config.A2AAgentFileConfig — stored per-org
// or per-team in PostgreSQL rather than in a local JSON file.
type A2AAgent struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	URL            string            `json:"url"`                        // Base URL of remote agent
	CredentialName string            `json:"credential_name,omitempty"`  // Reference to credential in store
	AuthType       string            `json:"auth_type,omitempty"`        // bearer, api_key, oauth
	Enabled        *bool             `json:"enabled,omitempty"`          // nil defaults to true
	Headers        map[string]string `json:"headers,omitempty"`          // Additional headers
	Timeout        string            `json:"timeout,omitempty"`          // Duration string e.g. "30s", "2m"
	CachedCard     json.RawMessage   `json:"cached_card,omitempty"`     // Serialized AgentCard from last refresh
	CachedSkills   json.RawMessage   `json:"cached_skills,omitempty"`   // Cached skill list for tool generation
	CreatedBy      string            `json:"created_by,omitempty"`
	CreatedAt      time.Time         `json:"created_at,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at,omitempty"`
}

// IsEnabled returns whether the A2A agent is enabled.
// A nil Enabled pointer defaults to true.
func (a *A2AAgent) IsEnabled() bool {
	if a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

// A2AAgentStore manages remote A2A agent connection configurations.
//
// In platform mode, this can be org-level (shared across all teams)
// or team-level (specific to one team, overrides org by name).
type A2AAgentStore interface {
	// List returns all A2A agent configurations.
	List(ctx context.Context) ([]A2AAgent, error)

	// Get retrieves an A2A agent by name.
	Get(ctx context.Context, name string) (*A2AAgent, error)

	// Save creates or updates an A2A agent configuration (upsert by name).
	Save(ctx context.Context, agent *A2AAgent) error

	// Delete removes an A2A agent configuration by name.
	Delete(ctx context.Context, name string) error

	// UpdateCachedCard updates only the cached_card and cached_skills columns for an agent.
	// This is called after async agent card discovery completes.
	UpdateCachedCard(ctx context.Context, name string, card json.RawMessage, skills json.RawMessage) error
}
