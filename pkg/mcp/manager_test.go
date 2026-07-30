package mcp

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

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
