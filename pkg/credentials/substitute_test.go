package credentials

import (
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestFormatPlaceholder(t *testing.T) {
	got := FormatPlaceholder("my-ssh", "password")
	want := "{{CREDENTIAL:my-ssh:password}}"
	if got != want {
		t.Errorf("FormatPlaceholder = %q, want %q", got, want)
	}
}

func TestContainsPlaceholder(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"hello world", false},
		{"{{CREDENTIAL:my-ssh:password}}", true},
		{"prefix {{CREDENTIAL:x:token}} suffix", true},
		{"{{CREDENTIAL:}}", false},   // missing field
		{"{{CREDENTIAL:a:}}", false}, // empty field
		{"multiple {{CREDENTIAL:a:password}} and {{CREDENTIAL:b:token}}", true},
	}

	for _, tt := range tests {
		if got := ContainsPlaceholder(tt.input); got != tt.want {
			t.Errorf("ContainsPlaceholder(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestSubstitutePlaceholders_NilStore(t *testing.T) {
	input := "{{CREDENTIAL:my-ssh:password}}"
	got := SubstitutePlaceholders(input, nil)
	if got != input {
		t.Errorf("SubstitutePlaceholders with nil store should return input unchanged, got %q", got)
	}
}

func TestSubstitutePlaceholders_NoPlaceholders(t *testing.T) {
	// Create a minimal store (in-memory, no encryption)
	input := "hello world"
	got := SubstitutePlaceholders(input, nil)
	if got != input {
		t.Errorf("SubstitutePlaceholders with no placeholders should return input unchanged, got %q", got)
	}
}

func TestSubstituteMap_NilInputs(t *testing.T) {
	// Nil map
	got := SubstituteMap(nil, nil)
	if got != nil {
		t.Errorf("SubstituteMap(nil, nil) should return nil")
	}

	// Nil store
	m := map[string]any{"key": "{{CREDENTIAL:x:password}}"}
	got = SubstituteMap(m, nil)
	if got["key"] != "{{CREDENTIAL:x:password}}" {
		t.Errorf("SubstituteMap with nil store should return unchanged, got %v", got)
	}
}

func TestSubstituteMap_NoPlaceholders(t *testing.T) {
	m := map[string]any{"key": "hello", "nested": map[string]any{"k": "world"}}
	got := SubstituteMap(m, nil)
	// Should return the same map reference (no copy)
	if got["key"] != "hello" {
		t.Errorf("SubstituteMap with no placeholders should return input unchanged")
	}
}

type testCredentialResolver struct {
	creds map[string]*Credential
}

func (r *testCredentialResolver) Get(name string) *Credential            { return r.creds[name] }
func (r *testCredentialResolver) Resolve(string) (string, string, error) { return "", "", nil }
func (r *testCredentialResolver) Reload() error                          { return nil }

func TestSubstitutePlaceholders_RawContent(t *testing.T) {
	content := "providers:\n  alpaca:\n    key: abc12345\n"
	resolver := &testCredentialResolver{creds: map[string]*Credential{
		"providers-file": {Type: CredRawContent, Content: content},
	}}

	got := SubstitutePlaceholders("file={{CREDENTIAL:providers-file:content}}", resolver)
	want := "file=" + content
	if got != want {
		t.Fatalf("SubstitutePlaceholders = %q, want %q", got, want)
	}
}

func TestResolveField_UnknownCredential(t *testing.T) {
	// resolveField with unknown credential should return fallback
	fallback := "{{CREDENTIAL:nonexistent:password}}"
	got := resolveField(nil, "nonexistent", "password", fallback)
	if got != fallback {
		t.Errorf("resolveField with nil store = %q, want %q", got, fallback)
	}
}

func TestPlaceholderRegex(t *testing.T) {
	tests := []struct {
		input   string
		matches [][]string // each match: [full, name, field]
	}{
		{
			"{{CREDENTIAL:my-ssh:password}}",
			[][]string{{"{{CREDENTIAL:my-ssh:password}}", "my-ssh", "password"}},
		},
		{
			"prefix {{CREDENTIAL:api:token}} middle {{CREDENTIAL:db:password}} suffix",
			[][]string{
				{"{{CREDENTIAL:api:token}}", "api", "token"},
				{"{{CREDENTIAL:db:password}}", "db", "password"},
			},
		},
		{
			"{{CREDENTIAL:oauth-app:client_secret}}",
			[][]string{{"{{CREDENTIAL:oauth-app:client_secret}}", "oauth-app", "client_secret"}},
		},
	}

	for _, tt := range tests {
		matches := credentialPlaceholderRe.FindAllStringSubmatch(tt.input, -1)
		if len(matches) != len(tt.matches) {
			t.Errorf("regex on %q: got %d matches, want %d", tt.input, len(matches), len(tt.matches))
			continue
		}
		for i, m := range matches {
			if m[0] != tt.matches[i][0] || m[1] != tt.matches[i][1] || m[2] != tt.matches[i][2] {
				t.Errorf("regex on %q match %d: got %v, want %v", tt.input, i, m, tt.matches[i])
			}
		}
	}
}

func TestSubstituteAndRestore_NilStore(t *testing.T) {
	m := map[string]any{"input": "{{CREDENTIAL:x:password}}"}
	restore := SubstituteAndRestore(m, nil)
	// Should be a no-op, map unchanged
	if m["input"] != "{{CREDENTIAL:x:password}}" {
		t.Errorf("nil store should leave map unchanged, got %q", m["input"])
	}
	restore() // no-op, should not panic
}

func TestSubstituteAndRestore_NilMap(t *testing.T) {
	restore := SubstituteAndRestore(nil, nil)
	restore() // no-op, should not panic
}

func TestSubstituteAndRestore_NoPlaceholders(t *testing.T) {
	m := map[string]any{"key": "hello", "num": 42}
	restore := SubstituteAndRestore(m, nil)
	if m["key"] != "hello" {
		t.Errorf("no placeholders should leave map unchanged")
	}
	restore() // no-op
}

func TestSubstituteAndRestore_WithStore(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	store.Set("my-ssh", &Credential{
		Type:     CredPassword,
		Username: "root",
		Password: "secret123",
	})

	// Build args map simulating what ADK would pass (shared by reference
	// with the session event).
	args := map[string]any{
		"input":      "{{CREDENTIAL:my-ssh:password}}\n",
		"session_id": "abc123",
	}

	// Step 1: BeforeToolCallback — substitute placeholders.
	restore := SubstituteAndRestore(args, store)

	// After substitution, the tool should see the real value.
	if args["input"] != "secret123\n" {
		t.Errorf("after substitute, input = %q, want %q", args["input"], "secret123\n")
	}
	// Non-placeholder keys should be unchanged.
	if args["session_id"] != "abc123" {
		t.Errorf("session_id should be unchanged, got %q", args["session_id"])
	}

	// Step 2: AfterToolCallback — restore placeholders.
	restore()

	// After restore, the args map should have the original placeholders.
	if args["input"] != "{{CREDENTIAL:my-ssh:password}}\n" {
		t.Errorf("after restore, input = %q, want %q", args["input"], "{{CREDENTIAL:my-ssh:password}}\n")
	}
	if args["session_id"] != "abc123" {
		t.Errorf("session_id should still be unchanged, got %q", args["session_id"])
	}
}

func TestSubstituteAndRestore_MultipleKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	store.Set("db", &Credential{
		Type:     CredPassword,
		Username: "admin",
		Password: "dbpass",
	})
	store.Set("api", &Credential{
		Type:  CredBearer,
		Token: "tok-abc",
	})

	args := map[string]any{
		"password": "{{CREDENTIAL:db:password}}",
		"token":    "{{CREDENTIAL:api:token}}",
		"host":     "localhost",
	}

	restore := SubstituteAndRestore(args, store)

	if args["password"] != "dbpass" {
		t.Errorf("password = %q, want %q", args["password"], "dbpass")
	}
	if args["token"] != "tok-abc" {
		t.Errorf("token = %q, want %q", args["token"], "tok-abc")
	}
	if args["host"] != "localhost" {
		t.Errorf("host should be unchanged")
	}

	restore()

	if args["password"] != "{{CREDENTIAL:db:password}}" {
		t.Errorf("after restore, password = %q, want placeholder", args["password"])
	}
	if args["token"] != "{{CREDENTIAL:api:token}}" {
		t.Errorf("after restore, token = %q, want placeholder", args["token"])
	}
}

func TestSubstituteAndRestore_RestoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	store.Set("x", &Credential{Type: CredPassword, Username: "u", Password: "p"})

	args := map[string]any{"input": "{{CREDENTIAL:x:password}}"}
	restore := SubstituteAndRestore(args, store)
	restore()
	restore() // calling twice should be safe

	if args["input"] != "{{CREDENTIAL:x:password}}" {
		t.Errorf("double restore should still have placeholder, got %q", args["input"])
	}
}

// TestSubstituteAndRestore_ParallelCalls verifies that using per-call restores
// (as opposed to a shared variable) correctly handles concurrent tool calls.
// This test reproduces the race condition that occurred when ADK dispatched
// parallel tool calls — a shared credentialRestore variable could be overwritten
// by a concurrent goroutine, causing placeholders to leak into tool execution.
func TestSubstituteAndRestore_ParallelCalls(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	store.Set("proxmox", &Credential{Type: CredAPIKey, Header: "Authorization", Value: "PVEAPIToken=root@pam!astonish=secret123"})

	// Simulate two parallel tool calls — one with a credential placeholder,
	// one without. This is the exact pattern from session 1b919508 where
	// search_flows + shell_command ran in parallel.
	const iterations = 100
	for i := 0; i < iterations; i++ {
		var restoreFuncs sync.Map

		argsWithCred := map[string]any{
			"command": `curl -sk -H "Authorization: {{CREDENTIAL:proxmox:value}}" "https://proxmox.local/api2/json/status"`,
		}
		argsNoCred := map[string]any{
			"query": "proxmox memory",
		}

		var wg sync.WaitGroup
		wg.Add(2)

		// Goroutine 1: tool with credential placeholder (like shell_command)
		go func() {
			defer wg.Done()
			callID := "call-with-cred"
			restore := SubstituteAndRestore(argsWithCred, store)
			restoreFuncs.Store(callID, restore)

			// Verify substitution happened (real secret visible)
			cmd := argsWithCred["command"].(string)
			if ContainsPlaceholder(cmd) {
				t.Errorf("iteration %d: credential not substituted in goroutine 1, got %q", i, cmd)
			}
			if !strings.Contains(cmd, "PVEAPIToken=root@pam!astonish=secret123") {
				t.Errorf("iteration %d: expected real token in command, got %q", i, cmd)
			}

			// Simulate tool execution delay
			runtime.Gosched()

			// Restore
			if fn, ok := restoreFuncs.LoadAndDelete(callID); ok {
				fn.(func())()
			}
		}()

		// Goroutine 2: tool without credential placeholder (like search_flows)
		go func() {
			defer wg.Done()
			callID := "call-no-cred"
			restore := SubstituteAndRestore(argsNoCred, store)
			restoreFuncs.Store(callID, restore)

			// This should be a no-op restore
			runtime.Gosched()

			if fn, ok := restoreFuncs.LoadAndDelete(callID); ok {
				fn.(func())()
			}
		}()

		wg.Wait()

		// After both goroutines complete and restore, the credential args should have
		// the placeholder back, not the real secret.
		cmd := argsWithCred["command"].(string)
		if !ContainsPlaceholder(cmd) {
			t.Fatalf("iteration %d: placeholder not restored after parallel calls, got %q", i, cmd)
		}
		if strings.Contains(cmd, "secret123") {
			t.Fatalf("iteration %d: real secret leaked into restored args", i)
		}
	}
}

func TestSubstituteShellCommand_DollarSign(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// SAP AI Core style credentials with $ and special characters.
	store.Set("sap-ai-core", &Credential{
		Type:         CredOAuthClientCreds,
		AuthURL:      "https://auth.example.com/oauth/token",
		ClientID:     "sb-1c8c055a!b589421|aicore!b164",
		ClientSecret: "46898dfe-secret$4xghIXJReKKDiYvjki0bqtacT0SzRatkuCcyns-qvkA=",
	})

	command := `curl -s -X POST "https://auth.example.com/oauth/token" -u "{{CREDENTIAL:sap-ai-core:client_id}}:{{CREDENTIAL:sap-ai-core:client_secret}}" -d "grant_type=client_credentials"`

	result := SubstituteShellCommand(command, store)

	// The result should NOT contain the raw credential value in the command portion
	// (after the exports). The exports use single-quoted values which are shell-safe.
	// Split on the last semicolon-space before curl to isolate the command part.
	parts := strings.SplitN(result, "; curl", 2)
	if len(parts) != 2 {
		t.Fatalf("expected export prefix + curl command, got:\n%s", result)
	}
	commandPart := "curl" + parts[1]

	// The command part should use env var references, not inline values.
	if strings.Contains(commandPart, "$4xghIXJReKKDiYvjki0bqtacT0SzRatkuCcyns") {
		t.Errorf("command part should not contain raw dollar-sign value, got:\n%s", commandPart)
	}

	// The result should contain env var exports with single-quoted values.
	if !strings.Contains(result, "export __ASTONISH_CRED_") {
		t.Errorf("expected env var exports in command, got:\n%s", result)
	}

	// The result should reference env vars in the command.
	if !strings.Contains(commandPart, "${__ASTONISH_CRED_") {
		t.Errorf("expected env var references in command, got:\n%s", commandPart)
	}

	// The env var values should be single-quoted for shell safety ($ not expanded).
	exportPart := parts[0]
	if !strings.Contains(exportPart, "'46898dfe-secret$4xghIXJReKKDiYvjki0bqtacT0SzRatkuCcyns-qvkA='") {
		t.Errorf("expected single-quoted secret value in export, got:\n%s", exportPart)
	}
}

func TestSubstituteShellCommand_SingleQuoteInValue(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	store.Set("test-cred", &Credential{
		Type:     CredPassword,
		Username: "user",
		Password: "pass'word",
	})

	command := `echo "{{CREDENTIAL:test-cred:password}}"`
	result := SubstituteShellCommand(command, store)

	// Single quotes in the value should be escaped with '\'' technique.
	if !strings.Contains(result, "'pass'\\''word'") {
		t.Errorf("single quotes should be escaped, got:\n%s", result)
	}
}

func TestSubstituteShellCommand_NoPlaceholders(t *testing.T) {
	command := `echo "hello world"`
	result := SubstituteShellCommand(command, nil)

	if result != command {
		t.Errorf("command without placeholders should be unchanged, got %q", result)
	}
}

func TestSubstituteShellCommand_UnresolvablePlaceholder(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	command := `echo "{{CREDENTIAL:nonexistent:password}}"`
	result := SubstituteShellCommand(command, store)

	// Unresolvable placeholders should leave command unchanged.
	if result != command {
		t.Errorf("unresolvable placeholder should leave command unchanged, got:\n%s", result)
	}
}

func TestSubstituteAndRestore_ShellCommandField(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	store.Set("db", &Credential{
		Type:     CredPassword,
		Username: "admin",
		Password: "p@ss$word",
	})

	args := map[string]any{
		"command": `mysql -u "{{CREDENTIAL:db:username}}" -p"{{CREDENTIAL:db:password}}" mydb`,
		"timeout": 30,
	}

	// With shellCommandFields=["command"], the command field should use env-var injection.
	restore := SubstituteAndRestore(args, store, "command")

	cmd := args["command"].(string)

	// Should NOT contain raw $word (which shell would expand).
	if strings.Contains(cmd, "$word") && !strings.Contains(cmd, "${__ASTONISH_CRED_") {
		t.Errorf("command field should use env var injection, got:\n%s", cmd)
	}

	// Should contain env var exports.
	if !strings.Contains(cmd, "export __ASTONISH_CRED_") {
		t.Errorf("expected env var exports in command, got:\n%s", cmd)
	}

	// Non-command fields should not be affected.
	if args["timeout"] != 30 {
		t.Errorf("timeout should be unchanged, got %v", args["timeout"])
	}

	// Restore should put back the original placeholder.
	restore()
	if !ContainsPlaceholder(args["command"].(string)) {
		t.Errorf("after restore, placeholder should be back, got %q", args["command"])
	}
}

func TestShellQuoteSingle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"has$dollar", "'has$dollar'"},
		{"has`backtick`", "'has`backtick`'"},
		{"has'quote", "'has'\\''quote'"},
		{"multi'ple'quotes", "'multi'\\''ple'\\''quotes'"},
		{"", "''"},
		{"normal-value-123", "'normal-value-123'"},
	}

	for _, tt := range tests {
		got := shellQuoteSingle(tt.input)
		if got != tt.expected {
			t.Errorf("shellQuoteSingle(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestUnresolvedCredentialNames(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected []string
	}{
		{
			name:     "no placeholders",
			input:    map[string]any{"command": "echo hello"},
			expected: nil,
		},
		{
			name:     "single credential placeholder",
			input:    map[string]any{"command": "curl -u {{CREDENTIAL:openstack:username}}:{{CREDENTIAL:openstack:password}} http://api"},
			expected: []string{"openstack"},
		},
		{
			name:     "multiple credential names",
			input:    map[string]any{"command": "curl -u {{CREDENTIAL:aws:key}} -H {{CREDENTIAL:github:token}}"},
			expected: []string{"aws", "github"},
		},
		{
			name:     "nil map",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty map",
			input:    map[string]any{},
			expected: nil,
		},
		{
			name:     "nested map with placeholder",
			input:    map[string]any{"config": map[string]any{"token": "{{CREDENTIAL:vault:api_key}}"}},
			expected: []string{"vault"},
		},
		{
			name:     "array with placeholder",
			input:    map[string]any{"args": []any{"--token", "{{CREDENTIAL:myservice:token}}"}},
			expected: []string{"myservice"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnresolvedCredentialNames(tt.input)
			if tt.expected == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.expected) {
				t.Errorf("expected %d names %v, got %d names %v", len(tt.expected), tt.expected, len(got), got)
				return
			}
			// Check all expected names are present (order may vary)
			gotSet := make(map[string]bool)
			for _, name := range got {
				gotSet[name] = true
			}
			for _, expected := range tt.expected {
				if !gotSet[expected] {
					t.Errorf("expected name %q not found in result %v", expected, got)
				}
			}
		})
	}
}

func TestZeroWidthCredentialPlaceholderNormalization(t *testing.T) {
	placeholder := "{\u200b{CREDENTIAL:openstack-keystone:token}}"
	if !ContainsPlaceholder(placeholder) {
		t.Fatal("expected zero-width credential placeholder to be detected")
	}

	input := map[string]any{"command": "curl -H 'X-Auth-Token: " + placeholder + "' http://api"}
	got := UnresolvedCredentialNames(input)
	if len(got) != 1 || got[0] != "openstack-keystone" {
		t.Fatalf("expected unresolved openstack-keystone placeholder, got %v", got)
	}
}

func TestSubstituteShellCommand_ZeroWidthPlaceholder(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Set("openstack-keystone", &Credential{Type: CredBearer, Token: "ks-token-123"})

	command := "curl -s -H \"X-Auth-Token: {\u200b{CREDENTIAL:openstack-keystone:token}}\" https://kubernikus.qa-de-1.cloud.sap/api/v1/clusters"
	result := SubstituteShellCommand(command, store)

	if !strings.Contains(result, "export __ASTONISH_CRED_") {
		t.Fatalf("expected env var exports, got:\n%s", result)
	}
	if strings.Contains(result, "CREDENTIAL:openstack-keystone:token") {
		t.Fatalf("placeholder should be resolved, got:\n%s", result)
	}
	if !strings.Contains(result, "ks-token-123") {
		t.Fatalf("expected token value in shell-safe export, got:\n%s", result)
	}
}
