package api

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
)

type mcpSandboxDiagnosticsError struct {
	message string
	cause   error
	stderr  string
	stdout  string
}

type MCPNetworkAuthorization struct {
	Required bool                       `json:"required"`
	Message  string                     `json:"message,omitempty"`
	Denials  []map[string]any           `json:"denials,omitempty"`
	Hints    []MCPNetworkPreflightGrant `json:"hints,omitempty"`
}

type MCPNetworkPreflightGrant struct {
	Host   string `json:"host"`
	Port   uint32 `json:"port"`
	Reason string `json:"reason,omitempty"`
}

func newMCPSandboxDiagnosticsError(message string, cause error, stderr, stdout *bytes.Buffer, serverCfg config.MCPServerConfig) error {
	return &mcpSandboxDiagnosticsError{
		message: message,
		cause:   cause,
		stderr:  mcp.DiagnosticStderrForServer(stderr, serverCfg),
		stdout:  mcp.DiagnosticOutputForServer(stdout, serverCfg),
	}
}

func (e *mcpSandboxDiagnosticsError) Error() string {
	return fmt.Sprintf("%s: %v (stderr: %s, stdout: %s)", e.message, e.cause, e.stderr, e.stdout)
}

func (e *mcpSandboxDiagnosticsError) Unwrap() error {
	return e.cause
}

func networkAuthorizationFromMCPError(err error, serverCfg config.MCPServerConfig) *MCPNetworkAuthorization {
	var diagErr *mcpSandboxDiagnosticsError
	if !errors.As(err, &diagErr) || diagErr == nil {
		return nil
	}
	text := diagErr.stderr + "\n" + diagErr.stdout + "\n" + diagErr.cause.Error()
	if !netpolicy.LooksLikeNetworkFailureText(text) {
		return nil
	}
	denials := netpolicy.ExtractDenialsFromText(text)
	hints := mcpPackageManagerPreflightGrants(serverCfg)
	if len(denials) == 0 && len(hints) == 0 {
		return nil
	}
	return &MCPNetworkAuthorization{
		Required: true,
		Message:  "This MCP server needs outbound network access before Astonish can install or start it.",
		Denials:  denials,
		Hints:    hints,
	}
}

func mcpPackageManagerPreflightGrants(serverCfg config.MCPServerConfig) []MCPNetworkPreflightGrant {
	if len(serverCfg.Args) == 0 {
		return nil
	}
	switch serverCfg.Command {
	case "npx", "npm", "pnpm", "yarn":
		pkg := firstPackageArgument(serverCfg.Args)
		reason := "Install npm package"
		if pkg != "" {
			reason = fmt.Sprintf("Install npm package %s", pkg)
		}
		return []MCPNetworkPreflightGrant{{Host: "registry.npmjs.org", Port: 443, Reason: reason}}
	case "uvx", "pipx", "pip":
		pkg := firstPackageArgument(serverCfg.Args)
		reason := "Install Python package"
		if pkg != "" {
			reason = fmt.Sprintf("Install Python package %s", pkg)
		}
		return []MCPNetworkPreflightGrant{
			{Host: "pypi.org", Port: 443, Reason: reason},
			{Host: "files.pythonhosted.org", Port: 443, Reason: reason},
		}
	default:
		return nil
	}
}

func firstPackageArgument(args []string) string {
	for _, arg := range args {
		if arg == "" || arg == "-y" || arg == "--yes" || arg == "dlx" || arg == "exec" || arg == "run" {
			continue
		}
		if arg[0] == '-' {
			continue
		}
		return arg
	}
	return ""
}
