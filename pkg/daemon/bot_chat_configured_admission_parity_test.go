package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

const configuredAdmissionTask = "Write and read the fixture file, run the exact fixture shell command, and execute the configured admission-flow. Reply in chat only."
const configuredAdmissionBody = "durable controlled result"
const configuredAdmissionFlow = `description: Offline configured admission flow
nodes:
  - name: done
    type: output
    user_message: [configured admission flow completed]
flow:
  - from: START
    to: done
  - from: done
    to: END
`

type configuredAdmissionCall struct {
	name string
	args map[string]any
}

// This transport runs the real node dispatcher, not a second implementation of
// the tools. Its capability ceiling is exactly one test-owned path and
// one literal command; it cannot execute arbitrary model-provided host commands.
// It is not evidence of physical Docker/Kubernetes isolation.
type configuredAdmissionPool struct {
	intentHTTPPool
	mu            sync.Mutex
	path, command string
	readOnly      bool
	calls         []string
	sessions      []string
	results       []json.RawMessage
	aliases       map[string]string
}

func (p *configuredAdmissionPool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *configuredAdmissionPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *configuredAdmissionPool) Alias(alias, original string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.aliases == nil {
		p.aliases = map[string]string{}
	}
	p.aliases[alias] = original
}
func (p *configuredAdmissionPool) GetBackend() sandbox.Backend { return nil }
func (p *configuredAdmissionPool) Call(s, n string, a map[string]any) (json.RawMessage, error) {
	return p.CallContext(context.Background(), s, n, a)
}
func (p *configuredAdmissionPool) CallContext(ctx context.Context, s, n string, a map[string]any) (json.RawMessage, error) {
	if s == "" || p.readOnly && (n == "write_file" || n == "shell_command") {
		return nil, store.ErrBotForbidden
	}
	switch n {
	case "read_file", "write_file":
		if a["path"] != p.path {
			return nil, store.ErrBotForbidden
		}
		if n == "write_file" && a["content"] != configuredAdmissionBody {
			return nil, store.ErrBotForbidden
		}
	case "find_files":
		if a["search_path"] != filepath.Dir(p.path) || a["pattern"] != "result.txt" || a["recursive"] == true {
			return nil, store.ErrBotForbidden
		}
	case "file_tree":
		if a["path"] != filepath.Dir(p.path) || a["max_depth"] != float64(1) {
			return nil, store.ErrBotForbidden
		}
	case "shell_command":
		if a["command"] != p.command {
			return nil, store.ErrBotForbidden
		}
	default:
		return nil, store.ErrBotForbidden
	}
	result, err := tools.ExecuteTool(ctx, n, a, "configured-admission-fixture")
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	p.mu.Lock()
	defer p.mu.Unlock()
	if original := p.aliases[s]; original != "" {
		s = original
	}
	p.calls = append(p.calls, n)
	p.sessions = append(p.sessions, s)
	p.results = append(p.results, raw)
	return raw, err
}
func (p *configuredAdmissionPool) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.calls) }

// Every surface receives the same script. Bot-specific delivery bookkeeping is
// additive; generation and review remain the production ChatRunner/controller.
type configuredAdmissionModel struct {
	mu        sync.Mutex
	script    []configuredAdmissionCall
	step      int
	work      func() (*store.BotWorkState, error)
	delivery  bool
	prompts   []string
	responses []string
}

func (*configuredAdmissionModel) Name() string { return "configured-admission" }
func (m *configuredAdmissionModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		// Session naming is auxiliary, not another execution turn.
		if len(r.Tools) == 0 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Admission parity", "model"), TurnComplete: true}, nil)
			return
		}
		if r.Config != nil && r.Config.SystemInstruction != nil {
			raw, _ := json.Marshal(r.Config.SystemInstruction)
			m.prompts = append(m.prompts, string(raw))
		}
		for _, c := range r.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					raw, _ := json.Marshal(p.FunctionResponse)
					m.responses = append(m.responses, string(raw))
				}
			}
		}
		var call configuredAdmissionCall
		if m.work != nil && !m.delivery {
			w, err := m.work()
			if err != nil {
				yield(nil, err)
				return
			}
			m.delivery = true
			call = configuredAdmissionCall{"bot_work_update", map[string]any{"expected_revision": w.Revision, "kind": "set_delivery", "delivery_intent": string(store.BotDeliveryChatOnly)}}
		} else if m.step < len(m.script) {
			call = m.script[m.step]
			m.step++
		} else {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(configuredAdmissionBody, "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
			return
		}
		name, args := call.name, call.args
		if _, direct := r.Tools[name]; !direct {
			name, args = "execute_tool", map[string]any{"name": name, "arguments": args}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}}}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
	}
}

// Ordinary Studio admission is personal-only today. The team rows exercise both
// public Bot admissions, not a fabricated team Studio endpoint. Shared team Chat
// construction is covered by the scoped-runtime tests in launcher.
func TestBotChatConfiguredAdmissionParityHTTP(t *testing.T) {
	runConfiguredAdmissionParityHTTP(t, false)
}

func runConfiguredAdmissionParityHTTP(t *testing.T, continuous bool) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
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
			repo, pin, flows := team.Bots(), team.SetSessionPin, team.Flows()
			if scope == store.BotScopePersonal {
				repo, pin, flows = personal.Bots(), personal.SetSessionPin, personal.Flows()
			}
			if err := flows.SaveFlow(auth, "admission-flow", configuredAdmissionFlow); err != nil {
				t.Fatal(err)
			}
			// Explicitly disable network-backed standard MCP servers in this offline task.
			mcp := backend.PlatformMCPServers()
			disabled := false
			for _, standard := range config.GetStandardServers() {
				if standard.Kind != "model" {
					if err := mcp.Save(auth, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
						t.Fatal(err)
					}
				}
			}
			svc := &store.Services{Mode: store.ModePlatform, Platform: backend, TenantRouter: backend, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), Flows: team.Flows(), PersonalFlows: personal.Flows(), PlatformMCPServers: mcp}
			home := testRetainedContentBackend(t, backend)
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			defer registry.Close()
			policy := newProductionBotMandatePolicy(backend, &config.AppConfig{})
			svc.BotTurnRegistry = registry
			svc.BotInteractiveReady = true
			modes := []string{"interactive", "interactive-mandate", "autonomous"}
			if continuous {
				modes = []string{"interactive", "autonomous"}
			}
			if scope == store.BotScopePersonal {
				modes = append([]string{"studio"}, modes...)
			}
			for _, mode := range modes {
				t.Run(mode, func(t *testing.T) {
					// The caller-authorized route and production durable mandate are
					// distinct authority configurations, not interchangeable grants.
					svc.BotMandatePolicy = nil
					if mode == "interactive-mandate" || continuous {
						svc.BotMandatePolicy = policy
					}
					ownedPath := filepath.Join(t.TempDir(), "result.txt")
					command := "printf 'configured-shell-result'"
					pool := &configuredAdmissionPool{path: ownedPath, command: command, readOnly: continuous}
					llm := &configuredAdmissionModel{script: []configuredAdmissionCall{
						{"write_file", map[string]any{"path": ownedPath, "content": configuredAdmissionBody}},
						{"read_file", map[string]any{"path": ownedPath}},
						{"shell_command", map[string]any{"command": command, "timeout": float64(5)}},
						{"run_flow", map[string]any{"flow_name": "admission-flow", "parameters": map[string]any{}}},
					}}
					task := configuredAdmissionTask
					if continuous {
						task = configuredContinuousTask
						if err := os.WriteFile(ownedPath, []byte(configuredAdmissionBody), 0600); err != nil {
							t.Fatal(err)
						}
						llm.script = []configuredAdmissionCall{
							{"read_file", map[string]any{"path": ownedPath}},
							{"find_files", map[string]any{"search_path": filepath.Dir(ownedPath), "pattern": "result.txt", "recursive": false}},
							{"file_tree", map[string]any{"path": filepath.Dir(ownedPath), "max_depth": float64(1)}},
							{"read_file", map[string]any{"path": ownedPath}},
						}
					}
					if mode == "interactive-mandate" {
						// Negative authority probe: these shared capabilities are not in
						// the production routine mandate. Do not broaden it for parity.
						llm.script = llm.script[2:]
					}
					var prepared, cleaned atomic.Int32
					worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: home, Pool: workerTestPool{}, WorkerID: "admission-" + mode, TurnRegistry: registry, MandatePolicy: policy, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
						return llm, nil
					}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
						prepared.Add(1)
						pool.Alias(q.Run.SessionID.String(), q.Bot.HomeSessionID.String())
						llm.mu.Lock()
						llm.work = func() (*store.BotWorkState, error) { return repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease) }
						llm.mu.Unlock()
						s.Services.BotFilesFactory = func(c context.Context, s *store.Services, scope store.BotScope) (store.BotFiles, error) {
							return productionBotFiles(c, home, s, scope)
						}
						return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { cleaned.Add(1) }}, nil
					}})
					if err != nil {
						t.Fatal(err)
					}
					defer worker.Stop()
					router := mux.NewRouter()
					router.HandleFunc("/studio/chat", api.StudioChatHandler).Methods("POST")
					router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
					router.HandleFunc("/bots/{botID}/test-run", api.BotTestRunHandler).Methods("POST")
					router.HandleFunc("/bots/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler).Methods("POST")
					router.HandleFunc("/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler).Methods("POST")
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						p := principal
						if r.Header.Get("X-Foreign") != "" {
							p.Subject = uuid.NewString()
						}
						c, e := execution.WithPrincipal(r.Context(), p)
						if e != nil {
							t.Error(e)
							http.Error(w, "identity", 500)
							return
						}
						c, e = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
						if e != nil {
							t.Error(e)
							http.Error(w, "identity", 500)
							return
						}
						c = store.WithServices(c, svc)
						c = store.WithTenantContext(c, &store.TenantContext{UserID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
						c = api.WithPlatformUser(c, &api.PlatformUser{ID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Role: "member"})
						router.ServeHTTP(w, r.WithContext(c))
					}))
					defer server.Close()
					server.Client().Timeout = 30 * time.Second
					post := func(path string, payload any, foreign bool) (int, []byte) {
						t.Helper()
						raw, e := json.Marshal(payload)
						if e != nil {
							t.Fatal(e)
						}
						r, e := http.NewRequestWithContext(t.Context(), "POST", server.URL+path, strings.NewReader(string(raw)))
						if e != nil {
							t.Fatal(e)
						}
						r.Header.Set("Content-Type", "application/json")
						if foreign {
							r.Header.Set("X-Foreign", "1")
						}
						resp, e := server.Client().Do(r)
						if e != nil {
							t.Fatal(e)
						}
						defer resp.Body.Close()
						raw, e = io.ReadAll(resp.Body)
						if e != nil {
							t.Fatal(e)
						}
						return resp.StatusCode, raw
					}
					mustPost := func(path string, payload any, want int) []byte {
						t.Helper()
						code, raw := post(path, payload, false)
						if code != want {
							t.Fatalf("POST %s: %d want %d: %s", path, code, want, raw)
						}
						return raw
					}
					if mode == "studio" {
						cm := api.GetChatManager()
						cm.Reset()
						api.SetLLMPool(nil)
						bootstrap := agent.NewChatAgent(llm, nil, nil, personal.Sessions(), &agent.SystemPromptBuilder{}, false, true)
						api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
							return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: personal.Sessions(), RuntimeFactory: func(c context.Context, r api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
								prepared.Add(1)
								wired, e := launcher.NewScopedChatRuntime(c, launcher.ChatRuntimeDependencies{PlanInitializer: r.PlanInitializer, Inputs: r.Inputs, Config: &launcher.ChatFactoryConfig{AppConfig: r.AppConfig, PlatformMode: true, AutoApprove: r.AutoApprove, WorkspaceDir: "/workspace", ProviderName: r.ProviderName, ModelName: r.ModelName}, Resources: &launcher.ChatCatalogResources{Pool: pool}, LLM: r.LLM, Sessions: r.SessionService, SessionID: r.SessionID, UseScopedEngines: true, Cleanup: func() { cleaned.Add(1) }}, launcher.ChatRuntimeHooks{})
								if e != nil {
									return nil, e
								}
								return &api.StudioChatRuntime{ChatAgent: wired.ChatAgent, Cleanup: wired.Cleanup}, nil
							}}, nil
						})
						defer func() { cm.Reset(); api.SetStudioChatInitFunc(nil) }()
						raw := mustPost("/studio/chat", map[string]any{"message": task, "autoApprove": true}, 200)
						if strings.Contains(string(raw), "event: error") || !strings.Contains(string(raw), configuredAdmissionBody) {
							t.Fatalf("Studio failed: %s", raw)
						}
						var sessionID string
						for _, line := range strings.Split(string(raw), "\n") {
							if strings.HasPrefix(line, "data: ") {
								var v map[string]any
								_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v)
								if s, ok := v["sessionId"].(string); ok {
									sessionID = s
									break
								}
							}
						}
						if sessionID == "" {
							t.Fatal("missing Studio session")
						}
						before := pool.count()
						code, raw := post("/studio/chat", map[string]any{"message": task, "sessionId": sessionID, "autoApprove": true}, true)
						if code < 400 || pool.count() != before {
							t.Fatalf("foreign Studio admission: %d %s", code, raw)
						}
					} else {
						v := store.DefaultBotVersion()
						v.Charter = task
						v.Autonomy.AllowedTools = []string{"write_file", "read_file", "shell_command", "run_flow", "bot_work_update"}
						if continuous {
							v.Autonomy.AllowedTools = []string{"read_file", "find_files", "file_tree", "bot_work_update"}
						}
						b, e := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Configured admission " + mode, OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: mode})
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
						endpoint := "test-run"
						if mode != "autonomous" {
							endpoint = "messages"
						}
						base := "/bots/" + b.ID.String() + "/"
						originalScript := append([]configuredAdmissionCall(nil), llm.script...)
						var autonomousCalls []configuredAdmissionCall
						if mode == "autonomous" && !continuous {
							// Legacy exact-action resume intentionally executes only its
							// checkpoint, then reports. Admit each task step separately;
							// do not synthesize a broad grant or imply one-run parity.
							autonomousCalls = originalScript
						} else {
							autonomousCalls = []configuredAdmissionCall{{}}
						}
						for actionIndex, action := range autonomousCalls {
							if mode == "autonomous" && !continuous {
								llm.mu.Lock()
								llm.script = []configuredAdmissionCall{action}
								llm.step = 0
								llm.delivery = false
								llm.mu.Unlock()
							}
							payload := map[string]any{"version": b.Version, "idempotency_key": fmt.Sprintf("%s-%d", mode, actionIndex), "instructions": task}

							beforeForeign := pool.count()
							code, raw := post(base+endpoint+"?scope="+string(scope), payload, true)
							if code < 400 || pool.count() != beforeForeign {
								t.Fatalf("foreign Bot admission: %d %s", code, raw)
							}
							if continuous && mode == "autonomous" {
								raw = admitConfiguredContinuousSchedule(t, auth, worker, repo, b, task)
							} else {
								raw = mustPost(base+endpoint+"?scope="+string(scope), payload, 202)
							}
							var run store.BotRun
							if mode != "autonomous" {
								var receipt store.BotMessageReceipt
								if e = json.Unmarshal(raw, &receipt); e != nil {
									t.Fatal(e)
								}
								run = receipt.Run
							} else if e = json.Unmarshal(raw, &run); e != nil {
								t.Fatal(e)
							}
							finished := false
							for attempt := 0; attempt < 8; attempt++ {
								if e = worker.Poll(auth); e != nil {
									t.Fatal(e)
								}
								saved, e := repo.GetRun(auth, b.ID, run.ID)
								if e != nil {
									t.Fatal(e)
								}
								if saved.Status == store.RunWaitingApproval {
									if continuous {
										t.Fatalf("continuous safe script unexpectedly requested approval: %+v", saved)
									}
									approvals, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
									if e != nil {
										t.Fatal(e)
									}
									var pending *store.BotApproval
									for i := range approvals {
										if approvals[i].Status == store.ApprovalPending {
											pending = &approvals[i]
											break
										}
									}
									if pending == nil {
										t.Fatal("waiting without exact approval")
									}
									checkpoint, e := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
									if e != nil || !checkpoint.Matches(*pending) {
										t.Fatalf("checkpoint mismatch: %v", e)
									}
									matched := false
									for _, call := range llm.script {
										var args map[string]any
										if json.Unmarshal(pending.Action.Arguments, &args) == nil && pending.Action.Tool == call.name && reflect.DeepEqual(args, call.args) {
											matched = true
										}
									}
									if !matched {
										t.Fatalf("approval escaped configured task: %+v", pending.Action)
									}
									before := pool.count()
									decisionPath := base + "approvals/" + pending.ID.String() + "/decision?scope=" + string(scope)
									decision := map[string]any{"action_digest": pending.ActionDigest, "status": "approved"}
									code, raw := post(decisionPath, decision, true)
									if code < 400 || pool.count() != before {
										t.Fatalf("foreign approval decision: %d %s", code, raw)
									}
									mustPost(decisionPath, decision, 200)
									if pool.count() != before {
										t.Fatal("approval decision executed tool")
									}
									resumePath := base + "runs/" + run.ID.String() + "/resume?scope=" + string(scope)
									resume := map[string]any{"approval_id": pending.ID}
									code, raw = post(resumePath, resume, true)
									if code < 400 || pool.count() != before {
										t.Fatalf("foreign approved-action resume: %d %s", code, raw)
									}
									mustPost(resumePath, resume, 202)
									continue
								}
								if mode == "interactive-mandate" {
									if saved.MandateRef == nil || saved.Status != store.RunFailed || saved.Report.Failure == nil {
										t.Fatalf("production mandate did not fail closed: %+v", saved)
									}
								} else if saved.Status != store.RunSucceeded && !(saved.Status == store.RunPartial && saved.Report.Failure == nil && saved.Report.EffectState == store.BotReportEffectsUncertain && len(saved.Report.Unverified) > 0) {
									t.Fatalf("run failed: status=%s report=%+v failure=%+v responses=%s", saved.Status, saved.Report, saved.Report.Failure, strings.Join(llm.responses, "\n"))
								}
								if continuous && (saved.Status != store.RunSucceeded || saved.Usage.ToolCalls < 5 || mode == "interactive" && saved.MandateRef == nil || mode == "autonomous" && saved.Trigger != store.TriggerGoalDeadline) {
									t.Fatalf("continuous production run did not finish all safe steps under its admission policy: %+v", saved)
								}
								minCalls := int64(len(llm.script) + 1)
								if mode == "interactive-mandate" {
									// Denial precedes tool budget admission; only bookkeeping ran.
									minCalls = 1
								}
								if saved.Usage.ToolCalls < minCalls || saved.Usage.InputTokens == 0 || saved.Usage.OutputTokens == 0 {
									t.Fatalf("missing cumulative accounting: %+v", saved.Usage)
								}
								approvals, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
								if e != nil {
									t.Fatal(e)
								}
								if continuous && len(approvals) != 0 {
									t.Fatalf("continuous run created grants: %+v", approvals)
								}
								if mode == "autonomous" && !continuous && actionIndex == len(originalScript)-1 && len(approvals) < 3 {
									t.Fatalf("autonomous exact effects bypassed approval: %+v", approvals)
								}
								for _, a := range approvals {
									if a.Status != store.ApprovalConsumed {
										t.Fatalf("unconsumed grant: %+v", a)
									}
								}
								if mode == "interactive-mandate" && len(approvals) != 0 {
									t.Fatal("out-of-mandate action incorrectly became approvable")
								}
								finished = true
								break
							}
							if !finished {
								t.Fatal("run never reached terminal state after approval/resume")
							}
						}
					}
					deadline := time.Now().Add(time.Second)
					for cleaned.Load() != prepared.Load() && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if prepared.Load() == 0 || cleaned.Load() != prepared.Load() || continuous && prepared.Load() != 1 {
						t.Fatalf("cleanup prepared=%d cleaned=%d", prepared.Load(), cleaned.Load())
					}
					if mode == "interactive-mandate" {
						if pool.count() != 0 {
							t.Fatal("denied production mandate reached node")
						}
						llm.mu.Lock()
						defer llm.mu.Unlock()
						responses := strings.Join(llm.responses, "\n")
						for _, tool := range []string{"shell_command", "run_flow"} {
							if !strings.Contains(responses, `\"`+tool+`\" is not in allowed_tools`) {
								t.Fatalf("missing explicit %s mandate denial: %s", tool, responses)
							}
						}
						if strings.Contains(responses, "configured admission flow completed") || strings.Contains(responses, "configured-shell-result") {
							t.Fatal("out-of-mandate tools executed")
						}
						return
					}
					body, e := os.ReadFile(ownedPath)
					if e != nil || string(body) != configuredAdmissionBody {
						t.Fatalf("actual file bytes=%q err=%v", body, e)
					}
					pool.mu.Lock()
					defer pool.mu.Unlock()
					wantCalls := []string{"write_file", "read_file", "shell_command"}
					if continuous {
						wantCalls = []string{"read_file", "find_files", "file_tree", "read_file"}
					}
					if !reflect.DeepEqual(pool.calls, wantCalls) {
						t.Fatalf("node dispatch drift or replay: %v results=%s", pool.calls, pool.results)
					}
					for _, s := range pool.sessions {
						if s != pool.sessions[0] {
							t.Fatalf("session identity drift: %v", pool.sessions)
						}
					}
					llm.mu.Lock()
					defer llm.mu.Unlock()
					if llm.step != len(llm.script) {
						t.Fatalf("unfinished script: %d", llm.step)
					}
					responses := strings.Join(llm.responses, "\n")
					if continuous {
						if !strings.Contains(responses, "result.txt") || !strings.Contains(responses, configuredAdmissionBody) {
							t.Fatalf("continuous actual results missing: %s", responses)
						}
					} else if !strings.Contains(responses, "configured-shell-result") || !strings.Contains(responses, "configured admission flow completed") {
						t.Fatalf("real results did not reach next model: %s", responses)
					}
					prompts := strings.Join(llm.prompts, "\n")
					if len(llm.prompts) == 0 || !strings.Contains(prompts, "/workspace") {
						t.Fatal("scoped workspace missing from prompt")
					}
				})
			}
		})
	}
}
