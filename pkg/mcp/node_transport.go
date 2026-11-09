package mcp

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/SAP/astonish/pkg/config"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NodeOperation is private daemon-to-sandbox dispatch, never an LLM tool.
const NodeOperation = "_astonish_mcp"

type NodeAction string

const (
	NodeDiscover NodeAction = "discover"
	NodeCall     NodeAction = "call"
)

// NodeRequest contains symbolic owner configuration, not resolved credentials.
// For remote transports Env carries HTTP headers, not process environment.
type NodeRequest struct {
	Action    NodeAction             `json:"action"`
	Config    config.MCPServerConfig `json:"config"`
	Tool      string                 `json:"tool,omitempty"`
	Arguments map[string]any         `json:"arguments,omitempty"`
}

// NewNodeTransport must only be called inside the sandbox node. The supplied
// HTTP client enforces origin restrictions and retains the node's network proxy.
// Credential expansion is the caller's responsibility.
func NewNodeTransport(cfg config.MCPServerConfig, client *http.Client) (sdk.Transport, *bytes.Buffer, error) {
	if !cfg.IsEnabled() {
		return nil, nil, fmt.Errorf("MCP server disabled")
	}
	switch cfg.Transport {
	case "", "stdio":
		return createStdioTransport(cfg)
	case "sse":
		if client == nil || cfg.URL == "" {
			return nil, nil, fmt.Errorf("remote MCP requires endpoint and scoped HTTP client")
		}
		return &sdk.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: client}, nil, nil
	case "streamableHTTP":
		if client == nil || cfg.URL == "" {
			return nil, nil, fmt.Errorf("remote MCP requires endpoint and scoped HTTP client")
		}
		return &sdk.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: client}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported MCP transport")
	}
}
