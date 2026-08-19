package apps

import (
	"regexp"
	"strings"
	"time"
)

// VisualApp represents a saved generative-UI app.
type VisualApp struct {
	Name        string       `json:"name" yaml:"name"`
	Description string       `json:"description" yaml:"description"`
	Code        string       `json:"code" yaml:"code"`
	Version     int          `json:"version" yaml:"version"`
	DataSources []DataSource `json:"dataSources,omitempty" yaml:"data_sources,omitempty"`
	CreatedAt   time.Time    `json:"createdAt" yaml:"created_at"`
	UpdatedAt   time.Time    `json:"updatedAt" yaml:"updated_at"`
	SessionID   string       `json:"sessionId,omitempty" yaml:"session_id,omitempty"`
}

// DataSource defines a data feed for a running app.
// Supported types:
//   - "mcp_tool"  — invoke an MCP server tool (config: server, tool)
//   - "http_api"  — make an HTTP request (config: url, method)
//   - "a2a_agent" — send a message to a remote A2A agent (config: agent)
//   - "static"    — return static data from config
type DataSource struct {
	ID       string         `json:"id" yaml:"id"`
	Type     string         `json:"type" yaml:"type"`     // "mcp_tool", "http_api", "a2a_agent", "static"
	Config   map[string]any `json:"config" yaml:"config"`
	Interval string         `json:"interval,omitempty" yaml:"interval,omitempty"`
}

// AppListItem is the lightweight representation returned by list operations.
type AppListItem struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Version     int       `json:"version"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// slugify converts a title to a filesystem-safe name.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = nonAlphanumRe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "untitled_app"
	}
	return s
}
