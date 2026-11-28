package daemon

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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	sandboxmock "github.com/SAP/astonish/pkg/sandbox/mock"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"golang.org/x/net/websocket"
	"google.golang.org/adk/model"
)

// Only process transport and the CDP peer are controlled. Production HTTP
// admission, scoped construction, browser_pdf, exact approval and file handlers
// are exercised. This is not physical Chrome, Docker, Kubernetes or Studio UI.
const configuredBrowserTask = "Capture the current fixture page as proof.pdf using browser_pdf. Reply in chat only."

type configuredBrowserBackend struct {
	sandbox.Backend
	home                           *regressionHomeBackend
	principal                      execution.Principal
	personal                       bool
	mu                             sync.Mutex
	session, address               string
	binding                        *sandbox.BotHomeBinding
	data                           []byte
	starts, tunnels, pushes, pulls atomic.Int32
}

func (*configuredBrowserBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *configuredBrowserBackend) PushFile(c context.Context, id, path string, r io.Reader, mode os.FileMode) error {
	p, ok := execution.PrincipalFromContext(c)
	if !ok || p.Subject != b.principal.Subject || p.OrgSlug != "admission" || p.TeamSlug != "team" || store.FromContext(c) == nil || path != "/workspace/proof.pdf" || mode != 0600 {
		return store.ErrBotForbidden
	}
	services := store.FromContext(c)
	if b.binding != nil {
		if services.Bots == nil || (b.personal && services.PersonalBots == nil) || (!b.personal && services.PersonalBots != nil) {
			return fmt.Errorf("browser artifact repository scope drift")
		}
	}
	if _, ok := c.Deadline(); !ok {
		return fmt.Errorf("unbounded artifact write")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if id != b.session {
		return fmt.Errorf("artifact session drift")
	}
	// Model prose cannot manufacture these bytes: this receipt follows the real
	// CDP stream and production PushFile callback. Retained transport is test-owned.
	if b.binding != nil {
		target := filepath.Join(b.home.documentsRoot, b.binding.Scope.BotID, "proof.pdf")
		if err := os.WriteFile(target, data, mode); err != nil {
			return err
		}
	}
	b.data = append([]byte(nil), data...)
	b.pushes.Add(1)
	return nil
}
func (*configuredBrowserBackend) ArtifactReadsConfined() bool { return true }
func (b *configuredBrowserBackend) PullArtifact(c context.Context, id, path string) (io.ReadCloser, error) {
	return b.PullFile(c, id, path)
}
func (*configuredBrowserBackend) SessionState(context.Context, string) (sandbox.SessionState, error) {
	return sandbox.SessionStateRunning, nil
}
func (b *configuredBrowserBackend) PullFile(c context.Context, id, path string) (io.ReadCloser, error) {
	p, ok := execution.PrincipalFromContext(c)
	b.mu.Lock()
	defer b.mu.Unlock()
	if !ok || p.Subject != b.principal.Subject || id != b.session || path != "/workspace/proof.pdf" || b.data == nil {
		return nil, store.ErrBotNotFound
	}
	b.pulls.Add(1)
	return io.NopCloser(bytes.NewReader(append([]byte(nil), b.data...))), nil
}
func (b *configuredBrowserBackend) ExecStreaming(_ context.Context, id string, s sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	b.mu.Lock()
	valid := id == b.session
	b.mu.Unlock()
	if !valid || len(s.Command) != 4 || s.Command[0] != "/usr/local/bin/astonish-shell" {
		return nil, fmt.Errorf("browser escaped session")
	}
	if strings.HasPrefix(s.Command[3], "exec socat STDIO TCP:127.0.0.1:") {
		b.tunnels.Add(1)
		c, e := net.DialTimeout("tcp", b.address, time.Second)
		if e != nil {
			return nil, e
		}
		return &configuredBrowserStream{c}, nil
	}
	if !strings.HasPrefix(s.Command[3], "eval ") {
		return nil, fmt.Errorf("unexpected browser process")
	}
	b.starts.Add(1)
	return &configuredBrowserStream{&configuredBrowserLaunch{strings.NewReader("DONE: browser ready\n")}}, nil
}

type configuredBrowserStream struct{ io.ReadWriteCloser }

func (*configuredBrowserStream) Resize(int, int) error { return nil }
func (*configuredBrowserStream) Wait() (int, error)    { return 0, nil }

type configuredBrowserLaunch struct{ io.Reader }

func (*configuredBrowserLaunch) Write(p []byte) (int, error) { return len(p), nil }
func (*configuredBrowserLaunch) Close() error                { return nil }

type configuredBrowserPool struct {
	configuredProtocolPool
	browser *configuredBrowserBackend
	ready   atomic.Int32
}

func (p *configuredBrowserPool) GetBackend() sandbox.Backend               { return p.browser }
func (p *configuredBrowserPool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *configuredBrowserPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *configuredBrowserPool) EnsureReady(id string) error {
	p.browser.mu.Lock()
	defer p.browser.mu.Unlock()
	if id != p.browser.session {
		return store.ErrBotForbidden
	}
	p.ready.Add(1)
	return nil
}

func configuredBrowserCDP(t *testing.T, payload []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	printed := &atomic.Int32{}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_ = json.NewEncoder(w).Encode(map[string]any{"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/fixture"})
			return
		}
		websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(c *websocket.Conn) {
			defer c.Close()
			reads := 0
			for {
				var m struct {
					ID        int    `json:"id"`
					Method    string `json:"method"`
					SessionID string `json:"sessionId"`
				}
				if websocket.JSON.Receive(c, &m) != nil {
					return
				}
				result := map[string]any{}
				target := map[string]any{"targetId": "page", "type": "page", "url": "https://fixture.invalid/", "attached": true}
				switch m.Method {
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
					part := payload[:len(payload)/2]
					if reads > 0 {
						part = payload[len(payload)/2:]
					}
					result = map[string]any{"data": base64.StdEncoding.EncodeToString(part), "base64Encoded": true, "eof": reads > 0}
					reads++
				}
				if websocket.JSON.Send(c, map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": result}) != nil {
					return
				}
			}
		}}.ServeHTTP(w, r)
	}))
	t.Cleanup(peer.Close)
	return peer, printed
}

func TestBotConfiguredBrowserAdmissionHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	enabled := true
	if e := config.SaveAppConfig(&config.AppConfig{Sandbox: config.SandboxConfig{Enabled: &enabled, Backend: "mock"}}); e != nil {
		t.Fatal(e)
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		modes := []string{"interactive", "scheduled-exact-resume", "scheduled-membership-drift"}
		if scope == store.BotScopePersonal {
			modes = append([]string{"studio"}, modes...)
		}
		for _, mode := range modes {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) { runConfiguredBrowserAdmission(t, scope, mode) })
		}
	}
}

func runConfiguredBrowserAdmission(t *testing.T, scope store.BotScope, mode string) {
	db, owner := setupChannelResolverFixture(t, "admission", "team", "telegram", "linked")
	org, e := db.ForOrg("admission")
	if e != nil {
		t.Fatal(e)
	}
	team, personal := org.ForTeam("team"), org.ForUser(owner)
	repo, pin := team.Bots(), team.SetSessionPin
	if scope == store.BotScopePersonal {
		repo, pin = personal.Bots(), personal.SetSessionPin
	}
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "admission", TeamSlug: "team", Authenticated: true}
	auth, e := execution.WithPrincipal(t.Context(), principal)
	if e != nil {
		t.Fatal(e)
	}
	auth, e = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	disabled := false
	for _, s := range config.GetStandardServers() {
		if s.Kind != "model" {
			if e := db.PlatformMCPServers().Save(auth, &store.MCPServer{Name: s.ID, Transport: "stdio", Enabled: &disabled}); e != nil {
				t.Fatal(e)
			}
		}
	}
	home := testRetainedContentBackend(t, db)
	payload := []byte("%PDF-1.7\n" + string(scope) + "/" + mode + "\n\x00\xff\x80\n%%EOF")
	peer, printed := configuredBrowserCDP(t, payload)
	transport := &configuredBrowserBackend{home: home, principal: principal, personal: scope == store.BotScopePersonal, address: peer.Listener.Addr().String()}
	home.setupHomeBackend.Backend = transport
	pool := &configuredBrowserPool{browser: transport}
	bundle := &launcher.ChatCatalogResources{Backend: transport, Pool: pool}
	shared := productionBotChatResources(&launcher.ChatFactoryResult{SharedResources: bundle, SandboxPool: pool})
	if shared == nil {
		t.Fatal("shared bundle rejected")
	}
	llm := &configuredAdmissionModel{script: []configuredAdmissionCall{{"browser_pdf", map[string]any{"path": "proof.pdf"}}}}
	registry := execution.NewBotTurnAuthorizationRegistry(8)
	defer registry.Close()
	var prepared, cleaned atomic.Int32
	svc := &store.Services{Mode: store.ModePlatform, Platform: db, TenantRouter: db, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), PlatformMCPServers: db.PlatformMCPServers(), BotTurnRegistry: registry, BotInteractiveReady: true}
	svc.BotFilesFactory = func(c context.Context, s *store.Services, requested store.BotScope) (store.BotFiles, error) {
		return productionBotFiles(c, home, s, requested)
	}
	worker, e := NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: home, Pool: pool, WorkerID: "browser-" + mode, TurnRegistry: registry, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return llm, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		prepared.Add(1)
		transport.mu.Lock()
		transport.session = q.Run.SessionID.String()
		transport.binding = &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "admission", TeamID: "team", UserID: owner, BotID: q.Bot.ID.String()}, Backend: sandbox.BackendKind(q.Bot.Home.Backend), Ref: q.Bot.Home.StorageKey}
		transport.mu.Unlock()
		s.Services.BotFilesFactory = svc.BotFilesFactory
		llm.mu.Lock()
		llm.work = func() (*store.BotWorkState, error) { return repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease) }
		llm.mu.Unlock()
		local, e := launcher.BotScopedChatConfig(c, s)
		if e != nil {
			return nil, e
		}
		copy := *shared
		return &launcher.BotChatSafeResources{ChatConfig: local, ProviderFactory: provider.GetProvider, Catalog: &copy, WorkspaceDir: "/workspace", PolicyRuntime: productionBotPolicyRuntime(botLiveGuard{repo: repo, request: q}), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { cleaned.Add(1) }}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer worker.Stop()
	router := mux.NewRouter()
	router.HandleFunc("/studio/chat", api.StudioChatHandler).Methods("POST")
	router.HandleFunc("/artifacts", api.StudioArtifactDownloadHandler).Methods("GET")
	router.HandleFunc("/artifacts/content", api.StudioArtifactContentHandler).Methods("GET")
	router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/files/download", api.BotFileDownloadHandler).Methods("GET")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := execution.WithPrincipal(r.Context(), principal)
		if e != nil {
			t.Error(e)
			return
		}
		c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Error(e)
			return
		}
		c = store.WithServices(c, svc)
		c = store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: "admission", TeamSlug: "team"})
		c = api.WithPlatformUser(c, &api.PlatformUser{ID: owner, OrgSlug: "admission", TeamSlug: "team", Role: "member"})
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	server.Client().Timeout = 30 * time.Second
	request := func(method, path string, payload any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(payload)
		r, e := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		out, e := io.ReadAll(resp.Body)
		if e != nil || resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s %v", method, path, resp.StatusCode, out, e)
		}
		return out
	}
	var sid, download string
	if mode == "studio" {
		sandbox.RegisterBackendFactory(sandbox.BackendKindMock, func(sandbox.BackendFactoryConfig) (sandbox.Backend, error) { return transport, nil })
		defer sandbox.RegisterBackendFactory(sandbox.BackendKindMock, func(sandbox.BackendFactoryConfig) (sandbox.Backend, error) { return sandboxmock.New(), nil })
		cm := api.GetChatManager()
		cm.Reset()
		api.SetLLMPool(nil)
		bootstrap := agent.NewChatAgent(llm, nil, nil, personal.Sessions(), &agent.SystemPromptBuilder{}, false, true)
		api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
			return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: personal.Sessions(), SandboxPool: pool, RuntimeFactory: func(c context.Context, r api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
				prepared.Add(1)
				transport.mu.Lock()
				transport.session = r.SessionID
				transport.mu.Unlock()
				wired, e := launcher.NewScopedChatRuntime(c, launcher.ChatRuntimeDependencies{Inputs: r.Inputs, PlanInitializer: r.PlanInitializer, Config: &launcher.ChatFactoryConfig{AppConfig: r.AppConfig, PlatformMode: true, AutoApprove: r.AutoApprove, WorkspaceDir: "/workspace"}, Resources: shared, LLM: r.LLM, Sessions: r.SessionService, SessionID: r.SessionID, UseScopedEngines: true, Cleanup: func() { cleaned.Add(1) }}, launcher.ChatRuntimeHooks{})
				if e != nil {
					return nil, e
				}
				return &api.StudioChatRuntime{ChatAgent: wired.ChatAgent, Cleanup: wired.Cleanup}, nil
			}}, nil
		})
		defer func() { cm.Reset(); api.SetStudioChatInitFunc(nil) }()
		out := request("POST", "/studio/chat", map[string]any{"message": configuredBrowserTask, "autoApprove": true}, 200)
		if strings.Contains(string(out), "event: error") {
			t.Fatalf("Studio: %s", out)
		}
		transport.mu.Lock()
		sid = transport.session
		transport.mu.Unlock()
		download = "/artifacts?path=%2Fworkspace%2Fproof.pdf&session=" + sid
	} else {
		v := store.DefaultBotVersion()
		v.Charter = configuredBrowserTask
		v.Autonomy.AllowedTools = []string{"browser_pdf", "bot_work_update"}
		b, e := repo.Create(auth, store.BotCreateRequest{Name: "Configured browser " + mode, OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: mode})
		if e != nil {
			t.Fatal(e)
		}
		b, e = worker.provisionHome(auth, repo, b.ID, b.Revision)
		if e != nil {
			t.Fatal(e)
		}
		if e = pin(auth, b.HomeSessionID.String(), "controlled", "model"); e != nil {
			t.Fatal(e)
		}
		var run store.BotRun
		if mode == "interactive" {
			var receipt store.BotMessageReceipt
			if e = json.Unmarshal(request("POST", "/bots/"+b.ID.String()+"/messages?scope="+string(scope), map[string]any{"version": b.Version, "idempotency_key": mode, "instructions": configuredBrowserTask}, 202), &receipt); e != nil {
				t.Fatal(e)
			}
			run = receipt.Run
		} else {
			if e = json.Unmarshal(admitConfiguredContinuousSchedule(t, auth, worker, repo, b, configuredBrowserTask), &run); e != nil {
				t.Fatal(e)
			}
		}
		if e = worker.Poll(auth); e != nil {
			t.Fatal(e)
		}
		if mode == "interactive" {
			configuredFixtureInteractiveActions(t, auth, worker, repo, b, run, scope, llm, llm.script, func(path string, body any, want int) []byte { return request("POST", path, body, want) })
		}
		if mode != "interactive" {
			saved, e := repo.GetRun(auth, b.ID, run.ID)
			if e != nil {
				t.Fatal(e)
			}
			all, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
			if e != nil {
				t.Fatal(e)
			}
			if saved.Status != store.RunWaitingApproval || len(all) != 1 || all[0].Status != store.ApprovalPending || all[0].Action.Tool != "browser_pdf" || string(all[0].Action.Arguments) != `{"path":"proof.pdf"}` || printed.Load() != 0 || transport.starts.Load() != 0 || transport.pushes.Load() != 0 {
				t.Fatalf("exact boundary escaped: run=%+v approvals=%+v starts=%d", saved, all, transport.starts.Load())
			}
			checkpoint, e := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
			if e != nil || !checkpoint.Matches(all[0]) {
				t.Fatalf("checkpoint mismatch %v", e)
			}
			path := "/bots/" + b.ID.String() + "/approvals/" + all[0].ID.String() + "/approve-and-continue?scope=" + string(scope)
			request("POST", path, map[string]any{"action_digest": "altered"}, 409)
			request("POST", path, map[string]any{"action_digest": all[0].ActionDigest, "arguments": map[string]any{"path": "replacement.pdf"}}, 400)
			request("POST", path, map[string]any{"action_digest": all[0].ActionDigest}, 202)
			if mode == "scheduled-membership-drift" {
				orgRow, e := db.Organizations().GetBySlug(t.Context(), "admission")
				if e != nil {
					t.Fatal(e)
				}
				if e = db.Organizations().RemoveMember(t.Context(), owner, orgRow.ID); e != nil {
					t.Fatal(e)
				}
				if e = worker.Poll(auth); e != nil {
					t.Fatal(e)
				}
				if printed.Load() != 0 || transport.starts.Load() != 0 || transport.pushes.Load() != 0 || prepared.Load() != 1 {
					t.Fatal("revoked owner reached browser consume")
				}
				if e = db.Organizations().AddMember(t.Context(), owner, orgRow.ID, "member"); e != nil {
					t.Fatal(e)
				}
				all, e = repo.ListApprovals(auth, b.ID, store.BotPage{})
				if e != nil || len(all) != 1 || all[0].Status != store.ApprovalApproved {
					t.Fatalf("revocation consumed approval: %+v %v", all, e)
				}
			}
			if e = worker.Poll(auth); e != nil {
				t.Fatal(e)
			}
			all, e = repo.ListApprovals(auth, b.ID, store.BotPage{})
			if e != nil || len(all) != 1 || all[0].Status != store.ApprovalConsumed {
				t.Fatalf("resume failed approvals=%+v %v", all, e)
			}
		}
		saved, e := repo.GetRun(auth, b.ID, run.ID)
		if e != nil {
			t.Fatal(e)
		}
		if saved.Status != store.RunPartial || len(saved.Report.Unverified) == 0 || saved.Report.EffectState != store.BotReportEffectsUncertain {
			t.Fatalf("typed artifact transport receipt was promoted to verified effects: %+v", saved.Report)
		}
		if saved.Usage.ToolCalls < 1 || saved.Usage.InputTokens == 0 {
			t.Fatalf("browser execution lost cumulative budget: %+v", saved.Usage)
		}
		if saved.Report.Failure != nil {
			t.Fatalf("browser run failure: %+v", saved.Report.Failure)
		}
		sid = run.SessionID.String()
		if transport.session != sid {
			t.Fatal("resumed browser changed session")
		}
		if e = worker.Poll(auth); e != nil {
			t.Fatal(e)
		}
		if printed.Load() != 1 || transport.pushes.Load() != 1 {
			t.Fatal("completed browser action replayed")
		}
		download = "/bots/" + b.ID.String() + "/files/download?scope=" + string(scope) + "&path=proof.pdf"
		wrong := store.BotScopePersonal
		if scope == wrong {
			wrong = store.BotScopeTeam
		}
		request("GET", "/bots/"+b.ID.String()+"/files/download?scope="+string(wrong)+"&path=proof.pdf", nil, 404)
	}
	if printed.Load() != 1 || transport.pushes.Load() != 1 || transport.starts.Load() != 1 || pool.ready.Load() < 1 {
		t.Fatalf("browser execution missing printed=%d pushes=%d starts=%d ready=%d", printed.Load(), transport.pushes.Load(), transport.starts.Load(), pool.ready.Load())
	}
	if out := request("GET", download, nil, 200); !bytes.Equal(out, payload) {
		t.Fatalf("download differs: %x want %x", out, payload)
	}
	if mode == "studio" {
		if out := request("GET", "/artifacts/content?path="+url.QueryEscape("/workspace/proof.pdf")+"&session="+sid, nil, 200); !bytes.Equal(out, payload) {
			t.Fatal("content route changed binary bytes")
		}
		request("GET", "/artifacts?path=%2Fworkspace%2Fproof.pdf&session="+uuid.NewString(), nil, 404)
	}
	// A test-owned host file must never become a platform artifact fallback.
	trap := filepath.Join(t.TempDir(), "host.pdf")
	if e = os.WriteFile(trap, payload, 0600); e != nil {
		t.Fatal(e)
	}
	request("GET", "/artifacts?path="+url.QueryEscape(trap)+"&session="+sid, nil, 404)
	llm.mu.Lock()
	responses := strings.Join(llm.responses, "\n")
	llm.mu.Unlock()
	if !strings.Contains(responses, "/workspace/proof.pdf") || !strings.Contains(responses, "size_bytes") {
		t.Fatalf("typed browser receipt missing: %s", responses)
	}
	deadline := time.Now().Add(time.Second)
	for cleaned.Load() != prepared.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if cleaned.Load() != prepared.Load() {
		t.Fatalf("cleanup prepared=%d cleaned=%d", prepared.Load(), cleaned.Load())
	}
	if printed.Load() != 1 || transport.starts.Load() != 1 || transport.pushes.Load() != 1 {
		t.Fatal("download triggered browser execution")
	}
	wantAttempts := int32(1)
	if strings.HasPrefix(mode, "scheduled-") || mode == "interactive" {
		wantAttempts = 2
	}
	if prepared.Load() != wantAttempts || cleaned.Load() != wantAttempts {
		t.Fatalf("attempt lifecycle prepared=%d cleaned=%d want=%d", prepared.Load(), cleaned.Load(), wantAttempts)
	}
	if pool.closed {
		t.Fatal("borrowed pool closed")
	}
}
