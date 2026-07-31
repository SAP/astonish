package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/gorilla/mux"
)

func testSetup(t *testing.T) func() {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "api-cache-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	cache.SetCacheDir(tmpDir)

	return func() {
		os.RemoveAll(tmpDir)
		cache.SetCacheDir("")
	}
}

func TestMCPStatusHandler(t *testing.T) {
	cleanup := testSetup(t)
	defer cleanup()

	// Without platform context (no MCP store), handler returns empty servers list
	req, err := http.NewRequest("GET", "/api/mcp/status", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(MCPStatusHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response struct {
		Servers []cache.ServerStatus `json:"servers"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(response.Servers) != 0 {
		t.Errorf("expected 0 servers without platform context, got %d", len(response.Servers))
	}
}

func TestMCPInspectorUsesSandbox(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  config.MCPServerConfig
		want bool
	}{
		{"default stdio uses sandbox", config.MCPServerConfig{Command: "npx"}, true},
		{"explicit stdio uses sandbox", config.MCPServerConfig{Transport: "stdio", Command: "npx"}, true},
		{"sse stays remote", config.MCPServerConfig{Transport: "sse", URL: "https://example.test/sse"}, false},
		{"streamable http stays remote", config.MCPServerConfig{Transport: "streamable-http", URL: "https://example.test/mcp"}, false},
		{"url without transport still uses sandbox", config.MCPServerConfig{URL: "https://example.test/sse"}, true},
		{"stdio with url still uses sandbox", config.MCPServerConfig{Transport: "stdio", Command: "npx", URL: "https://example.test/sse"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := mcpInspectorUsesSandbox(tt.cfg); got != tt.want {
				t.Fatalf("mcpInspectorUsesSandbox() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeMCPToolSchemas(t *testing.T) {
	t.Parallel()
	tools, err := decodeMCPToolSchemas(json.RawMessage(`[{"name":"lookup","description":"Lookup docs","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}]`))
	if err != nil {
		t.Fatalf("decodeMCPToolSchemas failed: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "lookup" || tools[0].Description != "Lookup docs" {
		t.Fatalf("unexpected tool metadata: %+v", tools[0])
	}
	params, ok := tools[0].Parameters.(map[string]any)
	if !ok || params["type"] != "object" {
		t.Fatalf("expected object parameters, got %#v", tools[0].Parameters)
	}
}

func TestLogMCPInspectorFailure_IncludesListToolsDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	cfg := config.MCPServerConfig{
		Command: "npx",
		Args:    []string{"-y", "@upstash/context7-mcp"},
		Env: map[string]string{
			"CONTEXT7_API_KEY": "secret-value",
		},
	}
	stderr := bytes.NewBufferString("server exited during initialize")

	logMCPInspectorFailure("MCP inspector failed to list tools", "context7", cfg, errors.New("calling initialize: EOF"), stderr, "phase", "list_tools")

	out := buf.String()
	for _, want := range []string{"MCP inspector failed to list tools", "server=context7", "phase=list_tools", "command=npx", "@upstash/context7-mcp", "CONTEXT7_API_KEY", "calling initialize: EOF", "server exited during initialize"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected log output to contain %q, got %s", want, out)
		}
	}
	if strings.Contains(out, "secret-value") {
		t.Fatalf("log output leaked env value: %s", out)
	}
}

func TestListServerToolsHandler_Error(t *testing.T) {
	cleanup := testSetup(t)
	defer cleanup()

	// Invalidate cache to ensure it tries to load
	cache.InvalidateCache()

	// Create a request with a non-existent server
	req, err := http.NewRequest("GET", "/api/mcp/nonexistent/tools", nil)
	if err != nil {
		t.Fatal(err)
	}
	req = mux.SetURLVars(req, map[string]string{"serverName": "nonexistent"})

	// Record the response
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(ListServerToolsHandler)
	handler.ServeHTTP(rr, req)

	// Verify results
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response ListServerToolsResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response.Error == "" {
		t.Error("expected error message for non-existent server, got empty")
	}
}
