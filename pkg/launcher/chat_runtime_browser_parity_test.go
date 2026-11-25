package launcher

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"golang.org/x/net/websocket"
	"google.golang.org/adk/tool"
)

// Only the process and CDP peer are simulated. Production backend browser wiring,
// shared browser_pdf, progressive dispatch and artifact callbacks remain intact.
// This deliberately does NOT prove a physical browser, HTTP download or scheduler.
type browserParityBackend struct {
	sandbox.Backend
	principal               execution.Principal
	session, address        string
	services                *store.Services
	personal                bool
	data                    []byte
	starts, tunnels, pushes atomic.Int32
	fail                    bool
}

func (*browserParityBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *browserParityBackend) PushFile(ctx context.Context, id, path string, data io.Reader, mode os.FileMode) error {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Subject != b.principal.Subject || p.OrgSlug != b.principal.OrgSlug || p.TeamSlug != b.principal.TeamSlug || id != b.session || path != "/workspace/proof.pdf" || mode != 0600 || store.FromContext(ctx) == nil {
		return fmt.Errorf("artifact escaped owner/session/path")
	}
	// Scoped services must retain the selected repository, not an ambient store.
	services := store.FromContext(ctx)
	if services.Bots != b.services.Bots || (b.personal && services.PersonalBots != b.services.PersonalBots) || (!b.personal && services.PersonalBots != nil) {
		return fmt.Errorf("artifact escaped services")
	}
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("unbounded artifact write")
	}
	b.pushes.Add(1)
	if b.fail {
		return fmt.Errorf("fixture artifact unavailable")
	}
	var err error
	b.data, err = io.ReadAll(data)
	return err
}
func (b *browserParityBackend) ExecStreaming(_ context.Context, id string, spec sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	if id != b.session || len(spec.Command) != 4 || spec.Command[0] != "/usr/local/bin/astonish-shell" {
		return nil, fmt.Errorf("browser transport escaped session: %s", id)
	}
	if strings.HasPrefix(spec.Command[3], "exec socat STDIO TCP:127.0.0.1:") {
		b.tunnels.Add(1)
		c, err := net.DialTimeout("tcp", b.address, time.Second)
		if err != nil {
			return nil, err
		}
		return &browserParityStream{ReadWriteCloser: c}, nil
	}
	if !strings.HasPrefix(spec.Command[3], "eval ") {
		return nil, fmt.Errorf("unexpected browser command")
	}
	b.starts.Add(1)
	return &browserParityStream{ReadWriteCloser: &browserParityLaunch{Reader: strings.NewReader("DONE: browser ready\n")}}, nil
}

type browserParityStream struct{ io.ReadWriteCloser }

func (*browserParityStream) Resize(int, int) error { return nil }
func (*browserParityStream) Wait() (int, error)    { return 0, nil }

type browserParityLaunch struct{ io.Reader }

func (*browserParityLaunch) Write(p []byte) (int, error) { return len(p), nil }
func (*browserParityLaunch) Close() error                { return nil }

type browserParityNode struct {
	sandbox.ToolNodeClient
	ready atomic.Int32
}

func (n *browserParityNode) EnsureReady(string) error { n.ready.Add(1); return nil }
func (*browserParityNode) BindSession(string)         {}

type browserParityPool struct {
	productionCapabilityPool
	session   string
	principal execution.Principal
	scoped    atomic.Int32
}

func (p *browserParityPool) SetSessionScope(id, org, team string) {
	if id == p.session && org == p.principal.OrgSlug && team == p.principal.TeamSlug {
		p.scoped.Add(1)
	}
}

func browserParityCDP(t *testing.T, payload []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	printed := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_ = json.NewEncoder(w).Encode(map[string]any{"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/parity"})
			return
		}
		websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(c *websocket.Conn) {
			defer c.Close()
			reads := 0
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
					printed.Add(1)
					reads = 0
					result = map[string]any{"stream": "pdf"}
				case "IO.read":
					// Two base64 chunks include binary bytes, not merely a result marker.
					part := payload[:len(payload)/2]
					if reads > 0 {
						part = payload[len(payload)/2:]
					}
					result = map[string]any{"data": base64.StdEncoding.EncodeToString(part), "base64Encoded": true, "eof": reads > 0}
					reads++
				}
				if websocket.JSON.Send(c, map[string]any{"id": msg.ID, "sessionId": msg.SessionID, "result": result}) != nil {
					return
				}
			}
		}}.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, printed
}

func TestChatRuntimeBrowserArtifactModeParity(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("HOME", root)
				t.Setenv("XDG_CONFIG_HOME", root)
				f := newBotFactoryFixture(t)
				f.req.Bot.Scope = store.BotScope(scope)
				membership := newLiveMembershipFixture(t, &servicesParityRuntime{f: f}, scope)
				if mode == "ordinary-chat" {
					f.services.Bots = nil
					f.services.PersonalBots = nil
				}
				args := map[string]any{"path": "proof.pdf"}
				guard := &drillGuard{consumed: map[uuid.UUID]bool{}}
				if mode == "autonomous-bot" {
					drillGrants(t, f, map[string][]map[string]any{"browser_pdf": {args}})
				}
				if mode == "interactive-bot" {
					setBotSharedInteractive(f)
					registry := execution.NewBotTurnAuthorizationRegistry(1)
					t.Cleanup(registry.Close)
					ctx, err := execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					id := execution.BotRunIdentity{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Version.Version, ActorID: f.req.Principal.Subject, OrgSlug: f.req.Principal.OrgSlug, TeamSlug: f.req.Principal.TeamSlug}
					original, err := registry.Register(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					ceiling := original.CallerCeiling()
					f.req.Run.CallerCeiling = &ceiling
					f.req.Authorization, err = registry.Authorizer(id, ceiling, membership.current)
					if err != nil {
						t.Fatal(err)
					}
				}
				payload := []byte("%PDF-1.7\n" + string(scope) + "/" + mode + "\n\x00\xff\x80\n%%EOF")
				peer, printed := browserParityCDP(t, payload)
				backend := &browserParityBackend{principal: f.req.Principal, session: f.req.Run.SessionID.String(), address: peer.Listener.Addr().String(), services: f.services, personal: scope == chatruntime.ResourceScopePersonal}
				node := &browserParityNode{}
				pool := &browserParityPool{productionCapabilityPool: productionCapabilityPool{backend: backend, client: node}, session: backend.session, principal: f.req.Principal}
				cfg := &config.AppConfig{}
				resources := ChatCatalogResources{Backend: backend, Pool: pool}
				var chat *agent.ChatAgent
				var cleanup func()
				if mode == "ordinary-chat" {
					inputs, err := chatruntime.Build(f.ctx, chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: scope, AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = inputs.Close() })
					runtime, err := NewScopedChatRuntime(f.ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg}, LLM: provider.NewPlaceholderLLM(), Sessions: f.sessions, SessionID: backend.session, Resources: &resources}, ChatRuntimeHooks{})
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.ChatAgent, runtime.Cleanup
				} else {
					prepare := f.cfg.Prepare
					f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, s *BotChatScope) (*BotChatSafeResources, error) {
						safe, err := prepare(ctx, req, s)
						if err == nil {
							safe.Catalog = &resources
							safe.ChatConfig = cfg
							safe.PolicyRuntime = agent.BotPolicyRuntime{Guard: guard, Classifier: BotCatalogueClassifier{}}
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
				entry := chat.ToolIndex.GetToolEntry("browser_pdf")
				if entry == nil {
					t.Fatal("browser_pdf missing")
				}
				wrapped, ok := entry.Tool.(*chatRuntimeTool)
				if !ok {
					t.Fatalf("nonshared tool %T", entry.Tool)
				}
				// A real functiontool must remain inside the shared wrapper, not the legacy
				// Bot PDF replacement, pinned browser adapter or a synthetic result tool.
				if !strings.Contains(reflect.TypeOf(wrapped.chatRuntimeRunnable).String(), "functiontool") {
					t.Fatalf("replacement browser executable: %T", wrapped.chatRuntimeRunnable)
				}
				if resources.BrowserManager != nil {
					t.Fatal("constructor mutated borrowed manager")
				}
				bridge, err := fixedProviderTools(chat.ToolIndex)
				if err != nil {
					t.Fatal(err)
				}
				var describe, execute botRunnableTool
				for _, candidate := range bridge {
					switch candidate.Name() {
					case "describe_tools":
						describe = candidate.(botRunnableTool)
					case "execute_tool":
						execute = candidate.(botRunnableTool)
					}
				}
				if describe == nil || execute == nil {
					t.Fatal("progressive bridge missing")
				}
				toolCtx := func(ctx context.Context) tool.Context {
					return servicesParityContext{Context: runtimeCapabilityToolContext{sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: ctx}, sid: backend.session}}}, owner: f.req.Principal.Subject, sid: backend.session}
				}
				out, err := describe.Run(toolCtx(f.ctx), map[string]any{"names": []string{"browser_pdf"}})
				raw, _ := json.Marshal(out)
				if err != nil || !strings.Contains(string(raw), "browser_pdf") {
					t.Fatalf("discover %s: %v", raw, err)
				}
				invoke := func(ctx context.Context, args map[string]any) (map[string]any, error) {
					return execute.Run(toolCtx(ctx), map[string]any{"name": "browser_pdf", "arguments": args})
				}
				noWork := func() {
					t.Helper()
					if backend.starts.Load() != 0 || backend.tunnels.Load() != 0 || backend.pushes.Load() != 0 || printed.Load() != 0 || node.ready.Load() != 0 || len(guard.consumed) != 0 {
						t.Fatal("denied/discovery call started browser or consumed fresh grant")
					}
				}
				noWork()
				membership.revoke()
				if _, err := membership.repo.Get(f.ctx, f.req.Bot.ID); err == nil {
					t.Fatal("membership revocation ineffective")
				}
				if _, err := invoke(f.ctx, args); err == nil {
					t.Fatal("revoked discovered browser action executed")
				}
				noWork()
				membership.restore()
				foreign := f.req.Principal
				foreign.TeamSlug = "foreign-team"
				foreignCtx, err := execution.WithPrincipal(t.Context(), foreign)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := invoke(foreignCtx, args); err == nil {
					t.Fatal("foreign scope executed")
				}
				cancelled, cancel := context.WithCancel(f.ctx)
				cancel()
				if _, err := invoke(cancelled, args); err == nil {
					t.Fatal("cancelled call executed")
				}
				if mode != "ordinary-chat" {
					f.req.Run.Lease.Fence++
					if _, err := invoke(f.ctx, args); err == nil {
						t.Fatal("replaced lease executed")
					}
					f.req.Run.Lease.Fence--
				}
				if mode == "autonomous-bot" {
					if _, err := invoke(f.ctx, map[string]any{"path": "unreviewed.pdf"}); err == nil {
						t.Fatal("changed arguments borrowed exact grant")
					}
				}
				// A host absolute path is never an alternate artifact destination.
				if _, err := invoke(f.ctx, map[string]any{"path": root + "/host-fallback.pdf"}); err == nil {
					t.Fatal("host artifact path accepted")
				}
				if _, err := os.Stat(root + "/host-fallback.pdf"); !os.IsNotExist(err) {
					t.Fatalf("host artifact fallback: %v", err)
				}
				noWork()
				out, err = invoke(f.ctx, args)
				raw, _ = json.Marshal(out)
				var receipt struct {
					Path      string `json:"path"`
					SizeBytes int    `json:"size_bytes"`
				}
				if decodeErr := json.Unmarshal(raw, &receipt); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if receipt.Path != "/workspace/proof.pdf" || receipt.SizeBytes != len(payload) {
					t.Fatalf("incorrect artifact receipt %s", raw)
				}
				if err != nil || !strings.Contains(string(raw), "/workspace/proof.pdf") || !bytes.Equal(backend.data, payload) || backend.pushes.Load() != 1 || printed.Load() != 1 || backend.starts.Load() != 1 || backend.tunnels.Load() == 0 || pool.scoped.Load() == 0 {
					t.Fatalf("artifact result=%s err=%v bytes=%x starts=%d tunnels=%d pushes=%d prints=%d scope=%d", raw, err, backend.data, backend.starts.Load(), backend.tunnels.Load(), backend.pushes.Load(), printed.Load(), pool.scoped.Load())
				}
				if mode == "autonomous-bot" {
					if len(guard.consumed) != 1 {
						t.Fatal("exact grant not consumed")
					}
					if _, err := invoke(f.ctx, args); err == nil {
						t.Fatal("single-use grant replayed")
					}
					if printed.Load() != 1 || backend.pushes.Load() != 1 {
						t.Fatal("replay reached browser")
					}
				} else {
					backend.fail = true
					if _, err := invoke(f.ctx, args); err == nil {
						t.Fatal("failed publication reported success")
					}
					if !bytes.Equal(backend.data, payload) || backend.starts.Load() != 1 {
						t.Fatal("failed backend publication fell back or replaced bytes")
					}
				}
				cleanup()
				cleanup()
				if mode != "ordinary-chat" && (f.scopeCleaned != 1 || f.safeCleaned != 1) {
					t.Fatal("cleanup not exactly once")
				}
			})
		}
	}
}
