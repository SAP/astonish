package api

import (
	"bytes"
	"fmt"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/mcp"
)

type mcpSandboxDiagnosticsError struct {
	message string
	cause   error
	stderr  string
	stdout  string
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
