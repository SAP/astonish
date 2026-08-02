package sandbox

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// nopCloser wraps an io.Reader as an io.ReadCloser with a no-op Close.
type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error { return nil }

func TestJSONLineFilterReader_PureJSON(t *testing.T) {
	// All lines are valid JSON-RPC — they should all pass through.
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}` + "\n"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != input {
		t.Errorf("expected all JSON to pass through\ngot:  %q\nwant: %q", string(out), input)
	}
}

func TestJSONLineFilterReader_DiscardsANSI(t *testing.T) {
	// ANSI spinner output interleaved with JSON responses.
	input := "\x1b[1G\x1b[0K|\x1b[1G\x1b[0K/\x1b[1G\x1b[0K-\n" +
		"\x1b[1G\x1b[0K\\\x1b[1G\x1b[0K|\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n" +
		"\x1b[1G\x1b[0K\n"

	expected := `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != expected {
		t.Errorf("ANSI lines not filtered\ngot:  %q\nwant: %q", string(out), expected)
	}
}

func TestJSONLineFilterReader_CapturesDiscardedStdoutDiagnostics(t *testing.T) {
	input := "npm ERR! network timeout\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n" +
		"server exited before initialize"

	var diagnostics bytes.Buffer
	r := newJSONLineFilterReaderWithDiagnostics(nopCloser{strings.NewReader(input)}, &diagnostics)
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}` + "\n"
	if string(out) != expected {
		t.Fatalf("JSON protocol output changed\ngot:  %q\nwant: %q", string(out), expected)
	}
	gotDiagnostics := diagnostics.String()
	if !strings.Contains(gotDiagnostics, "npm ERR! network timeout") {
		t.Fatalf("expected npm diagnostic in discarded stdout, got %q", gotDiagnostics)
	}
	if !strings.Contains(gotDiagnostics, "server exited before initialize") {
		t.Fatalf("expected unterminated diagnostic in discarded stdout, got %q", gotDiagnostics)
	}
	if strings.Contains(gotDiagnostics, "jsonrpc") {
		t.Fatalf("protocol JSON must not be captured as diagnostics: %q", gotDiagnostics)
	}
}

func TestJSONLineFilterReader_BoundsDiscardedStdoutDiagnostics(t *testing.T) {
	input := strings.Repeat("x", maxCapturedMCPStdoutDiagnosticsLen+100) + "\n"

	var diagnostics bytes.Buffer
	r := newJSONLineFilterReaderWithDiagnostics(nopCloser{strings.NewReader(input)}, &diagnostics)
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected no protocol output, got %q", string(out))
	}
	if diagnostics.Len() != maxCapturedMCPStdoutDiagnosticsLen {
		t.Fatalf("diagnostics length = %d, want %d", diagnostics.Len(), maxCapturedMCPStdoutDiagnosticsLen)
	}
}

func TestJSONLineFilterReader_DiscardsBanner(t *testing.T) {
	// MCP server startup banner followed by JSON.
	input := "Tavily MCP server running on stdio\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}` + "\n"

	expected := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}` + "\n"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != expected {
		t.Errorf("banner not filtered\ngot:  %q\nwant: %q", string(out), expected)
	}
}

func TestJSONLineFilterReader_CRLFLineEndings(t *testing.T) {
	// PTY output uses \r\n line endings.
	input := "\x1b[1G\x1b[0K|\r\n" +
		`{"jsonrpc":"2.0","id":1,"result":{}}` + "\r\n"

	// The filter passes the full line including \r\n.
	expected := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\r\n"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != expected {
		t.Errorf("CRLF handling wrong\ngot:  %q\nwant: %q", string(out), expected)
	}
}

func TestJSONLineFilterReader_UnterminatedJSONOnEOF(t *testing.T) {
	// Stream closes without final newline — should still pass JSON.
	input := `{"jsonrpc":"2.0","id":1,"result":{}}`

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != input {
		t.Errorf("unterminated JSON not passed\ngot:  %q\nwant: %q", string(out), input)
	}
}

func TestJSONLineFilterReader_UnterminatedNonJSONOnEOF(t *testing.T) {
	// Stream closes with unterminated non-JSON — should be discarded.
	input := "some random garbage without newline"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("unterminated non-JSON should be discarded, got: %q", string(out))
	}
}

func TestJSONLineFilterReader_SmallReadBuffer(t *testing.T) {
	// Read with a very small buffer to test partial serving.
	input := `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}}}}` + "\n"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})

	var result bytes.Buffer
	buf := make([]byte, 5) // tiny buffer
	for {
		n, err := r.Read(buf)
		if n > 0 {
			result.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if result.String() != input {
		t.Errorf("small buffer reads failed\ngot:  %q\nwant: %q", result.String(), input)
	}
}

func TestJSONLineFilterReader_MixedInterleavedContamination(t *testing.T) {
	// Simulates the exact scenario from the bug: npx spinner interleaved
	// between multiple JSON-RPC messages.
	input := "\x1b[1G\x1b[0K⠋ Installing...\n" +
		"\x1b[1G\x1b[0K⠙ Installing...\n" +
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}` + "\n" +
		"\x1b[1G\x1b[0K\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		"Node.js ExperimentalWarning: something\n" +
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"search"}]}}` + "\n"

	expected := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"search"}]}}` + "\n"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != expected {
		t.Errorf("interleaved contamination not handled\ngot:  %q\nwant: %q", string(out), expected)
	}
}

func TestJSONLineFilterReader_EmptyInput(t *testing.T) {
	r := newJSONLineFilterReader(nopCloser{strings.NewReader("")})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got: %q", string(out))
	}
}

func TestJSONLineFilterReader_OnlyNonJSON(t *testing.T) {
	// Stream with only non-JSON lines — should produce empty output.
	input := "hello world\n\x1b[31mred text\x1b[0m\nsome banner\n"

	r := newJSONLineFilterReader(nopCloser{strings.NewReader(input)})
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output for non-JSON input, got: %q", string(out))
	}
}

func TestIsJSONRPCLine(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{"jsonrpc":"2.0"}` + "\n", true},
		{`{"id":1,"result":{}}` + "\n", true},
		{"\r" + `{"jsonrpc":"2.0"}` + "\r\n", true}, // leading \r stripped
		{`{not-a-quote}` + "\n", false},             // { not followed by "
		{"\x1b[1G\x1b[0K\n", false},                 // ANSI
		{"hello\n", false},                          // plain text
		{"{", false},                                // too short
		{"{\n", false},                              // { followed by \n, not "
		{`{""}` + "\n", true},                       // minimal valid prefix
	}
	for _, tt := range tests {
		got := isJSONRPCLine([]byte(tt.input))
		if got != tt.want {
			t.Errorf("isJSONRPCLine(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests for binary resolution (npx/uvx → direct binary path)
// ---------------------------------------------------------------------------

func TestExtractBinaryName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Unscoped packages with version
		{"tavily-mcp@latest", "tavily-mcp"},
		{"tavily-mcp@0.2.20", "tavily-mcp"},
		{"firecrawl-mcp@1.0.0", "firecrawl-mcp"},

		// Unscoped packages without version
		{"tavily-mcp", "tavily-mcp"},
		{"firecrawl-mcp", "firecrawl-mcp"},

		// Scoped packages with version
		{"@anthropic/mcp-server@latest", "mcp-server"},
		{"@brave/brave-search-mcp-server@1.2.3", "brave-search-mcp-server"},
		{"@modelcontextprotocol/server-filesystem@0.1.0", "server-filesystem"},

		// Scoped packages without version
		{"@brave/brave-search-mcp-server", "brave-search-mcp-server"},
		{"@anthropic/mcp-server", "mcp-server"},

		// Edge cases
		{"", ""},
		{"@malformed", ""},   // scoped but no slash
		{"simple", "simple"}, // plain name, no version
	}
	for _, tt := range tests {
		got := extractBinaryName(tt.input)
		if got != tt.want {
			t.Errorf("extractBinaryName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParsePackageManagerCommand_Npx(t *testing.T) {
	tests := []struct {
		name      string
		command   []string
		wantBin   string
		wantExtra []string
		wantOK    bool
	}{
		{
			name:      "npx -y package@version",
			command:   []string{"npx", "-y", "tavily-mcp@latest"},
			wantBin:   "tavily-mcp",
			wantExtra: nil,
			wantOK:    true,
		},
		{
			name:      "npx --yes package",
			command:   []string{"npx", "--yes", "firecrawl-mcp"},
			wantBin:   "firecrawl-mcp",
			wantExtra: nil,
			wantOK:    true,
		},
		{
			name:      "npx -y scoped package with extra args",
			command:   []string{"npx", "-y", "@brave/brave-search-mcp-server", "--transport", "stdio"},
			wantBin:   "brave-search-mcp-server",
			wantExtra: []string{"--transport", "stdio"},
			wantOK:    true,
		},
		{
			name:      "npx without -y — not matched",
			command:   []string{"npx", "tavily-mcp@latest"},
			wantBin:   "",
			wantExtra: nil,
			wantOK:    false,
		},
		{
			name:      "not npx",
			command:   []string{"node", "server.js"},
			wantBin:   "",
			wantExtra: nil,
			wantOK:    false,
		},
		{
			name:      "empty command",
			command:   []string{},
			wantBin:   "",
			wantExtra: nil,
			wantOK:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, extra, ok := parsePackageManagerCommand(tt.command)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if bin != tt.wantBin {
				t.Errorf("bin = %q, want %q", bin, tt.wantBin)
			}
			if tt.wantExtra == nil && extra != nil {
				t.Errorf("extra = %v, want nil", extra)
			}
			if tt.wantExtra != nil {
				if len(extra) != len(tt.wantExtra) {
					t.Errorf("extra = %v, want %v", extra, tt.wantExtra)
				} else {
					for i := range extra {
						if extra[i] != tt.wantExtra[i] {
							t.Errorf("extra[%d] = %q, want %q", i, extra[i], tt.wantExtra[i])
						}
					}
				}
			}
		})
	}
}

func TestParsePackageManagerCommand_Uvx(t *testing.T) {
	tests := []struct {
		name      string
		command   []string
		wantBin   string
		wantExtra []string
		wantOK    bool
	}{
		{
			name:      "uvx package@version",
			command:   []string{"uvx", "mcp-server-fetch@latest"},
			wantBin:   "mcp-server-fetch",
			wantExtra: nil,
			wantOK:    true,
		},
		{
			name:      "uvx package without version",
			command:   []string{"uvx", "mcp-server-fetch"},
			wantBin:   "mcp-server-fetch",
			wantExtra: nil,
			wantOK:    true,
		},
		{
			name:      "uvx with extra args",
			command:   []string{"uvx", "mcp-server-git@0.5.0", "--repo", "/workspace"},
			wantBin:   "mcp-server-git",
			wantExtra: []string{"--repo", "/workspace"},
			wantOK:    true,
		},
		{
			name:      "uvx with flags before package",
			command:   []string{"uvx", "--from", "some-package", "binary-name"},
			wantBin:   "some-package",
			wantExtra: []string{"binary-name"},
			wantOK:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, extra, ok := parsePackageManagerCommand(tt.command)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if bin != tt.wantBin {
				t.Errorf("bin = %q, want %q", bin, tt.wantBin)
			}
			if tt.wantExtra == nil && extra != nil {
				t.Errorf("extra = %v, want nil", extra)
			}
			if tt.wantExtra != nil {
				if len(extra) != len(tt.wantExtra) {
					t.Errorf("extra = %v, want %v", extra, tt.wantExtra)
				} else {
					for i := range extra {
						if extra[i] != tt.wantExtra[i] {
							t.Errorf("extra[%d] = %q, want %q", i, extra[i], tt.wantExtra[i])
						}
					}
				}
			}
		})
	}
}

// mockExecBackend is a minimal Backend mock for testing resolvePackageManagerCommand.
type mockExecBackend struct {
	Backend // embed to satisfy interface (unimplemented methods will panic)
	execFn  func(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error)
}

func (m *mockExecBackend) Exec(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error) {
	return m.execFn(ctx, sessionID, opts)
}

func (m *mockExecBackend) Kind() BackendKind {
	return BackendKindOpenShell
}

func TestResolvePackageManagerCommand_BinaryFound(t *testing.T) {
	backend := &mockExecBackend{
		execFn: func(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error) {
			if len(opts.Command) == 2 && opts.Command[0] == "which" && opts.Command[1] == "tavily-mcp" {
				return &ExecResult{
					ExitCode: 0,
					Stdout:   []byte("/usr/bin/tavily-mcp\n"),
				}, nil
			}
			return &ExecResult{ExitCode: 1}, nil
		},
	}

	command := []string{"npx", "-y", "tavily-mcp@latest"}
	resolved := resolvePackageManagerCommand(context.Background(), backend, "test-session", command)

	if len(resolved) != 1 || resolved[0] != "/usr/bin/tavily-mcp" {
		t.Errorf("expected [/usr/bin/tavily-mcp], got %v", resolved)
	}
}

func TestResolvePackageManagerCommand_BinaryNotFound(t *testing.T) {
	backend := &mockExecBackend{
		execFn: func(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error) {
			return &ExecResult{ExitCode: 1, Stderr: []byte("not found")}, nil
		},
	}

	command := []string{"npx", "-y", "some-unknown-package@latest"}
	resolved := resolvePackageManagerCommand(context.Background(), backend, "test-session", command)

	// Should return original command unchanged
	if len(resolved) != 3 || resolved[0] != "npx" {
		t.Errorf("expected original command unchanged, got %v", resolved)
	}
}

func TestResolvePackageManagerCommand_WithExtraArgs(t *testing.T) {
	backend := &mockExecBackend{
		execFn: func(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error) {
			if opts.Command[1] == "brave-search-mcp-server" {
				return &ExecResult{
					ExitCode: 0,
					Stdout:   []byte("/usr/local/bin/brave-search-mcp-server\n"),
				}, nil
			}
			return &ExecResult{ExitCode: 1}, nil
		},
	}

	command := []string{"npx", "-y", "@brave/brave-search-mcp-server", "--transport", "stdio"}
	resolved := resolvePackageManagerCommand(context.Background(), backend, "test-session", command)

	expected := []string{"/usr/local/bin/brave-search-mcp-server", "--transport", "stdio"}
	if len(resolved) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, resolved)
	}
	for i := range expected {
		if resolved[i] != expected[i] {
			t.Errorf("resolved[%d] = %q, want %q", i, resolved[i], expected[i])
		}
	}
}

func TestResolvePackageManagerCommand_NonNpxCommand(t *testing.T) {
	backend := &mockExecBackend{
		execFn: func(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error) {
			t.Fatal("Exec should not be called for non-npx/uvx commands")
			return nil, nil
		},
	}

	command := []string{"node", "server.js"}
	resolved := resolvePackageManagerCommand(context.Background(), backend, "test-session", command)

	if len(resolved) != 2 || resolved[0] != "node" {
		t.Errorf("expected original command, got %v", resolved)
	}
}

func TestResolvePackageManagerCommand_UvxBinaryFound(t *testing.T) {
	backend := &mockExecBackend{
		execFn: func(ctx context.Context, sessionID string, opts ExecSpec) (*ExecResult, error) {
			if opts.Command[1] == "mcp-server-fetch" {
				return &ExecResult{
					ExitCode: 0,
					Stdout:   []byte("/usr/local/bin/mcp-server-fetch\n"),
				}, nil
			}
			return &ExecResult{ExitCode: 1}, nil
		},
	}

	command := []string{"uvx", "mcp-server-fetch@latest"}
	resolved := resolvePackageManagerCommand(context.Background(), backend, "test-session", command)

	if len(resolved) != 1 || resolved[0] != "/usr/local/bin/mcp-server-fetch" {
		t.Errorf("expected [/usr/local/bin/mcp-server-fetch], got %v", resolved)
	}
}
