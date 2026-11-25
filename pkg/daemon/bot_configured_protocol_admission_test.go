package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
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
	astonishmcp "github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/model"
)

// Transport-only fixture: the production lazy MCP adapter and node dispatcher
// execute against a real loopback MCP server. No replacement catalog tool,
// arbitrary host command, paid provider, or physical sandbox is involved.
type configuredProtocolPool struct {
	intentHTTPPool
	backend    sandbox.Backend
	endpoint   string
	mu         sync.Mutex
	sessions   []string
	operations []string
	closed     bool
}

func (p *configuredProtocolPool) GetBackend() sandbox.Backend               { return nil }
func (p *configuredProtocolPool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *configuredProtocolPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *configuredProtocolPool) Alias(alias, owner string) {}
func (p *configuredProtocolPool) Cleanup()                  { p.mu.Lock(); defer p.mu.Unlock(); p.closed = true }
func (p *configuredProtocolPool) Call(s, n string, a map[string]any) (json.RawMessage, error) {
	return p.CallContext(context.Background(), s, n, a)
}
func (p *configuredProtocolPool) CallContext(ctx context.Context, s, n string, a map[string]any) (json.RawMessage, error) {
	if s == "" || n != astonishmcp.NodeOperation {
		return nil, store.ErrBotForbidden
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var req astonishmcp.NodeRequest
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.Config.URL != p.endpoint || req.Config.Transport != "streamableHTTP" {
		return nil, store.ErrBotForbidden
	}
	p.mu.Lock()
	p.sessions = append(p.sessions, s)
	p.operations = append(p.operations, n)
	p.mu.Unlock()
	ctx = store.WithCredentialStore(ctx, sessioncreds.NewStore(nil))
	out, err := tools.ExecuteTool(ctx, n, a, s)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

const configuredProtocolTask = "Read the configured echo fixture, search for controlled public evidence, then list sandbox templates. Reply in chat only; do not switch templates, write files, or send messages."

// Ordinary Studio has a personal-only admission. Team rows use actual public Bot
// messages and scheduler admission, not an invented team Studio HTTP endpoint.
// Generic MCP annotations are not authority: the scheduled row must checkpoint
// the exact action, rather than silently granting a supposedly read-only tool.
func TestBotConfiguredProtocolAdmissionHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		modes := []string{"interactive", "scheduled-exact-boundary", "scheduled-mcp-exact-boundary", "scheduled-mcp-stale-config"}
		if scope == store.BotScopePersonal {
			modes = append([]string{"studio"}, modes...)
		}
		for _, mode := range modes {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				backend, owner := setupChannelResolverFixture(t, "admission", "team", "telegram", "linked")
				org, err := backend.ForOrg("admission")
				if err != nil {
					t.Fatal(err)
				}
				team, personal := org.ForTeam("team"), org.ForUser(owner)
				principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: "admission", TeamSlug: "team", Authenticated: true}
				auth, err := execution.WithPrincipal(t.Context(), principal)
				if err != nil {
					t.Fatal(err)
				}
				auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				repo, pin := team.Bots(), team.SetSessionPin
				if scope == store.BotScopePersonal {
					repo, pin = personal.Bots(), personal.SetSessionPin
				}
				var mcpCalls, searchCalls, prepared, cleaned atomic.Int32
				remote := sdk.NewServer(&sdk.Implementation{Name: "configured-admission", Version: "1"}, nil)
				sdk.AddTool(remote, &sdk.Tool{Name: "echo", Description: "Read controlled fixture"}, func(ctx context.Context, _ *sdk.CallToolRequest, a struct {
					Message string `json:"message"`
				}) (*sdk.CallToolResult, any, error) {
					if a.Message != "controlled-question" {
						return nil, nil, fmt.Errorf("unexpected fixture argument")
					}
					mcpCalls.Add(1)
					return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "configured-mcp-evidence"}}}, nil, nil
				})
				endpoint := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return remote }, nil))
				defer endpoint.Close()
				enabled, disabled := true, false
				platformMCP := backend.PlatformMCPServers()
				for _, standard := range config.GetStandardServers() {
					if standard.Kind != "model" {
						if err := platformMCP.Save(auth, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Save through the real scoped configuration store. Team inheritance is the
				// same for ordinary personal Chat and personal/team Bot construction.
				if err := team.MCPServers().Save(auth, &store.MCPServer{Name: "configured-fixture", Transport: "streamableHTTP", URL: endpoint.URL, Enabled: &enabled, CachedTools: json.RawMessage(`[{"name":"echo","description":"Read controlled fixture","inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}}]`)}); err != nil {
					t.Fatal(err)
				}
				search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v1/chat/completions" {
						http.NotFound(w, r)
						return
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					raw, _ := json.Marshal(body)
					if body["model"] != "sonar-fixture" || !strings.Contains(string(raw), "controlled public evidence") {
						t.Errorf("unexpected search request %s", raw)
					}
					searchCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"search-fixture","object":"chat.completion","model":"sonar-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"configured-search-evidence https://evidence.example/source"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21}}`)
				}))
				defer search.Close()
				if err := backend.PlatformSettings().Save(auth, &store.PlatformSettings{WebSearchTool: "perplexity:perplexity_web_search", PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "search-fixture", Model: "sonar-fixture"}, Providers: map[string]store.ProviderConfig{"search-fixture": {"type": "lm_studio", "base_url": search.URL + "/v1"}}}); err != nil {
					t.Fatal(err)
				}
				home := testRetainedContentBackend(t, backend)
				catalogBackend := &templateWiringBackend{}
				home.setupHomeBackend.Backend = catalogBackend
				pool := &configuredProtocolPool{backend: catalogBackend, endpoint: endpoint.URL}
				templates := sandbox.NewInMemoryRegistry(t.TempDir())
				if err := templates.Add(&sandbox.TemplateMeta{Name: "configured-template-evidence", CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				bundle := &launcher.ChatCatalogResources{Backend: catalogBackend, Pool: pool, TemplateRegistry: templates}
				factoryResult := &launcher.ChatFactoryResult{SharedResources: bundle, SandboxPool: pool}
				shared := productionBotChatResources(factoryResult)
				if shared == nil || shared.Pool != pool || shared.Backend != catalogBackend || shared.TemplateRegistry != templates {
					t.Fatal("production bundle identity lost")
				}
				llm := &configuredAdmissionModel{script: []configuredAdmissionCall{{"echo", map[string]any{"message": "controlled-question"}}, {"perplexity_web_search", map[string]any{"query": "controlled public evidence", "max_results": float64(2)}}, {"list_sandbox_templates", map[string]any{}}}}
				// Isolate each autonomous exact-approval boundary.
				if mode == "scheduled-exact-boundary" {
					llm.script = llm.script[1:2]
				} else if strings.HasPrefix(mode, "scheduled-mcp-") {
					llm.script = llm.script[:1]
				}
				turns := execution.NewBotTurnAuthorizationRegistry(8)
				defer turns.Close()
				svc := &store.Services{Mode: store.ModePlatform, Platform: backend, TenantRouter: backend, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), PlatformMCPServers: platformMCP, TeamMCPServers: team.MCPServers(), PlatformSettings: backend.PlatformSettings(), Settings: team.Settings(), PersonalSettings: personal.PersonalSettings(), BotTurnRegistry: turns, BotInteractiveReady: true}
				// No routine mandate enrichment. Interactive admission retains the owner's
				// verified caller ceiling; autonomous allowlists still need exact approvals.
				worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: home, Pool: pool, WorkerID: "protocol-" + mode, TurnRegistry: turns, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return llm, nil
				}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
					prepared.Add(1)
					defer func() {
						if v := recover(); v != nil {
							t.Errorf("prepare panic %v\n%s", v, debug.Stack())
							panic(v)
						}
					}()
					pool.Alias(q.Run.SessionID.String(), q.Bot.HomeSessionID.String())
					s.Services.BotFilesFactory = func(c context.Context, services *store.Services, requested store.BotScope) (store.BotFiles, error) {
						return productionBotFiles(c, home, services, requested)
					}
					llm.mu.Lock()
					llm.work = func() (*store.BotWorkState, error) { return repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease) }
					llm.mu.Unlock()
					local, err := launcher.BotScopedChatConfig(c, s)
					if err != nil {
						return nil, err
					}
					copy := *shared
					return &launcher.BotChatSafeResources{ChatConfig: local, ProviderFactory: provider.GetProvider, Catalog: &copy, WorkspaceDir: "/workspace", PolicyRuntime: productionBotPolicyRuntime(botLiveGuard{repo: repo, request: q}), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { cleaned.Add(1) }}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Stop()
				router := mux.NewRouter()
				router.HandleFunc("/studio/chat", api.StudioChatHandler).Methods("POST")
				router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
				router.HandleFunc("/bots/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler).Methods("POST")
				router.HandleFunc("/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler).Methods("POST")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, e := execution.WithPrincipal(r.Context(), principal)
					if e != nil {
						t.Error(e)
						return
					}
					c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
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
				post := func(path string, payload any, want int) []byte {
					t.Helper()
					raw, _ := json.Marshal(payload)
					r, e := http.NewRequestWithContext(t.Context(), "POST", server.URL+path, strings.NewReader(string(raw)))
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
						t.Fatalf("POST %s: %d %s %v", path, resp.StatusCode, out, e)
					}
					return out
				}
				if mode == "studio" {
					cm := api.GetChatManager()
					cm.Reset()
					api.SetLLMPool(nil)
					bootstrap := agent.NewChatAgent(llm, nil, nil, personal.Sessions(), &agent.SystemPromptBuilder{}, false, true)
					api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
						return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: personal.Sessions(), SandboxPool: pool, RuntimeFactory: func(c context.Context, r api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
							prepared.Add(1)
							wired, e := launcher.NewScopedChatRuntime(c, launcher.ChatRuntimeDependencies{Inputs: r.Inputs, PlanInitializer: r.PlanInitializer, Config: &launcher.ChatFactoryConfig{AppConfig: r.AppConfig, PlatformMode: true, AutoApprove: r.AutoApprove, WorkspaceDir: "/workspace"}, Resources: shared, LLM: r.LLM, Sessions: r.SessionService, SessionID: r.SessionID, UseScopedEngines: true, Cleanup: func() { cleaned.Add(1) }}, launcher.ChatRuntimeHooks{})
							if e != nil {
								return nil, e
							}
							return &api.StudioChatRuntime{ChatAgent: wired.ChatAgent, Cleanup: wired.Cleanup}, nil
						}}, nil
					})
					defer func() { cm.Reset(); api.SetStudioChatInitFunc(nil) }()
					raw := post("/studio/chat", map[string]any{"message": configuredProtocolTask, "autoApprove": true}, 200)
					if strings.Contains(string(raw), "event: error") {
						t.Fatalf("Studio: %s", raw)
					}
				} else {
					v := store.DefaultBotVersion()
					v.Charter = configuredProtocolTask
					v.Autonomy.AllowedTools = []string{"mcp:configured-fixture/echo", "perplexity_web_search", "list_sandbox_templates", "bot_work_update"}
					b, e := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Configured protocol " + mode, OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: mode})
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
						raw := post("/bots/"+b.ID.String()+"/messages?scope="+string(scope), map[string]any{"version": b.Version, "idempotency_key": mode, "instructions": configuredProtocolTask}, 202)
						var receipt store.BotMessageReceipt
						if e = json.Unmarshal(raw, &receipt); e != nil {
							t.Fatal(e)
						}
						run = receipt.Run
					} else {
						raw := admitConfiguredContinuousSchedule(t, auth, worker, repo, b, configuredProtocolTask)
						if e = json.Unmarshal(raw, &run); e != nil {
							t.Fatal(e)
						}
					}
					if e = worker.Poll(auth); e != nil {
						t.Fatal(e)
					}
					saved, e := repo.GetRun(auth, b.ID, run.ID)
					if e != nil {
						t.Fatal(e)
					}
					approvals, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
					if saved.Report.Failure != nil {
						t.Logf("failure detail: %+v", *saved.Report.Failure)
					}
					if e != nil {
						t.Fatal(e)
					}
					if strings.HasPrefix(mode, "scheduled-") {
						wantTool := "perplexity_web_search"
						if strings.HasPrefix(mode, "scheduled-mcp-") {
							wantTool = "mcp:configured-fixture/echo"
						}
						if saved.Status != store.RunWaitingApproval || len(approvals) != 1 || approvals[0].Status != store.ApprovalPending || approvals[0].Action.Tool != wantTool || mcpCalls.Load() != 0 || searchCalls.Load() != 0 {
							t.Fatalf("scheduled action escaped exact boundary: status=%s approvals=%+v mcp=%d search=%d report=%+v", saved.Status, approvals, mcpCalls.Load(), searchCalls.Load(), saved.Report)
						}
						checkpoint, e := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
						if e != nil || !checkpoint.Matches(approvals[0]) {
							t.Fatalf("exact checkpoint mismatch: %v", e)
						}
						if saved.Usage.ToolCalls < 1 || saved.Usage.InputTokens == 0 {
							t.Fatalf("bookkeeping/model budget not accumulated: %+v", saved.Usage)
						}
						if strings.HasPrefix(mode, "scheduled-mcp-") {
							approval := approvals[0]
							if approval.Action.Effect != execution.EffectUnknown || !strings.HasPrefix(approval.Action.TargetFingerprint, "mcp-config-v1:") || string(approval.Action.Arguments) != `{"message":"controlled-question"}` {
								t.Fatalf("MCP approval lost executable/config/args binding: %+v", approval.Action)
							}
							pool.mu.Lock()
							before := len(pool.operations)
							pool.mu.Unlock()
							if before != 0 {
								t.Fatal("pending MCP approval reached transport")
							}
							post("/bots/"+b.ID.String()+"/approvals/"+approval.ID.String()+"/decision?scope="+string(scope), map[string]any{"action_digest": approval.ActionDigest, "status": store.ApprovalApproved}, 200)
							if mode == "scheduled-mcp-stale-config" {
								if e = team.MCPServers().Save(auth, &store.MCPServer{Name: "configured-fixture", Transport: "streamableHTTP", URL: endpoint.URL + "/changed", Enabled: &enabled, CachedTools: json.RawMessage(`[{"name":"echo","description":"Read controlled fixture","inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}}]`)}); e != nil {
									t.Fatal(e)
								}
							}
							resumePath := "/bots/" + b.ID.String() + "/runs/" + run.ID.String() + "/resume?scope=" + string(scope)
							post(resumePath, map[string]any{"approval_id": approval.ID}, 202)
							orgRow, e := backend.Organizations().GetBySlug(t.Context(), "admission")
							if e != nil {
								t.Fatal(e)
							}
							if e = backend.Organizations().RemoveMember(t.Context(), owner, orgRow.ID); e != nil {
								t.Fatal(e)
							}
							if e = worker.Poll(auth); e != nil {
								t.Fatal(e)
							}
							if mcpCalls.Load() != 0 || prepared.Load() != 1 {
								t.Fatal("revoked owner reached MCP resume construction/transport")
							}
							if e = backend.Organizations().AddMember(t.Context(), owner, orgRow.ID, "member"); e != nil {
								t.Fatal(e)
							}
							approvals, e = repo.ListApprovals(auth, b.ID, store.BotPage{})
							if e != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalApproved {
								t.Fatalf("revoked membership consumed approval: %+v %v", approvals, e)
							}
							if e = worker.Poll(auth); e != nil {
								t.Fatal(e)
							}
							saved, e = repo.GetRun(auth, b.ID, run.ID)
							if e != nil {
								t.Fatal(e)
							}
							approvals, e = repo.ListApprovals(auth, b.ID, store.BotPage{})
							if mode == "scheduled-mcp-stale-config" {
								if e != nil || len(approvals) != 1 || (approvals[0].Status != store.ApprovalApproved && approvals[0].Status != store.ApprovalRevoked) || mcpCalls.Load() != 0 || searchCalls.Load() != 0 || saved.Status == store.RunSucceeded {
									t.Fatalf("stale MCP config consumed approval or executed: run=%+v approvals=%+v err=%v", saved, approvals, e)
								}
								pool.mu.Lock()
								operations := len(pool.operations)
								pool.mu.Unlock()
								if operations != 0 {
									t.Fatal("stale MCP config reached node transport")
								}
								deadline := time.Now().Add(time.Second)
								for cleaned.Load() != prepared.Load() && time.Now().Before(deadline) {
									time.Sleep(time.Millisecond)
								}
								if prepared.Load() != 2 || cleaned.Load() != 2 {
									t.Fatalf("stale MCP attempt cleanup prepared=%d cleaned=%d", prepared.Load(), cleaned.Load())
								}
								return
							}
							if e != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalConsumed || mcpCalls.Load() != 1 || searchCalls.Load() != 0 || (saved.Status != store.RunSucceeded && saved.Status != store.RunPartial) {
								t.Fatalf("owner MCP resume: status=%s approvals=%+v mcp=%d search=%d report=%+v err=%v", saved.Status, approvals, mcpCalls.Load(), searchCalls.Load(), saved.Report, e)
							}
							post(resumePath, map[string]any{"approval_id": approval.ID}, 409)
							if e = worker.Poll(auth); e != nil {
								t.Fatal(e)
							}
							if mcpCalls.Load() != 1 {
								t.Fatal("approved MCP action replayed")
							}
						}
					} else if (saved.Status != store.RunSucceeded && !(saved.Status == store.RunPartial && saved.Report.Failure == nil && saved.Report.EffectState == store.BotReportEffectsUncertain && len(saved.Report.Unverified) > 0)) || len(approvals) != 0 || saved.Usage.ToolCalls < 4 || saved.Usage.InputTokens < 12 || saved.Usage.OutputTokens < 9 {
						// Adapter responses prove execution, not independently verified effects.
						// The production report must retain that uncertainty; this fixture must
						// not force RunSucceeded or promote returned evidence to verified work.
						t.Fatalf("interactive execution: status=%s usage=%+v approvals=%+v report=%+v responses=%s", saved.Status, saved.Usage, approvals, saved.Report, strings.Join(llm.responses, "\n"))
					}
				}
				deadline := time.Now().Add(time.Second)
				for cleaned.Load() != prepared.Load() && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				wantAttempts := int32(1)
				if strings.HasPrefix(mode, "scheduled-mcp-") {
					wantAttempts = 2
				}
				if prepared.Load() != wantAttempts || cleaned.Load() != wantAttempts {
					t.Fatalf("attempt cleanup prepared=%d cleaned=%d", prepared.Load(), cleaned.Load())
				}
				pool.mu.Lock()
				defer pool.mu.Unlock()
				if pool.closed {
					t.Fatal("borrowed node pool closed")
				}
				for _, sid := range pool.sessions {
					if sid != pool.sessions[0] {
						t.Fatalf("MCP node identity drift: %v", pool.sessions)
					}
				}
				if mode == "scheduled-exact-boundary" {
					if len(pool.operations) != 0 {
						t.Fatal("pending approval reached node transport")
					}
					return
				}
				if strings.HasPrefix(mode, "scheduled-mcp-") {
					if len(pool.operations) != 1 {
						t.Fatalf("MCP dispatch count %d", len(pool.operations))
					}
					llm.mu.Lock()
					responses := strings.Join(llm.responses, "\n")
					llm.mu.Unlock()
					if !strings.Contains(responses, "configured-mcp-evidence") {
						t.Fatalf("resumed MCP result missing: %s", responses)
					}
					return
				}
				if mcpCalls.Load() != 1 || searchCalls.Load() != 1 {
					t.Fatalf("configured adapters calls mcp=%d search=%d", mcpCalls.Load(), searchCalls.Load())
				}
				llm.mu.Lock()
				defer llm.mu.Unlock()
				responses := strings.Join(llm.responses, "\n")
				for _, want := range []string{"configured-mcp-evidence", "configured-search-evidence", "https://evidence.example/source", "configured-template-evidence"} {
					if !strings.Contains(responses, want) {
						t.Fatalf("actual result missing %s: %s", want, responses)
					}
				}
				if llm.step != len(llm.script) {
					t.Fatalf("script incomplete %d", llm.step)
				}
			})
		}
	}
}
