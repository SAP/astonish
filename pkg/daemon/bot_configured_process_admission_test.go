package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
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
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
)

// HTTP admission and production dispatch into independent manager-owning test
// binaries. This is real PTY/process evidence, not physical Docker/Kubernetes or
// Studio UI evidence. No parent/global manager is created or cleaned here.
const configuredProcessHelperEnv = "ASTONISH_ADMITTED_PROCESS_HELPER"
const configuredProcessCommand = `printf 'ready\n'; while IFS= read -r line; do printf 'ack:%s\n' "$line"; done`

type configuredProcessRequest struct {
	Session, Name, WaitFor string
	Args                   map[string]any
}
type configuredProcessResponse struct {
	Result json.RawMessage
	Error  string
}

func TestBotConfiguredProcessNodeHelper(t *testing.T) {
	if os.Getenv(configuredProcessHelperEnv) != "1" {
		return
	}
	input, output := os.NewFile(3, "requests"), os.NewFile(4, "responses")
	defer input.Close()
	defer output.Close()
	root, owner := os.Getenv("ASTONISH_ADMITTED_PROCESS_ROOT"), os.Getenv("ASTONISH_ADMITTED_PROCESS_SESSION")
	if root == "" || owner == "" {
		t.Fatal("helper requires disposable scope")
	}
	defer func() {
		tools.CleanupProcessManager()
		for _, s := range tools.GetProcessManager().List() {
			if s.IsRunning() {
				t.Errorf("owned process %s survived cleanup", s.ID)
			}
		}
	}()
	dec, enc := json.NewDecoder(input), json.NewEncoder(output)
	for {
		var q configuredProcessRequest
		if err := dec.Decode(&q); err != nil {
			if err != io.EOF {
				t.Error(err)
			}
			return
		}
		out := configuredProcessResponse{}
		execute := func() (any, error) {
			if q.Session != owner {
				return nil, fmt.Errorf("foreign node session")
			}
			if q.WaitFor != "" {
				s := tools.GetProcessManager().Get(q.Args["session_id"].(string))
				if s == nil {
					return nil, fmt.Errorf("missing owned process")
				}
				deadline := time.Now().Add(10 * time.Second)
				for !strings.Contains(string(s.Output.Bytes()), q.WaitFor) {
					if !s.IsRunning() || time.Now().After(deadline) {
						return nil, fmt.Errorf("missing handshake %q", q.WaitFor)
					}
					runtime.Gosched()
				}
				if !s.IsRunning() {
					return nil, fmt.Errorf("owned process exited before handshake")
				}
				return map[string]any{"ready": true}, nil
			}
			switch q.Name {
			case "shell_command":
				if q.Args["command"] != configuredProcessCommand || q.Args["background"] != true || q.Args["working_dir"] != root {
					return nil, fmt.Errorf("unsafe fixture command")
				}
			case "process_read", "process_write", "process_kill", "process_list":
				// No process-ID filtering: foreign-ID denial must come from real manager.
			default:
				return nil, fmt.Errorf("unexpected fixture tool %s", q.Name)
			}
			return tools.ExecuteTool(context.Background(), q.Name, q.Args, owner)
		}
		result, err := execute()
		if err != nil {
			out.Error = err.Error()
		} else {
			out.Result, err = json.Marshal(result)
			if err != nil {
				out.Error = err.Error()
			}
		}
		if err := enc.Encode(out); err != nil {
			t.Fatal(err)
		}
	}
}

type configuredProcessNode struct {
	sandbox.ToolNodeClient
	mu                sync.Mutex
	session, root, id string
	input             io.WriteCloser
	output            *json.Decoder
	calls             []string
	results           []json.RawMessage
	errors            []string
}

func newConfiguredProcessNode(t *testing.T) *configuredProcessNode {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		t.Fatal(err)
	}
	n := &configuredProcessNode{session: uuid.NewString(), root: t.TempDir(), input: inW, output: json.NewDecoder(outR)}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestBotConfiguredProcessNodeHelper$", "-test.timeout=85s")
	cmd.Env = append(os.Environ(), configuredProcessHelperEnv+"=1", "ASTONISH_ADMITTED_PROCESS_ROOT="+n.root, "ASTONISH_ADMITTED_PROCESS_SESSION="+n.session)
	cmd.ExtraFiles = []*os.File{inR, outW}
	logs := tools.NewRingBuffer(16 * 1024)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		cancel()
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()
	t.Cleanup(func() {
		inW.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("helper cleanup/reap: %v; %s", err, logs.Bytes())
		}
		outR.Close()
		cancel()
	})
	return n
}
func (n *configuredProcessNode) exchange(q configuredProcessRequest) (json.RawMessage, error) {
	if err := json.NewEncoder(n.input).Encode(q); err != nil {
		return nil, err
	}
	var out configuredProcessResponse
	if err := n.output.Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("node: %s", out.Error)
	}
	return out.Result, nil
}
func (n *configuredProcessNode) BindSession(string) {}
func (n *configuredProcessNode) EnsureReady(id string) error {
	if id != n.session {
		return store.ErrBotForbidden
	}
	return nil
}
func (n *configuredProcessNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	result, err := n.exchange(configuredProcessRequest{Session: id, Name: name, Args: args})
	n.calls = append(n.calls, name)
	n.results = append(n.results, result)
	text := ""
	if err != nil {
		text = err.Error()
	}
	n.errors = append(n.errors, text)
	if name == "shell_command" && err == nil {
		var s struct {
			SessionID string `json:"session_id"`
		}
		if e := json.Unmarshal(result, &s); e != nil {
			return nil, e
		}
		n.id = s.SessionID
	}
	return result, err
}
func (n *configuredProcessNode) handshake(t *testing.T, want string) {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, err := n.exchange(configuredProcessRequest{Session: n.session, Args: map[string]any{"session_id": n.id}, WaitFor: want}); err != nil {
		t.Fatal(err)
	}
}
func (n *configuredProcessNode) processID() string { n.mu.Lock(); defer n.mu.Unlock(); return n.id }
func (n *configuredProcessNode) count() int        { n.mu.Lock(); defer n.mu.Unlock(); return len(n.calls) }

type configuredProcessPool struct {
	intentHTTPPool
	mu      sync.Mutex
	node    *configuredProcessNode
	aliases map[string]string
}

func (p *configuredProcessPool) GetBackend() sandbox.Backend               { return nil }
func (p *configuredProcessPool) GetOrCreate(string) sandbox.ToolNodeClient { return p }
func (p *configuredProcessPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p
}
func (p *configuredProcessPool) Alias(alias, original string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.aliases[alias] = original
}
func (p *configuredProcessPool) EnsureReady(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.aliases[id]; !ok {
		return store.ErrBotForbidden
	}
	return nil
}
func (p *configuredProcessPool) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	if err := p.EnsureReady(id); err != nil {
		return nil, err
	}
	return p.node.Call(p.node.session, name, args)
}
func (p *configuredProcessPool) CallContext(ctx context.Context, id, name string, args map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.Call(id, name, args)
}

// The ID is filled only from the actual node's shell result, never invented.
// The model still receives ordinary FunctionResponse evidence through ChatRunner.
type configuredProcessModel struct {
	*configuredAdmissionModel
	node *configuredProcessNode
}

func (m *configuredProcessModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	id := m.node.processID()
	m.mu.Lock()
	for _, c := range m.script {
		if c.args["session_id"] == "$owned-process" && id != "" {
			c.args["session_id"] = id
		}
	}
	m.mu.Unlock()
	return m.configuredAdmissionModel.GenerateContent(ctx, r, stream)
}

func TestBotConfiguredProcessAdmissionHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		modes := []string{"interactive"}
		if scope == store.BotScopePersonal {
			modes = append([]string{"studio"}, modes...)
		}
		for _, mode := range modes {
			t.Run(string(scope)+"/"+mode+"/lifecycle", func(t *testing.T) { runConfiguredProcessAdmission(t, scope, mode, "lifecycle") })
			for _, leaf := range []string{"foreign-process_read", "foreign-process_write", "foreign-process_kill"} {
				t.Run(string(scope)+"/"+mode+"/"+leaf, func(t *testing.T) { runConfiguredProcessAdmission(t, scope, mode, leaf) })
			}
		}
		// Exact-action reports deliberately end after one leaf. These rows each
		// prove a scheduled admitted leaf against its own test-owned process. They
		// do NOT pretend that four new runs are one scheduled lifecycle continuation.
		for _, leaf := range []string{"shell_command", "process_read", "process_write", "process_kill", "foreign-process_read", "foreign-process_write", "foreign-process_kill", "membership-loss"} {
			t.Run(string(scope)+"/scheduled-exact/"+leaf, func(t *testing.T) { runConfiguredProcessAdmission(t, scope, "scheduled-exact", leaf) })
		}
	}
}

func runConfiguredProcessAdmission(t *testing.T, scope store.BotScope, mode, leaf string) {
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
	disabled := false
	for _, s := range config.GetStandardServers() {
		if s.Kind != "model" {
			if err := backend.PlatformMCPServers().Save(auth, &store.MCPServer{Name: s.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
				t.Fatal(err)
			}
		}
	}
	node, foreign := newConfiguredProcessNode(t), newConfiguredProcessNode(t)
	start := func(n *configuredProcessNode) {
		t.Helper()
		if _, err := n.Call(n.session, "shell_command", map[string]any{"command": configuredProcessCommand, "background": true, "working_dir": n.root}); err != nil {
			t.Fatal(err)
		}
		n.handshake(t, "ready")
	}
	start(foreign)
	pool := &configuredProcessPool{node: node, aliases: map[string]string{}}
	shellArgs := map[string]any{"command": configuredProcessCommand, "background": true, "working_dir": node.root}
	script := []configuredAdmissionCall{{"shell_command", shellArgs}, {"process_read", map[string]any{"session_id": "$owned-process"}}, {"process_write", map[string]any{"session_id": "$owned-process", "input": "admitted"}}, {"process_read", map[string]any{"session_id": "$owned-process"}}, {"process_kill", map[string]any{"session_id": "$owned-process"}}, {"process_read", map[string]any{"session_id": "$owned-process"}}}
	denied := strings.HasPrefix(leaf, "foreign-")
	if mode == "scheduled-exact" || denied {
		toolName := strings.TrimPrefix(leaf, "foreign-")
		if leaf == "membership-loss" {
			toolName = "process_kill"
		}
		args := shellArgs
		if toolName != "shell_command" {
			start(node)
			id := node.processID()
			if denied {
				id = foreign.processID()
			}
			args = map[string]any{"session_id": id}
			if toolName == "process_write" {
				args["input"] = "admitted"
			}
		}
		script = []configuredAdmissionCall{{toolName, args}}
	}
	initialCalls := node.count()
	llm := &configuredProcessModel{configuredAdmissionModel: &configuredAdmissionModel{script: script}, node: node}
	if mode == "interactive" {
		llm.script = script[:1]
	}
	turns := execution.NewBotTurnAuthorizationRegistry(8)
	defer turns.Close()
	svc := &store.Services{Mode: store.ModePlatform, Platform: backend, TenantRouter: backend, Bots: team.Bots(), PersonalBots: personal.Bots(), Sessions: team.Sessions(), PersonalSessions: personal.Sessions(), PlatformMCPServers: backend.PlatformMCPServers(), BotTurnRegistry: turns, BotInteractiveReady: true}
	home := testRetainedContentBackend(t, backend)
	var prepared, cleaned atomic.Int32
	shared := &launcher.ChatCatalogResources{Pool: pool}
	worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: home, Pool: pool, WorkerID: "process-" + mode, TurnRegistry: turns, MandatePolicy: newProductionBotMandatePolicy(backend, &config.AppConfig{}), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
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
	router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
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
		raw, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
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
	task := "Execute the exact test-owned process task and reply in chat only."
	wantAttempts := int32(1)
	if mode == "studio" {
		cm := api.GetChatManager()
		cm.Reset()
		api.SetLLMPool(nil)
		bootstrap := agent.NewChatAgent(llm, nil, nil, personal.Sessions(), &agent.SystemPromptBuilder{}, false, true)
		api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
			return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: personal.Sessions(), SandboxPool: pool, RuntimeFactory: func(c context.Context, r api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
				prepared.Add(1)
				pool.Alias(r.SessionID, r.SessionID)
				wired, e := launcher.NewScopedChatRuntime(c, launcher.ChatRuntimeDependencies{Inputs: r.Inputs, PlanInitializer: r.PlanInitializer, Config: &launcher.ChatFactoryConfig{AppConfig: r.AppConfig, PlatformMode: true, AutoApprove: r.AutoApprove, WorkspaceDir: "/workspace"}, Resources: shared, LLM: r.LLM, Sessions: r.SessionService, SessionID: r.SessionID, UseScopedEngines: true, Cleanup: func() { cleaned.Add(1) }}, launcher.ChatRuntimeHooks{})
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
		v.Autonomy.AllowedTools = []string{"shell_command", "process_read", "process_write", "process_kill", "bot_work_update"}
		b, e := repo.Create(auth, store.BotCreateRequest{Name: "Configured process " + mode, OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: mode})
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
			raw := post("/bots/"+b.ID.String()+"/messages?scope="+string(scope), map[string]any{"version": b.Version, "idempotency_key": mode, "instructions": task}, 202)
			if e = json.Unmarshal(raw, &receipt); e != nil {
				t.Fatal(e)
			}
			run = receipt.Run
		} else {
			if e = json.Unmarshal(admitConfiguredProcessSchedule(t, auth, worker, repo, b, task), &run); e != nil {
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
		if mode == "scheduled-exact" {
			if saved.Status != store.RunWaitingApproval || len(approvals) != 1 || approvals[0].Status != store.ApprovalPending || approvals[0].Action.Tool != script[0].name || node.count() != initialCalls {
				t.Fatalf("scheduled predispatch: run=%+v approvals=%+v calls=%d", saved, approvals, node.count())
			}
			approval := approvals[0]
			checkpoint, e := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
			expected, _ := json.Marshal(script[0].args)
			if e != nil || !checkpoint.Matches(approval) || string(approval.Action.Arguments) != string(expected) {
				t.Fatalf("exact checkpoint: %+v %v", approval, e)
			}
			post("/bots/"+b.ID.String()+"/approvals/"+approval.ID.String()+"/decision?scope="+string(scope), map[string]any{"action_digest": approval.ActionDigest, "status": store.ApprovalApproved}, 200)
			resume := "/bots/" + b.ID.String() + "/runs/" + run.ID.String() + "/resume?scope=" + string(scope)
			post(resume, map[string]any{"approval_id": approval.ID}, 202)
			if leaf == "membership-loss" {
				orgRow, e := backend.Organizations().GetBySlug(t.Context(), "admission")
				if e != nil {
					t.Fatal(e)
				}
				if e = backend.Organizations().RemoveMember(t.Context(), owner, orgRow.ID); e != nil {
					t.Fatal(e)
				}
			}
			if e = worker.Poll(auth); e != nil {
				t.Fatal(e)
			}
			if leaf == "membership-loss" {
				if node.count() != initialCalls || prepared.Load() != 1 || cleaned.Load() != 1 {
					t.Fatal("revoked owner reached process resume construction/dispatch")
				}
				orgRow, err := backend.Organizations().GetBySlug(t.Context(), "admission")
				if err != nil {
					t.Fatal(err)
				}
				if err = backend.Organizations().AddMember(t.Context(), owner, orgRow.ID, "member"); err != nil {
					t.Fatal(err)
				}
			}
			saved, e = repo.GetRun(auth, b.ID, run.ID)
			if e != nil {
				t.Fatal(e)
			}
			approvals, e = repo.ListApprovals(auth, b.ID, store.BotPage{})
			if e != nil {
				t.Fatal(e)
			}
			if leaf == "membership-loss" {
				if len(approvals) != 1 || approvals[0].Status != store.ApprovalApproved || node.count() != initialCalls {
					t.Fatalf("membership loss consumed/dispatched: %+v calls=%d", approvals, node.count())
				}
				node.handshake(t, "ready")
				return
			}
			if len(approvals) != 1 || approvals[0].Status != store.ApprovalConsumed || node.count() != initialCalls+1 {
				t.Fatalf("exact dispatch/consumption: run=%+v approvals=%+v calls=%d", saved, approvals, node.count())
			}
			post(resume, map[string]any{"approval_id": approval.ID}, 409)
			if e = worker.Poll(auth); e != nil {
				t.Fatal(e)
			}
			if node.count() != initialCalls+1 {
				t.Fatal("consumed process action replayed")
			}
			wantAttempts = 2
		} else if mode == "interactive" {
			saved, wantAttempts = configuredFixtureInteractiveActions(t, auth, worker, repo, b, run, scope, llm.configuredAdmissionModel, script, post)
		} else if len(approvals) != 0 {
			t.Fatalf("interactive approvals: %+v run=%+v responses=%v", approvals, saved, llm.responses)
		}
		if !denied && saved.Status != store.RunSucceeded && saved.Status != store.RunPartial {
			t.Fatalf("admitted run: %+v responses=%v", saved, llm.responses)
		}
	}
	deadline := time.Now().Add(time.Second)
	for cleaned.Load() != prepared.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if prepared.Load() != wantAttempts || cleaned.Load() != wantAttempts {
		t.Fatalf("attempt cleanup: prepared=%d cleaned=%d", prepared.Load(), cleaned.Load())
	}
	node.mu.Lock()
	actualCalls := append([]string(nil), node.calls[initialCalls:]...)
	actualResults := append([]json.RawMessage(nil), node.results[initialCalls:]...)
	actualErrors := append([]string(nil), node.errors[initialCalls:]...)
	node.mu.Unlock()
	if len(actualCalls) != len(script) {
		t.Fatalf("dispatch count calls=%v want=%d responses=%v", actualCalls, len(script), llm.responses)
	}
	for i, c := range script {
		if actualCalls[i] != c.name {
			t.Fatalf("dispatch %d: %v", i, actualCalls)
		}
		if denied {
			if !strings.Contains(actualErrors[i], "not found") {
				t.Fatalf("foreign node ID accepted: %s %s", actualResults[i], actualErrors[i])
			}
		} else if actualErrors[i] != "" {
			t.Fatalf("tool error: %s", actualErrors[i])
		}
	}
	llm.mu.Lock()
	responses := strings.Join(llm.responses, "\n")
	llm.mu.Unlock()
	if denied {
		if !strings.Contains(responses, "not found") {
			t.Fatal("foreign failure not returned to model", responses)
		}
		foreign.handshake(t, "ready")
		node.handshake(t, "ready")
	} else if leaf == "lifecycle" {
		if !strings.Contains(string(actualResults[2]), "ack:admitted") || !strings.Contains(string(actualResults[4]), "killed") || !strings.Contains(string(actualResults[5]), `"running":false`) || !strings.Contains(responses, "ack:admitted") {
			t.Fatalf("lifecycle evidence: %s responses=%s", actualResults, responses)
		}
	} else {
		switch leaf {
		case "shell_command":
			node.handshake(t, "ready")
		case "process_read":
			if !strings.Contains(string(actualResults[0]), "ready") || !strings.Contains(responses, "ready") {
				t.Fatalf("read evidence: %s", actualResults)
			}
		case "process_write":
			node.handshake(t, "ack:admitted")
			if !strings.Contains(string(actualResults[0]), "ack:admitted") || !strings.Contains(responses, "ack:admitted") {
				t.Fatalf("write evidence: %s", actualResults)
			}
		case "process_kill":
			if !strings.Contains(string(actualResults[0]), "killed") || !strings.Contains(responses, "killed") {
				t.Fatalf("kill evidence: %s", actualResults)
			}
		}
	}
	// Foreign node remains independently live even after owner kill/denials; EOF
	// cleanup/reaping below owns both helpers, never the daemon/user manager.
	foreign.handshake(t, "ready")
}

func admitConfiguredProcessSchedule(t *testing.T, ctx context.Context, worker *BotWorker, repo store.BotStore, b *store.Bot, task string) []byte {
	t.Helper()
	worker.schedulerReady.Store(true)
	var err error
	b, err = repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	goal, err := repo.(store.BotGoalStore).SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Inspect fixture", CompletionCriteria: "Return fixture bytes in chat", DueAt: &due}})
	if err != nil {
		t.Fatal(err)
	}
	tid := uuid.New()
	for _, action := range []store.BotTriggerAction{store.BotTriggerSave, store.BotTriggerEnable} {
		b, err = repo.Get(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		definition := store.TriggerSpec{}
		if action == store.BotTriggerSave {
			definition = store.TriggerSpec{Kind: store.TriggerGoalDeadline, GoalID: &goal.ID, Instructions: task}
		}
		b, err = repo.(store.BotTriggerStore).ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: action, Reason: "reviewed exact process fixture", Definition: definition})
		if err != nil {
			t.Fatal(err)
		}
	}
	version, err := repo.GetVersion(ctx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	var jobID string
	for _, trigger := range version.Triggers {
		if trigger.ID == tid && trigger.ScheduleID != nil {
			jobID = trigger.ScheduleID.String()
		}
	}
	if jobID == "" {
		t.Fatal("missing durable schedule")
	}
	identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, due.Format(time.RFC3339Nano))
	key := fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
	route := BotSchedulerScope{OrgSlug: "admission", TeamSlug: "team"}
	if b.Scope == store.BotScopePersonal {
		route.UserID = b.OwnerID.String()
	}
	callback := worker.SchedulerCallbacks().Admission()
	request := scheduler.BotTriggerRequest{JobID: jobID, BotID: b.ID, Version: b.Version, TriggerID: tid, OccurrenceAt: due, IdempotencyKey: key}
	receipt, err := callback(withBotSchedulerScope(ctx, route), request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := callback(withBotSchedulerScope(ctx, route), request)
	if err != nil || duplicate.RunID != receipt.RunID || duplicate.Status != scheduler.BotAdmissionDuplicate {
		t.Fatalf("scheduled duplicate changed logical run: %+v %v", duplicate, err)
	}
	run, err := repo.GetRun(ctx, b.ID, receipt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
