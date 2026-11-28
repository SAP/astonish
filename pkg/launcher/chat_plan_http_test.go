package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// The model drives ordinary tool dispatch; no direct PlanRuntime calls implement
// the task. Separate requests must resume the authoritative ADK snapshot.
type planHTTPModel struct {
	t     *testing.T
	calls []capabilityHTTPCall
	next  atomic.Int32
}

func (*planHTTPModel) Name() string { return "offline-plan" }
func (m *planHTTPModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if len(req.Tools) == 0 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Plan", "model"), TurnComplete: true}, nil)
			return
		}
		n := int(m.next.Add(1)) - 1
		if n > 0 && n <= len(m.calls) {
			var response *genai.FunctionResponse
			for _, c := range req.Contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil {
						response = p.FunctionResponse
					}
				}
			}
			if response == nil {
				m.t.Error("missing plan response")
				return
			}
			raw, _ := json.Marshal(response.Response)
			if !strings.Contains(string(raw), m.calls[n-1].want) {
				m.t.Errorf("%s missing %q: %s", m.calls[n-1].name, m.calls[n-1].want, raw)
				return
			}
		}
		if n >= len(m.calls) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("PLAN_TURN_DONE", "model"), TurnComplete: true}, nil)
			return
		}
		c := m.calls[n]
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprint(n + 1), Name: "execute_tool", Args: map[string]any{"name": c.name, "arguments": c.args}}}}}}, nil)
	}
}

type planHTTPNode struct {
	sandbox.ToolNodeClient
	mu   sync.Mutex
	ids  []string
	exit int
}

func (*planHTTPNode) BindSession(string)       {}
func (*planHTTPNode) EnsureReady(string) error { return nil }
func (n *planHTTPNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if name != "shell_command" || args["command"] != "printf verified" || args["working_dir"] != "/workspace" {
		return nil, fmt.Errorf("not shared plan verifier: %s %v", name, args)
	}
	n.ids = append(n.ids, id)
	return json.Marshal(map[string]any{"exit_code": n.exit, "stdout": "verified"})
}

type planHTTPPool struct {
	continuationHTTPPool
	node *planHTTPNode
}

func (p *planHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient                     { return p.node }
func (p *planHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient { return p.node }
func (p *planHTTPPool) GetOrCreateWithImage(string, string, []string, string) sandbox.ToolNodeClient {
	return p.node
}
func (*planHTTPPool) GetBackend() sandbox.Backend { return nil }

type planHTTPSettings struct {
	store.SettingsStore
	deny atomic.Bool
}

func (s *planHTTPSettings) Get(context.Context) (*store.TeamSettings, error) {
	settings := &store.TeamSettings{}
	if s.deny.Load() {
		settings.DisabledTools = []string{"shell_command"}
	}
	return settings, nil
}

func TestStudioChatRuntimeSharedPlanHTTP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	ctx := t.Context()
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "plan-http", Name: "Plan HTTP", Status: "active"}
	if err = db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = db.Users().Create(ctx, &store.User{ID: owner, Email: "plan@example.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err = db.Organizations().AddMember(ctx, owner, org.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	settings := &planHTTPSettings{}
	svc.Settings = settings
	team := &store.Team{ID: uuid.NewString(), Slug: "general", Name: "General", SchemaName: "team_general"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	svc.PersonalSessions = ods.ForUser(owner).Sessions()
	svc.PlatformMCPServers = db.PlatformMCPServers()
	disabled := false
	for _, s := range config.GetStandardServers() {
		if s.Kind != "model" {
			if err = svc.PlatformMCPServers.Save(ctx, &store.MCPServer{Name: s.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
				t.Fatal(err)
			}
		}
	}
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: "general", Authenticated: true}
	ownerCtx, _ := execution.WithPrincipal(ctx, principal)
	llm := &planHTTPModel{t: t}
	node := &planHTTPNode{exit: 1}
	pool := &planHTTPPool{node: node}
	bootstrap := agent.NewChatAgent(llm, nil, nil, svc.PersonalSessions, &agent.SystemPromptBuilder{}, false, true)
	factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: &config.AppConfig{}, WorkspaceDir: "/workspace"}, ChatCatalogResources{Pool: pool}, nil)
	cm := api.GetChatManager()
	cm.Reset()
	api.SetLLMPool(nil)
	var cleanups atomic.Int32
	api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
		return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: svc.PersonalSessions, SandboxPool: pool, RuntimeFactory: func(c context.Context, r api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
			a, e := factory(c, r)
			if e == nil {
				old := a.Cleanup
				a.Cleanup = func() { old(); cleanups.Add(1) }
			}
			return a, e
		}}, nil
	})
	defer func() { cm.Reset(); api.SetStudioChatInitFunc(nil) }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := execution.WithPrincipal(r.Context(), principal)
		c = store.WithServices(c, svc)
		c = store.WithTenantContext(c, &store.TenantContext{OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug, UserID: owner})
		c = api.WithPlatformUser(c, &api.PlatformUser{ID: owner, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug, Role: "member"})
		settings.deny.Store(r.Header.Get("X-Deny-Shell") != "")
		api.StudioChatHandler(w, r.WithContext(c))
	}))
	defer server.Close()
	server.Client().Timeout = 20 * time.Second
	sessionID := ""
	turn := func(calls []capabilityHTTPCall, approve, deny bool, n int32) {
		t.Helper()
		llm.calls = calls
		llm.next.Store(0)
		body, _ := json.Marshal(map[string]any{"message": "continue plan", "sessionId": sessionID, "planMode": sessionID == "", "autoApprove": true, "approvedPlanExecution": approve})
		r, _ := http.NewRequest("POST", server.URL, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		if deny {
			r.Header.Set("X-Deny-Shell", "1")
		}
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		if resp.StatusCode != 200 || !strings.Contains(string(raw), "PLAN_TURN_DONE") {
			t.Fatalf("HTTP plan: %d %s", resp.StatusCode, raw)
		}
		if sessionID == "" {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "data: ") {
					var v map[string]any
					_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v)
					if id, _ := v["sessionId"].(string); id != "" {
						sessionID = id
					}
				}
			}
		}
		if sessionID == "" {
			t.Fatal("missing session ID")
		}
		deadline := time.Now().Add(time.Second)
		for cleanups.Load() < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if cleanups.Load() != n {
			t.Fatal("cleanup not completed")
		}
	}
	announce := map[string]any{"goal": "HTTP guarded plan", "context": "Controlled HTTP task", "what_not_to_do": "No host execution", "verification": "Observe guarded verifier", "steps": []any{map[string]any{"name": "verify", "description": "Verify scope", "details": "Run the guarded session verifier", "summary": "Prove scoped verification", "outcome": "Verified", "files": []any{map[string]any{"path": "evidence.txt", "kind": "new"}}, "verify": "printf verified", "verify_kind": "behavior"}}}
	turn([]capabilityHTTPCall{{"announce_plan", announce, `"status":"ok"`}}, false, false, 1)
	snapshot := func() agent.PlanSnapshot {
		t.Helper()
		s, e := svc.PersonalSessions.Get(ownerCtx, &session.GetRequest{AppName: "astonish", UserID: owner, SessionID: sessionID})
		if e != nil {
			t.Fatal(e)
		}
		v, e := s.Session.State().Get(agent.PlanSnapshotStateKey)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(v)
		var p agent.PlanSnapshot
		if e = json.Unmarshal(raw, &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	first := snapshot()
	turn([]capabilityHTTPCall{{"update_plan", map[string]any{"step": "verify", "status": "running"}, `"status":"ok"`}, {"update_plan", map[string]any{"step": "verify", "status": "complete"}, agent.PlanStepVerifyFailed}}, true, false, 2)
	if p := snapshot(); p.PlanID != first.PlanID || p.Document.Results != "" || p.Steps[0].Status != "failed" || !strings.Contains(p.Steps[0].Evidence, "exit 1") {
		t.Fatalf("failed verification lost state: %+v", p)
	}
	node.mu.Lock()
	count := len(node.ids)
	node.exit = 0
	node.mu.Unlock()
	if count != 1 {
		t.Fatalf("shared verifier did not execute: %d", count)
	}
	turn([]capabilityHTTPCall{{"update_plan", map[string]any{"step": "verify", "status": "complete"}, agent.PlanStepVerifyFailed}}, false, true, 3)
	node.mu.Lock()
	count = len(node.ids)
	node.mu.Unlock()
	if count != 1 {
		t.Fatal("denied shell executed")
	}
	if p := snapshot(); p.PlanID != first.PlanID || p.Steps[0].Status != "failed" || p.Document.Results != "" {
		t.Fatalf("denied verification published completion: %+v", p)
	}
	turn([]capabilityHTTPCall{{"update_plan", map[string]any{"step": "verify", "status": "complete"}, `"status":"ok"`}, {"announce_completion", map[string]any{"outcome_observed": "HTTP scoped verification proven", "unverified": "Physical container"}, `"status":"ok"`}}, false, false, 4)
	p := snapshot()
	if p.PlanID != first.PlanID || p.Steps[0].Status != "complete" || !strings.Contains(p.Document.Results, "HTTP scoped verification proven") {
		t.Fatalf("missing durable completion: %+v", p)
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if len(node.ids) != 2 {
		t.Fatalf("completion reran verification: %v", node.ids)
	}
	for _, id := range node.ids {
		if id != sessionID {
			t.Fatalf("wrong current session: %s != %s", id, sessionID)
		}
	}
}
