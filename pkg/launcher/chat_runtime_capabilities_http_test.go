package launcher

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	astonishmcp "github.com/SAP/astonish/pkg/mcp"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type capabilityHTTPA2AStore struct {
	store.A2AAgentStore
	value store.A2AAgent
}

func (s *capabilityHTTPA2AStore) Save(_ context.Context, a *store.A2AAgent) error {
	s.value = *a
	return nil
}
func (s *capabilityHTTPA2AStore) List(context.Context) ([]store.A2AAgent, error) {
	return []store.A2AAgent{s.value}, nil
}

type capabilityHTTPCall struct {
	name string
	args map[string]any
	want string
}

// The model supplies only dispatch decisions. Each following model turn checks
// the real function response before allowing the script to advance.
type capabilityHTTPModel struct {
	t     *testing.T
	calls []capabilityHTTPCall
	next  atomic.Int32
}

func (*capabilityHTTPModel) Name() string { return "offline-capability-dispatch" }
func (m *capabilityHTTPModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if len(req.Tools) == 0 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Runtime capabilities", "model"), TurnComplete: true}, nil)
			return
		}
		n := int(m.next.Add(1)) - 1
		if n > 0 {
			var response *genai.FunctionResponse
			for _, content := range req.Contents {
				for _, part := range content.Parts {
					if part.FunctionResponse != nil {
						response = part.FunctionResponse
					}
				}
			}
			if response == nil {
				m.t.Error("dispatch did not return a function response")
				return
			}
			raw, _ := json.Marshal(response.Response)
			if n <= len(m.calls) && !strings.Contains(string(raw), m.calls[n-1].want) {
				m.t.Errorf("%s response missing %q: %s", m.calls[n-1].name, m.calls[n-1].want, raw)
				return
			}
			if strings.Contains(string(raw), `"error":`) {
				m.t.Errorf("tool failed: %s", raw)
				return
			}
		}
		if n >= len(m.calls) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("CAPABILITY_MATRIX_COMPLETE", "model"), TurnComplete: true}, nil)
			return
		}
		call := m.calls[n]
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprint(n + 1), Name: "execute_tool", Args: map[string]any{"name": call.name, "arguments": call.args}}}}}}, nil)
	}
}

// Only the compute transport is replaced: NodeTool still resolves the real pool
// client, and the client invokes the same ExecuteTool dispatcher as astonish node.
// This is NOT physical container evidence. The allowlist confines test effects
// to one temporary path and a fixed harmless shell command.
type capabilityHTTPNode struct {
	root     string
	mcpURL   string
	mu       sync.Mutex
	sessions []string
	names    []string
}

func (*capabilityHTTPNode) BindSession(string)       {}
func (*capabilityHTTPNode) EnsureReady(string) error { return nil }
func (n *capabilityHTTPNode) Call(sessionID, name string, args map[string]any) (json.RawMessage, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if sessionID == "" {
		return nil, fmt.Errorf("missing session identity")
	}
	switch name {
	case "read_file", "write_file":
		if args["path"] != filepath.Join(n.root, "dispatch.txt") {
			return nil, fmt.Errorf("unexpected file path: %v", args)
		}
	case "shell_command":
		if args["command"] != "printf runtime-shell-ok" {
			return nil, fmt.Errorf("unexpected shell command")
		}
	case astonishmcp.NodeOperation:
		encoded, _ := json.Marshal(args)
		var request astonishmcp.NodeRequest
		if err := json.Unmarshal(encoded, &request); err != nil {
			return nil, err
		}
		if n.mcpURL == "" || request.Config.URL != n.mcpURL || request.Config.Transport != "streamableHTTP" {
			return nil, fmt.Errorf("unexpected MCP endpoint")
		}
	case "process_list":
	default:
		return nil, fmt.Errorf("unexpected node operation %s", name)
	}
	n.sessions = append(n.sessions, sessionID)
	n.names = append(n.names, name)
	nodeCtx := store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil))
	result, err := tools.ExecuteTool(nodeCtx, name, args, sessionID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

type capabilityHTTPPool struct {
	continuationHTTPPool
	node *capabilityHTTPNode
}

func (p *capabilityHTTPPool) GetBackend() sandbox.Backend               { return nil }
func (p *capabilityHTTPPool) GetOrCreate(string) sandbox.ToolNodeClient { return p.node }
func (p *capabilityHTTPPool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p.node
}
func (p *capabilityHTTPPool) GetOrCreateWithImage(string, string, []string, string) sandbox.ToolNodeClient {
	return p.node
}

// TestStudioChatRuntimeCapabilitiesHTTP exercises real HTTP/SSE, scoped SQL
// settings/session stores, the production factory, ToolIndex/execute_tool,
// callbacks, NodeTool and the actual node dispatcher. A local OpenAI-compatible
// endpoint is the configured successful Perplexity provider boundary, not a
// sentinel error or a replacement search tool. No paid provider is contacted.
func TestStudioChatRuntimeCapabilitiesHTTP(t *testing.T) {
	testStudioChatRuntimeCapabilitiesHTTP(t, false, false, false)
}

// Remote MCP currently reaches the stdio-only sandbox initializer. Keep this
// successful-protocol acceptance separate so the missing wiring is visible.
func TestStudioChatRuntimeRemoteMCPHTTP(t *testing.T) {
	testStudioChatRuntimeCapabilitiesHTTP(t, true, false, false)
}

// This acceptance requires the real A2A adapter to satisfy shared dispatch.
func TestStudioChatRuntimeA2AHTTP(t *testing.T) {
	testStudioChatRuntimeCapabilitiesHTTP(t, false, true, false)
}
func TestStudioChatRuntimeTemplatesHTTP(t *testing.T) {
	testStudioChatRuntimeCapabilitiesHTTP(t, false, false, true)
}

func testStudioChatRuntimeCapabilitiesHTTP(t *testing.T, withMCP, withA2A, withTemplates bool) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	ctx := t.Context()
	svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "capabilities", Name: "Capabilities", Status: "active"}
	if err := db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err := db.Users().Create(ctx, &store.User{ID: owner, Email: "capabilities@example.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Organizations().AddMember(ctx, owner, org.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "general", Name: "General", SchemaName: "team_general"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	svc.PersonalSessions = ods.ForUser(owner).Sessions()
	svc.PersonalFlows = &productionFlowStore{yaml: continuationHTTPYAML}
	svc.PlatformMCPServers = db.PlatformMCPServers()
	disabled := false
	for _, standard := range config.GetStandardServers() {
		if standard.Kind != "model" {
			if err := svc.PlatformMCPServers.Save(ctx, &store.MCPServer{Name: standard.ID, Transport: "stdio", Enabled: &disabled}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var mcpCalls atomic.Int32
	mcpServer := sdk.NewServer(&sdk.Implementation{Name: "runtime-http-fixture", Version: "1"}, nil)
	sdk.AddTool(mcpServer, &sdk.Tool{Name: "echo", Description: "Echo local protocol fixture"}, func(ctx context.Context, req *sdk.CallToolRequest, args struct {
		Message string `json:"message"`
	}) (*sdk.CallToolResult, any, error) {
		if args.Message != "runtime-mcp-question" {
			t.Errorf("wrong MCP request %q", args.Message)
		}
		mcpCalls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "runtime-mcp-ok"}}}, nil, nil
	})
	mcpHTTP := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return mcpServer }, nil))
	defer mcpHTTP.Close()
	if err := svc.PlatformMCPServers.Save(ctx, &store.MCPServer{Enabled: &withMCP, Name: "runtime-http", Transport: "streamableHTTP", URL: mcpHTTP.URL, CachedTools: json.RawMessage(`[{"name":"echo","description":"Echo local protocol fixture","inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}}]`)}); err != nil {
		t.Fatal(err)
	}
	var a2aCalls atomic.Int32
	var remote *httptest.Server
	remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent-card.json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "runtime-remote", "description": "runtime remote fixture", "url": remote.URL, "version": "1", "protocolVersion": "0.3.0", "capabilities": map[string]any{"streaming": false}, "skills": []any{}})
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(request)
		if request["method"] != "message/send" || !strings.Contains(string(raw), "runtime-a2a-question") {
			t.Errorf("unexpected A2A request %s", raw)
		}
		a2aCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"kind": "task", "id": "runtime-a2a-task", "status": map[string]any{"state": "completed", "message": map[string]any{"role": "agent", "parts": []any{map[string]any{"kind": "text", "text": "runtime-a2a-ok"}}}}}})
	}))
	defer remote.Close()
	// Explicit administrator configuration grants this local fixture endpoint;
	// no ambient agent or external credential is consulted.
	svc.PlatformA2AAgents = &capabilityHTTPA2AStore{}
	if err := svc.PlatformA2AAgents.Save(ctx, &store.A2AAgent{Enabled: &withA2A, Name: "runtime_remote", URL: remote.URL, Timeout: "5s"}); err != nil {
		t.Fatal(err)
	}
	var searches atomic.Int32
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected provider path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(body)
		if body["model"] != "sonar-fixture" || !strings.Contains(string(raw), "runtime search question") {
			t.Errorf("wrong configured provider request %s", raw)
		}
		searches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"search-fixture","object":"chat.completion","model":"sonar-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"runtime-search-ok https://example.invalid/source"},"finish_reason":"stop"}]}`)
	}))
	defer search.Close()
	if err := svc.PlatformSettings.Save(ctx, &store.PlatformSettings{WebSearchTool: "perplexity:perplexity_web_search", PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: "search-fixture", Model: "sonar-fixture"}, Providers: map[string]store.ProviderConfig{"search-fixture": {"type": "lm_studio", "base_url": search.URL + "/v1"}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dispatch.txt")
	llm := &capabilityHTTPModel{t: t, calls: []capabilityHTTPCall{
		{"write_file", map[string]any{"path": path, "content": "runtime-file-ok"}, "dispatch.txt"},
		{"read_file", map[string]any{"path": path}, "runtime-file-ok"},
		{"shell_command", map[string]any{"command": "printf runtime-shell-ok", "working_dir": dir}, "runtime-shell-ok"},
		{"process_list", map[string]any{}, "sessions"},
		{"perplexity_web_search", map[string]any{"query": "runtime search question"}, "runtime-search-ok"},
		{"run_flow", map[string]any{"flow_name": "flow", "parameters": map[string]any{"seed": "seed", "answer": "answer"}}, "continued original HTTP flow"},
	}}
	if withMCP {
		llm.calls = append(llm.calls, capabilityHTTPCall{"echo", map[string]any{"message": "runtime-mcp-question"}, "runtime-mcp-ok"})
	}
	if withA2A {
		llm.calls = append(llm.calls, capabilityHTTPCall{"a2a_runtime_remote", map[string]any{"message": "runtime-a2a-question"}, "runtime-a2a-ok"})
	}
	if withTemplates {
		llm.calls = append(llm.calls, capabilityHTTPCall{"list_sandbox_templates", map[string]any{}, "runtime-template-ok"})
	}
	node := &capabilityHTTPNode{root: dir, mcpURL: mcpHTTP.URL}
	if withMCP {
		tools.SetAllowPrivateNetworks(true)
		defer tools.SetAllowPrivateNetworks(false)
	}
	pool := &capabilityHTTPPool{node: node}
	bootstrap := agent.NewChatAgent(llm, nil, nil, svc.PersonalSessions, &agent.SystemPromptBuilder{}, false, true)
	resources := ChatCatalogResources{Pool: pool}
	if withTemplates {
		registry, err := sandbox.NewTemplateRegistry()
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Add(&sandbox.TemplateMeta{Name: "runtime-template-ok", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		resources.Backend = &scopedBrowserTestBackend{t: t, owner: owner}
		resources.TemplateRegistry = registry
	}
	factory := newStudioChatRuntimeFactory(ChatFactoryConfig{AppConfig: &config.AppConfig{}}, resources, nil)
	cm := api.GetChatManager()
	cm.Reset()
	api.SetLLMPool(nil)
	var attempts, cleanups atomic.Int32
	api.SetStudioChatInitFunc(func(context.Context) (*api.StudioChatComponents, error) {
		return &api.StudioChatComponents{ChatAgent: bootstrap, SessionService: svc.PersonalSessions, SandboxPool: pool, RuntimeFactory: func(ctx context.Context, req api.StudioChatRuntimeRequest) (*api.StudioChatRuntime, error) {
			runtime, err := factory(ctx, req)
			if err != nil {
				t.Errorf("production runtime construction: %v", err)
				return nil, err
			}
			attempts.Add(1)
			if runtime.ChatAgent.ToolIndex == nil {
				t.Error("missing production tool index")
			}
			cleanup := runtime.Cleanup
			runtime.Cleanup = func() { cleanup(); cleanups.Add(1) }
			return runtime, nil
		}}, nil
	})
	defer func() { cm.Reset(); api.SetStudioChatInitFunc(nil) }()
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: "general", Authenticated: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := execution.WithPrincipal(r.Context(), principal)
		c = store.WithServices(c, svc)
		c = store.WithTenantContext(c, &store.TenantContext{OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug, UserID: owner})
		c = api.WithPlatformUser(c, &api.PlatformUser{ID: owner, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug, Role: "member"})
		api.StudioChatHandler(w, r.WithContext(c))
	}))
	defer server.Close()
	server.Client().Timeout = 30 * time.Second
	response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(`{"message":"exercise runtime capabilities","autoApprove":true,"pinnedToolGroups":["core","process","web","flows"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(body), "CAPABILITY_MATRIX_COMPLETE") {
		t.Fatalf("HTTP dispatch %d: %s", response.StatusCode, body)
	}
	deadline := time.Now().Add(time.Second)
	for cleanups.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if attempts.Load() != 1 || cleanups.Load() != 1 || searches.Load() != 1 || llm.next.Load() != int32(len(llm.calls)+1) {
		t.Fatalf("incomplete dispatch attempts=%d cleanup=%d search=%d turns=%d", attempts.Load(), cleanups.Load(), searches.Load(), llm.next.Load())
	}
	if withMCP && mcpCalls.Load() != 1 {
		t.Fatalf("MCP protocol calls=%d", mcpCalls.Load())
	}
	if withA2A && a2aCalls.Load() != 1 {
		t.Fatalf("A2A protocol calls=%d", a2aCalls.Load())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "runtime-file-ok" {
		t.Fatalf("actual file bytes %q: %v", data, err)
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	commonNames := []string{}
	for _, name := range node.names {
		if name != astonishmcp.NodeOperation {
			commonNames = append(commonNames, name)
		}
	}
	if strings.Join(commonNames, ",") != "write_file,read_file,shell_command,process_list" {
		t.Fatalf("node dispatch %v", node.names)
	}
	for _, id := range node.sessions {
		if id != node.sessions[0] {
			t.Fatal("node session identity changed")
		}
	}
}
