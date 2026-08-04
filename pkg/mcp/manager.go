package mcp

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
)

// Manager handles MCP server lifecycle and toolset creation
type Manager struct {
	config        *config.MCPConfig
	toolsets      []tool.Toolset
	namedToolsets []NamedToolset
	transports    []mcp.Transport // Track transports for cleanup
	initResults   []InitResult    // Track initialization results per server
	// resolver expands {{CREDENTIAL:name:field}} placeholders in MCP env at
	// process start. Nil disables resolution (plain env still works).
	resolver credentials.CredentialResolver
}

// NamedToolset wraps an ADK toolset with its server name and stderr buffer
type NamedToolset struct {
	Name    string
	Toolset tool.Toolset
	Stderr  *bytes.Buffer
}

// InitResult tracks the result of initializing a single MCP server
type InitResult struct {
	Name    string
	Success bool
	Error   string
}

// NewManager creates a new MCP manager
func NewManager() (*Manager, error) {
	cfg, err := config.LoadMCPConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load MCP config: %w", err)
	}

	m := &Manager{
		config:        cfg,
		toolsets:      make([]tool.Toolset, 0),
		namedToolsets: make([]NamedToolset, 0),
		transports:    make([]mcp.Transport, 0),
		initResults:   make([]InitResult, 0),
	}
	// Personal-mode default: file credential store so MCP env placeholders
	// resolve without callers wiring a resolver explicitly.
	if configDir, dirErr := config.GetConfigDir(); dirErr == nil {
		if cs, openErr := credentials.Open(configDir); openErr == nil {
			m.resolver = cs
		}
	}
	return m, nil
}

// NewManagerFromConfig creates a new MCP manager with an explicit config.
// Used in platform mode where MCP server configs come from the database
// rather than the local filesystem.
func NewManagerFromConfig(cfg *config.MCPConfig) *Manager {
	return &Manager{
		config:        cfg,
		toolsets:      make([]tool.Toolset, 0),
		namedToolsets: make([]NamedToolset, 0),
		transports:    make([]mcp.Transport, 0),
		initResults:   make([]InitResult, 0),
	}
}

// SetCredentialResolver registers a credential store used to expand
// {{CREDENTIAL:name:field}} placeholders in MCP server env values when the
// process is started. The config itself is left unchanged (placeholders stay
// in the stored config).
func (m *Manager) SetCredentialResolver(resolver credentials.CredentialResolver) {
	if m == nil {
		return
	}
	m.resolver = resolver
}

// InitializeToolsets creates ADK mcptoolset instances for all configured servers
func (m *Manager) InitializeToolsets(ctx context.Context) error {
	if len(m.config.MCPServers) == 0 {
		slog.Info("no MCP servers configured", "component", "mcp")
		return nil
	}

	for serverName, serverConfig := range m.config.MCPServers {
		// Skip disabled servers
		if !serverConfig.IsEnabled() {
			slog.Info("skipping disabled MCP server", "component", "mcp", "server", serverName)
			m.initResults = append(m.initResults, InitResult{
				Name:    serverName,
				Success: false,
				Error:   "Server is disabled",
			})
			continue
		}

		transport, stderrBuf, err := m.createTransportForServer(serverName, serverConfig)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to create transport: %v (Stderr: %s)", err, DiagnosticStderr(stderrBuf))
			slog.Warn("failed to create transport for MCP server", FailureLogAttrs(serverName, serverConfig, err, stderrBuf)...)
			m.initResults = append(m.initResults, InitResult{
				Name:    serverName,
				Success: false,
				Error:   errMsg,
			})
			continue
		}

		// Create ADK mcptoolset - it handles everything automatically
		toolset, err := mcptoolset.New(mcptoolset.Config{
			Transport: transport,
			// ToolFilter can be added here if needed to filter specific tools
		})
		if err != nil {
			errMsg := fmt.Sprintf("Failed to create toolset: %v (Stderr: %s)", err, DiagnosticStderr(stderrBuf))
			slog.Warn("failed to create toolset for MCP server", FailureLogAttrs(serverName, serverConfig, err, stderrBuf)...)
			m.initResults = append(m.initResults, InitResult{
				Name:    serverName,
				Success: false,
				Error:   errMsg,
			})
			continue
		}

		m.toolsets = append(m.toolsets, toolset)
		m.namedToolsets = append(m.namedToolsets, NamedToolset{
			Name:    serverName,
			Toolset: toolset,
			Stderr:  stderrBuf,
		})
		m.transports = append(m.transports, transport)
		m.initResults = append(m.initResults, InitResult{
			Name:    serverName,
			Success: true,
		})
		slog.Info("initialized MCP server", "component", "mcp", "server", serverName)
	}

	return nil
}

// InitializeSingleToolset creates ADK mcptoolset for a specific server by name
// Returns the named toolset if successful, nil if failed
func (m *Manager) InitializeSingleToolset(ctx context.Context, serverName string) (*NamedToolset, error) {
	serverConfig, exists := m.config.MCPServers[serverName]
	if !exists {
		return nil, fmt.Errorf("server '%s' not found in config", serverName)
	}

	// Check if server is enabled
	if !serverConfig.IsEnabled() {
		return nil, fmt.Errorf("server '%s' is disabled", serverName)
	}

	transport, stderrBuf, err := m.createTransportForServer(serverName, serverConfig)
	if err != nil {
		slog.Warn("failed to create transport for MCP server", FailureLogAttrs(serverName, serverConfig, err, stderrBuf)...)
		return nil, fmt.Errorf("failed to create transport: %w (Stderr: %s)", err, DiagnosticStderr(stderrBuf))
	}

	// Create ADK mcptoolset
	toolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: transport,
	})
	if err != nil {
		slog.Warn("failed to create toolset for MCP server", FailureLogAttrs(serverName, serverConfig, err, stderrBuf)...)
		return nil, fmt.Errorf("failed to create toolset: %w (Stderr: %s)", err, DiagnosticStderr(stderrBuf))
	}

	namedToolset := &NamedToolset{
		Name:    serverName,
		Toolset: toolset,
		Stderr:  stderrBuf,
	}

	m.toolsets = append(m.toolsets, toolset)
	m.namedToolsets = append(m.namedToolsets, *namedToolset)
	slog.Info("initialized single MCP server", "component", "mcp", "server", serverName)

	return namedToolset, nil
}

// GetToolsets returns all initialized MCP toolsets
func (m *Manager) GetToolsets() []tool.Toolset {
	return m.toolsets
}

// GetNamedToolsets returns all toolsets with their server names
func (m *Manager) GetNamedToolsets() []NamedToolset {
	return m.namedToolsets
}

// GetInitResults returns the initialization results for all servers
func (m *Manager) GetInitResults() []InitResult {
	return m.initResults
}

// InitializeSelectiveToolsets creates ADK mcptoolset instances only for specified servers
// This is more efficient when a flow only needs a subset of configured MCP servers
func (m *Manager) InitializeSelectiveToolsets(ctx context.Context, serverNames []string) error {
	if len(serverNames) == 0 {
		slog.Info("no MCP servers requested for this flow", "component", "mcp")
		return nil
	}

	// Create a set for fast lookup
	needed := make(map[string]bool)
	for _, name := range serverNames {
		needed[name] = true
	}

	for serverName, serverConfig := range m.config.MCPServers {
		if !needed[serverName] {
			continue // Skip servers not needed for this flow
		}

		// Skip disabled servers
		if !serverConfig.IsEnabled() {
			slog.Info("skipping disabled MCP server", "component", "mcp", "server", serverName)
			continue
		}

		transport, stderrBuf, err := m.createTransportForServer(serverName, serverConfig)
		if err != nil {
			slog.Warn("failed to create transport for selective MCP server", FailureLogAttrs(serverName, serverConfig, err, stderrBuf)...)
			continue
		}

		toolset, err := mcptoolset.New(mcptoolset.Config{
			Transport: transport,
		})
		if err != nil {
			slog.Warn("failed to create toolset for selective MCP server", FailureLogAttrs(serverName, serverConfig, err, stderrBuf)...)
			continue
		}

		m.toolsets = append(m.toolsets, toolset)
		m.namedToolsets = append(m.namedToolsets, NamedToolset{
			Name:    serverName,
			Toolset: toolset,
			Stderr:  stderrBuf,
		})
		m.transports = append(m.transports, transport)
		slog.Info("initialized MCP server for flow", "component", "mcp", "server", serverName)
	}

	slog.Info("selectively initialized MCP servers", "component", "mcp", "initialized", len(m.toolsets), "requested", len(serverNames))
	return nil
}

// Cleanup closes all MCP transports and clears the manager state
// Should be called when the flow run completes
func (m *Manager) Cleanup() {
	for i, transport := range m.transports {
		if closer, ok := transport.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				slog.Warn("failed to close transport", "component", "mcp", "index", i, "error", err)
			}
		}
	}
	m.transports = nil
	m.toolsets = nil
	m.namedToolsets = nil
	slog.Info("MCP manager cleaned up", "component", "mcp")
}

// createTransportForServer resolves credential placeholders in env, then builds
// the transport. serverName is used only in error messages.
func (m *Manager) createTransportForServer(serverName string, cfg config.MCPServerConfig) (mcp.Transport, *bytes.Buffer, error) {
	resolved, err := ResolveMCPServerConfig(cfg, m.resolver)
	if err != nil {
		return nil, nil, fmt.Errorf("MCP server %q credential resolution: %w", serverName, err)
	}
	return createTransport(resolved)
}

// createTransport creates the appropriate MCP transport based on configuration.
// cfg.Env must already be resolved (no credential placeholders).
func createTransport(cfg config.MCPServerConfig) (mcp.Transport, *bytes.Buffer, error) {
	// Default to stdio if not specified
	transportType := cfg.Transport
	if transportType == "" {
		transportType = "stdio"
	}

	switch transportType {
	case "stdio":
		return createStdioTransport(cfg)
	case "sse":
		return createSSETransport(cfg)
	default:
		return nil, nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}
}

// createStdioTransport creates a CommandTransport for stdio-based MCP servers
func createStdioTransport(cfg config.MCPServerConfig) (mcp.Transport, *bytes.Buffer, error) {
	if cfg.Command == "" {
		return nil, nil, fmt.Errorf("command is required for stdio transport")
	}

	// Create the command
	cmd := exec.Command(cfg.Command, cfg.Args...)

	// Buffer to capture stderr
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	// Set environment variables
	if len(cfg.Env) > 0 {
		// Start with current environment
		cmd.Env = append(cmd.Env, cmd.Environ()...)

		// Add custom environment variables
		for key, value := range cfg.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// Create CommandTransport - ADK will manage the subprocess lifecycle
	return &mcp.CommandTransport{
		Command: cmd,
	}, &stderrBuf, nil
}

// createSSETransport creates an SSE transport for remote MCP servers
func createSSETransport(cfg config.MCPServerConfig) (mcp.Transport, *bytes.Buffer, error) {
	if cfg.URL == "" {
		return nil, nil, fmt.Errorf("URL is required for SSE transport")
	}

	// Create SSE client transport
	return &mcp.SSEClientTransport{
		Endpoint: cfg.URL,
		// HTTPClient can be customized here if needed (e.g., for auth)
	}, nil, nil
}

// Helper to safely get stderr string
func GetStderr(buf *bytes.Buffer) string {
	if buf == nil {
		return ""
	}
	out := buf.String()
	if out == "" {
		return "no stderr output"
	}
	return out
}

// GetConfig returns the MCP configuration
func (m *Manager) GetConfig() *config.MCPConfig {
	return m.config
}
