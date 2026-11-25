package launcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/cache"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"golang.org/x/net/websocket"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

type scopedBrowserTestBackend struct {
	sandbox.Backend
	t           *testing.T
	owner       string
	data        []byte
	stat        *sandbox.ExecResult
	err         error
	calls       int
	pushStarted chan struct{}
}

func (b *scopedBrowserTestBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *scopedBrowserTestBackend) PushFile(ctx context.Context, id, p string, data io.Reader, mode os.FileMode) error {
	b.calls++
	principal, _ := execution.PrincipalFromContext(ctx)
	if id != "browser-owner-session" || principal.Subject != b.owner || p != "/workspace/proof.pdf" || mode != 0600 {
		b.t.Fatal("wrong artifact scope")
	}
	if _, ok := ctx.Deadline(); !ok {
		b.t.Fatal("unbounded push")
	}
	b.data, _ = io.ReadAll(data)
	if b.pushStarted != nil {
		close(b.pushStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	return b.err
}
func (b *scopedBrowserTestBackend) Exec(ctx context.Context, id string, spec sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	b.calls++
	principal, _ := execution.PrincipalFromContext(ctx)
	if id != "browser-owner-session" || principal.Subject != b.owner || len(spec.Command) != 5 || spec.Command[4] != "/tmp/astonish-recordings/demo.mp4" {
		b.t.Fatal("wrong stat scope")
	}
	if _, ok := ctx.Deadline(); !ok {
		b.t.Fatal("unbounded stat")
	}
	return b.stat, b.err
}

type scopedBrowserToolContext struct{ runtimeTestToolContext }

func (scopedBrowserToolContext) SessionID() string { return "browser-owner-session" }

// Protocol fixture, not physical browser evidence. Common tools execute through
// the real runtime callbacks and fake session backend; no user's Chrome starts.
func TestChatRuntimeScopedBrowserArtifactsAndHandoff(t *testing.T) {
	f := newBotFactoryFixture(t)
	cfg := &config.AppConfig{}
	inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputs.Close() })
	ctx, err := inputs.Bind(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	backend := &scopedBrowserTestBackend{t: t, owner: f.req.Principal.Subject, stat: &sandbox.ExecResult{Stdout: []byte("123\n")}}
	resources := &ChatCatalogResources{Backend: backend, Pool: &catalogTestPool{}}
	// The complete constructor creates its own manager/catalog without mutating
	// the caller's resource container or closing the borrowed backend.
	runtime, err := NewScopedChatRuntime(ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), SessionID: "browser-owner-session", Resources: resources}, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ChatAgent.ToolIndex.GetToolEntry("browser_pdf") == nil || resources.BrowserManager != nil {
		t.Fatal("constructor browser composition not isolated")
	}
	runtime.Cleanup()
	missing := &ChatCatalogResources{Pool: &catalogTestPool{}}
	if _, err := prepareScopedChatBrowser(ctx, &ChatFactoryConfig{AppConfig: cfg}, "browser-owner-session", missing, inputs); err == nil {
		t.Fatal("nil backend accepted")
	}
	cleanup, err := prepareScopedChatBrowser(ctx, &ChatFactoryConfig{AppConfig: cfg}, "browser-owner-session", resources, inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	mgr := resources.BrowserManager
	if mgr == nil || !mgr.ScopedHandoff || mgr.WriteArtifactFunc == nil || mgr.StatArtifactFunc == nil || mgr.RegisterHandoffFunc == nil || len(resources.BrowserTools) == 0 {
		t.Fatal("incomplete browser wiring")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_ = json.NewEncoder(w).Encode(map[string]any{"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/test"})
			return
		}
		websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(c *websocket.Conn) {
			defer c.Close()
			for {
				var msg struct {
					ID        int    `json:"id"`
					Method    string `json:"method"`
					SessionID string `json:"sessionId"`
				}
				if websocket.JSON.Receive(c, &msg) != nil {
					return
				}
				result := map[string]any{}
				target := map[string]any{"targetId": "page", "type": "page", "url": "https://fixture.invalid/", "attached": true}
				switch msg.Method {
				case "Browser.getVersion":
					result = map[string]any{"product": "Chrome/130.0", "protocolVersion": "1.3"}
				case "Target.getTargets":
					result = map[string]any{"targetInfos": []any{target}}
				case "Target.attachToTarget":
					result = map[string]any{"sessionId": "page-session"}
				case "Target.getTargetInfo":
					result = map[string]any{"targetInfo": target}
				case "Page.printToPDF":
					result = map[string]any{"stream": "pdf"}
				case "IO.read":
					result = map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("%PDF-session")), "base64Encoded": true, "eof": true}
				}
				if websocket.JSON.Send(c, map[string]any{"id": msg.ID, "sessionId": msg.SessionID, "result": result}) != nil {
					return
				}
			}
		}}.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	// Only process/CDP transport is simulated. Artifact/handoff callbacks above
	// are unchanged from production composition.
	mgr.ContainerEnsureReadyFunc = func(context.Context, string) error { return nil }
	mgr.ContainerResolveFunc = func(id string) (string, string, error) { return id, "127.0.0.1", nil }
	mgr.ContainerStartBrowserFunc = func(string) (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	mgr.ContainerDialFunc = func(string, int) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) }
	stopped := false
	mgr.ContainerStartRecordingFunc = func(id, display, p string) (func() error, int, int, error) {
		if id != "browser-owner-session" || p != "/tmp/astonish-recordings/demo.mp4" {
			t.Fatal("wrong recording session")
		}
		return func() error { stopped = true; return nil }, 1920, 1080, nil
	}
	toolCtx := scopedBrowserToolContext{runtimeTestToolContext{lifetime: ctx}}
	pdf, err := tools.BrowserPDF(mgr)(toolCtx, tools.BrowserPDFArgs{Path: "proof.pdf"})
	if err != nil || pdf.SizeBytes != 12 || string(backend.data) != "%PDF-session" {
		t.Fatalf("PDF %+v %q %v", pdf, backend.data, err)
	}
	if _, err = tools.BrowserStartRecording(mgr)(toolCtx, tools.BrowserStartRecordingArgs{Filename: "demo.mp4"}); err != nil {
		t.Fatal(err)
	}
	recording, err := tools.BrowserStopRecording(mgr)(toolCtx, tools.BrowserStopRecordingArgs{})
	if err != nil || recording.SizeBytes != 123 || !stopped {
		t.Fatalf("recording %+v %v", recording, err)
	}
	handoff, err := tools.BrowserRequestHuman(mgr)(toolCtx, tools.BrowserRequestHumanArgs{Reason: "fixture"})
	if err != nil || !handoff.Success || handoff.VNCProxyURL == "" {
		t.Fatalf("handoff %+v %v", handoff, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/browser/handoff-done", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	api.BrowserHandoffDoneHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Done %d %s", response.Code, response.Body.String())
	}
	before := backend.calls
	foreign := f.req.Principal
	foreign.Subject = "other-owner"
	foreignCtx, _ := execution.WithPrincipal(t.Context(), foreign)
	for _, bad := range []context.Context{foreignCtx, t.Context()} {
		if mgr.WriteArtifactFunc(bad, "browser-owner-session", "/workspace/proof.pdf", []byte("x")) == nil {
			t.Fatal("foreign write accepted")
		}
		if _, err := mgr.StatArtifactFunc(bad, "browser-owner-session", "/tmp/astonish-recordings/demo.mp4"); err == nil {
			t.Fatal("foreign stat accepted")
		}
	}
	if backend.calls != before {
		t.Fatal("denial reached backend")
	}
	if mgr.WriteArtifactFunc(ctx, "foreign-session", "/workspace/proof.pdf", []byte("x")) == nil {
		t.Fatal("foreign session accepted")
	}
	for _, filename := range []string{"/tmp/proof.pdf", "/workspace/../proof.pdf", "/workspace/sub/proof.pdf"} {
		if mgr.WriteArtifactFunc(ctx, "browser-owner-session", filename, []byte("x")) == nil {
			t.Fatal("invalid path accepted")
		}
	}
	if backend.calls != before {
		t.Fatal("invalid path/session reached backend")
	}
	backend.stat = nil
	if _, err := mgr.StatArtifactFunc(ctx, "browser-owner-session", "/tmp/astonish-recordings/demo.mp4"); err == nil {
		t.Fatal("nil stat response accepted")
	}
	backend.err = errors.New("backend unavailable")
	if mgr.WriteArtifactFunc(ctx, "browser-owner-session", "/workspace/proof.pdf", []byte("x")) == nil {
		t.Fatal("push error hidden")
	}
	backend.err = nil
	backend.pushStarted = make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- mgr.WriteArtifactFunc(ctx, "browser-owner-session", "/workspace/proof.pdf", []byte("x"))
	}()
	select {
	case <-backend.pushStarted:
	case <-time.After(time.Second):
		t.Fatal("push did not start")
	}
	cleanup()
	cleanup()
	select {
	case err := <-writeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cleanup did not cancel active push: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup left backend write running")
	}
	before = backend.calls
	if mgr.WriteArtifactFunc(ctx, "browser-owner-session", "/workspace/proof.pdf", []byte("x")) == nil {
		t.Fatal("released artifact callback accepted")
	}
	if backend.calls != before {
		t.Fatal("released callback reached backend")
	}
	if _, err := mgr.RegisterHandoffFunc("browser-owner-session", "browser-owner-session"); err == nil {
		t.Fatal("released handoff resurrected")
	}
}

type runtimeDiscoveryToolset struct {
	tool.Toolset
	check func(adkagent.ReadonlyContext) error
	calls atomic.Int32
}

func (s *runtimeDiscoveryToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	s.calls.Add(1)
	if s.check != nil {
		if err := s.check(ctx); err != nil {
			return nil, err
		}
	}
	return s.Toolset.Tools(ctx)
}

type runtimeBindingFunc func(context.Context) (context.Context, error)

func (f runtimeBindingFunc) Bind(ctx context.Context) (context.Context, error) { return f(ctx) }

func TestChatRuntimeLazyDiscoveryScopeAndIdentity(t *testing.T) {
	f := newBotFactoryFixture(t)
	inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: &config.AppConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputs.Close() })
	policy := &runtimeTestPolicy{deny: true}
	lazy := agent.NewLazyMCPToolset("fixture", []cache.ToolEntry{{Name: "lazy_fixture", Description: "cached MCP fixture"}}, config.MCPServerConfig{}, false)
	probe := &runtimeDiscoveryToolset{Toolset: lazy, check: func(ctx adkagent.ReadonlyContext) error {
		p, ok := execution.PrincipalFromContext(ctx)
		if !ok || p.Subject != f.req.Principal.Subject {
			return errors.New("discovery lost principal")
		}
		g := agent.RequestMCPGroupsFromContext(ctx)["mcp:fixture"]
		if g == nil {
			return errors.New("discovery lost guarded groups")
		}
		if _, ok := g.Toolsets[0].(*chatRuntimeToolset); !ok {
			return errors.New("discovery exposed raw toolset")
		}
		return nil
	}}
	deps := ChatRuntimeDependencies{Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), Catalog: &ChatCatalog{Groups: map[string]*agent.ToolGroup{"mcp:fixture": {Name: "mcp:fixture", Toolsets: []tool.Toolset{probe}}}}, Toolsets: []tool.Toolset{probe}}
	runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{Context: inputs, Authorization: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	entry := runtime.ChatAgent.ToolIndex.GetToolEntry("lazy_fixture")
	if entry == nil {
		t.Fatal("missing indexed lazy tool")
	}
	sets := []tool.Toolset{runtime.MCPToolsets[0], runtime.PromptBuilder.Catalog[0].Toolsets[0], runtime.ChatAgent.Toolsets[0]}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, set := range sets {
				got, err := set.Tools(runtimeTestToolContext{lifetime: t.Context()})
				if err != nil || len(got) != 1 {
					t.Errorf("discovery: %v %v", got, err)
					return
				}
				if got[0] != entry.Tool {
					t.Error("lazy executable changed across discovery views")
				}
			}
		}()
	}
	wg.Wait()
	second, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{Context: inputs, Authorization: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Cleanup)
	if second.ChatAgent.ToolIndex.GetToolEntry("lazy_fixture").Tool == entry.Tool {
		t.Fatal("separate attempts shared guarded executable")
	}
	request := &model.LLMRequest{}
	ctx := runtimeTestToolContext{lifetime: t.Context()}
	if err := entry.Tool.(chatRuntimeRunnable).ProcessRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if request.Tools[entry.Name] != entry.Tool {
		t.Fatal("declaration registered raw executable")
	}
	if _, err := entry.Tool.(chatRuntimeRunnable).Run(ctx, map[string]any{}); !errors.Is(err, errRuntimeTestDenied) {
		t.Fatalf("lazy dispatch bypassed policy: %v", err)
	}
	foreign := f.req.Principal
	foreign.Subject = "foreign-owner"
	foreignCtx, err := execution.WithPrincipal(t.Context(), foreign)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	before := probe.calls.Load()
	for _, bad := range []context.Context{foreignCtx, cancelled} {
		for _, set := range sets {
			if _, err := set.Tools(runtimeTestToolContext{lifetime: bad}); err == nil {
				t.Error("out-of-scope discovery accepted")
			}
		}
	}
	if probe.calls.Load() != before {
		t.Fatal("invalid discovery reached underlying toolset")
	}
	if _, err := sets[0].Tools(nil); err == nil {
		t.Fatal("nil discovery context accepted")
	}
}

type runtimeFixedToolset struct{ tools []tool.Tool }

func (*runtimeFixedToolset) Name() string { return "fixed" }
func (s *runtimeFixedToolset) Tools(adkagent.ReadonlyContext) ([]tool.Tool, error) {
	return append([]tool.Tool(nil), s.tools...), nil
}

func TestChatRuntimeLazyAndEagerExecutableIdentity(t *testing.T) {
	calls := 0
	fixture, err := functiontool.New(functiontool.Config{Name: "shared_fixture", Description: "fixture"}, func(tool.Context, struct{}) (map[string]any, error) {
		calls++
		return map[string]any{"executed": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	set := &runtimeFixedToolset{tools: []tool.Tool{fixture}}
	policy := &runtimeTestPolicy{}
	runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), Catalog: &ChatCatalog{MainThreadTools: []tool.Tool{fixture}, Groups: map[string]*agent.ToolGroup{"fixture": {Name: "fixture", Tools: []tool.Tool{fixture}, Toolsets: []tool.Toolset{set}}}}}, ChatRuntimeHooks{Authorization: policy, Accounting: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	ctx := runtimeTestToolContext{lifetime: t.Context()}
	for i := 0; i < 2; i++ {
		ts, err := runtime.MCPToolsets[0].Tools(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ts[0] != runtime.ChatAgent.Tools[0] || ts[0] != runtime.ChatAgent.ToolIndex.GetToolEntry(fixture.Name()).Tool {
			t.Fatal("eager/lazy executable split")
		}
		got, err := ts[0].(chatRuntimeRunnable).Run(ctx, map[string]any{})
		if err != nil || got["executed"] != true {
			t.Fatalf("guarded execution: %v %v", got, err)
		}
	}
	if calls != 2 || policy.observed != 2 {
		t.Fatalf("execution/accounting mismatch: %d/%d", calls, policy.observed)
	}
}

func TestChatRuntimeLazyRejectsInvalidDiscovery(t *testing.T) {
	for _, tools := range [][]tool.Tool{{nil}, {runtimeInertTool{}}} {
		set := &chatRuntimeToolset{Toolset: &runtimeFixedToolset{tools: tools}, binding: &chatRuntimeBinding{}}
		if _, err := set.Tools(runtimeTestToolContext{lifetime: t.Context()}); err == nil {
			t.Fatal("invalid executable accepted")
		}
	}
	lazy := agent.NewLazyMCPToolset("duplicate", []cache.ToolEntry{{Name: "duplicate"}, {Name: "duplicate"}}, config.MCPServerConfig{}, false)
	set := &chatRuntimeToolset{Toolset: lazy, binding: &chatRuntimeBinding{}}
	if _, err := set.Tools(runtimeTestToolContext{lifetime: t.Context()}); err == nil {
		t.Fatal("same-toolset name collision accepted")
	}
}

func TestChatRuntimeLazyNameCollisions(t *testing.T) {
	newLazy := func() tool.Toolset {
		return agent.NewLazyMCPToolset("fixture", []cache.ToolEntry{{Name: "collision", Description: "fixture"}}, config.MCPServerConfig{}, false)
	}
	fixture, err := functiontool.New(functiontool.Config{Name: "collision", Description: "ordinary"}, func(tool.Context, struct{}) (map[string]any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ordinary", "extension", "toolset", "direct"} {
		t.Run(kind, func(t *testing.T) {
			catalog := &ChatCatalog{Groups: map[string]*agent.ToolGroup{"mcp:first": {Name: "mcp:first", Toolsets: []tool.Toolset{newLazy()}}}}
			hooks := ChatRuntimeHooks{Authorization: &runtimeTestPolicy{}}
			switch kind {
			case "ordinary":
				catalog.MainThreadTools = []tool.Tool{fixture}
			case "extension":
				hooks.AdditionalTools = []tool.Tool{fixture}
			case "toolset":
				catalog.Groups["mcp:second"] = &agent.ToolGroup{Name: "mcp:second", Toolsets: []tool.Toolset{newLazy()}}
			case "direct":
				catalog.MainThreadTools = []tool.Tool{fixture}
				other, _ := functiontool.New(functiontool.Config{Name: "collision", Description: "different executable"}, func(tool.Context, struct{}) (map[string]any, error) { return nil, nil })
				catalog.Groups = map[string]*agent.ToolGroup{"other": {Name: "other", Tools: []tool.Tool{other}}}
			}
			result, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), Catalog: catalog}, hooks)
			if result != nil {
				result.Cleanup()
			}
			if err == nil {
				t.Fatal("ambiguous executable name accepted")
			}
		})
	}
}

type runtimeLifetimeTool struct {
	chatRuntimeRunnable
	check func(context.Context)
}

func (t *runtimeLifetimeTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	t.check(ctx)
	return t.chatRuntimeRunnable.ProcessRequest(ctx, req)
}

func TestChatRuntimeBoundContextLifetime(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	bound, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	check := func(ctx context.Context) {
		if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
			t.Error("hook deadline lost")
		}
		if ctx.Done() == nil {
			t.Error("hook cancellation channel lost")
		}
	}
	fixture, err := functiontool.New(functiontool.Config{Name: "lifetime", Description: "fixture"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
		check(ctx)
		cancel()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Error("hook cancellation channel did not close")
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("hook cancellation lost during execution")
		}
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := &chatRuntimeBinding{hooks: ChatRuntimeHooks{Context: runtimeBindingFunc(func(context.Context) (context.Context, error) { return bound, nil })}}
	wrapped := &chatRuntimeTool{chatRuntimeRunnable: &runtimeLifetimeTool{chatRuntimeRunnable: fixture.(chatRuntimeRunnable), check: check}, binding: binding}
	ctx := runtimeTestToolContext{lifetime: t.Context()}
	if err := wrapped.ProcessRequest(ctx, &model.LLMRequest{}); err != nil {
		t.Fatal(err)
	}
	lazy := agent.NewLazyMCPToolset("lifetime", []cache.ToolEntry{{Name: "lifetime_lazy"}}, config.MCPServerConfig{}, false)
	probe := &runtimeDiscoveryToolset{Toolset: lazy, check: func(ctx adkagent.ReadonlyContext) error { check(ctx); return nil }}
	set := &chatRuntimeToolset{Toolset: probe, binding: binding}
	if _, err := set.Tools(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = wrapped.Run(ctx, map[string]any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execution lost bound cancellation: %v", err)
	}
	if err := wrapped.ProcessRequest(ctx, &model.LLMRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hook declaration: %v", err)
	}
	if _, err := set.Tools(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hook discovery: %v", err)
	}
	if probe.calls.Load() != 1 {
		t.Fatal("cancelled hook reached discovery")
	}
}

func TestChatRuntimeHookCannotExtendInvocationLifetime(t *testing.T) {
	for _, mode := range []string{"run", "declaration", "discovery"} {
		t.Run(mode, func(t *testing.T) {
			invocation, cancel := context.WithDeadline(t.Context(), time.Now().Add(time.Minute))
			defer cancel()
			deadline, _ := invocation.Deadline()
			check := func(ctx context.Context) {
				if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
					t.Error("hook extended invocation deadline")
				}
				cancel()
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Error("hook discarded invocation cancellation")
				}
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Error("hook discarded invocation cancellation error")
				}
			}
			fixture, err := functiontool.New(functiontool.Config{Name: "short_lifetime", Description: "fixture"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
				check(ctx)
				return nil, ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			binding := &chatRuntimeBinding{hooks: ChatRuntimeHooks{Context: runtimeBindingFunc(func(context.Context) (context.Context, error) { return context.Background(), nil })}}
			wrapped := &chatRuntimeTool{chatRuntimeRunnable: &runtimeLifetimeTool{chatRuntimeRunnable: fixture.(chatRuntimeRunnable), check: check}, binding: binding}
			ctx := runtimeTestToolContext{lifetime: invocation}
			switch mode {
			case "run":
				_, _ = wrapped.Run(ctx, map[string]any{})
			case "declaration":
				_ = wrapped.ProcessRequest(ctx, &model.LLMRequest{})
			case "discovery":
				lazy := agent.NewLazyMCPToolset("short_lifetime", []cache.ToolEntry{{Name: "short_lifetime_lazy"}}, config.MCPServerConfig{}, false)
				set := &chatRuntimeToolset{Toolset: &runtimeDiscoveryToolset{Toolset: lazy, check: func(ctx adkagent.ReadonlyContext) error { check(ctx); return ctx.Err() }}, binding: binding}
				_, _ = set.Tools(ctx)
			}
		})
	}
}

func TestChatRuntimeLifetimeIntersection(t *testing.T) {
	for _, shorter := range []string{"caller", "hook"} {
		for _, cancelled := range []string{"caller", "hook"} {
			t.Run(shorter+"/cancel-"+cancelled, func(t *testing.T) {
				callerDeadline, hookDeadline := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
				if shorter == "hook" {
					callerDeadline, hookDeadline = hookDeadline, callerDeadline
				}
				caller, cancelCaller := context.WithDeadline(t.Context(), callerDeadline)
				defer cancelCaller()
				hook, cancelHook := context.WithDeadline(context.Background(), hookDeadline)
				defer cancelHook()
				type valueKey struct{}
				hook = context.WithValue(hook, valueKey{}, "hook value")
				merged, release := intersectChatRuntimeContext(caller, hook)
				defer release()
				wantDeadline := callerDeadline
				if shorter == "hook" {
					wantDeadline = hookDeadline
				}
				if got, ok := merged.Deadline(); !ok || !got.Equal(wantDeadline) {
					t.Fatalf("deadline = %v, %v; want %v", got, ok, wantDeadline)
				}
				if merged.Value(valueKey{}) != "hook value" {
					t.Fatal("hook values lost")
				}
				if cancelled == "caller" {
					cancelCaller()
				} else {
					cancelHook()
				}
				select {
				case <-merged.Done():
				case <-time.After(time.Second):
					t.Fatal("cancellation did not reach intersection")
				}
				if !errors.Is(merged.Err(), context.Canceled) {
					t.Fatalf("cancellation = %v", merged.Err())
				}
			})
		}
	}
}

// A custom Context exposes AfterFunc registrations, making link cleanup
// deterministic without relying on process-wide goroutine counts.
type runtimeLifetimeParent struct {
	context.Context
	links atomic.Int32
}

func (c *runtimeLifetimeParent) Value(any) any { return nil }
func (c *runtimeLifetimeParent) AfterFunc(f func()) func() bool {
	c.links.Add(1)
	stop := context.AfterFunc(c.Context, f)
	return func() bool {
		if stop() {
			c.links.Add(-1)
			return true
		}
		return false
	}
}

type runtimeLifetimeToolContext struct {
	runtimeTestToolContext
}

func (c runtimeLifetimeToolContext) AfterFunc(f func()) func() bool {
	return context.AfterFunc(c.lifetime, f)
}

func TestChatRuntimeLifetimeLinkCleanup(t *testing.T) {
	for _, mode := range []string{"run", "declaration", "discovery", "flow", "denied", "discovery-error"} {
		t.Run(mode, func(t *testing.T) {
			parent := &runtimeLifetimeParent{Context: t.Context()}
			hook, cancelHook := context.WithCancel(context.Background())
			defer cancelHook()
			var captured context.Context
			check := func(ctx context.Context) {
				captured = ctx
				if got := parent.links.Load(); got != 1 {
					t.Fatalf("active dispatch has %d cancellation links, want 1", got)
				}
				if ctx.Err() != nil {
					t.Fatalf("released context before dispatch: %v", ctx.Err())
				}
			}
			fixture, err := functiontool.New(functiontool.Config{Name: "cleanup_lifetime", Description: "fixture"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
				check(ctx)
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			binding := &chatRuntimeBinding{hooks: ChatRuntimeHooks{Context: runtimeBindingFunc(func(context.Context) (context.Context, error) { return hook, nil })}}
			wrapped := &chatRuntimeTool{chatRuntimeRunnable: &runtimeLifetimeTool{chatRuntimeRunnable: fixture.(chatRuntimeRunnable), check: check}, binding: binding}
			ctx := runtimeLifetimeToolContext{runtimeTestToolContext{lifetime: parent}}
			switch mode {
			case "run":
				_, err = wrapped.Run(ctx, map[string]any{})
			case "denied":
				binding.hooks.Authorization = &runtimeTestPolicy{deny: true}
				_, err = wrapped.Run(ctx, map[string]any{})
				if !errors.Is(err, errRuntimeTestDenied) {
					t.Fatalf("denial = %v", err)
				}
				err = nil
			case "declaration":
				err = wrapped.ProcessRequest(ctx, &model.LLMRequest{})
			case "discovery", "discovery-error":
				probe := &runtimeDiscoveryToolset{Toolset: &runtimeFixedToolset{}, check: func(ctx adkagent.ReadonlyContext) error {
					check(ctx)
					if mode == "discovery-error" {
						return errRuntimeTestDenied
					}
					return nil
				}}
				_, err = (&chatRuntimeToolset{Toolset: probe, binding: binding}).Tools(ctx)
				if mode == "discovery-error" && errors.Is(err, errRuntimeTestDenied) {
					err = nil
				}
			case "flow":
				runtime, buildErr := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{
					Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(),
					Sessions: session.InMemoryService(), Catalog: &ChatCatalog{},
					FlowRunner: func(ctx context.Context, _ string, _ map[string]string) (*agent.DryRunExecResult, error) {
						check(ctx)
						return nil, nil
					},
				}, binding.hooks)
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				t.Cleanup(runtime.Cleanup)
				_, err = runtime.ChatAgent.FlowRunner(parent, "", nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := parent.links.Load(); got != 0 {
				t.Fatalf("dispatch retained %d cancellation links", got)
			}
			if mode != "denied" && (captured == nil || !errors.Is(captured.Err(), context.Canceled)) {
				t.Fatal("dispatch did not release its bound context")
			}
			if hook.Err() != nil || parent.Err() != nil {
				t.Fatal("cleanup cancelled borrowed hook or caller context")
			}
		})
	}
}

// TestStudioChatRuntimeFactoryFlowContinuationAcrossAttempts is a regression
// contract: closing request resources must not discard an owner's paused flow.
func TestStudioChatRuntimeFactoryFlowContinuationAcrossAttempts(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.Flows = &productionFlowStore{yaml: `description: cross-attempt continuation
nodes:
  - name: seed
    type: input
    prompt: Seed
  - name: answer
    type: input
    prompt: Answer
  - name: done
    type: output
    user_message: [continued original flow]
flow:
  - from: START
    to: seed
  - from: seed
    to: answer
  - from: answer
    to: done
  - from: done
    to: END
`}
	cfg := &config.AppConfig{}
	factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg}, ChatCatalogResources{Pool: &catalogTestPool{}}, nil)
	ctx, err := execution.WithPrincipal(t.Context(), f.req.Principal)
	if err != nil {
		t.Fatal(err)
	}
	ss := session.InMemoryService()
	attempt := func(params map[string]string) map[string]any {
		t.Helper()
		inputs, err := chatruntime.Build(ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: cfg})
		if err != nil {
			t.Fatal(err)
		}
		defer inputs.Close()
		runtime, err := factory(ctx, api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: provider.NewPlaceholderLLM(), SessionService: ss, SessionID: "same-session", UserID: f.req.Principal.Subject, AppName: "astonish"})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Cleanup()
		entry := runtime.ChatAgent.ToolIndex.GetToolEntry("run_flow")
		if entry == nil {
			t.Fatal("missing run_flow")
		}
		out, err := entry.Tool.(chatRuntimeRunnable).Run(runtimeFlowDrillContext{runtimeTestToolContext{lifetime: ctx}}, map[string]any{"flow_name": "flow", "parameters": params})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := attempt(map[string]string{"seed": "original child state"})
	if first["status"] != "waiting_for_input" || first["input_node"] != "answer" {
		t.Fatalf("first attempt did not pause at answer: %v", first)
	}
	second := attempt(map[string]string{"answer": "owner response"})
	if second["status"] != "completed" || second["output"] != "continued original flow" {
		t.Fatalf("owner response restarted instead of resuming the paused child: %v", second)
	}
}

func TestStudioChatRuntimeFactoryUsesSharedScopedConstructor(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.Flows = &productionFlowStore{yaml: "description: factory engine\nnodes:\n  - name: done\n    type: output\n    user_message: [factory engine output]\nflow:\n  - from: START\n    to: done\n  - from: done\n    to: END\n"}
	cfg := &config.AppConfig{}
	inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inputs.Close() })
	factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg, ProviderName: "bootstrap-model"}, ChatCatalogResources{Pool: &catalogTestPool{}}, nil)
	comp := studioChatComponentsFromFactoryResult(&ChatFactoryResult{RuntimeFactory: factory}, true)
	if comp.RuntimeFactory == nil {
		t.Fatal("Studio/daemon bootstrap lost factory")
	}
	ctx, err := execution.WithPrincipal(t.Context(), f.req.Principal)
	if err != nil {
		t.Fatal(err)
	}
	// Supply the required live-authority wrapper explicitly so dependency identity
	// assertions also prove the constructor does not replace an already guarded model.
	llm := inputs.GuardModel(provider.NewPlaceholderLLM())
	sessions := session.InMemoryService()
	req := api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: llm, SessionService: sessions, SessionID: "scoped-session", UserID: f.req.Principal.Subject, AppName: "astonish", ProviderName: "scoped", ModelName: "fixture"}
	first, err := comp.RuntimeFactory(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Cleanup)
	flow := first.ChatAgent.ToolIndex.GetToolEntry("run_flow")
	if flow == nil {
		t.Fatal("shared factory omitted run_flow")
	}
	out, err := flow.Tool.(chatRuntimeRunnable).Run(runtimeFlowDrillContext{runtimeTestToolContext{lifetime: ctx}}, map[string]any{"flow_name": "factory-flow"})
	if err != nil || out["output"] != "factory engine output" || out["status"] != "completed" {
		t.Fatalf("factory flow engine: %v %v", out, err)
	}
	if first.ChatAgent.FlowRunner == nil {
		t.Fatal("shared factory omitted scoped distillation dry-run")
	}
	dryYAML, err := f.services.Flows.GetFlow(ctx, "factory-flow")
	if err != nil {
		t.Fatal(err)
	}
	dryResult, err := first.ChatAgent.FlowRunner(ctx, dryYAML, nil)
	if err != nil || dryResult == nil || !dryResult.Success || dryResult.Output != "factory engine output" {
		t.Fatalf("factory dry-run engine: %+v %v", dryResult, err)
	}
	otherLLM := inputs.GuardModel(provider.NewPlaceholderLLM())
	otherSessions := session.InMemoryService()
	secondReq := req
	secondReq.LLM, secondReq.SessionService = otherLLM, otherSessions
	second, err := comp.RuntimeFactory(ctx, secondReq)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Cleanup)
	if first.ChatAgent == second.ChatAgent || first.ChatAgent.ToolIndex == second.ChatAgent.ToolIndex || first.ChatAgent.Redactor == second.ChatAgent.Redactor {
		t.Fatal("shared mutable attempt state")
	}
	if first.ChatAgent.LLM != llm || first.ChatAgent.SessionService != sessions || first.ChatAgent.FlowDistiller == nil {
		t.Fatal("incomplete shared composition")
	}
	if second.ChatAgent.LLM != otherLLM || second.ChatAgent.SessionService != otherSessions {
		t.Fatal("request model/session dependencies leaked across attempts")
	}
	foreignReq := req
	foreignReq.UserID = "foreign-owner"
	if result, err := comp.RuntimeFactory(ctx, foreignReq); err == nil || result != nil {
		t.Fatal("factory accepted a session owner different from the bound principal")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if result, err := comp.RuntimeFactory(cancelled, req); err == nil || result != nil {
		t.Fatal("factory accepted a cancelled attempt")
	}
	first.Cleanup()
	first.Cleanup() // must never close borrowed pool/resources
}

// TestChatRuntimeCapabilityParity executes one future registration through the
// same construction with and without policy; it does not claim physical parity.
func TestChatRuntimeCapabilityParity(t *testing.T) {
	t.Run("configured-three-mode-subset", testChatRuntimeConfiguredThreeModeSubset)
	var calls atomic.Int32
	fixture, err := functiontool.New(functiontool.Config{Name: "future_capability", Description: "fixture"}, func(ctx tool.Context, args struct {
		Value string `json:"value"`
	}) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"value": args.Value}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, guarded := range []bool{false, true} {
		hooks := ChatRuntimeHooks{}
		policy := &runtimeTestPolicy{}
		if guarded {
			hooks.Authorization = policy
			hooks.Accounting = policy
		}
		catalog := &ChatCatalog{MainThreadTools: []tool.Tool{fixture}, Groups: map[string]*agent.ToolGroup{"fixture": {Name: "fixture", Tools: []tool.Tool{fixture}}}}
		runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Catalog: catalog, Sessions: session.InMemoryService(), Prompt: &agent.SystemPromptBuilder{}}, hooks)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(runtime.Cleanup)
		direct := runtime.ChatAgent.Tools[0]
		if direct != runtime.PromptBuilder.Catalog[0].Tools[0] {
			t.Fatal("direct and catalog executable differ")
		}
		runnable := direct.(interface {
			Run(tool.Context, any) (map[string]any, error)
		})
		got, err := runnable.Run(runtimeTestToolContext{lifetime: t.Context()}, map[string]any{"value": "executed"})
		if err != nil || got["value"] != "executed" {
			t.Fatalf("execution: %v %v", got, err)
		}
		if guarded {
			if policy.observed != 1 {
				t.Fatal("missing accounting")
			}
			policy.deny = true
			if _, err := runnable.Run(runtimeTestToolContext{lifetime: t.Context()}, map[string]any{"value": "denied"}); !errors.Is(err, errRuntimeTestDenied) {
				t.Fatalf("denial: %v", err)
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("denied capability executed: %d", calls.Load())
	}
}

type runtimeCapabilityToolContext struct{ sharedBotToolContext }

func (runtimeCapabilityToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

// TestChatRuntimeCapabilityParityConfiguredHTTPAndFlow exercises real HTTP and
// flow implementations through all three factory recipes. The borrowed node
// transport is the existing in-process fixture, not Docker/Kubernetes evidence.
func TestChatRuntimeCapabilityParityConfiguredHTTPAndFlow(t *testing.T) {
	tools.SetAllowPrivateNetworks(true) // local fixture endpoint only
	defer tools.SetAllowPrivateNetworks(false)
	const ordinary = "chat"
	const interactive = "interactive-bot"
	const autonomous = "autonomous-bot"
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{ordinary, interactive, autonomous} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				if scope == chatruntime.ResourceScopeTeam {
					f.req.Bot.Scope = store.BotScopeTeam
				}
				if mode == interactive {
					setBotSharedInteractive(f)
				} else if mode == autonomous {
					var err error
					f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"http_request", "run_flow"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				own := sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: productionTestSecret}})
				other := sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: "wrong-scope-secret"}})
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+productionTestSecret {
						t.Error("HTTP execution lost scoped credential")
					}
					fmt.Fprint(w, "configured HTTP response")
				}))
				defer srv.Close()
				fs := &productionFlowStore{yaml: fmt.Sprintf(`description: configured HTTP flow
nodes:
  - name: request
    type: tool
    tools_selection: [http_request]
    args:
      url: %q
      credential: owner
  - name: done
    type: output
    user_message: [configured flow completed]
flow:
  - from: START
    to: request
  - from: request
    to: done
  - from: done
    to: END
`, srv.URL)}
				foreignFlow := &productionFlowStore{yaml: "not the selected scope"}
				if scope == chatruntime.ResourceScopePersonal {
					f.services.PersonalCredentials, f.services.Credentials = own, other
					f.services.PersonalFlows, f.services.Flows = fs, foreignFlow
				} else {
					f.services.Credentials, f.services.PersonalCredentials = own, other
					f.services.Flows, f.services.PersonalFlows = fs, foreignFlow
				}
				cfg := &config.AppConfig{}
				backend := &productionCapabilityBackend{}
				client := &productionCapabilityClient{backend: backend}
				resources := ChatCatalogResources{Pool: productionCapabilityPool{backend: backend, client: client}}
				var chat *agent.ChatAgent
				var cleanup func()
				if mode == ordinary {
					inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: scope, AppConfig: cfg, Pool: resources.Pool, FallbackPolicy: chatruntime.FallbackForbidden})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = inputs.Close() })
					factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: cfg}, resources, nil)
					runtime, err := factory(f.ctx, api.StudioChatRuntimeRequest{Inputs: inputs, AppConfig: cfg, LLM: provider.NewPlaceholderLLM(), SessionService: f.sessions, SessionID: f.req.Run.SessionID.String(), UserID: f.req.Principal.Subject, AppName: "astonish"})
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.ChatAgent, runtime.Cleanup
				} else {
					prepare := f.cfg.Prepare
					f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
						safe, err := prepare(ctx, req, scope)
						if err == nil {
							safe.ChatConfig, safe.Catalog = cfg, &resources
						}
						return safe, err
					}
					runtime, err := f.construct(t)
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.Agent, runtime.Cleanup
				}
				t.Cleanup(cleanup)
				if client.calls != 0 || calls.Load() != 0 || len(backend.vault) != 0 {
					t.Fatal("constructor eagerly accessed transport or credentials")
				}
				invoke := func(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
					t.Helper()
					entry := chat.ToolIndex.GetToolEntry(name)
					if entry == nil {
						t.Fatalf("missing configured capability %s", name)
					}
					if _, ok := entry.Tool.(*chatRuntimeTool); !ok {
						t.Fatalf("%s bypassed shared wrapper: %T", name, entry.Tool)
					}
					return entry.Tool.(chatRuntimeRunnable).Run(runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: ctx}, sid: f.req.Run.SessionID.String()}}}, args)
				}
				args := map[string]any{"url": srv.URL, "credential": "owner"}
				out, err := invoke(f.ctx, "http_request", args)
				encoded, _ := json.Marshal(out)
				if err != nil || calls.Load() != 1 || !strings.Contains(string(encoded), "configured HTTP response") {
					t.Fatalf("HTTP capability: %s %v calls=%d", encoded, err, calls.Load())
				}
				out, err = invoke(f.ctx, "run_flow", map[string]any{"flow_name": "configured-flow"})
				if err != nil || out["status"] != "completed" || out["output"] != "configured flow completed" || calls.Load() != 2 || fs.calls != 1 || foreignFlow.calls != 0 {
					t.Fatalf("flow capability: %v %v calls=%d selected=%d foreign=%d", out, err, calls.Load(), fs.calls, foreignFlow.calls)
				}
				if client.calls != 2 || backend.owner != f.req.Run.SessionID.String() || strings.Contains(string(backend.vault), "wrong-scope-secret") {
					t.Fatal("nested execution escaped session/credential scope or replayed")
				}
				cancelled, cancel := context.WithCancel(f.ctx)
				cancel()
				foreign := f.req.Principal
				foreign.TeamSlug = "foreign-team"
				foreignCtx, err := execution.WithPrincipal(t.Context(), foreign)
				if err != nil {
					t.Fatal(err)
				}
				for _, ctx := range []context.Context{cancelled, foreignCtx} {
					for _, name := range []string{"http_request", "run_flow"} {
						arguments := args
						if name == "run_flow" {
							arguments = map[string]any{"flow_name": "configured-flow"}
						}
						if _, err := invoke(ctx, name, arguments); err == nil || calls.Load() != 2 || client.calls != 2 || fs.calls != 1 {
							t.Fatalf("cancelled/foreign %s dispatched: %v", name, err)
						}
					}
				}
				if mode != ordinary {
					f.req.Run.Lease.Fence++
					if _, err := invoke(f.ctx, "http_request", args); err == nil || client.calls != 2 {
						t.Fatalf("stale lease dispatched: %v", err)
					}
				}
				cleanup()
				cleanup()
				if mode != ordinary && (f.safeCleaned != 1 || f.scopeCleaned != 1) {
					t.Fatal("attempt cleanup was not exactly once")
				}
			})
		}
	}
}

type runtimeTestToolContext struct {
	tool.Context
	lifetime context.Context
}

func (c runtimeTestToolContext) SessionID() string                                    { return "runtime-test-session" }
func (c runtimeTestToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c runtimeTestToolContext) Value(key any) any                                    { return c.lifetime.Value(key) }
func (c runtimeTestToolContext) Deadline() (time.Time, bool)                          { return c.lifetime.Deadline() }
func (c runtimeTestToolContext) Done() <-chan struct{}                                { return c.lifetime.Done() }
func (c runtimeTestToolContext) Err() error                                           { return c.lifetime.Err() }

var errRuntimeTestDenied = errors.New("policy denied")

type runtimeTestPolicy struct {
	deny     bool
	observed int
}

func (p *runtimeTestPolicy) Authorize(context.Context, string, any) error {
	if p.deny {
		return errRuntimeTestDenied
	}
	return nil
}
func (p *runtimeTestPolicy) Observe(context.Context, string, any, map[string]any, error) {
	p.observed++
}

func TestChatRuntimeResolvedComposition(t *testing.T) {
	cfg := &config.AppConfig{}
	enabled := true
	cfg.Security.SecretScanner.Enabled = &enabled
	cfg.Security.SecretScanner.EntropyThreshold = 6.25
	cfg.Security.SecretScanner.MinTokenLength = 27
	mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
	runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{
		Config: &ChatFactoryConfig{AppConfig: cfg}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), ContextWindow: 8192,
		Catalog: &ChatCatalog{Groups: map[string]*agent.ToolGroup{}, SubAgentManager: mgr},
		Prompt:  &agent.SystemPromptBuilder{WorkspaceDir: "/explicit-workspace", WebSearchAvailable: true, WebSearchToolName: "web-search", WebExtractAvailable: true, WebExtractToolName: "web-extract"},
	}, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	scanner := runtime.ChatAgent.PendingSecrets.Scanner
	if scanner == nil || scanner.EntropyThreshold != 6.25 || scanner.MinTokenLength != 27 {
		t.Error("shared constructor lost configured secret-scanner settings")
	}
	if mgr.WebSearchToolName != "web_search" || mgr.WebExtractToolName != "web_extract" {
		t.Error("shared constructor lost delegated MCP name normalization")
	}
	if runtime.Compactor == nil || mgr.Compactor == nil || runtime.Compactor == mgr.Compactor {
		t.Error("missing isolated parent/child compaction")
	}
}

func TestChatRuntimeCodeAuthorizationAndDelegation(t *testing.T) {
	for _, autoApprove := range []bool{false, true} {
		mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
		child := ""
		runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{
			Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}, CodeMode: true, AutoApprove: autoApprove, WorkspaceDir: t.TempDir()},
			LLM:    provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), ContextWindow: 8192,
			Catalog:        &ChatCatalog{Groups: map[string]*agent.ToolGroup{}, SubAgentManager: mgr},
			OnChildSession: func(parent, id string) { child = parent + "/" + id },
		}, ChatRuntimeHooks{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(runtime.Cleanup)
		if runtime.ChatAgent.EnforceAuthorization == autoApprove || !mgr.Config.CodeMode {
			t.Fatal("Code authorization mode not preserved")
		}
		if !autoApprove {
			if mgr.AuthorizationGate == nil || mgr.AuthorizationGate(agent.SubAgentAuthRequest{}).Granted {
				t.Fatal("missing deny-by-default child authorization")
			}
		} else if mgr.AuthorizationGate != nil {
			t.Fatal("auto-approved Code unexpectedly gated")
		}
		mgr.OnChildSession("parent", "child")
		if child != "parent/child" {
			t.Fatal("child alias callback not supplied")
		}
		mgr.FileArtifactCapture("/workspace/result.md", "write_file")
		if len(runtime.ChatAgent.DrainFiles()) != 1 {
			t.Fatal("child artifact not forwarded")
		}
		if runtime.Compactor.StrategyName() != "code" || mgr.Compactor.StrategyName() != "code" {
			t.Fatal("Code compaction mode not preserved")
		}
	}
}

func TestChatRuntimeFlowRunnerBinding(t *testing.T) {
	policy := &runtimeTestPolicy{}
	calls := 0
	fixture, err := functiontool.New(functiontool.Config{Name: "flow_fixture", Description: "fixture"}, func(tool.Context, struct{}) (map[string]any, error) {
		return map[string]any{"executed": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{
		Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(),
		Catalog: &ChatCatalog{Groups: map[string]*agent.ToolGroup{"fixture": {Name: "fixture", Tools: []tool.Tool{fixture}}}},
		FlowRunner: func(ctx context.Context, yaml string, params map[string]string) (*agent.DryRunExecResult, error) {
			calls++
			groups := agent.RequestMCPGroupsFromContext(ctx)
			if groups["fixture"] == nil {
				t.Fatal("flow lost bound executable catalog")
			}
			_, err := groups["fixture"].Tools[0].(chatRuntimeRunnable).Run(runtimeTestToolContext{lifetime: ctx}, map[string]any{})
			if err != nil {
				return nil, err
			}
			return &agent.DryRunExecResult{Success: true, Output: yaml + params["input"]}, nil
		},
	}, ChatRuntimeHooks{Authorization: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	got, err := runtime.ChatAgent.FlowRunner(t.Context(), "flow:", map[string]string{"input": "bound"})
	if err != nil || got.Output != "flow:bound" {
		t.Fatalf("flow callback: %v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = runtime.ChatAgent.FlowRunner(ctx, "cancelled", nil)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled flow dispatched: calls=%d err=%v", calls, err)
	}
	policy.deny = true
	_, err = runtime.ChatAgent.FlowRunner(t.Context(), "denied", nil)
	if !errors.Is(err, errRuntimeTestDenied) {
		t.Fatalf("nested flow tool bypassed policy: %v", err)
	}
}

func TestChatRuntimeUnscopedFlowPreservesRequestCatalog(t *testing.T) {
	requestGroup := &agent.ToolGroup{Name: "request_only"}
	runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{
		Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(),
		Catalog: &ChatCatalog{Groups: map[string]*agent.ToolGroup{}},
		FlowRunner: func(ctx context.Context, _ string, _ map[string]string) (*agent.DryRunExecResult, error) {
			if agent.RequestMCPGroupsFromContext(ctx)["request_only"] != requestGroup {
				t.Error("bootstrap catalog overwrote live request catalog")
			}
			return &agent.DryRunExecResult{Success: true}, nil
		},
	}, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	ctx := agent.WithRequestMCPGroups(t.Context(), map[string]*agent.ToolGroup{"request_only": requestGroup})
	if _, err := runtime.ChatAgent.FlowRunner(ctx, "fixture", nil); err != nil {
		t.Fatal(err)
	}
}

func TestChatFactoryDoesNotRecomposeRuntime(t *testing.T) {
	tree, err := parser.ParseFile(token.NewFileSet(), "chat_factory.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(tree, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			switch id.Name {
			case "wireChatDelegation", "wireChatCompactor", "buildChatFlowDistiller":
				t.Errorf("host bootstrap repeats shared composition: %s", id.Name)
			}
		}
		return true
	})
}

func TestChatRuntimeCleanupAndAdditiveHooks(t *testing.T) {
	var closed atomic.Int32
	process, err := tools.GetProcessTools()
	if err != nil {
		t.Fatal(err)
	}
	deps := ChatRuntimeDependencies{Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Catalog: &ChatCatalog{MainThreadTools: process, Groups: map[string]*agent.ToolGroup{}}, Sessions: session.InMemoryService(), Cleanup: func() { closed.Add(1) }}
	if _, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{AdditionalTools: process[:1]}); err == nil {
		t.Fatal("hook replaced ordinary capability")
	}
	if closed.Load() != 1 {
		t.Fatal("failed construction leaked owned resources")
	}
	runtime, err := NewScopedChatRuntime(t.Context(), deps, ChatRuntimeHooks{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Cleanup()
	runtime.Cleanup()
	if closed.Load() != 2 {
		t.Fatal("cleanup not exactly once")
	}
	if runtime.ChatAgent.Redactor == nil || runtime.ChatAgent.PendingSecrets == nil || runtime.ChatAgent.ToolIndex == nil || runtime.ChatAgent.FlowDistiller == nil {
		t.Fatal("incomplete shared composition")
	}
}

func TestChatRuntimeRejectsUnwrappableCapability(t *testing.T) {
	inert := runtimeInertTool{}
	_, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{
		Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(),
		Catalog: &ChatCatalog{MainThreadTools: []tool.Tool{inert}, Groups: map[string]*agent.ToolGroup{}},
	}, ChatRuntimeHooks{Authorization: &runtimeTestPolicy{}})
	if err == nil {
		t.Fatal("policy silently bypassed for unwrappable tool")
	}
}

type runtimeInertTool struct{}

func (runtimeInertTool) Name() string        { return "inert" }
func (runtimeInertTool) Description() string { return "not a runnable tool" }
func (runtimeInertTool) IsLongRunning() bool { return false }

func TestChatRuntimeCancelledInvocationDoesNotDispatch(t *testing.T) {
	fixture, err := functiontool.New(functiontool.Config{Name: "cancel_fixture", Description: "fixture"}, func(tool.Context, struct{}) (map[string]any, error) {
		t.Error("cancelled invocation dispatched")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), Catalog: &ChatCatalog{MainThreadTools: []tool.Tool{fixture}, Groups: map[string]*agent.ToolGroup{}}}, ChatRuntimeHooks{Authorization: &runtimeTestPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = runtime.ChatAgent.Tools[0].(interface {
		Run(tool.Context, any) (map[string]any, error)
	}).Run(runtimeTestToolContext{lifetime: ctx}, map[string]any{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

// TestBotEnhancedChatBaseline characterizes the ordinary supplied Chat core.
// It does not claim full platform catalog or physical sandbox parity.
func TestBotEnhancedChatBaseline(t *testing.T) {
	t.Run("ordinary_chat_capability_identity", testOrdinaryChatCapabilityIdentity)
	t.Run("supplied_dependencies_no_fallback", TestSuppliedChatDependenciesNoFallback)
	t.Run("delegation_events_and_compaction", TestSharedChatDelegationAndCompactionBehavior)
	t.Run("construction_recipes", TestChatConstructionSharedByAllRecipes)
}

func testOrdinaryChatCapabilityIdentity(t *testing.T) {
	// Real ordinary Chat tools, not substitutes with the same names. Construction
	// must retain their identity without executing any process operation.
	processTools, err := tools.GetProcessTools()
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	prompt := &agent.SystemPromptBuilder{WorkspaceDir: t.TempDir()}
	chat := newSuppliedChatAgent(suppliedChatDependencies{Sessions: sessions, Prompt: prompt, Tools: processTools})
	if chat.SessionService != sessions || chat.SystemPrompt != prompt || chat.AutoApprove || chat.BeginModelPreparation != nil || chat.EndModelPreparation != nil {
		t.Fatal("ordinary Chat session/prompt/approval identity changed or Bot-only hooks installed")
	}
	if len(chat.Tools) != len(processTools) || len(chat.Toolsets) != 0 {
		t.Fatalf("construction substituted capabilities: %d tools, %d toolsets", len(chat.Tools), len(chat.Toolsets))
	}
	for i, supplied := range processTools {
		if chat.Tools[i] != supplied {
			t.Fatalf("ordinary tool implementation replaced: %s", supplied.Name())
		}
	}
	created, err := sessions.Create(t.Context(), &session.CreateRequest{AppName: "astonish_chat", UserID: "baseline-owner", SessionID: "baseline-chat"})
	if err != nil {
		t.Fatal(err)
	}
	event := &session.Event{ID: "baseline-event", Author: "model", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("ordinary Chat output", "model")}}
	if err := chat.SessionService.AppendEvent(t.Context(), created.Session, event); err != nil {
		t.Fatal(err)
	}
	restored, err := sessions.Get(t.Context(), &session.GetRequest{AppName: "astonish_chat", UserID: "baseline-owner", SessionID: "baseline-chat"})
	if err != nil || restored.Session.Events().Len() != 1 || restored.Session.Events().At(0).ID != event.ID {
		t.Fatalf("ordinary Chat did not persist through the supplied session service: %v", err)
	}
}

// Both Bot recipes must construct through Chat's supplied-dependency core, not
// merely enter a Bot branch hidden in the ordinary factory's context.
func TestChatConstructionSharedByAllRecipes(t *testing.T) {
	for file, constructor := range map[string]string{
		"chat_factory.go":       "NewScopedChatRuntime",
		"bot_chat_factory.go":   "newBotScopedChatRuntime",
		"bot_shared_runtime.go": "NewScopedChatRuntime",
	} {
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		shared := false
		ast.Inspect(tree, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if id.Name == constructor {
					shared = true
				}
			}
			return true
		})
		if !shared {
			t.Errorf("%s does not use the shared Chat constructor", file)
		}
		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "NewWiredChatAgent" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Value" {
						t.Error("ordinary Chat entry point must not select a hidden context-injected constructor")
					}
				}
				return true
			})
		}
	}
}

func TestSuppliedChatDependenciesNoFallback(t *testing.T) {
	sessions := session.InMemoryService()
	redactor := credentials.NewRedactor()
	prompt := &agent.SystemPromptBuilder{WorkspaceDir: "/explicit-owner-home"}
	chat := newSuppliedChatAgent(suppliedChatDependencies{Sessions: sessions, Prompt: prompt, Redactor: redactor})
	if chat.SystemPrompt != prompt || chat.Redactor != redactor {
		t.Fatal("supplied dependencies replaced")
	}
	if len(chat.Tools) != 0 || chat.ToolIndex != nil || chat.Compactor != nil || chat.SubAgentManager != nil {
		t.Fatal("unsupplied capability installed")
	}
	if chat.PendingSecrets == nil {
		t.Fatal("missing ordinary Chat secret vault")
	}
	mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
	wireChatDelegation(chat, mgr, nil, sessions)
	if mgr.SessionService != sessions || mgr.PendingSecrets != chat.PendingSecrets || mgr.Redactor != redactor {
		t.Fatal("delegation lost scoped dependencies")
	}
	called := false
	cfg := &config.CompactionConfig{}
	c := wireChatCompactor(chat, 8192, cfg, false, false, func(context.Context, string) (string, error) { called = true; return "", nil })
	if called {
		t.Fatal("construction invoked model")
	}
	if c.Threshold != cfg.GetThreshold() || c.PreserveRecent != cfg.GetPreserveRecent() {
		t.Fatal("Chat compaction defaults changed")
	}
	if mgr.Compactor == nil || mgr.Compactor == c {
		t.Fatal("child compactor must be isolated")
	}
	index, err := newChatToolIndex(context.Background(), []tool.Tool{}, nil, nil, nil)
	if err != nil || index.Count() != 0 {
		t.Fatalf("catalog added unsupplied tools: %v", err)
	}
}

// Exercise the callbacks installed by construction, not just non-nil fields.
// Child compaction must use the supplied model without firing parent UI hooks
// or sharing its memoized summary; child events/files must reach that parent.
func TestSharedChatDelegationAndCompactionBehavior(t *testing.T) {
	for _, codeMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "code"}[codeMode], func(t *testing.T) {
			redactor := credentials.NewRedactor()
			redactor.AddSecret("owner", "owner-secret-value")
			resolver := credentials.NewStoreAdapter(nil)
			chat := newSuppliedChatAgent(suppliedChatDependencies{Redactor: redactor, Credentials: resolver})
			mgr := agent.NewSubAgentManager(agent.SubAgentConfig{})
			wireChatDelegation(chat, mgr, nil, nil)
			if mgr.CredentialStore != resolver || mgr.Redactor.Redact("owner-secret-value") != "[REDACTED]" {
				t.Fatal("delegation did not retain owner credential binding")
			}
			event := &session.Event{LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("child output", "model")}}
			var seen *session.Event
			chat.UIEventCallback = func(e *session.Event) { seen = e }
			mgr.EventForwarder("parent", event)
			if seen != event {
				t.Fatal("delegation event did not reach parent")
			}
			mgr.FileArtifactCapture("/workspace/report.md", "write_file")
			if files := chat.DrainFiles(); len(files) != 1 || files[0].Path != "/workspace/report.md" {
				t.Fatalf("delegation file not delivered: %+v", files)
			}
			calls, parentNotices := 0, 0
			parent := wireChatCompactor(chat, 8192, nil, false, codeMode, func(context.Context, string) (string, error) {
				calls++
				return "short summary", nil
			})
			parent.SetOnCompaction(func(int, int) { parentNotices++ })
			contents := make([]*genai.Content, 12)
			for i := range contents {
				contents[i] = genai.NewContentFromText(strings.Repeat("retained evidence ", 300), "model")
			}
			child := mgr.Compactor
			if child.StrategyName() != parent.StrategyName() {
				t.Fatal("child lost compaction mode")
			}
			if _, err := child.CompactContents(context.Background(), contents); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || parentNotices != 0 || parent.CompactionCount() != 0 {
				t.Fatalf("child shared parent compaction state: calls=%d notices=%d", calls, parentNotices)
			}
			if _, err := parent.CompactContents(context.Background(), contents); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || parentNotices != 1 {
				t.Fatalf("parent failed independent compaction: calls=%d notices=%d", calls, parentNotices)
			}
		})
	}
}
