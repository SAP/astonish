package api

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

func TestNetworkAuthorizationFromMCPError_ExtractsURLAndPackageManagerHint(t *testing.T) {
	serverCfg := config.MCPServerConfig{
		Command: "npx",
		Args:    []string{"-y", "@upstash/context7-mcp"},
	}
	err := newMCPSandboxDiagnosticsError(
		"failed to list tools from MCP server \"context7\"",
		fmt.Errorf("calling initialize: EOF"),
		bytes.NewBufferString(""),
		bytes.NewBufferString("npm error code ECONNRESET\nnpm error network request to https://registry.npmjs.org/@upstash%2fcontext7-mcp failed, reason: socket hang up\n"),
		serverCfg,
	)

	auth := networkAuthorizationFromMCPError(err, serverCfg)
	if auth == nil || !auth.Required {
		t.Fatalf("expected network authorization, got %#v", auth)
	}
	if len(auth.Denials) != 1 {
		t.Fatalf("denials = %v, want one registry denial", auth.Denials)
	}
	if got := auth.Denials[0]["host"]; got != "registry.npmjs.org" {
		t.Fatalf("denial host = %v, want registry.npmjs.org", got)
	}
	if got := auth.Denials[0]["port"]; got != 443 {
		t.Fatalf("denial port = %v, want 443", got)
	}
	if len(auth.Hints) != 1 {
		t.Fatalf("hints = %v, want one npm registry hint", auth.Hints)
	}
	if auth.Hints[0].Host != "registry.npmjs.org" || auth.Hints[0].Port != 443 {
		t.Fatalf("hint = %+v, want registry.npmjs.org:443", auth.Hints[0])
	}
}

func TestNetworkAuthorizationFromMCPError_UsesPackageManagerHintWhenHostMissing(t *testing.T) {
	serverCfg := config.MCPServerConfig{
		Command: "uvx",
		Args:    []string{"mcp-server-demo"},
	}
	err := newMCPSandboxDiagnosticsError(
		"failed to start MCP server \"demo\" in sandbox",
		fmt.Errorf("calling initialize: EOF"),
		bytes.NewBufferString("network is unreachable while installing package"),
		bytes.NewBufferString(""),
		serverCfg,
	)

	auth := networkAuthorizationFromMCPError(err, serverCfg)
	if auth == nil || !auth.Required {
		t.Fatalf("expected authorization from package-manager hint, got %#v", auth)
	}
	if len(auth.Denials) != 0 {
		t.Fatalf("denials = %v, want none without explicit host", auth.Denials)
	}
	if len(auth.Hints) != 2 {
		t.Fatalf("hints = %v, want PyPI and files.pythonhosted.org", auth.Hints)
	}
	if auth.Hints[0].Host != "pypi.org" || auth.Hints[1].Host != "files.pythonhosted.org" {
		t.Fatalf("unexpected hints: %+v", auth.Hints)
	}
}

func TestNetworkAuthorizationFromMCPError_IgnoresNonNetworkFailure(t *testing.T) {
	serverCfg := config.MCPServerConfig{Command: "npx", Args: []string{"demo"}}
	err := newMCPSandboxDiagnosticsError(
		"failed to list tools from MCP server \"demo\"",
		fmt.Errorf("invalid server response"),
		bytes.NewBufferString("syntax error in server script"),
		bytes.NewBufferString(""),
		serverCfg,
	)

	if auth := networkAuthorizationFromMCPError(err, serverCfg); auth != nil {
		t.Fatalf("expected no network authorization, got %#v", auth)
	}
}
