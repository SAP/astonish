package store

import (
	"context"
	"encoding/json"
	"time"
)

// MCPServer represents an MCP server configuration stored in the database.
// This is the multi-tenant equivalent of config.MCPServerConfig — stored per-org
// or per-team in PostgreSQL rather than in a local JSON file.
type MCPServer struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Transport   string            `json:"transport"`              // "stdio" or "sse"
	URL         string            `json:"url,omitempty"`          // For SSE transport
	Enabled     *bool             `json:"enabled,omitempty"`      // nil defaults to true
	CachedTools json.RawMessage   `json:"cached_tools,omitempty"` // Tool declarations from last refresh
	CreatedBy   string            `json:"created_by,omitempty"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
}

// IsEnabled returns whether the MCP server is enabled.
// A nil Enabled pointer defaults to true.
func (m *MCPServer) IsEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

// MCPServerStore manages MCP server configurations.
//
// In platform mode, this can be org-level (shared across all teams)
// or team-level (specific to one team, overrides org by name).
type MCPServerStore interface {
	// List returns all MCP server configurations.
	List(ctx context.Context) ([]MCPServer, error)

	// Get retrieves an MCP server by name.
	Get(ctx context.Context, name string) (*MCPServer, error)

	// Save creates or updates an MCP server configuration (upsert by name).
	Save(ctx context.Context, server *MCPServer) error

	// Delete removes an MCP server configuration by name.
	Delete(ctx context.Context, name string) error

	// UpdateCachedTools updates only the cached_tools column for a server.
	// This is called after async tool discovery completes.
	UpdateCachedTools(ctx context.Context, name string, tools json.RawMessage) error
}
