package credentials

import (
	"fmt"
	"regexp"
	"strings"
)

// CredentialResolver is the interface used by the substitution engine to
// look up credential values. Both the file-based *credentials.Store and
// the PG-backed store.CredentialStore satisfy this (the latter via an adapter
// in the agent package that converts store.Credential → credentials.Credential).
//
// This abstraction keeps the substitution logic decoupled from any specific
// storage backend, enabling multi-tenant credential isolation in platform mode.
type CredentialResolver interface {
	// Get retrieves a credential by name. Returns nil if not found.
	Get(name string) *Credential
	// Resolve returns the HTTP header key and value for a credential.
	// Used for OAuth auth-code token refresh.
	Resolve(name string) (headerKey, headerValue string, err error)
	// Reload re-reads credentials from the backing store.
	Reload() error
}

// Placeholder format: {{CREDENTIAL:credential-name:field}}
// Fields: password, token, value, client_secret, access_token, refresh_token
var credentialPlaceholderRe = regexp.MustCompile(`\{\{CREDENTIAL:([^:}]+):([^}]+)\}\}`)

// FormatPlaceholder returns a credential placeholder token.
// The LLM sees these instead of raw secret values.
func FormatPlaceholder(credName, field string) string {
	return fmt.Sprintf("{{CREDENTIAL:%s:%s}}", credName, field)
}

// ContainsPlaceholder reports whether text contains any credential placeholder.
func ContainsPlaceholder(text string) bool {
	return credentialPlaceholderRe.MatchString(normalizeCredentialPlaceholderText(text))
}

// UnresolvedCredentialNames extracts unique credential names from any
// {{CREDENTIAL:name:field}} placeholders remaining in the map values.
// Returns nil if no unresolved placeholders remain.
func UnresolvedCredentialNames(m map[string]any) []string {
	seen := make(map[string]bool)
	for _, v := range m {
		collectUnresolvedNames(v, seen)
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	return names
}

func collectUnresolvedNames(v any, seen map[string]bool) {
	switch val := v.(type) {
	case string:
		val = normalizeCredentialPlaceholderText(val)
		matches := credentialPlaceholderRe.FindAllStringSubmatch(val, -1)
		for _, m := range matches {
			if len(m) >= 2 {
				seen[m[1]] = true
			}
		}
	case map[string]any:
		for _, inner := range val {
			collectUnresolvedNames(inner, seen)
		}
	case []any:
		for _, item := range val {
			collectUnresolvedNames(item, seen)
		}
	}
}

func normalizeCredentialPlaceholderText(text string) string {
	if text == "" {
		return text
	}
	replacer := strings.NewReplacer(
		"\u200b", "",
		"\u200c", "",
		"\u200d", "",
		"\ufeff", "",
	)
	return replacer.Replace(text)
}

// SubstitutePlaceholders replaces all {{CREDENTIAL:name:field}} tokens in text
// with the actual secret values from the credential store. Unresolvable
// placeholders are left as-is (the tool will fail naturally, giving the LLM
// a chance to correct).
//
// This runs inside a BeforeToolCallback — after the LLM has chosen the tool
// and args, but before the tool executes. The real values never enter the
// conversation history.
func SubstitutePlaceholders(text string, resolver CredentialResolver) string {
	if resolver == nil {
		return text
	}
	text = normalizeCredentialPlaceholderText(text)

	return credentialPlaceholderRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := credentialPlaceholderRe.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		name, field := parts[1], parts[2]
		return resolveField(resolver, name, field, match)
	})
}

// SubstituteShellCommand replaces credential placeholders in a shell command
// string using environment variable injection. Instead of inlining secret values
// directly (which is unsafe because characters like $, `, !, etc. are interpreted
// by the shell), this function:
//
//  1. Resolves each unique placeholder to its real value
//  2. Assigns each to a numbered env var (__ASTONISH_CRED_0, __ASTONISH_CRED_1, ...)
//  3. Replaces placeholders in the command with env var references
//  4. Prepends the env var exports so the shell sets them before running the command
//
// The result is a shell-safe command string where secret values are never subject
// to shell expansion. Returns the original string unchanged if no placeholders
// are found or the resolver is nil.
func SubstituteShellCommand(command string, resolver CredentialResolver) string {
	if resolver == nil || !ContainsPlaceholder(command) {
		return command
	}
	command = normalizeCredentialPlaceholderText(command)

	// Find all unique placeholders and resolve them.
	type resolvedCred struct {
		envVar string
		value  string
	}
	seen := make(map[string]*resolvedCred) // placeholder → resolved info
	var envVars []resolvedCred
	counter := 0

	for _, match := range credentialPlaceholderRe.FindAllString(command, -1) {
		if _, exists := seen[match]; exists {
			continue
		}
		parts := credentialPlaceholderRe.FindStringSubmatch(match)
		if len(parts) != 3 {
			continue
		}
		name, field := parts[1], parts[2]
		value := resolveField(resolver, name, field, "")
		if value == "" {
			// Could not resolve — leave placeholder as-is.
			continue
		}
		envName := fmt.Sprintf("__ASTONISH_CRED_%d", counter)
		counter++
		rc := resolvedCred{envVar: envName, value: value}
		seen[match] = &rc
		envVars = append(envVars, rc)
	}

	if len(envVars) == 0 {
		return command
	}

	// Replace placeholders in the command with env var references.
	result := command
	for placeholder, rc := range seen {
		result = strings.ReplaceAll(result, placeholder, "${"+rc.envVar+"}")
	}

	// Prepend export statements with single-quoted values (safe from expansion).
	var prefix strings.Builder
	for _, ev := range envVars {
		prefix.WriteString("export ")
		prefix.WriteString(ev.envVar)
		prefix.WriteString("=")
		prefix.WriteString(shellQuoteSingle(ev.value))
		prefix.WriteString("; ")
	}

	return prefix.String() + result
}

// shellQuoteSingle wraps a value in single quotes, escaping any embedded single
// quotes with the standard '\” technique. Single-quoted strings in sh/bash
// have NO expansion — $, `, \, ! are all literal.
func shellQuoteSingle(s string) string {
	// Replace each ' with '\'' (end quote, escaped quote, start quote).
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}

// SubstituteMap deep-walks a map and substitutes credential placeholders in
// all string values. Returns a new map (the original is not modified).
func SubstituteMap(m map[string]any, resolver CredentialResolver) map[string]any {
	if resolver == nil || m == nil {
		return m
	}
	if !mapContainsPlaceholder(m) {
		return m
	}
	return substituteMapRecursive(m, resolver)
}

// SubstituteMapInPlace modifies the map's string values in-place, replacing
// credential placeholders with real values.
//
// WARNING: If the args map is shared by reference (e.g. with ADK session
// events), the mutation will corrupt the stored event. Prefer
// SubstituteAndRestore which restores the original placeholders after use.
func SubstituteMapInPlace(m map[string]any, resolver CredentialResolver) {
	if resolver == nil || m == nil {
		return
	}
	if !mapContainsPlaceholder(m) {
		return
	}
	for k, v := range m {
		m[k] = substituteValue(v, resolver)
	}
}

// SubstituteAndRestore substitutes credential placeholders in the args map
// in-place (so the tool receives real values) and returns a restore function
// that puts the original placeholder values back.
//
// This is designed for ADK's BeforeToolCallback / AfterToolCallback pair:
//
//   - BeforeToolCallback calls SubstituteAndRestore → tool runs with real values
//   - AfterToolCallback calls the returned restore function → the shared args
//     map reverts to placeholders, so the session event (which holds the same
//     map by reference) never persists real secrets.
//
// If there are no placeholders or the resolver is nil, returns a no-op function.
//
// If shellCommandField is non-empty and that key exists in the map with a string
// value containing placeholders, it will be substituted using the shell-safe
// env-var injection technique (SubstituteShellCommand) instead of inline replacement.
// Typically pass "command" for shell_command tools, or "" for other tools.
func SubstituteAndRestore(m map[string]any, resolver CredentialResolver, shellCommandFields ...string) (restore func()) {
	noop := func() {}

	if resolver == nil || m == nil {
		return noop
	}
	if !mapContainsPlaceholder(m) {
		return noop
	}

	// Build the set of shell-command fields for O(1) lookup.
	shellFields := make(map[string]bool, len(shellCommandFields))
	for _, f := range shellCommandFields {
		if f != "" {
			shellFields[f] = true
		}
	}

	// Snapshot original values for keys that contain placeholders.
	originals := snapshotPlaceholderValues(m)

	// Substitute in-place so the tool gets real values.
	for k, v := range m {
		if shellFields[k] {
			if s, ok := v.(string); ok && ContainsPlaceholder(s) {
				m[k] = SubstituteShellCommand(s, resolver)
				continue
			}
		}
		m[k] = substituteValue(v, resolver)
	}

	// Return a function that restores the originals.
	return func() {
		for k, v := range originals {
			m[k] = v
		}
	}
}

// snapshotPlaceholderValues returns a shallow copy of map entries whose values
// contain credential placeholders. Only top-level keys are tracked — this is
// sufficient because ADK tool args are flat JSON objects (nested structures
// are rare and the top-level value is what gets restored).
func snapshotPlaceholderValues(m map[string]any) map[string]any {
	snap := make(map[string]any)
	for k, v := range m {
		if containsPlaceholderValue(v) {
			snap[k] = v
		}
	}
	return snap
}

func substituteMapRecursive(m map[string]any, resolver CredentialResolver) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = substituteValue(v, resolver)
	}
	return result
}

func substituteValue(v any, resolver CredentialResolver) any {
	switch val := v.(type) {
	case string:
		if ContainsPlaceholder(val) {
			return SubstitutePlaceholders(val, resolver)
		}
		return val
	case map[string]any:
		return substituteMapRecursive(val, resolver)
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = substituteValue(item, resolver)
		}
		return result
	default:
		return v
	}
}

// mapContainsPlaceholder quickly checks whether any string in the map contains
// a placeholder, to avoid unnecessary deep copies.
func mapContainsPlaceholder(m map[string]any) bool {
	for _, v := range m {
		if containsPlaceholderValue(v) {
			return true
		}
	}
	return false
}

func containsPlaceholderValue(v any) bool {
	switch val := v.(type) {
	case string:
		return ContainsPlaceholder(val)
	case map[string]any:
		return mapContainsPlaceholder(val)
	case []any:
		for _, item := range val {
			if containsPlaceholderValue(item) {
				return true
			}
		}
	}
	return false
}

// ResolveField looks up a single credential field value by store name.
// Returns empty string if the credential or field is not found.
func ResolveField(resolver CredentialResolver, name, field string) string {
	return resolveField(resolver, name, field, "")
}

// resolveField looks up a single credential field value.
// Returns the fallback string if the credential or field is not found.
func resolveField(resolver CredentialResolver, name, field, fallback string) string {
	if resolver == nil {
		return fallback
	}

	// Reload to pick up any changes from CLI or other processes
	resolver.Reload()

	cred := resolver.Get(name)
	if cred == nil {
		return fallback
	}

	switch field {
	case "password":
		if cred.Password != "" {
			return cred.Password
		}
	case "token":
		// For OAuth auth-code and Keystone, get a fresh (possibly refreshed) token
		if cred.Type == CredOAuthAuthCode {
			_, headerValue, err := resolver.Resolve(name)
			if err == nil {
				// Extract token from "Bearer <token>"
				const prefix = "Bearer "
				if len(headerValue) > len(prefix) {
					return headerValue[len(prefix):]
				}
			}
		}
		if cred.Type == CredOpenStackKeystone {
			_, headerValue, err := resolver.Resolve(name)
			if err == nil && headerValue != "" {
				return headerValue
			}
		}
		if cred.Token != "" {
			return cred.Token
		}
	case "value":
		if cred.Value != "" {
			return cred.Value
		}
	case "username":
		if cred.Username != "" {
			return cred.Username
		}
	case "header":
		if cred.Header != "" {
			return cred.Header
		}
	case "client_secret":
		if cred.ClientSecret != "" {
			return cred.ClientSecret
		}
	case "client_id":
		if cred.ClientID != "" {
			return cred.ClientID
		}
	case "access_token":
		if cred.AccessToken != "" {
			return cred.AccessToken
		}
	case "refresh_token":
		if cred.RefreshToken != "" {
			return cred.RefreshToken
		}
	case "application_credential_id":
		if cred.ApplicationCredentialID != "" {
			return cred.ApplicationCredentialID
		}
	case "application_credential_secret":
		if cred.ApplicationCredentialSecret != "" {
			return cred.ApplicationCredentialSecret
		}
	case "user_domain":
		if cred.UserDomain != "" {
			return cred.UserDomain
		}
	case "project_id":
		if cred.ProjectID != "" {
			return cred.ProjectID
		}
	case "project_name":
		if cred.ProjectName != "" {
			return cred.ProjectName
		}
	case "project_domain":
		if cred.ProjectDomain != "" {
			return cred.ProjectDomain
		}
	case "auth_url":
		if cred.AuthURL != "" {
			return cred.AuthURL
		}
	case "content":
		if cred.Content != "" {
			return cred.Content
		}
	case "content_type":
		if cred.ContentType != "" {
			return cred.ContentType
		}
	}

	return fallback
}
