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
	done := make(chan struct{})
	close(done)
	client := &backendNodeClient{backend: backend, sessionID: "large-result-test", bound: true, bindDone: done}
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
