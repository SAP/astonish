package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
)

// These are production HTTP/worker admissions with controlled models, SQLite
// repositories and loopback services. They are not Docker/Kubernetes evidence.
// Only the transport is test-owned: no catalog replacement or policy grant.
func TestBotConfiguredServiceAdmissionHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, service := range []string{"a2a", "email", "drill"} {
		for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
			modes := []string{"interactive", "scheduled-exact-resume"}
			// Studio HTTP fixes ResourceScopePersonal independently of memoryScope;
			// team memory is not a team-owned mailbox/session admission.
			if scope == store.BotScopePersonal {
				modes = append([]string{"studio"}, modes...)
			}
			for _, mode := range modes {
				t.Run(service+"/"+string(scope)+"/"+mode, func(t *testing.T) {
					runConfiguredServiceAdmission(t, service, scope, mode)
				})
			}
		}
	}
}

func runConfiguredServiceAdmission(t *testing.T, service string, scope store.BotScope, mode string) {
	runConfiguredServiceAdmissionVariant(t, service, scope, mode, "")
}

func runConfiguredServiceAdmissionVariant(t *testing.T, service string, scope store.BotScope, mode, variant string) {
	t.Helper()
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
	auth = store.WithUserID(store.WithOrgSlug(store.WithTeamSlug(auth, "team"), "admission"), owner)
	repo, pin := team.Bots(), team.SetSessionPin
	if scope == store.BotScopePersonal {
		repo, pin = personal.Bots(), personal.SetSessionPin
	}
	var calls, prepared, cleaned atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, ".well-known") {
			fmt.Fprint(w, `{"name":"configured-service","description":"Controlled peer","url":"","version":"1","capabilities":{},"defaultInputModes":["text"],"defaultOutputModes":["text"],"skills":[]}`)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(req)
		if req["method"] != "message/send" || !strings.Contains(string(raw), "controlled-service-question") {
			t.Errorf("unexpected peer request: %s", raw)
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"kind": "task", "id": "configured-service-task", "status": map[string]any{"state": "completed", "message": map[string]any{"role": "agent", "parts": []any{map[string]any{"kind": "text", "text": "configured-a2a-service-evidence"}}}}}})
	}))
	defer peer.Close()
	disabled := false
	for _, standard := range config.GetStandardServers() {
		if standard.Kind != "model" {
			if err := backend.PlatformMCPServers().Save(auth, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := team.A2AAgents().Save(auth, &store.A2AAgent{Name: "configured_service", URL: peer.URL, Timeout: "5s"}); err != nil {
		t.Fatal(err)
	}
	home := testRetainedContentBackend(t, backend)
	catalogBackend := &configuredServiceBackend{}
	home.setupHomeBackend.Backend = catalogBackend
	pool := &configuredServicePool{vaultBackend: catalogBackend}
	pool.backend = catalogBackend
	bundle := &launcher.ChatCatalogResources{Backend: catalogBackend, Pool: pool}
	shared := productionBotChatResources(&launcher.ChatFactoryResult{SharedResources: bundle, SandboxPool: pool})
	if shared == nil || shared.Pool != pool || shared.Backend != catalogBackend {
		t.Fatal("production resource identity lost")
	}
	toolName, resultMarker := "a2a_configured_service", "configured-a2a-service-evidence"
	task := "Send the exact controlled-service-question to the configured service, then reply in chat only."
	policyName := "a2a/" + toolName
	args := map[string]any{"message": "controlled-service-question"}
	if service == "email" {
		toolName, policyName, resultMarker = "email_send", "email_send", "message_id"
		args = map[string]any{"to": []any{"recipient@example.com"}, "subject": "Reviewed configured subject", "body": "Reviewed configured email body"}
		endpoint := configuredServiceSMTP(t, func() { calls.Add(1) })
		settings := &store.BotEmailSettings{Provider: "imap", Address: "owner@example.com", IMAPServer: "127.0.0.1:1", SMTPServer: endpoint, Credential: "mail", Folder: "INBOX"}
		own, other := personal.Credentials(), team.Credentials()
		if scope == store.BotScopeTeam {
			own, other = other, own
		}
		if err := own.Set(auth, "mail", &store.Credential{Type: store.CredPassword, Username: "owner", Password: configuredServiceSecret}); err != nil {
			t.Fatal("own-scope credential setup failed", err)
		}
		if err := other.Set(auth, "mail", &store.Credential{Type: store.CredPassword, Username: "foreign", Password: "wrong-scope-secret"}); err != nil {
			t.Fatal("foreign-scope credential setup failed", err)
		}
		if scope == store.BotScopePersonal {
			if err := personal.PersonalSettings().Save(auth, &store.PersonalSettings{BotEmail: settings}); err != nil {
				t.Fatal(err)
			}
			persisted, err := personal.PersonalSettings().Get(auth)
			if err != nil || persisted.BotEmail == nil || *persisted.BotEmail != *settings {
				t.Fatalf("personal mailbox persistence prerequisite failed (credential reference only): err=%v", err)
			}
		} else {
			if err := team.Settings().Save(auth, &store.TeamSettings{BotEmail: settings}); err != nil {
				t.Fatal(err)
			}
			persisted, err := team.Settings().Get(auth)
			if err != nil || persisted.BotEmail == nil || *persisted.BotEmail != *settings {
				t.Fatalf("team mailbox persistence prerequisite failed (credential reference only): err=%v", err)
			}
		}
	}
	if service == "drill" {
		toolName, policyName, resultMarker = "run_drill", "run_drill", "passed"
		args = map[string]any{"suite_name": "configured-service-suite"}
		// Platform drills are team-scoped artifacts even for a personal Chat/Bot.
		// Seed only the configured team store, not a personal fallback.
		fs := team.Flows()
		if err := fs.SaveFlow(auth, "configured-service-suite", "name: configured-service-suite\ntype: drill_suite\nsuite_config: {}\n"); err != nil {
			t.Fatal(err)
		}
		if err := fs.SaveFlow(auth, "configured-service-check", "name: configured-service-check\ntype: drill\nsuite: configured-service-suite\nnodes:\n  - name: verify\n    type: tool\n    args: {tool: filter_json, json_data: '{\"evidence\":\"configured-drill-result\"}', fields_to_extract: [evidence]}\n    assert: {type: contains, value: configured-drill-result}\n"); err != nil {
			t.Fatal(err)
		}
		if variant == "restricted-shell" {
			if err := fs.SaveFlow(auth, "configured-service-check", "name: configured-service-check\ntype: drill\nsuite: configured-service-suite\nnodes:\n  - name: verify\n    type: tool\n    args: {tool: shell_command, command: 'printf forbidden-child'}\n"); err != nil {
				t.Fatal(err)
			}
		}
	}
	llm := &configuredAdmissionModel{script: []configuredAdmissionCall{{toolName, args}}}
	turns := execution.NewBotTurnAuthorizationRegistry(8)
	defer turns.Close()
	svc := &store.Services{Mode: store.ModePlatform, Platform: backend, TenantRouter: backend, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), PlatformMCPServers: backend.PlatformMCPServers(), TeamMCPServers: team.MCPServers(), A2AAgents: org.OrgA2AAgents(), TeamA2AAgents: team.A2AAgents(), PlatformSettings: backend.PlatformSettings(), Settings: team.Settings(), PersonalSettings: personal.PersonalSettings(), BotTurnRegistry: turns, BotInteractiveReady: true}
	svc.Flows, svc.PersonalFlows = team.Flows(), personal.Flows()
	svc.Credentials, svc.PersonalCredentials = team.Credentials(), personal.Credentials()
	worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: home, Pool: pool, WorkerID: "service-" + mode, TurnRegistry: turns, ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return llm, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		prepared.Add(1)
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
	wantCalls := int32(1)
	if service == "drill" {
		wantCalls = 0
	}
	wantAttempts := int32(1)
	if mode == "studio" {
		cm := api.GetChatManager()
		cm.Reset()
		api.SetLLMPool(nil)
		bootstrap := agent.NewChatAgent(llm, nil, nil, personal.Sessions(), &agent.SystemPromptBuilder{}, false, true)
		api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
			return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: personal.Sessions(), SandboxPool: pool, RuntimeFactory: func(c context.Context, r api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
				prepared.Add(1)
				resources := *shared
				emailTools, e := launcher.ScopedChatEmailTools(launcher.ScopedChatEmailOptions{Inputs: r.Inputs, Scope: chatruntime.ResourceScopePersonal, Backend: catalogBackend, Client: pool, SessionID: r.SessionID})
				if e != nil {
					return nil, e
				}
				resources.EmailTools = emailTools
				wired, e := launcher.NewScopedChatRuntime(c, launcher.ChatRuntimeDependencies{Inputs: r.Inputs, PlanInitializer: r.PlanInitializer, Config: &launcher.ChatFactoryConfig{AppConfig: r.AppConfig, PlatformMode: true, AutoApprove: r.AutoApprove, WorkspaceDir: "/workspace"}, Resources: &resources, LLM: r.LLM, Sessions: r.SessionService, SessionID: r.SessionID, UseScopedEngines: true, Cleanup: func() { cleaned.Add(1) }}, launcher.ChatRuntimeHooks{})
				if e != nil {
					return nil, e
				}
				return &api.StudioChatRuntime{ChatAgent: wired.ChatAgent, Cleanup: wired.Cleanup}, nil
			}}, nil
		})
		defer func() { cm.Reset(); api.SetStudioChatInitFunc(nil) }()
		raw := post("/studio/chat", map[string]any{"message": task, "autoApprove": true}, 200)
		if strings.Contains(string(raw), "event: error") {
			t.Fatalf("Studio: %s", raw)
		}
	} else {
		v := store.DefaultBotVersion()
		v.Charter = task
		v.Autonomy.AllowedTools = []string{policyName, "bot_work_update", "filter_json"}
		b, e := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Configured service " + mode, OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: mode})
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
			raw := post("/bots/"+b.ID.String()+"/messages?scope="+string(scope), map[string]any{"version": b.Version, "idempotency_key": mode, "instructions": task}, 202)
			var receipt store.BotMessageReceipt
			if e = json.Unmarshal(raw, &receipt); e != nil {
				t.Fatal(e)
			}
			run = receipt.Run
		} else {
			raw := admitConfiguredContinuousSchedule(t, auth, worker, repo, b, task)
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
		if e != nil {
			t.Fatal(e)
		}
		if mode == "scheduled-exact-resume" {
			if saved.Status != store.RunWaitingApproval || len(approvals) != 1 || approvals[0].Action.Tool != policyName || approvals[0].Status != store.ApprovalPending || calls.Load() != 0 {
				t.Fatalf("exact scheduled boundary: run=%+v approvals=%+v calls=%d", saved, approvals, calls.Load())
			}
			checkpoint, e := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
			if e != nil || !checkpoint.Matches(approvals[0]) {
				t.Fatalf("durable checkpoint: %v", e)
			}
			approval := approvals[0]
			if service == "drill" {
				captured, err := tools.DecodeCapturedDrill(approval.Action)
				wantChild := "filter_json"
				if variant == "restricted-shell" {
					wantChild = "shell_command"
				}
				if err != nil || len(captured.Children) != 1 || captured.Children[0].Tool != wantChild || len(captured.Suite.Tests) != 1 {
					t.Fatalf("scheduled drill did not capture exact child evidence: %v", err)
				}
			}
			expected, _ := json.Marshal(llm.script[0].args)
			if string(approval.Action.Arguments) != string(expected) {
				t.Fatalf("approval arguments widened: %+v", approval.Action)
			}
			if variant != "" && variant != "restricted-shell" {
				if variant == "suite" {
					e = team.Flows().SaveFlow(auth, "configured-service-suite", "name: configured-service-suite\ntype: drill_suite\ndescription: altered\nsuite_config: {}\n")
				} else {
					toolName := "filter_json"
					value := "altered"
					if variant == "tool" {
						toolName = "shell_command"
					}
					e = team.Flows().SaveFlow(auth, "configured-service-check", fmt.Sprintf("name: configured-service-check\ntype: drill\nsuite: configured-service-suite\nnodes:\n  - name: verify\n    type: tool\n    args: {tool: %s, json_data: '%s', fields_to_extract: [evidence]}\n    assert: {type: contains, value: configured-drill-result}\n", toolName, value))
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			post("/bots/"+b.ID.String()+"/approvals/"+approval.ID.String()+"/decision?scope="+string(scope), map[string]any{"action_digest": approval.ActionDigest, "status": store.ApprovalApproved}, 200)
			resume := "/bots/" + b.ID.String() + "/runs/" + run.ID.String() + "/resume?scope=" + string(scope)
			post(resume, map[string]any{"approval_id": approval.ID}, 202)
			if e = worker.Poll(auth); e != nil {
				t.Fatal(e)
			}
			saved, e = repo.GetRun(auth, b.ID, run.ID)
			if e != nil {
				t.Fatal(e)
			}
			approvals, e = repo.ListApprovals(auth, b.ID, store.BotPage{})
			if variant == "restricted-shell" {
				if e != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalConsumed || saved.Status == store.RunSucceeded {
					t.Fatalf("consequential child inherited parent authority: run=%+v approvals=%+v err=%v", saved, approvals, e)
				}
				post(resume, map[string]any{"approval_id": approval.ID}, 409)
				if e = worker.Poll(auth); e != nil {
					t.Fatal(e)
				}
				pool.mu.Lock()
				defer pool.mu.Unlock()
				if len(pool.operations) != 0 {
					t.Fatal("restricted shell dispatched", pool.operations)
				}
				return
			}
			if variant != "" {
				if e != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalApproved || saved.Status != store.RunWaitingApproval {
					t.Fatalf("drift consumed parent approval: status=%s approvals=%+v err=%v", saved.Status, approvals, e)
				}
				pool.mu.Lock()
				defer pool.mu.Unlock()
				if len(pool.operations) != 0 {
					t.Fatal("drift dispatched a child", pool.operations)
				}
				return
			}
			if e != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalConsumed || calls.Load() != wantCalls {
				t.Fatalf("exact owner resume: run=%+v approvals=%+v calls=%d err=%v", saved, approvals, calls.Load(), e)
			}
			post(resume, map[string]any{"approval_id": approval.ID}, 409)
			if e = worker.Poll(auth); e != nil {
				t.Fatal(e)
			}
			if calls.Load() != wantCalls {
				t.Fatal("approved service replayed")
			}
			wantAttempts = 2
		} else if len(approvals) != 0 {
			t.Fatalf("interactive approval mismatch: %+v", approvals)
		}
		// Returned external-service evidence is not independent effect verification.
		if (saved.Status != store.RunSucceeded && saved.Status != store.RunPartial) || saved.Report.Failure != nil || saved.Usage.InputTokens == 0 || saved.Usage.ToolCalls < 2 {
			t.Fatalf("admitted service run: %+v responses=%v", saved, llm.responses)
		}
	}
	deadline := time.Now().Add(time.Second)
	for cleaned.Load() != prepared.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if prepared.Load() != wantAttempts || cleaned.Load() != wantAttempts {
		t.Fatalf("attempt cleanup prepared=%d cleaned=%d", prepared.Load(), cleaned.Load())
	}
	pool.mu.Lock()
	wantOperations := 0
	if service == "email" || service == "drill" {
		wantOperations = 1
	}
	if pool.closed || len(pool.operations) != wantOperations {
		t.Fatalf("borrowed pool closed or unexpected node/host fallback: %v %v", pool.closed, pool.operations)
	}
	pool.mu.Unlock()
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if calls.Load() != wantCalls || llm.step != len(llm.script) || !strings.Contains(strings.Join(llm.responses, "\n"), resultMarker) {
		t.Fatalf("service result not returned to subsequent model: calls=%d step=%d responses=%v", calls.Load(), llm.step, llm.responses)
	}
}
