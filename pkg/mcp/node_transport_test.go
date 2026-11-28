package mcp

import (
	"net/http"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNodeTransport(t *testing.T) {
	for _, kind := range []string{"stdio", "sse", "streamableHTTP"} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.MCPServerConfig{Transport: kind, Command: "fixture", URL: "https://mcp.example.test"}
			client := &http.Client{}
			transport, _, err := NewNodeTransport(cfg, client)
			if err != nil {
				t.Fatal(err)
			}
			switch v := transport.(type) {
			case *sdk.CommandTransport:
				if kind != "stdio" {
					t.Fatal("wrong transport")
				}
			case *sdk.SSEClientTransport:
				if v.HTTPClient != client {
					t.Fatal("scoped client lost")
				}
			case *sdk.StreamableClientTransport:
				if v.HTTPClient != client {
					t.Fatal("scoped client lost")
				}
			default:
				t.Fatal("unknown transport")
			}
		})
	}
	disabled := false
	for _, cfg := range []config.MCPServerConfig{{Transport: "unknown"}, {Transport: "sse", URL: "https://mcp.example.test"}, {Transport: "streamableHTTP", URL: "https://mcp.example.test"}, {Command: "fixture", Enabled: &disabled}, {}} {
		if _, _, err := NewNodeTransport(cfg, nil); err == nil {
			t.Fatalf("unsafe config accepted: %+v", cfg)
		}
	}
}
