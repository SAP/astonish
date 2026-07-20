// Package backendpool_test — unit tests for pkg/sandbox/backend_pool.go
// (Phase E, slice 2).
//
// These tests live in a dedicated subpackage (pkg/sandbox/internal/...)
// rather than alongside backend_pool.go because they need to import
// pkg/sandbox/mock. Doing so from an external test file colocated with
// pkg/sandbox would trigger mock's init() inside pkg/sandbox's test
// binary, violating the pkg/sandbox.TestNewBackend_MockRequiresImport
// contract (which pins that production pkg/sandbox does NOT depend on
// mock — the factory entry is purely opt-in via importing the mock
// package).
//
// The `internal/` path component additionally enforces at build time
// that no external caller can import this test package. It exists
// solely to carry tests.
//
// The tests pin three behaviors that are load-bearing for Phase E:
//
//  1. backendPool.GetOrCreate returns a ToolNodeClient whose first
//     Call provisions the session via Backend.CreateSession+StartSession
//     and dispatches the NDJSON request-response pair via Backend.Exec.
//
//  2. NDJSON framing tolerates the {"ready":true} line the astonish-node
//     server emits before the real response, regardless of ordering.
//
//  3. Pool.Cleanup destroys every vended session and returns nil from
//     subsequent GetOrCreate calls — matching *NodeClientPool semantics
//     so flow code can swap between the two without behavior drift.
//
// MockBackend's ExecResultFn hook stands in for the sandbox-side
// astonish-node process: it inspects the stdin payload (the NDJSON tool
// request), produces a canned response, and returns it as ExecResult.Stdout.

package backendpool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
)

// execHandler is a convenience wrapper that replays the astonish-node
// protocol inside MockBackend.ExecResultFn. readBody pulls all of stdin
// (the single NDJSON request line) and returns the decoded payload so
// tests can assert on it; respond writes the ready+response pair as
// stdout. Exit code 0 unless overridden.
type execHandler struct {
	t           *testing.T
	respondWith func(req map[string]any) (result json.RawMessage, errStr string)
	readyFirst  bool // if false, ready line is emitted AFTER the response
	exitCode    int
	calls       atomic.Int32
}

func (h *execHandler) run(sessionID string, spec sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	h.calls.Add(1)
	if spec.Stdin == nil {
		h.t.Fatalf("exec spec missing stdin — backend node client must pipe request JSON on stdin")
	}
	reqBytes, err := io.ReadAll(spec.Stdin)
	if err != nil {
		h.t.Fatalf("read stdin: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(reqBytes), &req); err != nil {
		h.t.Fatalf("stdin was not valid JSON: %v (raw=%q)", err, reqBytes)
	}

	result, errStr := h.respondWith(req)

	ready := []byte(`{"ready":true}` + "\n")
	resp := map[string]any{"id": req["id"]}
	if errStr != "" {
		resp["error"] = errStr
	} else {
		resp["result"] = result
	}
	respLine, _ := json.Marshal(resp)
	respLine = append(respLine, '\n')

	var stdout bytes.Buffer
	if h.readyFirst {
		stdout.Write(ready)
		stdout.Write(respLine)
	} else {
		stdout.Write(respLine)
		stdout.Write(ready)
	}
	return &sandbox.ExecResult{ExitCode: h.exitCode, Stdout: stdout.Bytes()}, nil
}

// TestBackendPool_CallHappyPath covers the canonical flow: BindSession
// provisions the session, Call pipes a request, parses the response,
// returns the raw result JSON.
func TestBackendPool_CallHappyPath(t *testing.T) {
	m := mock.New()
	h := &execHandler{
		t:          t,
		readyFirst: true,
		respondWith: func(req map[string]any) (json.RawMessage, string) {
			// Pin the on-wire request shape so the NDJSON format drifts
			// fail here rather than silently in production.
			if req["tool"] != "read_file" {
				t.Errorf("tool = %v, want read_file", req["tool"])
			}
			args, ok := req["args"].(map[string]any)
			if !ok {
				t.Fatalf("args missing or wrong type: %T", req["args"])
			}
			if args["path"] != "/tmp/foo" {
				t.Errorf("args.path = %v, want /tmp/foo", args["path"])
			}
			return json.RawMessage(`{"content":"hello"}`), ""
		},
	}
	m.ExecResultFn = h.run

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{CPUs: 2, MemoryMiB: 512})
	defer pool.Cleanup()

	// MockBackend requires a TemplateID on CreateSession; real backends
	// also require one (it keys the layer chain). Using GetOrCreateWithTemplate
	// here exercises the production path for chat sessions.
	client := pool.GetOrCreateWithTemplate("sess-happy", "default")
	if client == nil {
		t.Fatalf("GetOrCreate returned nil")
	}

	// BindSession is what NodeTool.ProcessRequest invokes before the LLM
	// call; Call's safety-net binding also works, but exercising the
	// fast path mirrors production behavior.
	client.BindSession("sess-happy")

	raw, err := client.Call("sess-happy", "read_file", map[string]any{"path": "/tmp/foo"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got["content"] != "hello" {
		t.Errorf("result.content = %q, want %q", got["content"], "hello")
	}

	// Verify Backend saw the expected lifecycle calls.
	if len(m.CreateSessionCalls()) != 1 {
		t.Errorf("CreateSessionCalls = %d, want 1", len(m.CreateSessionCalls()))
	}
	if got := m.CreateSessionCalls()[0].Spec.SessionID; got != "sess-happy" {
		t.Errorf("CreateSession.SessionID = %q, want sess-happy", got)
	}
	if got := m.CreateSessionCalls()[0].Spec.Type; got != sandbox.SessionTypeChat {
		t.Errorf("CreateSession.Type = %q, want chat", got)
	}
	if got := m.CreateSessionCalls()[0].Spec.Limits.MemoryMiB; got != 512 {
		t.Errorf("CreateSession.Limits.MemoryMiB = %d, want 512", got)
	}
	if n := len(m.StartSessionCalls()); n != 1 {
		t.Errorf("StartSessionCalls = %d, want 1", n)
	}
	if n := h.calls.Load(); n != 1 {
		t.Errorf("Exec calls = %d, want 1", n)
	}
}

// TestBackendPool_ReadyLineAfterResponse pins framing robustness: the
// node server could, under load or panic recovery, flush the response
// before the ready line reaches stdout. parseNodeResponse must still
// pick the first id-bearing line regardless of position.
func TestBackendPool_ReadyLineAfterResponse(t *testing.T) {
	m := mock.New()
	m.ExecResultFn = (&execHandler{
		t:          t,
		readyFirst: false, // ready line comes last
		respondWith: func(req map[string]any) (json.RawMessage, string) {
			return json.RawMessage(`"ok"`), ""
		},
	}).run

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	client := pool.GetOrCreateWithTemplate("sess-oo", "default")
	raw, err := client.Call("sess-oo", "noop", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(raw) != `"ok"` {
		t.Errorf("result = %s, want \"ok\"", raw)
	}
}

// TestBackendPool_NodeError verifies error propagation: when the node
// server emits {"error": "..."}, Call returns that as a Go error.
func TestBackendPool_NodeError(t *testing.T) {
	m := mock.New()
	m.ExecResultFn = (&execHandler{
		t:          t,
		readyFirst: true,
		respondWith: func(req map[string]any) (json.RawMessage, string) {
			return nil, "permission denied"
		},
	}).run

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	client := pool.GetOrCreateWithTemplate("sess-err", "default")
	_, err := client.Call("sess-err", "read_file", nil)
	if err == nil {
		t.Fatalf("Call: want error, got nil")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v, want it to mention 'permission denied'", err)
	}
}

// TestBackendPool_EmptyStdout exercises the "no response line" branch —
// happens when the node process crashes before writing anything. The
// error should surface the exit code so operators can correlate with
// container logs.
func TestBackendPool_EmptyStdout(t *testing.T) {
	m := mock.New()
	m.ExecResultFn = func(sessionID string, spec sandbox.ExecSpec) (*sandbox.ExecResult, error) {
		// Drain stdin so the real client isn't blocked waiting to write.
		if spec.Stdin != nil {
			_, _ = io.ReadAll(spec.Stdin)
		}
		return &sandbox.ExecResult{ExitCode: 137}, nil // OOM-kill shape
	}

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	client := pool.GetOrCreateWithTemplate("sess-oom", "default")
	_, err := client.Call("sess-oom", "read_file", nil)
	if err == nil {
		t.Fatalf("Call on empty stdout: want error, got nil")
	}
	if !strings.Contains(err.Error(), "exit=137") {
		t.Errorf("err = %v, want it to mention exit=137", err)
	}
}

// TestBackendPool_GetOrCreateReusesClient confirms the pool caches one
// client per sessionID. Matching NodeClientPool behavior.
func TestBackendPool_GetOrCreateReusesClient(t *testing.T) {
	m := mock.New()
	m.ExecResultFn = (&execHandler{
		t:          t,
		readyFirst: true,
		respondWith: func(req map[string]any) (json.RawMessage, string) {
			return json.RawMessage(`{}`), ""
		},
	}).run

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	a := pool.GetOrCreateWithTemplate("sess-reuse", "default")
	b := pool.GetOrCreateWithTemplate("sess-reuse", "default")
	if a != b {
		t.Errorf("GetOrCreate returned different clients for same sessionID")
	}

	// Two calls should trigger only ONE CreateSession — this is the
	// whole point of caching (avoid re-creating pods on every tool call).
	if _, err := a.Call("sess-reuse", "noop", nil); err != nil {
		t.Fatalf("Call 1: %v", err)
	}
	if _, err := b.Call("sess-reuse", "noop", nil); err != nil {
		t.Fatalf("Call 2: %v", err)
	}
	if n := len(m.CreateSessionCalls()); n != 1 {
		t.Errorf("CreateSessionCalls = %d, want 1 after two Call()s on same session", n)
	}
}

// TestBackendPool_GetOrCreateWithTemplatePropagates pins that the
// template name flows into CreateSession.Spec.TemplateID. This is how
// chat sessions opt into non-default templates.
func TestBackendPool_GetOrCreateWithTemplatePropagates(t *testing.T) {
	m := mock.New()
	m.ExecResultFn = (&execHandler{
		t:          t,
		readyFirst: true,
		respondWith: func(req map[string]any) (json.RawMessage, string) {
			return json.RawMessage(`{}`), ""
		},
	}).run

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	client := pool.GetOrCreateWithTemplate("sess-tpl", "python-data-science")
	if _, err := client.Call("sess-tpl", "noop", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}

	calls := m.CreateSessionCalls()
	if len(calls) != 1 {
		t.Fatalf("CreateSessionCalls = %d, want 1", len(calls))
	}
	if got := calls[0].Spec.TemplateID; got != "python-data-science" {
		t.Errorf("CreateSession.TemplateID = %q, want python-data-science", got)
	}
}

// TestBackendPool_CleanupDestroysSessions verifies Cleanup calls
// Backend.DestroySession for every client, and blocks further
// GetOrCreate calls. Both are required for the flow-cleanup contract
// (pkg/sandbox/flow.go: callers defer pool.Cleanup()).
func TestBackendPool_CleanupDestroysSessions(t *testing.T) {
	m := mock.New()
	m.ExecResultFn = (&execHandler{
		t:          t,
		readyFirst: true,
		respondWith: func(req map[string]any) (json.RawMessage, string) {
			return json.RawMessage(`{}`), ""
		},
	}).run

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})

	// Seed two sessions via real Call (drives CreateSession).
	for _, sid := range []string{"s1", "s2"} {
		c := pool.GetOrCreateWithTemplate(sid, "default")
		if _, err := c.Call(sid, "noop", nil); err != nil {
			t.Fatalf("Call %s: %v", sid, err)
		}
	}

	pool.Cleanup()

	// Both sessions destroyed.
	destroyed := m.DestroySessionCalls()
	if len(destroyed) != 2 {
		t.Errorf("DestroySessionCalls = %d, want 2", len(destroyed))
	}
	got := map[string]bool{destroyed[0]: true, destroyed[1]: true}
	if !got["s1"] || !got["s2"] {
		t.Errorf("destroyed sessions = %v, want {s1,s2}", destroyed)
	}

	// Pool is closed; further GetOrCreate returns nil.
	if c := pool.GetOrCreate("s3"); c != nil {
		t.Errorf("GetOrCreate after Cleanup = %v, want nil", c)
	}
}

// TestBackendPool_EmptySessionID matches LazyNodeClient: GetOrCreate("")
// returns nil, keeps callers' nil-check pattern working.
func TestBackendPool_EmptySessionID(t *testing.T) {
	m := mock.New()
	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	if c := pool.GetOrCreate(""); c != nil {
		t.Errorf("GetOrCreate(\"\") = %v, want nil", c)
	}
	if c := pool.GetOrCreateWithTemplate("", "tpl"); c != nil {
		t.Errorf("GetOrCreateWithTemplate(\"\", _) = %v, want nil", c)
	}
}

// TestNewBackendPool_NilBackend documents the nil-safety contract:
// callers that construct a pool before knowing whether a backend is
// configured can swap in nil and check the pool == nil themselves.
func TestNewBackendPool_NilBackend(t *testing.T) {
	if p := sandbox.NewBackendPool(nil, sandbox.ResourceLimits{}); p != nil {
		t.Errorf("NewBackendPool(nil, _) = %v, want nil", p)
	}
}

// TestBackendPool_BindSessionIdempotent pins that BindSession only
// triggers ONE CreateSession regardless of how many times it's invoked.
// LazyNodeClient has the same guarantee; the interface contract demands
// it so ProcessRequest can over-bind without over-provisioning.
func TestBackendPool_BindSessionIdempotent(t *testing.T) {
	m := mock.New()
	m.ExecResultFn = (&execHandler{
		t:          t,
		readyFirst: true,
		respondWith: func(req map[string]any) (json.RawMessage, string) {
			return json.RawMessage(`{}`), ""
		},
	}).run

	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	c := pool.GetOrCreateWithTemplate("sess-idem", "default")
	for i := 0; i < 5; i++ {
		c.BindSession("sess-idem")
	}

	// Give the background goroutine a chance to settle before we assert.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.CreateSessionCalls()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := len(m.CreateSessionCalls()); n != 1 {
		t.Errorf("CreateSessionCalls = %d after 5 BindSession, want 1", n)
	}
}

// TestBackendPool_NetworkAllowEndpointsBakedIntoCreateSession pins that
// SetNetworkAllowEndpoints before BindSession flows into SessionSpec so
// OpenShell CreateSession can merge DB allows into create-time policy.
func TestBackendPool_NetworkAllowEndpointsBakedIntoCreateSession(t *testing.T) {
	m := mock.New()
	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	c := pool.GetOrCreateWithTemplate("sess-nps", "default")
	setter, ok := c.(interface {
		SetNetworkAllowEndpoints([]sandbox.NetworkAllowEndpoint)
	})
	if !ok {
		t.Fatal("client does not implement SetNetworkAllowEndpoints")
	}
	setter.SetNetworkAllowEndpoints([]sandbox.NetworkAllowEndpoint{
		{Host: "**.cloud.sap", Port: 443},
	})
	c.BindSession("sess-nps")
	if err := c.EnsureReady("sess-nps"); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}

	calls := m.CreateSessionCalls()
	if len(calls) != 1 {
		t.Fatalf("CreateSessionCalls = %d, want 1", len(calls))
	}
	eps := calls[0].Spec.NetworkAllowEndpoints
	if len(eps) != 1 || eps[0].Host != "**.cloud.sap" || eps[0].Port != 443 {
		t.Fatalf("NetworkAllowEndpoints = %+v, want **.cloud.sap:443", eps)
	}
}

// TestBackendPool_EnsureReadyRebindsAfterDestroy pins that a cached client
// whose sandbox was destroyed (ephemeral adaptive runs) re-provisions on
// the next EnsureReady instead of returning success against a gone pod.
func TestBackendPool_EnsureReadyRebindsAfterDestroy(t *testing.T) {
	m := mock.New()
	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	c := pool.GetOrCreateWithTemplate("sess-rebind", "default")
	if err := c.EnsureReady("sess-rebind"); err != nil {
		t.Fatalf("first EnsureReady: %v", err)
	}
	if n := len(m.CreateSessionCalls()); n != 1 {
		t.Fatalf("CreateSessionCalls after first EnsureReady = %d, want 1", n)
	}

	if err := m.DestroySession(context.Background(), "sess-rebind"); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	if err := c.EnsureReady("sess-rebind"); err != nil {
		t.Fatalf("second EnsureReady: %v", err)
	}
	if n := len(m.CreateSessionCalls()); n != 2 {
		t.Fatalf("CreateSessionCalls after rebind = %d, want 2", n)
	}
}

// TestBackendPool_RemoveDropsClientWithoutDestroy verifies Remove discards
// the cached client so GetOrCreate returns a fresh unbound client, without
// an extra DestroySession (caller already destroyed).
func TestBackendPool_RemoveDropsClientWithoutDestroy(t *testing.T) {
	m := mock.New()
	pool := sandbox.NewBackendPool(m, sandbox.ResourceLimits{})
	defer pool.Cleanup()

	a := pool.GetOrCreateWithTemplate("sess-rm", "default")
	if err := a.EnsureReady("sess-rm"); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	destroyBefore := len(m.DestroySessionCalls())

	pool.Remove("sess-rm")
	if n := len(m.DestroySessionCalls()); n != destroyBefore {
		t.Fatalf("Remove must not DestroySession; calls %d → %d", destroyBefore, n)
	}

	b := pool.GetOrCreateWithTemplate("sess-rm", "default")
	if a == b {
		t.Fatal("GetOrCreate after Remove returned the same discarded client")
	}
	if err := b.EnsureReady("sess-rm"); err != nil {
		t.Fatalf("EnsureReady on fresh client: %v", err)
	}
	if n := len(m.CreateSessionCalls()); n != 2 {
		t.Fatalf("CreateSessionCalls = %d, want 2 (second client provisions)", n)
	}
}
