package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BackendMCPTransport implements mcp.Transport by running the MCP server
// process inside a sandbox container via the abstract Backend interface.
// This works for both Incus and Kubernetes backends.
//
// The MCP SDK expects a Transport that returns a bidirectional JSON-RPC
// connection. This transport uses Backend.ExecInteractive() to get a stream
// attached to the MCP server process's stdin/stdout inside the container.
type BackendMCPTransport struct {
	backend   Backend
	sessionID string
	command   []string
	env       map[string]string

	// Stderr captures the MCP server's stderr output for diagnostics.
	Stderr *bytes.Buffer

	// StdoutDiagnostics captures non-JSON stdout lines discarded by the protocol
	// filter. Valid JSON-RPC messages are never captured here.
	StdoutDiagnostics *bytes.Buffer

	mu     sync.Mutex
	stream ExecStream
}

// NewBackendMCPTransport creates a transport that will run the given MCP
// server config inside the specified session's sandbox. The transport is
// not started until Connect() is called.
func NewBackendMCPTransport(backend Backend, sessionID string, cfg config.MCPServerConfig) (*BackendMCPTransport, *bytes.Buffer) {
	cmd := append([]string{cfg.Command}, cfg.Args...)
	env := make(map[string]string, len(cfg.Env))
	for k, v := range cfg.Env {
		env[k] = v
	}

	var stderr bytes.Buffer
	var stdoutDiagnostics bytes.Buffer
	return &BackendMCPTransport{
		backend:           backend,
		sessionID:         sessionID,
		command:           cmd,
		env:               env,
		Stderr:            &stderr,
		StdoutDiagnostics: &stdoutDiagnostics,
	}, &stderr
}

// Connect starts the MCP server process inside the container and returns a
// Connection that communicates with it over stdin/stdout. Implements mcp.Transport.
func (t *BackendMCPTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Ensure PATH includes common tool locations (npx, uv, etc.)
	env := make(map[string]string, len(t.env)+1)
	for k, v := range t.env {
		env[k] = v
	}
	if _, ok := env["PATH"]; !ok {
		env["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/root/.local/bin"
	}

	// Resolve npx/uvx commands to direct binary paths when the binary is
	// already installed in the sandbox. This avoids network round-trips to
	// package registries (npm, PyPI) that may be blocked or slow through
	// the supervisor's transparent proxy.
	resolved := resolvePackageManagerCommand(ctx, t.backend, t.sessionID, t.command)

	// Determine the exec command based on backend kind:
	//
	// K8s: kubectl exec lands in the pod's base namespace (bare Debian),
	//   not PID 1's chroot (/sandbox/rootfs where runtimes like Node.js,
	//   Python etc. are installed). The astonish-shell wrapper does
	//   `exec chroot /sandbox/rootfs "$@"` to enter the composed overlay.
	//
	// Incus: the overlay IS the container's root filesystem — no chroot
	//   needed. Commands execute directly in the correct namespace.
	//   Using astonish-shell here would be WRONG because the fallback
	//   path (when /sandbox/rootfs doesn't exist) discards all arguments
	//   and just launches /bin/bash -l.
	execCmd := resolved
	if t.backend.Kind() == BackendKindK8s {
		execCmd = append([]string{"/usr/local/bin/astonish-shell"}, resolved...)
	}

	stream, err := t.backend.ExecStreaming(ctx, t.sessionID, ExecStreamSpec{
		Command:        execCmd,
		Env:            env,
		SeparateStderr: t.Stderr, // Keep stderr separate from JSON-RPC stdout
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start MCP server in session %q: %w", t.sessionID, err)
	}
	t.stream = stream

	// Wrap the stream's Reader/Writer in an IOTransport.
	// ExecStream implements io.Reader (stdout) and io.Writer (stdin).
	// The jsonLineFilterReader discards any non-JSON lines (ANSI escapes,
	// spinners, banners) that PTY contamination injects into the stream.
	// Discarded lines are captured separately for diagnostics so startup EOFs
	// do not hide npm/node/package-manager failures printed on stdout.
	// Every line is checked — only lines starting with '{"' pass through.
	rwc := &backendStreamRWC{stream: stream}
	filteredReader := newJSONLineFilterReaderWithDiagnostics(rwc, t.StdoutDiagnostics)

	inner := &mcp.IOTransport{
		Reader: filteredReader,
		Writer: rwc,
	}
	return inner.Connect(ctx)
}

// Close terminates the MCP server process.
func (t *BackendMCPTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stream != nil {
		if err := t.stream.Close(); err != nil {
			slog.Warn("failed to close MCP backend stream", "component", "sandbox", "session", t.sessionID, "error", err)
		}
		t.stream = nil
	}
	return nil
}

// backendStreamRWC adapts an ExecStream to io.ReadWriteCloser for mcp.IOTransport.
type backendStreamRWC struct {
	stream    ExecStream
	closeOnce sync.Once
	closeErr  error
}

func (s *backendStreamRWC) Read(p []byte) (int, error) {
	return s.stream.Read(p)
}

func (s *backendStreamRWC) Write(p []byte) (int, error) {
	return s.stream.Write(p)
}

func (s *backendStreamRWC) Close() error {
	s.closeOnce.Do(func() {
		// Close stdin by closing the write side — signals MCP server to shut down
		if closer, ok := s.stream.(io.Closer); ok {
			s.closeErr = closer.Close()
		}
	})
	return s.closeErr
}

// ---------------------------------------------------------------------------
// Package manager binary resolution
// ---------------------------------------------------------------------------

// resolvePackageManagerCommand checks if an npx/uvx command can be replaced
// with a direct binary path already installed in the sandbox. This avoids
// network round-trips to package registries that may be blocked or slow
// through the supervisor's transparent proxy.
//
// Patterns handled:
//
//	npx -y <package>@<version> [extra-args...]  → [/path/to/binary, extra-args...]
//	npx -y <package> [extra-args...]            → [/path/to/binary, extra-args...]
//	npx -y @scope/name@version [extra-args...]  → [/path/to/name, extra-args...]
//	uvx <package>@<version> [extra-args...]     → [/path/to/binary, extra-args...]
//	uvx <package> [extra-args...]               → [/path/to/binary, extra-args...]
//
// If the binary is not found in the sandbox, the original command is returned
// unchanged so that npx/uvx can attempt a network install as a fallback.
func resolvePackageManagerCommand(ctx context.Context, backend Backend, sessionID string, command []string) []string {
	if len(command) == 0 {
		return command
	}

	binName, extraArgs, ok := parsePackageManagerCommand(command)
	if !ok {
		return command
	}

	// Run "which <binary>" inside the sandbox to check if it's installed.
	whichCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	result, err := backend.Exec(whichCtx, sessionID, ExecSpec{
		Command: []string{"which", binName},
	})
	if err != nil || result.ExitCode != 0 {
		slog.Debug("MCP binary not found in sandbox, using original command",
			"binary", binName, "command", command[0])
		return command
	}

	resolvedPath := strings.TrimSpace(string(result.Stdout))
	if resolvedPath == "" {
		return command
	}

	resolved := append([]string{resolvedPath}, extraArgs...)
	slog.Info("MCP command resolved to installed binary",
		"original", strings.Join(command, " "),
		"resolved", strings.Join(resolved, " "))
	return resolved
}

// parsePackageManagerCommand detects npx/uvx invocation patterns and extracts
// the binary name and any extra arguments that follow the package specifier.
//
// Returns (binaryName, extraArgs, true) if the command matches a known pattern,
// or ("", nil, false) if it does not.
func parsePackageManagerCommand(command []string) (string, []string, bool) {
	if len(command) == 0 {
		return "", nil, false
	}

	switch command[0] {
	case "npx":
		return parseNpxCommand(command[1:])
	case "uvx":
		return parseUvxCommand(command[1:])
	default:
		return "", nil, false
	}
}

// parseNpxCommand parses npx arguments to extract the package/binary name.
// Expected patterns:
//
//	npx -y <package>[@version] [extra-args...]
//	npx --yes <package>[@version] [extra-args...]
//
// The -y/--yes flag means "auto-confirm install". The package argument is
// the first positional arg after flags.
func parseNpxCommand(args []string) (string, []string, bool) {
	hasYes := false
	var packageArg string
	packageIdx := -1

	for i, arg := range args {
		if arg == "-y" || arg == "--yes" {
			hasYes = true
			continue
		}
		// Skip other flags (e.g., --package, -p)
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// First positional argument is the package specifier
		packageArg = arg
		packageIdx = i
		break
	}

	if !hasYes || packageArg == "" {
		return "", nil, false
	}

	binName := extractBinaryName(packageArg)
	if binName == "" {
		return "", nil, false
	}

	// Collect extra args (everything after the package argument)
	var extraArgs []string
	if packageIdx+1 < len(args) {
		extraArgs = args[packageIdx+1:]
	}

	return binName, extraArgs, true
}

// parseUvxCommand parses uvx arguments to extract the package/binary name.
// Expected patterns:
//
//	uvx <package>[@version] [extra-args...]
//
// uvx runs Python packages directly — the first positional arg is the package.
func parseUvxCommand(args []string) (string, []string, bool) {
	var packageArg string
	packageIdx := -1

	for i, arg := range args {
		// Skip flags
		if strings.HasPrefix(arg, "-") {
			continue
		}
		packageArg = arg
		packageIdx = i
		break
	}

	if packageArg == "" {
		return "", nil, false
	}

	binName := extractBinaryName(packageArg)
	if binName == "" {
		return "", nil, false
	}

	// Collect extra args (everything after the package argument)
	var extraArgs []string
	if packageIdx+1 < len(args) {
		extraArgs = args[packageIdx+1:]
	}

	return binName, extraArgs, true
}

// extractBinaryName extracts the likely binary name from an npm/PyPI package
// specifier. The binary name is typically the package name without scope or
// version suffix.
//
// Examples:
//
//	"tavily-mcp@latest"              → "tavily-mcp"
//	"tavily-mcp@0.2.20"             → "tavily-mcp"
//	"tavily-mcp"                     → "tavily-mcp"
//	"@anthropic/mcp-server@latest"   → "mcp-server"
//	"@brave/brave-search-mcp-server" → "brave-search-mcp-server"
//	"firecrawl-mcp"                  → "firecrawl-mcp"
func extractBinaryName(packageSpec string) string {
	if packageSpec == "" {
		return ""
	}

	name := packageSpec

	// Handle scoped packages: @scope/name[@version]
	if strings.HasPrefix(name, "@") {
		slashIdx := strings.Index(name, "/")
		if slashIdx < 0 {
			// Malformed scoped package (no slash)
			return ""
		}
		// Take the part after the scope
		name = name[slashIdx+1:]
	}

	// Strip version suffix: name@version → name
	// Be careful: only strip if there's an @ that isn't at the start
	if atIdx := strings.LastIndex(name, "@"); atIdx > 0 {
		name = name[:atIdx]
	}

	return name
}

const maxCapturedMCPStdoutDiagnosticsLen = 4096

// jsonLineFilterReader wraps an io.Reader and only passes through lines that
// begin with '{"' (a JSON-RPC message). All other lines are discarded. This
// provides robust protection against PTY contamination from any source:
//
//   - ANSI escape sequences from PTY setup or terminal title changes
//   - npx/uvx download spinners (which use ANSI progress indicators)
//   - Shell motd/banner output
//   - Node.js ExperimentalWarning messages
//   - MCP server startup banners ("Server running on stdio")
//
// The MCP protocol uses NDJSON (newline-delimited JSON): one compact JSON
// object per line, terminated by '\n'. Both the Go and TypeScript MCP SDKs
// always emit single-line compact JSON, so a line-based filter is safe.
//
// Every line is checked — there is no "passthrough mode" transition.
// This ensures interleaved contamination (e.g., spinner frames between
// JSON responses) is always caught.
type jsonLineFilterReader struct {
	inner       io.ReadCloser
	diagnostics *bytes.Buffer
	lineBuf     []byte // accumulates bytes until \n
	passData    []byte // current JSON line being served to caller
	passOff     int    // read offset into passData
}

func newJSONLineFilterReader(r io.ReadCloser) *jsonLineFilterReader {
	return newJSONLineFilterReaderWithDiagnostics(r, nil)
}

func newJSONLineFilterReaderWithDiagnostics(r io.ReadCloser, diagnostics *bytes.Buffer) *jsonLineFilterReader {
	return &jsonLineFilterReader{inner: r, diagnostics: diagnostics}
}

func (f *jsonLineFilterReader) Read(p []byte) (int, error) {
	for {
		// Serve buffered pass-through data first.
		if f.passOff < len(f.passData) {
			n := copy(p, f.passData[f.passOff:])
			f.passOff += n
			if f.passOff >= len(f.passData) {
				f.passData = nil
				f.passOff = 0
			}
			return n, nil
		}

		// Process any complete lines already in lineBuf before reading more.
		for {
			idx := bytes.IndexByte(f.lineBuf, '\n')
			if idx < 0 {
				break
			}
			line := f.lineBuf[:idx+1] // includes the \n
			f.lineBuf = f.lineBuf[idx+1:]

			if isJSONRPCLine(line) {
				f.passData = append([]byte(nil), line...)
				f.passOff = 0
				n := copy(p, f.passData[f.passOff:])
				f.passOff += n
				if f.passOff >= len(f.passData) {
					f.passData = nil
					f.passOff = 0
				}
				return n, nil
			}
			f.captureDiagnostic(line)
			// Non-JSON line — discard and continue processing lineBuf.
		}

		// No complete JSON line available — read more from underlying stream.
		tmp := make([]byte, 4096)
		n, readErr := f.inner.Read(tmp)
		if n > 0 {
			f.lineBuf = append(f.lineBuf, tmp[:n]...)
			continue // Loop back to process new data in lineBuf.
		}

		if readErr != nil {
			// On EOF/error, check if lineBuf has an unterminated JSON line.
			if len(f.lineBuf) > 0 && isJSONRPCLine(f.lineBuf) {
				f.passData = f.lineBuf
				f.lineBuf = nil
				f.passOff = 0
				n := copy(p, f.passData[f.passOff:])
				f.passOff += n
				if f.passOff >= len(f.passData) {
					f.passData = nil
					f.passOff = 0
				}
				return n, nil
			}
			if len(f.lineBuf) > 0 {
				f.captureDiagnostic(f.lineBuf)
				f.lineBuf = nil
			}
			return 0, readErr
		}
	}
}

// isJSONRPCLine returns true if the line starts with '{"' after stripping
// any leading \r bytes. Every valid JSON-RPC message begins with {"
// (e.g., {"jsonrpc":"2.0",...}). This is more specific than checking for
// just '{' and eliminates any theoretical ANSI sequence that might contain
// a lone '{' character.
func isJSONRPCLine(line []byte) bool {
	b := bytes.TrimLeft(line, "\r")
	return len(b) >= 2 && b[0] == '{' && b[1] == '"'
}

func (f *jsonLineFilterReader) captureDiagnostic(line []byte) {
	if f.diagnostics == nil || len(line) == 0 || f.diagnostics.Len() >= maxCapturedMCPStdoutDiagnosticsLen {
		return
	}
	remaining := maxCapturedMCPStdoutDiagnosticsLen - f.diagnostics.Len()
	if len(line) <= remaining {
		f.diagnostics.Write(line)
		return
	}
	f.diagnostics.Write(line[:remaining])
}

func (f *jsonLineFilterReader) Close() error {
	return f.inner.Close()
}
