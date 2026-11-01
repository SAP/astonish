package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// processNodeBackend exercises the actual node protocol, without importing or
// registering mock or contacting any external sandbox. Unused Backend methods
// deliberately remain unimplemented; this test starts with a bound client.
type processNodeBackend struct {
	Backend
	binary string
	home   string
	result *ExecResult
	state  *os.ProcessState
}

func (b *processNodeBackend) CreateSession(ctx context.Context, spec SessionSpec) (*Session, error) {
	return &Session{SessionID: spec.SessionID}, ctx.Err()
}

func (b *processNodeBackend) StartSession(ctx context.Context, _ string) error { return ctx.Err() }

func (b *processNodeBackend) SessionState(ctx context.Context, _ string) (SessionState, error) {
	return SessionStateRunning, ctx.Err()
}

func (b *processNodeBackend) WaitForSessionReady(ctx context.Context, _ string) error {
	return ctx.Err()
}

func (b *processNodeBackend) Exec(ctx context.Context, _ string, spec ExecSpec) (*ExecResult, error) {
	if len(spec.Command) != 2 || spec.Command[0] != "astonish" || spec.Command[1] != "node" {
		return nil, fmt.Errorf("unexpected command: %q", spec.Command)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.binary, spec.Command[1:]...)
	cmd.Dir = b.home
	cmd.Env = []string{"HOME=" + b.home, "XDG_CONFIG_HOME=" + b.home, "XDG_CACHE_HOME=" + b.home, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = spec.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run() // Run waits/reaps the child, including cancellation failures.
	b.state = cmd.ProcessState
	if err != nil {
		return nil, fmt.Errorf("node subprocess: %w; stderr=%s", err, stderr.String())
	}
	b.result = &ExecResult{ExitCode: cmd.ProcessState.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	return b.result, nil
}

func TestBackendNodeLargeResultProcess(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "astonish")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	build.Dir = filepath.Join("..", "..") // Build the root CLI, not a test protocol surrogate.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build root CLI: %v\n%s", err, output)
	}

	// One UTF-8 line remains below read_file's own 256 KiB output cap while
	// producing an NDJSON response well above Scanner's default 64 KiB limit.
	content := strings.Repeat("héllo 世界 🌍 ", 6000)
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &processNodeBackend{binary: binary, home: dir}
	client := &backendNodeClient{backend: backend, sessionID: "large-result-test", state: backendReady}
	result, err := client.Call("large-result-test", "read_file", map[string]interface{}{"path": path, "force": true})
	if err != nil {
		t.Fatalf("read_file via real node process: %v", err)
	}
	if backend.state == nil || !backend.state.Success() || backend.result == nil || backend.result.ExitCode != 0 {
		t.Fatal("node subprocess did not exit cleanly")
	}
	if len(backend.result.Stdout) <= 64*1024 || !bytes.HasPrefix(backend.result.Stdout, []byte("{\"ready\":true}\n")) {
		t.Fatal("expected real ready frame and >64 KiB response")
	}
	var got struct {
		Content    string `json:"content"`
		TotalLines int    `json:"total_lines"`
		Truncated  bool   `json:"truncated"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatalf("decode read_file result: %v", err)
	}
	// read_file intentionally prefixes each line with its line number.
	if got.Content != "1: "+content || got.TotalLines != 1 || got.Truncated {
		t.Fatalf("read_file content changed: got %d bytes, want %d; lines=%d truncated=%v", len(got.Content), len(content)+3, got.TotalLines, got.Truncated)
	}
	t.Logf("real node read_file: %d file bytes, %d stdout bytes, exact UTF-8 content, exit=%d", len(content), len(backend.result.Stdout), backend.result.ExitCode)
}

func TestBackendPool_Alias(t *testing.T) {
	// Use a nil backend — we only test pool-level aliasing, not Exec.
	pool := &backendPool{
		clients: make(map[string]*backendNodeClient),
	}

	parentID := "parent-session-111"
	childID := "child-session-222"

	// Pre-populate the parent client.
	parentClient := &backendNodeClient{}
	pool.clients[parentID] = parentClient

	// Alias child → parent.
	pool.Alias(childID, parentID)

	// GetOrCreate(child) should return the same client as the parent.
	got := pool.GetOrCreate(childID)
	if got != parentClient {
		t.Fatalf("Alias: GetOrCreate(child) returned %p, want %p (parent)", got, parentClient)
	}

	// GetOrCreate(parent) should still return the parent.
	gotParent := pool.GetOrCreate(parentID)
	if gotParent != parentClient {
		t.Fatalf("Alias: GetOrCreate(parent) returned %p, want %p", gotParent, parentClient)
	}
}

func TestBackendPool_Alias_NoOpWhenParentMissing(t *testing.T) {
	pool := &backendPool{
		clients: make(map[string]*backendNodeClient),
	}

	// Alias with no parent in pool — should be a no-op.
	pool.Alias("child", "nonexistent-parent")

	if _, ok := pool.clients["child"]; ok {
		t.Fatal("Alias should not create an entry when parent is missing")
	}
}

func TestBackendPool_Alias_NoOpAfterCleanup(t *testing.T) {
	pool := &backendPool{
		clients: make(map[string]*backendNodeClient),
	}

	parentClient := &backendNodeClient{}
	pool.clients["parent"] = parentClient

	// Close the pool.
	pool.closed = true

	pool.Alias("child", "parent")

	// Should not have created the alias because pool is closed.
	if _, ok := pool.clients["child"]; ok {
		t.Fatal("Alias should not create an entry when pool is closed")
	}
}

func TestBackendPool_Alias_EmptyIDs(t *testing.T) {
	pool := &backendPool{
		clients: make(map[string]*backendNodeClient),
	}

	parentClient := &backendNodeClient{}
	pool.clients["parent"] = parentClient

	// Empty child — no-op.
	pool.Alias("", "parent")
	if len(pool.clients) != 1 {
		t.Fatal("empty child should not add entry")
	}

	// Empty parent — no-op.
	pool.Alias("child", "")
	if len(pool.clients) != 1 {
		t.Fatal("empty parent should not add entry")
	}
}

type legacyReadinessClient struct {
	ToolNodeClient
	id  string
	err error
}

func (c *legacyReadinessClient) EnsureReady(id string) error { c.id = id; return c.err }

type ownedReadinessClient struct {
	legacyReadinessClient
	owner string
	ctx   context.Context
}

func (c *ownedReadinessClient) SandboxSessionID() string { return c.owner }
func (c *ownedReadinessClient) EnsureReadyContext(ctx context.Context, id string) error {
	c.ctx, c.id = ctx, id
	return c.err
}

func TestCanonicalSessionID(t *testing.T) {
	var typedNil *ownedReadinessClient
	for _, tt := range []struct {
		name   string
		client ToolNodeClient
		want   string
	}{
		{"nil", nil, "logical"},
		{"typed nil", typedNil, "logical"},
		{"legacy", &legacyReadinessClient{}, "logical"},
		{"empty owner", &ownedReadinessClient{}, "logical"},
		{"owned", &ownedReadinessClient{owner: "physical"}, "physical"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalSessionID(tt.client, "logical"); got != tt.want {
				t.Fatalf("owner = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadinessHelper(t *testing.T) {
	if err := EnsureToolClientReady(context.Background(), nil, "logical"); err == nil {
		t.Fatal("nil client accepted")
	}
	var typedNil *ownedReadinessClient
	if err := EnsureToolClientReady(context.Background(), typedNil, "logical"); err == nil {
		t.Fatal("typed nil accepted")
	}
	failure := fmt.Errorf("readiness failure")
	legacy := &legacyReadinessClient{err: failure}
	if err := EnsureToolClientReady(context.Background(), legacy, "logical"); err != failure || legacy.id != "logical" {
		t.Fatalf("legacy fallback: %v, %q", err, legacy.id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owned := &ownedReadinessClient{owner: "physical"}
	if err := EnsureToolClientReady(ctx, owned, "logical"); err != nil || owned.id != "physical" || owned.ctx != ctx {
		t.Fatalf("context ownership forwarding: %v, %+v", err, owned)
	}
	cancel()
	owned.id = ""
	if err := EnsureToolClientReady(ctx, owned, "logical"); err != context.Canceled || owned.id != "" {
		t.Fatalf("cancelled readiness caused side effect: %v", err)
	}
}

func TestBackendNodeReadinessDirectFirstBindOwner(t *testing.T) {
	c := newBackendNodeClient(&processNodeBackend{}, "default", nil, "", ResourceLimits{}, nil)
	c.BindSession("parent")
	if err := c.EnsureReady("child"); err != nil {
		t.Fatal(err)
	}
	if got := c.SandboxSessionID(); got != "parent" {
		t.Fatalf("owner = %q", got)
	}
	c.discard()
	if err := c.EnsureReady("child"); err == nil {
		t.Fatal("discarded client became ready")
	}
}
