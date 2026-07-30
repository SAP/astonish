package mcp

import (
	"bytes"
	"net/url"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/config"
)

const maxDiagnosticOutputLen = 4096

// ServerConfigLogAttrs returns structured, secret-safe diagnostic attributes for
// an MCP server configuration. It logs env var names but never env var values.
func ServerConfigLogAttrs(serverName string, cfg config.MCPServerConfig) []any {
	transport := strings.TrimSpace(cfg.Transport)
	if transport == "" {
		transport = "stdio"
	}

	attrs := []any{
		"component", "mcp",
		"server", serverName,
		"transport", transport,
		"enabled", cfg.IsEnabled(),
	}

	switch transport {
	case "sse", "streamable-http":
		attrs = append(attrs, safeURLAttrs(cfg.URL)...)
	default:
		attrs = append(attrs,
			"command", cfg.Command,
			"args", cfg.Args,
			"args_count", len(cfg.Args),
		)
	}

	if len(cfg.Env) > 0 {
		keys := make([]string, 0, len(cfg.Env))
		for key := range cfg.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		attrs = append(attrs, "env_keys", keys, "env_count", len(keys))
	} else {
		attrs = append(attrs, "env_count", 0)
	}

	return attrs
}

// FailureLogAttrs adds failure-specific fields to ServerConfigLogAttrs.
func FailureLogAttrs(serverName string, cfg config.MCPServerConfig, err error, stderr *bytes.Buffer) []any {
	attrs := ServerConfigLogAttrs(serverName, cfg)
	attrs = append(attrs, "error", err, "stderr", DiagnosticStderr(stderr))
	return attrs
}

// DiagnosticStderr returns stderr output suitable for logs, bounded to avoid
// flooding container logs with very large process output.
func DiagnosticStderr(buf *bytes.Buffer) string {
	out := GetStderr(buf)
	if len(out) <= maxDiagnosticOutputLen {
		return out
	}
	return out[:maxDiagnosticOutputLen] + "... [truncated]"
}

func safeURLAttrs(raw string) []any {
	if raw == "" {
		return []any{"url_present", false}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return []any{"url_present", true, "url_parse_error", err.Error()}
	}

	return []any{
		"url_present", true,
		"url_scheme", parsed.Scheme,
		"url_host", parsed.Host,
		"url_path", parsed.EscapedPath(),
	}
}
