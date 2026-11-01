// Package docker_test — unit tests for the Docker sandbox backend.
//
// These tests exercise construction, factory registration, config validation,
// and the pure-accessor methods (Kind, Capabilities, ServerArchitecture).
// Tests that require a live Docker daemon are guarded with t.Skip and only
// run when ASTONISH_TEST_DOCKER=1 is set.
package docker_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
)

// newTestRegistry returns a minimal session registry for tests.
func newTestRegistry(t *testing.T) *sandbox.SessionRegistry {
	t.Helper()
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatalf("NewSessionRegistry: %v", err)
	}
	return r
}

// newTestConfig returns a Config wired to t.TempDir() directories.
func newTestConfig(t *testing.T, r *sandbox.SessionRegistry) docker.Config {
	t.Helper()
	return docker.Config{
		Sessions:     r,
		LayersDir:    t.TempDir(),
		UppersDir:    t.TempDir(),
		SandboxImage: "ghcr.io/sap/astonish-sandbox-base:latest",
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNew_RequiresSessions(t *testing.T) {
	_, err := docker.New(docker.Config{
		LayersDir: t.TempDir(),
		UppersDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error when Sessions is nil, got nil")
	}
}

func TestNew_RequiresLayersDir(t *testing.T) {
	// LayersDir now defaults to <UserConfigDir>/astonish/sandbox/layers via
	// applyDefaults(). Verify that passing an empty LayersDir succeeds (defaults kick in).
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	b, err := docker.New(docker.Config{
		Sessions:  r,
		UppersDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected success with default LayersDir, got error: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestNew_RequiresUppersDir(t *testing.T) {
	// UppersDir now defaults to <UserConfigDir>/astonish/sandbox/uppers via
	// applyDefaults(). Verify that passing an empty UppersDir succeeds (defaults kick in).
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	b, err := docker.New(docker.Config{
		Sessions:  r,
		LayersDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected success with default UppersDir, got error: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestNew_CreatesDirectories(t *testing.T) {
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	layers := base + "/layers"
	uppers := base + "/uppers"

	_, err = docker.New(docker.Config{
		Sessions:  r,
		LayersDir: layers,
		UppersDir: uppers,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, dir := range []string{layers, uppers} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("directory %q was not created: %v", dir, err)
		}
	}
}

func TestNew_Success(t *testing.T) {
	r := newTestRegistry(t)
	cfg := newTestConfig(t, r)
	b, err := docker.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil DockerBackend")
	}
}

// ---------------------------------------------------------------------------
// Kind and Capabilities accessors
// ---------------------------------------------------------------------------

func TestKind(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != sandbox.BackendKindDocker {
		t.Errorf("Kind() = %q, want %q", b.Kind(), sandbox.BackendKindDocker)
	}
}

func TestCapabilities(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	caps := b.Capabilities()
	if caps.Kind != sandbox.BackendKindDocker {
		t.Errorf("Capabilities.Kind = %q, want %q", caps.Kind, sandbox.BackendKindDocker)
	}
	if !caps.SupportsFastClone {
		t.Error("expected SupportsFastClone = true")
	}
	if !caps.SupportsPortExpose {
		t.Error("expected SupportsPortExpose = true")
	}
	if !caps.SupportsOrgIsolation {
		t.Error("expected SupportsOrgIsolation = true")
	}
}

func TestServerArchitecture(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	arch := b.ServerArchitecture()
	if arch != "amd64" && arch != "arm64" {
		t.Errorf("ServerArchitecture() = %q, want amd64 or arm64", arch)
	}
}

// ---------------------------------------------------------------------------
// Factory registration (init())
// ---------------------------------------------------------------------------

func TestFactoryRegistration(t *testing.T) {
	// Importing pkg/sandbox/docker triggers init(), which calls
	// sandbox.RegisterBackendFactory(BackendKindDocker, ...).
	// Verify the factory is callable without a live Docker daemon.
	r := newTestRegistry(t)
	b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{
		Kind:     sandbox.BackendKindDocker,
		Sessions: r,
		Docker: sandbox.DockerRuntimeConfig{
			LayersDir: t.TempDir(),
			UppersDir: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("NewBackend(docker): %v", err)
	}
	if b.Kind() != sandbox.BackendKindDocker {
		t.Errorf("factory produced backend with Kind()=%q, want %q", b.Kind(), sandbox.BackendKindDocker)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation (contract requirement)
// ---------------------------------------------------------------------------

func TestDockerBackendContract(t *testing.T) {
	sandbox.RunBackendContract(t, func(t *testing.T) (sandbox.Backend, string) {
		r := newTestRegistry(t)
		b, err := docker.New(newTestConfig(t, r))
		if err != nil {
			t.Fatal(err)
		}
		return b, ""
	})
}

func TestCreateSession_RespectsContextCancel(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err = b.CreateSession(ctx, sandbox.SessionSpec{
		SessionID: "test-ctx",
		Type:      sandbox.SessionTypeChat,
	})
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// applyDefaults — ASTONISH_SANDBOX_IMAGE env var override
// ---------------------------------------------------------------------------

func TestApplyDefaults_SandboxImageEnvVar(t *testing.T) {
	const customImage = "my.registry.io/custom-sandbox:v2"
	t.Setenv("ASTONISH_SANDBOX_IMAGE", customImage)

	r := newTestRegistry(t)
	// Pass empty SandboxImage so applyDefaults must fill it from the env var.
	b, err := docker.New(docker.Config{
		Sessions:  r,
		LayersDir: t.TempDir(),
		UppersDir: t.TempDir(),
		// SandboxImage intentionally omitted.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
	// We cannot inspect cfg.SandboxImage directly (unexported), but the backend
	// must have been constructed without error when the env var is set.
	// The live-docker suite validates actual container creation with the image.
}

// ---------------------------------------------------------------------------
// Live Docker tests (require ASTONISH_TEST_DOCKER=1)
// ---------------------------------------------------------------------------

func TestHealth_LiveDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") == "" {
		t.Skip("set ASTONISH_TEST_DOCKER=1 to run live Docker tests")
	}
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	health, err := b.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Healthy {
		t.Errorf("Health.Healthy = false, reason: %s", health.Reason)
	}
}

type persistenceNamedTool string

func (n persistenceNamedTool) Name() string        { return string(n) }
func (n persistenceNamedTool) Description() string { return "Live sandbox acceptance" }
func (n persistenceNamedTool) IsLongRunning() bool { return false }

// persistenceToolContext supplies only the fields consumed by NodeTool/MCP.
// Unused ADK operations remain on the embedded interface and must not be called.
type persistenceToolContext struct {
	tool.Context
	ctx       context.Context
	sessionID string
}

func (c persistenceToolContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c persistenceToolContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c persistenceToolContext) Err() error                  { return c.ctx.Err() }
func (c persistenceToolContext) Value(key any) any           { return c.ctx.Value(key) }
func (c persistenceToolContext) SessionID() string           { return c.sessionID }
func (c persistenceToolContext) AgentName() string           { return "persistence-fixture" }

func (c persistenceToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c persistenceToolContext) RequestConfirmation(string, any) error                { return nil }

// TestDockerPersistenceAcceptance deliberately fails, rather than skips or falls
// back to host tools, when live verification is explicitly requested.
func TestDockerPersistenceAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("set ASTONISH_TEST_DOCKER=1 for isolated real-Docker acceptance")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE must name the locally built acceptance image")
	}
	scenario := os.Getenv("ASTONISH_PERSISTENCE_SCENARIO")
	if scenario == "" {
		scenario = "all"
	}
	switch scenario {
	case "baseline", "owner", "persistence", "consumers", "all":
	default:
		t.Fatalf("unknown persistence scenario %q", scenario)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	dockerCommand := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Logf("Docker server=%s image=%s", dockerCommand("version", "--format", "{{.Server.Version}}"), dockerCommand("image", "inspect", "--format", "{{.Id}}", image))
	// Docker Desktop/Colima share the checkout, but not necessarily macOS's
	// default /var/folders temporary directory. Keep the isolated test root in
	// the checkout; only immutable layers and persistence archives are bound.
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", workingDir)
	root := t.TempDir()
	registryDir := filepath.Join(root, "registry")
	openBackend := func() (sandbox.Backend, *sandbox.SessionRegistry) {
		t.Helper()
		st, err := sandbox.NewLocalSessionStore(registryDir)
		if err != nil {
			t.Fatal(err)
		}
		reg := sandbox.NewSessionRegistryFromStore(st)
		b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{
			Kind: sandbox.BackendKindDocker, Sessions: reg,
			Docker: sandbox.DockerRuntimeConfig{
				LayersDir: filepath.Join(root, "layers"), UppersDir: filepath.Join(root, "uppers"), SandboxImage: image,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b, reg
	}
	b, reg := openBackend()
	seed, ok := b.(interface{ ReseedBaseLayerFromImage(context.Context) error })
	if !ok {
		t.Fatal("Docker backend missing local-image seed capability")
	}
	if err := seed.ReseedBaseLayerFromImage(ctx); err != nil {
		t.Fatal(err)
	}
	// A lower-layer fixture proves whiteouts survive, not just upper file data.
	lowerFile := filepath.Join(root, "layers", sandbox.BaseTemplateID, "rootfs", "root", "persistence-lower")
	if err := os.WriteFile(lowerFile, []byte("lower\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("persistence-%d", time.Now().UnixNano())
	parent, child, other := prefix+"-parent", prefix+"-child", prefix+"-other"
	ids := []string{parent, child, other}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		for _, id := range ids {
			// IDs belong exclusively to this run. Clean up even when creation
			// failed before registration, so partial resources cannot leak.
			if err := b.DestroySession(cleanupCtx, id); err != nil && !strings.Contains(err.Error(), "is not registered") {
				t.Errorf("cleanup %s: %v", id, err)
			}
		}
	})
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	client := pool.GetOrCreate(parent)
	if err := client.EnsureReady(parent); err != nil {
		t.Fatal(err)
	}
	entry := reg.Get(parent)
	if entry == nil {
		t.Fatal("sandbox registration missing")
	}
	container := entry.ContainerName
	t.Logf("container=%s mount=%s", dockerCommand("inspect", "--format", "{{.Id}}", container), dockerCommand("inspect", "--format", "{{json .Mounts}}", container))
	if mount := dockerCommand("exec", container, "findmnt", "-n", "-o", "FSTYPE", "/sandbox/rootfs"); !strings.Contains(mount, "overlay") {
		t.Fatalf("not an overlay filesystem: %s", mount)
	}
	call := func(c sandbox.ToolNodeClient, sid, name string, args map[string]interface{}) map[string]interface{} {
		t.Helper()
		raw, err := c.Call(sid, name, args)
		if err != nil {
			t.Fatalf("%s/%s: %v", sid, name, err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("%s result: %s: %v", name, raw, err)
		}
		if result["error"] != nil || result["success"] == false {
			t.Fatalf("%s failed: %s", name, raw)
		}
		return result
	}
	file := "/root/persistence-check.txt"
	write := func(value string) {
		t.Helper()
		nt := sandbox.NewNodeToolWithPool(persistenceNamedTool("write_file"), pool)
		result, err := nt.Run(persistenceToolContext{ctx: ctx, sessionID: parent}, map[string]any{"path": file, "content": value})
		if err != nil || result["error"] != nil || result["success"] == false {
			t.Fatalf("NodeTool write: %v %v", result, err)
		}
	}
	read := func(c sandbox.ToolNodeClient, sid, expected string) {
		t.Helper()
		result := call(c, sid, "read_file", map[string]interface{}{"path": file, "force": true})
		if !strings.Contains(fmt.Sprint(result["content"]), strings.TrimSuffix(expected, "\n")) {
			t.Fatalf("read_file did not return latest bytes %q: %v", expected, result)
		}
		// Shell tools use a PTY, which transforms newlines. Compare encoded
		// bytes so terminal formatting cannot hide filesystem corruption.
		r := call(c, sid, "shell_command", map[string]interface{}{"command": "base64 -w0 /root/persistence-check.txt", "working_dir": "/root", "timeout": 30})
		if r["stdout"] != base64.StdEncoding.EncodeToString([]byte(expected)) || r["exit_code"] != float64(0) {
			t.Fatalf("shell/file disagreement: want %q, got %v", expected, r)
		}
		t.Logf("session=%s verified sentinel bytes=%q", sid, expected)
	}
	write("old\n")
	call(client, parent, "edit_file", map[string]interface{}{"path": file, "old_string": "old", "new_string": "baseline"})
	read(client, parent, "baseline\n")
	independent := pool.GetOrCreate(other)
	r := call(independent, other, "shell_command", map[string]interface{}{"command": "test ! -e /root/persistence-check.txt", "working_dir": "/root", "timeout": 30})
	if r["exit_code"] != float64(0) {
		t.Fatalf("independent session can see parent's file: %v", r)
	}
	if scenario == "baseline" {
		return
	}
	if scenario == "owner" || scenario == "all" {
		pool.Alias(child, parent)
		aliased := pool.GetOrCreate(child)
		if err := aliased.EnsureReady(child); err != nil {
			t.Fatal(err)
		}
		read(aliased, child, "baseline\n")
		if reg.Get(child) != nil {
			t.Fatal("delegate created a separate physical sandbox")
		}
	}
	if scenario == "owner" {
		return
	}
	if scenario == "consumers" || scenario == "all" {
		pool.Alias(child, parent)
		fixture := `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  [ -n "$id" ] || continue
  case "$line" in
    *'"initialize"'*) result='{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"persistence","version":"1"}}' ;;
    *'"tools/list"'*) result='{"tools":[{"name":"sentinel","description":"Read persisted sentinel","inputSchema":{"type":"object","properties":{}}}]}' ;;
    *'"tools/call"'*) result="{\"content\":[{\"type\":\"text\",\"text\":\"$(base64 -w0 /root/persistence-check.txt)\"}]}" ;;
    *) result='{}' ;;
  esac
  printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$result"
done
`
		call(client, parent, "write_file", map[string]interface{}{"path": "/root/persistence-mcp.sh", "content": fixture})
		mcpTools := agent.NewLazyMCPToolset("persistence", []cache.ToolEntry{{Name: "sentinel"}}, config.MCPServerConfig{Command: "/bin/sh", Args: []string{"/root/persistence-mcp.sh"}}, false)
		mcpTools.SetSandboxPool(pool)
		defer mcpTools.Cleanup()
		proxies, err := mcpTools.Tools(nil)
		if err != nil || len(proxies) != 1 {
			t.Fatalf("MCP proxies: %v %v", proxies, err)
		}
		invoke := func(sid string) {
			t.Helper()
			result, err := proxies[0].(interface {
				Run(tool.Context, any) (map[string]any, error)
			}).Run(persistenceToolContext{Context: nil, ctx: ctx, sessionID: sid}, map[string]any{})
			encoded, _ := json.Marshal(result)
			if err != nil || !strings.Contains(string(encoded), base64.StdEncoding.EncodeToString([]byte("baseline\n"))) {
				t.Fatalf("MCP sentinel %s: %s %v", sid, encoded, err)
			}
		}
		invoke(parent)
		invoke(child)
		dockerCommand("restart", container)
		invoke(child)
		invoke(parent)
		mcpTools.CleanupSession(child)
		invoke(parent)
		if scenario == "consumers" {
			return
		}
	}
	if err := b.StopSession(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureReady(parent); err != nil {
		t.Fatal(err)
	}
	write("newest\n")
	r = call(client, parent, "shell_command", map[string]interface{}{"command": "chmod 0751 /root/persistence-check.txt && ln -s persistence-check.txt /root/persistence-link && rm /root/persistence-lower", "working_dir": "/root", "timeout": 30})
	if r["exit_code"] != float64(0) {
		t.Fatalf("fixture setup: %v", r)
	}
	assertLatest := func() {
		t.Helper()
		read(client, parent, "newest\n")
		r := call(client, parent, "shell_command", map[string]interface{}{"command": "test \"$(stat -c %a /root/persistence-check.txt)\" = 751 && test \"$(readlink /root/persistence-link)\" = persistence-check.txt && test ! -e /root/persistence-lower", "working_dir": "/root", "timeout": 30})
		if r["exit_code"] != float64(0) {
			t.Fatalf("metadata/deletion state changed: %v", r)
		}
	}
	// A restart must not extract the older archive over the newer live upper.
	dockerCommand("restart", container)
	if err := client.EnsureReady(parent); err != nil {
		t.Fatal(err)
	}
	assertLatest()
	// Removal is explicitly container-only: the named volume must survive.
	dockerCommand("rm", "-f", container)
	// An unrelated template build must not prune this now-dangling volume.
	if _, err := b.BuildTemplate(ctx, sandbox.TemplateBuildSpec{
		TemplateID: prefix + "-template", ParentLayers: []string{sandbox.BaseTemplateID},
	}); err != nil {
		t.Fatal(err)
	}
	dockerCommand("volume", "inspect", container+"-overlay")
	b, reg = openBackend()
	pool = sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	client = pool.GetOrCreate(parent)
	if err := client.EnsureReady(parent); err != nil {
		t.Fatal(err)
	}
	assertLatest()
	// Concurrent callers must all observe the same recovery, without a race
	// resetting a newer bind or closing the wrong completion channel.
	dockerCommand("stop", container)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- client.EnsureReady(parent) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertLatest()
	// Archive-only initialization is the fallback when live storage is absent.
	if err := b.StopSession(ctx, parent); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "uppers", parent, "upper.tar.zst")
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	dockerCommand("rm", "-f", container)
	dockerCommand("volume", "rm", container+"-overlay")
	b, reg = openBackend()
	pool = sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	client = pool.GetOrCreate(parent)
	if err := client.EnsureReady(parent); err != nil {
		t.Fatal(err)
	}
	assertLatest()
	// A corrupt archive must fail readiness, never expose a partially restored
	// or silently empty filesystem. Retain the good archive for a later retry.
	dockerCommand("rm", "-f", container)
	dockerCommand("volume", "rm", container+"-overlay")
	if err := os.WriteFile(archivePath, []byte("not a zstd archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, reg = openBackend()
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: parent, Type: sandbox.SessionTypeChat}); err == nil {
		waitCtx, cancelWait := context.WithTimeout(ctx, 10*time.Second)
		err = b.WaitForSessionReady(waitCtx, parent)
		cancelWait()
		if err == nil {
			t.Fatal("corrupt restore was reported ready")
		}
	}
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	dockerCommand("rm", "-f", container)
	pool = sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	client = pool.GetOrCreate(parent)
	if err := client.EnsureReady(parent); err != nil {
		t.Fatal(err)
	}
	assertLatest()
	// Seed an interrupted extraction in the real volume. On startup the
	// canonical entrypoint must discard only staging, then restore the archive.
	dockerCommand("rm", "-f", container)
	dockerCommand("volume", "rm", container+"-overlay")
	dockerCommand("container", "run", "--rm", "--entrypoint=/bin/sh",
		"--mount=type=volume,source="+container+"-overlay,target=/overlay", image,
		"-c", `mkdir -p /overlay/upper /overlay/.astonish-restore-staging/root; printf partial > /overlay/.astonish-restore-staging/root/partial; printf 'version=1\nsession=%s\nchain=%s\n' "$1" "$2" > /overlay/.astonish-restore-in-progress`, "sh", parent, sandbox.BaseTemplateID)
	b, reg = openBackend()
	pool = sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	client = pool.GetOrCreate(parent)
	if err := client.EnsureReady(parent); err != nil {
		t.Fatal(err)
	}
	assertLatest()
	dockerCommand("exec", container, "test", "!", "-e", "/var/astonish/overlay/.astonish-restore-staging")
	if err := b.DestroySession(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if reg.Get(parent) != nil {
		t.Fatal("explicit destruction retained registry metadata")
	}
	if _, err := os.Stat(filepath.Join(root, "uppers", parent)); !os.IsNotExist(err) {
		t.Fatalf("explicit destruction retained persistence: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "volume", "inspect", container+"-overlay").CombinedOutput(); err == nil {
		t.Fatalf("explicit destruction retained volume: %s", out)
	}
}
