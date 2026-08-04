package mcp

import (
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
)

// ResolveMCPServerEnv expands {{CREDENTIAL:name:field}} placeholders in MCP
// server environment values using the credential store. Non-placeholder values
// are returned unchanged. When a value contains placeholders but no resolver is
// available, or a referenced credential cannot be resolved, an error is returned
// so the MCP server fails to start with a clear message instead of receiving a
// literal placeholder string as its secret.
//
// The returned map is a shallow copy; the input map is never modified.
// Callers must not persist the returned map to config or the MCP server store.
func ResolveMCPServerEnv(env map[string]string, resolver credentials.CredentialResolver) (map[string]string, error) {
	if len(env) == 0 {
		return env, nil
	}

	out := make(map[string]string, len(env))
	for key, value := range env {
		if !credentials.ContainsPlaceholder(value) {
			out[key] = value
			continue
		}
		if resolver == nil {
			return nil, fmt.Errorf("env %q references credentials but no credential store is available", key)
		}
		resolved := credentials.SubstitutePlaceholders(value, resolver)
		if credentials.ContainsPlaceholder(resolved) {
			names := credentials.UnresolvedCredentialNames(map[string]any{key: resolved})
			return nil, fmt.Errorf("env %q: could not resolve credential(s) %s", key, strings.Join(names, ", "))
		}
		out[key] = resolved
	}
	return out, nil
}

// ResolveMCPServerConfig returns a copy of cfg with Env placeholders expanded.
// Command, args, transport, and URL are left unchanged.
func ResolveMCPServerConfig(cfg config.MCPServerConfig, resolver credentials.CredentialResolver) (config.MCPServerConfig, error) {
	resolvedEnv, err := ResolveMCPServerEnv(cfg.Env, resolver)
	if err != nil {
		return cfg, err
	}
	out := cfg
	out.Env = resolvedEnv
	return out, nil
}

// EnvNeedsCredentialResolution reports whether any env value contains a
// credential placeholder.
func EnvNeedsCredentialResolution(env map[string]string) bool {
	for _, value := range env {
		if credentials.ContainsPlaceholder(value) {
			return true
		}
	}
	return false
}
