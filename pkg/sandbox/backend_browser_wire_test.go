package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/store"
)

type browserOwnerReadyClient struct {
	browserReadySpyClient
	owner string
	ctx   context.Context
}

func (c *browserOwnerReadyClient) SandboxSessionID() string { return c.owner }
func (c *browserOwnerReadyClient) EnsureReadyContext(ctx context.Context, id string) error {
	c.ctx = ctx
	c.sessionID = id
	return ctx.Err()
}

type browserOwnerExecBackend struct {
	interfaceTestStub
	owners []string
}

func (*browserOwnerExecBackend) Kind() BackendKind { return BackendKindDocker }
func (b *browserOwnerExecBackend) ExecStreaming(_ context.Context, id string, _ ExecStreamSpec) (ExecStream, error) {
	b.owners = append(b.owners, id)
	return &failedBrowserStream{stdout: strings.NewReader("DONE: browser ready\n")}, nil
}
func (b *browserOwnerExecBackend) Exec(_ context.Context, id string, _ ExecSpec) (*ExecResult, error) {
	b.owners = append(b.owners, id)
	return &ExecResult{Stdout: []byte("1920x1080"), ExitCode: 0}, nil
}

func TestWireBackendBrowserManager_AliasedRegistryAndExecHandle(t *testing.T) {
	reg := newTestRegistry(t)
	if err := reg.PutSession(&store.SandboxSession{SessionID: "owner", ContainerName: "physical", Backend: string(BackendKindDocker), State: store.SandboxSessionStateRunning}); err != nil {
		t.Fatal(err)
	}
	backend := &browserOwnerExecBackend{}
	pool := NewBackendPool(backend, ResourceLimits{})
	pool.GetOrCreate("owner")
	pool.Alias("child", "owner")
	pool.Alias("grandchild", "child")
	mgr := browser.NewManager(browser.DefaultConfig())
	var touched string
	WireBackendBrowserManager(mgr, backend, reg, pool, func(id string) { touched = id })
	handle, _, err := mgr.ContainerResolveFunc("grandchild")
	if err != nil || handle != "owner" {
		t.Fatalf("resolve = %q, %v", handle, err)
	}
	if rec, _ := reg.GetSession("grandchild"); rec != nil {
		t.Fatal("child registry record should not be fabricated")
	}
	launch, err := mgr.ContainerStartBrowserFunc(handle)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Close()
	conn, err := mgr.ContainerDialFunc(handle, 9222)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stop, _, _, err := mgr.ContainerStartRecordingFunc(handle, ":0", "/tmp/test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	mgr.ActivityTouchFunc("grandchild")
	if touched != "owner" {
		t.Fatalf("activity = %q", touched)
	}
	for _, id := range backend.owners {
		if id != "owner" {
			t.Fatalf("physical operation used %q", id)
		}
	}
	if len(backend.owners) < 3 {
		t.Fatal("expected launch, dial and recording operations")
	}
}

func TestWireBackendBrowserManager_PinnedSingleClientOwnership(t *testing.T) {
	reg := newTestRegistry(t)
	if err := reg.PutSession(&store.SandboxSession{SessionID: "fleet-owner", ContainerName: "fleet-container", Backend: string(BackendKindDocker)}); err != nil {
		t.Fatal(err)
	}
	client := &browserOwnerReadyClient{owner: "fleet-owner"}
	backend := &browserOwnerExecBackend{}
	pool := NewSingleClientPool(client, backend)
	mgr := browser.NewManager(browser.DefaultConfig())
	WireBackendBrowserManager(mgr, backend, reg, pool, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.ContainerEnsureReadyFunc(ctx, "logical-agent"); err != nil {
		t.Fatal(err)
	}
	if client.ctx != ctx || client.sessionID != "fleet-owner" {
		t.Fatalf("readiness lost context or owner: %q", client.sessionID)
	}
	handle, _, err := mgr.ContainerResolveFunc("logical-agent")
	if err != nil || handle != "fleet-owner" {
		t.Fatalf("resolve = %q, %v", handle, err)
	}
	cancel()
	if err := mgr.ContainerEnsureReadyFunc(ctx, "logical-agent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("readiness = %v, want cancellation", err)
	}
}

func TestBuildBackendBrowserLaunchScript_UsesSandboxBrowser(t *testing.T) {
	script := buildBackendBrowserLaunchScript(browser.BrowserConfig{
		ViewportWidth:       1366,
		ViewportHeight:      768,
		FingerprintSeed:     "seed-1",
		FingerprintPlatform: "linux",
		Proxy:               "http://proxy.local:8080",
	}, 1366, 768)

	wants := []string{
		"export LD_PRELOAD=/usr/lib/hwcap_mask.so",
		"HOME=/home/browser python3 -c 'from cloakbrowser.config import get_binary_path",
		"/home/browser/.cloakbrowser",
		"Xkasmvnc :0",
		"-websocketPort 6901",
		"resolution:\n    width: 1366\n    height: 768",
		"Xvfb :99",
		"--remote-debugging-port=9223",
		"socat TCP-LISTEN:9222",
		"--window-size=1366,768",
		"--fingerprint seed-1",
		"--fingerprint-platform linux",
		"--proxy-server=http://proxy.local:8080",
		"DONE: browser ready",
		"/home/browser/.cloakbrowser/*/chrome",
		"falling back to Xvfb",
	}
	for _, want := range wants {
		if !strings.Contains(script, want) {
			t.Fatalf("launch script missing %q\nscript:\n%s", want, script)
		}
	}
	if strings.Contains(script, "| head") {
		t.Fatal("launch script must not pipe to head (APFS HEAD/head collision)")
	}
}

func TestStartBackendBrowserReportsStderrAndExitCode(t *testing.T) {
	stream := &failedBrowserStream{stdout: strings.NewReader("partial stdout"), exitCode: 127}
	backend := &browserExecBackend{stream: stream, stderr: "No browser binary found"}

	_, err := startBackendBrowser(context.Background(), backend, "slides-pdf-user", browser.DefaultConfig())
	if err == nil {
		t.Fatal("expected launch error")
	}
	for _, want := range []string{"exit code 127", "partial stdout", "No browser binary found"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestWireBackendBrowserManager_K8sAndDocker(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := &SessionRegistry{}

	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindK8s}, reg, nil, nil) {
		t.Fatal("WireBackendBrowserManager returned false for K8s backend")
	}
	if !mgr.SandboxEnabled {
		t.Fatal("manager SandboxEnabled not set")
	}
	if mgr.ContainerResolveFunc == nil || mgr.ContainerStartBrowserFunc == nil || mgr.ContainerDialFunc == nil {
		t.Fatal("manager browser callbacks were not wired")
	}

	mgr = browser.NewManager(browser.DefaultConfig())
	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindDocker}, reg, nil, nil) {
		t.Fatal("WireBackendBrowserManager returned false for Docker backend")
	}

	mgr = browser.NewManager(browser.DefaultConfig())
	if WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindIncus}, reg, nil, nil) {
		t.Fatal("WireBackendBrowserManager should not wire Incus backend")
	}
}

func TestWireBackendBrowserManager_ResolveAcceptsDockerContainerName(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := newTestRegistry(t)
	sessionID := "c1b27dfb-f828-40f2-b510-c8e85df3cda7"
	if err := reg.PutSession(&store.SandboxSession{
		SessionID:     sessionID,
		Backend:       string(BackendKindDocker),
		ContainerName: "astonish-session-c1b27dfb-f828-40-e1deb88678e9",
		State:         store.SandboxSessionStateRunning,
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindDocker}, reg, nil, nil) {
		t.Fatal("WireBackendBrowserManager returned false")
	}
	name, ip, err := mgr.ContainerResolveFunc(sessionID)
	if err != nil {
		t.Fatalf("ContainerResolveFunc: %v", err)
	}
	if name != sessionID {
		t.Errorf("name = %q, want session ID", name)
	}
	if ip != "127.0.0.1" {
		t.Errorf("ip = %q, want 127.0.0.1", ip)
	}
}

func TestWireBackendBrowserManager_ResolveAcceptsK8sPodName(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := newTestRegistry(t)
	sessionID := "k8s-session"
	if err := reg.PutSession(&store.SandboxSession{
		SessionID: sessionID,
		Backend:   string(BackendKindK8s),
		PodName:   "astn-sess-k8s-session",
		State:     store.SandboxSessionStateRunning,
	}); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindK8s}, reg, nil, nil) {
		t.Fatal("WireBackendBrowserManager returned false")
	}
	if _, _, err := mgr.ContainerResolveFunc(sessionID); err != nil {
		t.Fatalf("ContainerResolveFunc: %v", err)
	}
}

func TestWireBackendBrowserManager_ResolveRejectsUnboundSession(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := newTestRegistry(t)
	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindDocker}, reg, nil, nil) {
		t.Fatal("WireBackendBrowserManager returned false")
	}
	_, _, err := mgr.ContainerResolveFunc("missing-session")
	if err == nil {
		t.Fatal("expected resolve error for missing session")
	}
	if !strings.Contains(err.Error(), `no running sandbox for session "missing-session"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWireBackendBrowserManager_EnsureReadyUsesSessionClient(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := &SessionRegistry{}
	client := &browserReadySpyClient{}
	pool := &browserReadySpyPool{client: client}

	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindK8s}, reg, pool, nil) {
		t.Fatal("WireBackendBrowserManager returned false for K8s backend")
	}
	if mgr.ContainerEnsureReadyFunc == nil {
		t.Fatal("manager ContainerEnsureReadyFunc not set")
	}
	if err := mgr.ContainerEnsureReadyFunc(context.Background(), "session-123"); err != nil {
		t.Fatalf("ContainerEnsureReadyFunc() error = %v", err)
	}
	if pool.sessionID != "session-123" {
		t.Errorf("GetOrCreate session = %q, want session-123", pool.sessionID)
	}
	if client.sessionID != "session-123" {
		t.Errorf("EnsureReady session = %q, want session-123", client.sessionID)
	}
}

func TestWireBackendBrowserManager_EnsureReadyUsesContextChain(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := &SessionRegistry{}
	client := &browserReadySpyClient{}
	pool := &browserReadySpyPool{client: client}

	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindK8s}, reg, pool, nil) {
		t.Fatal("WireBackendBrowserManager returned false")
	}

	ctx := store.WithSandboxLayerChain(context.Background(), []string{"@base", "abc123"})
	ctx = store.WithSandboxTemplate(ctx, "mytemplate")

	if err := mgr.ContainerEnsureReadyFunc(ctx, "session-456"); err != nil {
		t.Fatalf("ContainerEnsureReadyFunc() error = %v", err)
	}
	if pool.method != "GetOrCreateWithImage" {
		t.Errorf("pool method = %q, want GetOrCreateWithImage", pool.method)
	}
	if pool.sessionID != "session-456" {
		t.Errorf("pool session = %q, want session-456", pool.sessionID)
	}
}

func TestGetPoolClientFromContext_FallsBackToGetOrCreate(t *testing.T) {
	client := &browserReadySpyClient{}
	pool := &browserReadySpyPool{client: client}
	// Empty context — no chain, no template
	// Should use the pool's default template
	c := GetPoolClientFromContext(context.Background(), pool, "sess-1")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if pool.method != "GetOrCreate" {
		t.Errorf("pool method = %q, want GetOrCreate", pool.method)
	}
}

type browserExecBackend struct {
	interfaceTestStub
	stream ExecStream
	stderr string
}

func (b *browserExecBackend) ExecStreaming(_ context.Context, _ string, spec ExecStreamSpec) (ExecStream, error) {
	if spec.SeparateStderr == nil {
		return nil, errors.New("SeparateStderr was not configured")
	}
	_, _ = io.WriteString(spec.SeparateStderr, b.stderr)
	return b.stream, nil
}

type failedBrowserStream struct {
	stdout   io.Reader
	exitCode int
}

func (s *failedBrowserStream) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *failedBrowserStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *failedBrowserStream) Resize(_, _ int) error       { return nil }
func (s *failedBrowserStream) Wait() (int, error)          { return s.exitCode, nil }
func (s *failedBrowserStream) Close() error                { return nil }

type kindOnlyBackend struct {
	interfaceTestStub
	kind BackendKind
}

func (b kindOnlyBackend) Kind() BackendKind { return b.kind }

type browserReadySpyClient struct {
	sessionID string
}

func (*browserReadySpyClient) BindSession(string) {}
func (c *browserReadySpyClient) EnsureReady(sessionID string) error {
	c.sessionID = sessionID
	return nil
}
func (*browserReadySpyClient) Call(string, string, map[string]interface{}) (json.RawMessage, error) {
	return nil, nil
}

type browserReadySpyPool struct {
	client    ToolNodeClient
	sessionID string
	method    string
	template  string
}

func (p *browserReadySpyPool) GetOrCreate(sessionID string) ToolNodeClient {
	p.sessionID = sessionID
	p.method = "GetOrCreate"
	return p.client
}
func (p *browserReadySpyPool) GetOrCreateWithTemplate(sessionID, template string) ToolNodeClient {
	p.sessionID = sessionID
	p.template = template
	p.method = "GetOrCreateWithTemplate"
	return p.client
}
func (p *browserReadySpyPool) GetOrCreateWithChain(sessionID string, _ string, _ []string) ToolNodeClient {
	p.sessionID = sessionID
	p.method = "GetOrCreateWithChain"
	return p.client
}
func (p *browserReadySpyPool) GetOrCreateWithImage(sessionID string, _ string, _ []string, _ string) ToolNodeClient {
	p.sessionID = sessionID
	p.method = "GetOrCreateWithImage"
	return p.client
}
func (*browserReadySpyPool) GetBackend() Backend                    { return nil }
func (*browserReadySpyPool) Cleanup()                               {}
func (*browserReadySpyPool) Alias(string, string)                   {}
func (*browserReadySpyPool) Remove(string)                          {}
func (*browserReadySpyPool) SetSessionScope(string, string, string) {}

func TestGetPoolClientFromContext_TemplateOnlyUsesGetOrCreateWithTemplate(t *testing.T) {
	client := &browserReadySpyClient{}
	pool := &browserReadySpyPool{client: client}
	// Template set but NO chain and NO image — should use GetOrCreateWithTemplate
	ctx := store.WithSandboxTemplate(context.Background(), "my-template")
	c := GetPoolClientFromContext(ctx, pool, "sess-tpl")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if pool.method != "GetOrCreateWithTemplate" {
		t.Errorf("pool method = %q, want GetOrCreateWithTemplate", pool.method)
	}
	if pool.sessionID != "sess-tpl" {
		t.Errorf("pool sessionID = %q, want sess-tpl", pool.sessionID)
	}
}

// recordingExecBackend is a test Backend stub that records Exec calls and
// returns configurable results for the probe, start, and stop phases of
// startBackendRecording.
type recordingExecBackend struct {
	interfaceTestStub
	kind BackendKind
	// calls receives each script that was executed via Exec.
	calls []string
	// execResults maps call index → ExecResult. If no entry, returns a default
	// success result. If the result has ExitCode != 0 or err is set, that is
	// returned. Use execErrors[i] to force an error return (not ExitCode).
	execResults map[int]*ExecResult
	execErrors  map[int]error
	callIdx     int
}

func (b *recordingExecBackend) Kind() BackendKind { return b.kind }

func (b *recordingExecBackend) Exec(_ context.Context, _ string, opts ExecSpec) (*ExecResult, error) {
	idx := b.callIdx
	b.callIdx++
	if len(opts.Command) > 0 {
		b.calls = append(b.calls, strings.Join(opts.Command, " "))
	}
	if err, ok := b.execErrors[idx]; ok {
		return nil, err
	}
	if r, ok := b.execResults[idx]; ok {
		return r, nil
	}
	return &ExecResult{ExitCode: 0, Stdout: []byte("1920x1080")}, nil
}

func TestStartBackendRecording_ProbeFailure(t *testing.T) {
	b := &recordingExecBackend{
		kind: BackendKindDocker,
		execErrors: map[int]error{
			0: fmt.Errorf("xdpyinfo not found"),
		},
	}
	_, _, _, err := startBackendRecording(context.Background(), b, "sess-1", ":0", "/tmp/astonish-recordings/test.mp4")
	if err == nil {
		t.Fatal("expected error when display probe fails")
	}
	if !strings.Contains(err.Error(), "probe display size") {
		t.Errorf("error %q should mention 'probe display size'", err)
	}
}

func TestStartBackendRecording_ProbeBadDimensions(t *testing.T) {
	b := &recordingExecBackend{
		kind: BackendKindDocker,
		execResults: map[int]*ExecResult{
			0: {ExitCode: 0, Stdout: []byte("not-dimensions")},
		},
	}
	_, _, _, err := startBackendRecording(context.Background(), b, "sess-1", ":0", "/tmp/astonish-recordings/test.mp4")
	if err == nil {
		t.Fatal("expected error when dimensions cannot be parsed")
	}
	if !strings.Contains(err.Error(), "probe display size") {
		t.Errorf("error %q should mention 'probe display size'", err)
	}
}

func TestWireBackendBrowserManager_SetsRecordingFunc(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	reg := &SessionRegistry{}
	if !WireBackendBrowserManager(mgr, &kindOnlyBackend{kind: BackendKindK8s}, reg, nil, nil) {
		t.Fatal("WireBackendBrowserManager returned false")
	}
	if mgr.ContainerStartRecordingFunc == nil {
		t.Fatal("ContainerStartRecordingFunc was not wired by WireBackendBrowserManager")
	}
}

func TestBackendShellCommand_Docker(t *testing.T) {
	backend := &kindOnlyBackend{kind: BackendKindDocker}
	script := "echo hello"
	cmd := backendShellCommand(backend, script)
	if len(cmd) != 3 {
		t.Errorf("Docker backend command length = %d, want 3", len(cmd))
	}
	if cmd[0] != "sh" || cmd[1] != "-c" || cmd[2] != script {
		t.Errorf("Docker backend command = %v, want [sh -c %q]", cmd, script)
	}
}

func TestBackendShellCommand_K8s(t *testing.T) {
	backend := &kindOnlyBackend{kind: BackendKindK8s}
	script := "echo hello"
	cmd := backendShellCommand(backend, script)
	if len(cmd) != 4 {
		t.Errorf("K8s backend command length = %d, want 4", len(cmd))
	}
	if cmd[0] != "/usr/local/bin/astonish-shell" || cmd[1] != "sh" || cmd[2] != "-c" || cmd[3] != script {
		t.Errorf("K8s backend command = %v, want [/usr/local/bin/astonish-shell sh -c %q]", cmd, script)
	}
}

func TestBackendShellCommand_NilBackend(t *testing.T) {
	script := "echo hello"
	cmd := backendShellCommand(nil, script)
	if len(cmd) != 3 {
		t.Errorf("nil backend command length = %d, want 3", len(cmd))
	}
	if cmd[0] != "sh" || cmd[1] != "-c" || cmd[2] != script {
		t.Errorf("nil backend command = %v, want [sh -c %q]", cmd, script)
	}
}

func TestStartBackendRecording_K8sUsesAstonishShell(t *testing.T) {
	// recordingExecBackend captures all Exec calls; by default it returns
	// success with a valid probe response "1920x1080".
	b := &recordingExecBackend{
		kind: BackendKindK8s,
	}
	_, _, _, err := startBackendRecording(context.Background(), b, "sess-1", ":0", "/tmp/astonish-recordings/test.mp4")
	if err != nil {
		t.Fatalf("startBackendRecording failed: %v", err)
	}

	// We expect 4 Exec calls (probe, mkdir, start ffmpeg, and the stopFn closure will call stop ffmpeg).
	// We only verify the first 3 here since stopFn is lazy.
	if len(b.calls) < 3 {
		t.Fatalf("expected at least 3 Exec calls, got %d", len(b.calls))
	}

	// Each call should start with /usr/local/bin/astonish-shell for K8s backends.
	for i, call := range b.calls[:3] {
		if !strings.Contains(call, "/usr/local/bin/astonish-shell") {
			t.Errorf("call %d = %q; expected to contain /usr/local/bin/astonish-shell", i, call)
		}
	}
}
