package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
)

type lifecycleReadonlyContext struct {
	agent.ReadonlyContext
	ctx context.Context
}

func (c lifecycleReadonlyContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c lifecycleReadonlyContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c lifecycleReadonlyContext) Err() error                  { return c.ctx.Err() }
func (c lifecycleReadonlyContext) Value(key any) any           { return c.ctx.Value(key) }

type lifecycleTransport struct{ closed int }

func (t *lifecycleTransport) Connect(context.Context) (sdk.Connection, error) {
	return nil, errors.New("unused")
}
func (t *lifecycleTransport) Close() error { t.closed++; return nil }

type lifecycleToolset struct {
	tool.Toolset
	closed int
}

func (t *lifecycleToolset) Close() error { t.closed++; return nil }

func TestMCPPartialSingleCleanup(t *testing.T) {
	transport, partial := &lifecycleTransport{}, &lifecycleToolset{}
	want := errors.New("partial constructor failure")
	got, err := newSingleToolset(transport, func(mcptoolset.Config) (tool.Toolset, error) { return partial, want })
	if got != nil || !errors.Is(err, want) || transport.closed != 1 || partial.closed != 1 {
		t.Fatalf("partial resources not cleaned: result=%v err=%v transport=%d toolset=%d", got, err, transport.closed, partial.closed)
	}
}

// A real stdio subprocess speaking just enough MCP to establish the lazy
// ADK session. Cleanup must close the session, not merely its transport value.
func TestMCPSingleStdioServerHelper(t *testing.T) {
	if os.Getenv("ASTONISH_MCP_LIFECYCLE_HELPER") != "1" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if decoder.Decode(&request) != nil {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := any(map[string]any{})
		if request.Method == "initialize" {
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "lifecycle", "version": "1"}}
		} else if request.Method == "tools/list" {
			result = map[string]any{"tools": []any{}}
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			os.Exit(2)
		}
	}
}

func TestMCPSingleTransportCleanup(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManagerFromConfig(&config.MCPConfig{MCPServers: map[string]config.MCPServerConfig{
		"helper": {Command: executable, Args: []string{"-test.run=^TestMCPSingleStdioServerHelper$"}, Env: map[string]string{"ASTONISH_MCP_LIFECYCLE_HELPER": "1"}},
	}})
	defer manager.Cleanup()
	named, err := manager.InitializeSingleToolset(context.Background(), "helper")
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.transports) != 1 {
		t.Fatal("single transport not retained")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := named.Toolset.Tools(lifecycleReadonlyContext{ctx: ctx}); err != nil {
		t.Fatal(err)
	}
	transport := manager.transports[0].(*sdk.CommandTransport)
	if transport.Command.Process == nil {
		t.Fatal("stdio server was not started")
	}
	manager.Cleanup()
	if transport.Command.ProcessState == nil || !transport.Command.ProcessState.Exited() {
		t.Fatal("stdio server not reaped by cleanup")
	}
	if len(manager.toolsets) != 0 || len(manager.transports) != 0 {
		t.Fatal("cleanup retained resources")
	}
	manager.Cleanup()
}

func TestGetStderr_NilBuffer(t *testing.T) {
	t.Parallel()
	got := GetStderr(nil)
	if got != "" {
		t.Errorf("expected empty string for nil buffer, got %q", got)
	}
}

func TestGetStderr_EmptyBuffer(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	got := GetStderr(buf)
	if got != "no stderr output" {
		t.Errorf("expected 'no stderr output', got %q", got)
	}
}

func TestGetStderr_WithContent(t *testing.T) {
	t.Parallel()
	buf := bytes.NewBufferString("error: something failed")
	got := GetStderr(buf)
	if got != "error: something failed" {
		t.Errorf("expected 'error: something failed', got %q", got)
	}
}

func TestManager_Getters_Empty(t *testing.T) {
	t.Parallel()
	m := &Manager{
		config: &config.MCPConfig{
			MCPServers: make(map[string]config.MCPServerConfig),
		},
		namedToolsets: make([]NamedToolset, 0),
		transports:    nil,
		initResults:   make([]InitResult, 0),
	}

	if got := m.GetToolsets(); len(got) != 0 {
		t.Errorf("expected empty toolsets, got %d", len(got))
	}
	if got := m.GetNamedToolsets(); len(got) != 0 {
		t.Errorf("expected empty named toolsets, got %d", len(got))
	}
	if got := m.GetInitResults(); len(got) != 0 {
		t.Errorf("expected empty init results, got %d", len(got))
	}
	if got := m.GetConfig(); got == nil {
		t.Error("expected non-nil config")
	}
}

func TestManager_Cleanup_NilTransports(t *testing.T) {
	t.Parallel()
	m := &Manager{
		config: &config.MCPConfig{
			MCPServers: make(map[string]config.MCPServerConfig),
		},
		namedToolsets: make([]NamedToolset, 0),
		transports:    nil,
		initResults:   make([]InitResult, 0),
	}
	// Should not panic
	m.Cleanup()

	if m.transports != nil {
		t.Error("expected nil transports after cleanup")
	}
	if m.toolsets != nil {
		t.Error("expected nil toolsets after cleanup")
	}
	if m.namedToolsets != nil {
		t.Error("expected nil namedToolsets after cleanup")
	}
}

func TestInitResult_Fields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		result  InitResult
		wantOk  bool
		wantErr string
	}{
		{
			name:   "success",
			result: InitResult{Name: "server1", Success: true},
			wantOk: true,
		},
		{
			name:    "failure",
			result:  InitResult{Name: "server2", Success: false, Error: "connection refused"},
			wantOk:  false,
			wantErr: "connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.result.Success != tt.wantOk {
				t.Errorf("Success = %v, want %v", tt.result.Success, tt.wantOk)
			}
			if tt.result.Error != tt.wantErr {
				t.Errorf("Error = %q, want %q", tt.result.Error, tt.wantErr)
			}
		})
	}
}

func TestNamedToolset_Fields(t *testing.T) {
	t.Parallel()
	buf := bytes.NewBufferString("stderr output")
	nt := NamedToolset{
		Name:   "test-server",
		Stderr: buf,
	}
	if nt.Name != "test-server" {
		t.Errorf("Name = %q, want 'test-server'", nt.Name)
	}
	if nt.Stderr.String() != "stderr output" {
		t.Errorf("Stderr = %q, want 'stderr output'", nt.Stderr.String())
	}
}

func TestServerConfigLogAttrs_RedactsEnvValues(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Command: "npx",
		Args:    []string{"-y", "@example/server"},
		Env: map[string]string{
			"API_TOKEN": "super-secret-token",
			"BASE_URL":  "https://api.example.test",
		},
	}

	attrs := ServerConfigLogAttrs("example", cfg)
	joined := fmt.Sprint(attrs...)
	if strings.Contains(joined, "super-secret-token") {
		t.Fatalf("diagnostic attributes leaked env value: %s", joined)
	}
	if !strings.Contains(joined, "API_TOKEN") || !strings.Contains(joined, "BASE_URL") {
		t.Fatalf("diagnostic attributes should include env keys: %s", joined)
	}
	if !strings.Contains(joined, "npx") || !strings.Contains(joined, "@example/server") {
		t.Fatalf("diagnostic attributes should include command and args: %s", joined)
	}
}

func TestServerConfigLogAttrs_RedactsURLCredentials(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Transport: "sse",
		URL:       "https://user:secret@example.test/sse?token=hidden",
	}

	attrs := ServerConfigLogAttrs("remote", cfg)
	joined := fmt.Sprint(attrs...)
	if strings.Contains(joined, "secret") || strings.Contains(joined, "token=hidden") {
		t.Fatalf("diagnostic attributes leaked URL credentials or query: %s", joined)
	}
	if !strings.Contains(joined, "example.test") || !strings.Contains(joined, "/sse") {
		t.Fatalf("diagnostic attributes should include URL host and path: %s", joined)
	}
}

func TestDiagnosticStderr_TruncatesLongOutput(t *testing.T) {
	t.Parallel()
	buf := bytes.NewBufferString(strings.Repeat("x", maxDiagnosticOutputLen+10))
	got := DiagnosticStderr(buf)
	if len(got) <= maxDiagnosticOutputLen {
		t.Fatalf("expected truncated output to include suffix, got length %d", len(got))
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
}

func TestDiagnosticOutput_EmptyAndTruncation(t *testing.T) {
	t.Parallel()
	if got := DiagnosticOutput(nil); got != "" {
		t.Fatalf("nil diagnostic output = %q, want empty", got)
	}
	if got := DiagnosticOutput(bytes.NewBuffer(nil)); got != "" {
		t.Fatalf("empty diagnostic output = %q, want empty", got)
	}
	got := DiagnosticOutput(bytes.NewBufferString(strings.Repeat("x", maxDiagnosticOutputLen+10)))
	if len(got) <= maxDiagnosticOutputLen || !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected truncated diagnostic output, got length=%d value=%q", len(got), got)
	}
}

func TestDiagnosticOutputForServer_RedactsEnvValues(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{Env: map[string]string{"API_TOKEN": "secret-token-value"}}
	got := DiagnosticOutputForServer(bytes.NewBufferString("failed with secret-token-value"), cfg)
	if strings.Contains(got, "secret-token-value") {
		t.Fatalf("diagnostic output leaked env value: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("expected redaction marker, got %q", got)
	}
}

func TestCreateTransport_UnsupportedType(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Transport: "grpc",
	}
	_, _, err := createTransport(cfg)
	if err == nil {
		t.Fatal("expected error for unsupported transport type")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("unsupported transport type")) {
		t.Errorf("expected 'unsupported transport type' in error, got %q", err)
	}
}

func TestCreateTransportForServer_ResolvesCredentials(t *testing.T) {
	t.Parallel()
	mgr := NewManagerFromConfig(&config.MCPConfig{
		MCPServers: map[string]config.MCPServerConfig{
			"github": {
				Command: "echo",
				Env:     map[string]string{"GITHUB_TOKEN": "{{CREDENTIAL:github:token}}"},
			},
		},
	})
	mgr.SetCredentialResolver(&mockResolver{
		creds: map[string]*credentials.Credential{
			"github": {Type: credentials.CredBearer, Token: "ghp_resolved"},
		},
	})

	transport, _, err := mgr.createTransportForServer("github", mgr.config.MCPServers["github"])
	if err != nil {
		t.Fatalf("createTransportForServer: %v", err)
	}
	if transport == nil {
		t.Fatal("expected transport")
	}
	// Stored config must still contain the placeholder.
	if got := mgr.config.MCPServers["github"].Env["GITHUB_TOKEN"]; got != "{{CREDENTIAL:github:token}}" {
		t.Fatalf("stored config mutated: %q", got)
	}
}

func TestCreateTransportForServer_MissingCredential(t *testing.T) {
	t.Parallel()
	mgr := NewManagerFromConfig(&config.MCPConfig{})
	mgr.SetCredentialResolver(&mockResolver{creds: map[string]*credentials.Credential{}})
	_, _, err := mgr.createTransportForServer("svc", config.MCPServerConfig{
		Command: "echo",
		Env:     map[string]string{"TOKEN": "{{CREDENTIAL:missing:token}}"},
	})
	if err == nil {
		t.Fatal("expected credential resolution error")
	}
}

func TestCreateTransport_StdioDefault(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Command: "echo",
		Args:    []string{"hello"},
	}
	transport, stderrBuf, err := createTransport(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport == nil {
		t.Fatal("expected non-nil transport")
	}
	if stderrBuf == nil {
		t.Fatal("expected non-nil stderr buffer")
	}
}

func TestCreateTransport_StdioExplicit(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Transport: "stdio",
		Command:   "cat",
	}
	transport, _, err := createTransport(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestCreateStdioTransport_NoCommand(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Transport: "stdio",
		Command:   "",
	}
	_, _, err := createStdioTransport(cfg)
	if err == nil {
		t.Fatal("expected error when command is empty")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("command is required")) {
		t.Errorf("expected 'command is required' in error, got %q", err)
	}
}

func TestCreateStdioTransport_WithEnv(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Command: "echo",
		Args:    []string{"test"},
		Env:     map[string]string{"MY_VAR": "my_value"},
	}
	transport, buf, err := createStdioTransport(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport == nil {
		t.Fatal("expected non-nil transport")
	}
	if buf == nil {
		t.Fatal("expected non-nil stderr buffer")
	}
}

func TestCreateSSETransport_NoURL(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Transport: "sse",
		URL:       "",
	}
	_, _, err := createSSETransport(cfg)
	if err == nil {
		t.Fatal("expected error when URL is empty")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("URL is required")) {
		t.Errorf("expected 'URL is required' in error, got %q", err)
	}
}

func TestCreateSSETransport_WithURL(t *testing.T) {
	t.Parallel()
	cfg := config.MCPServerConfig{
		Transport: "sse",
		URL:       "http://localhost:8080/sse",
	}
	transport, buf, err := createSSETransport(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport == nil {
		t.Fatal("expected non-nil transport")
	}
	// SSE transport returns nil stderr buffer
	if buf != nil {
		t.Errorf("expected nil stderr buffer for SSE transport, got %v", buf)
	}
}
