package sandbox

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/store"
)

type argumentTestTool struct {
	testTool
	required  string
	localArgs any
}

func (t *argumentTestTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name: t.Name(),
		ParametersJsonSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{t.required: map[string]any{"type": "string"}},
			"required":   []string{t.required},
		},
	}
}

func (t *argumentTestTool) Run(_ tool.Context, args any) (map[string]any, error) {
	t.localArgs = args
	return map[string]any{"local": true}, nil
}

type argumentSpyClient struct {
	stubClient
	ready, called bool
	args          map[string]any
}

func (c *argumentSpyClient) EnsureReady(string) error {
	c.ready = true
	return nil
}

func (c *argumentSpyClient) Call(_, _ string, args map[string]any) (json.RawMessage, error) {
	c.called = true
	c.args = args
	return json.RawMessage(`{"ok":true}`), nil
}

type argumentSpyPool struct {
	spyPool
	client *argumentSpyClient
}

func (p *argumentSpyPool) GetOrCreate(sessionID string) ToolNodeClient {
	p.spyPool.GetOrCreate(sessionID)
	return p.client
}

func TestNodeToolRun_ValidatesBeforeSandboxDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, required, want string
		args                 any
	}{
		{"missing", "path", `required argument "path" is missing`, map[string]any{}},
		{"null", "path", `required argument "path" must not be null`, map[string]any{"path": nil}},
		{"nil", "path", `required argument "path" is missing`, nil},
		{"typed nil", "path", `required argument "path" is missing`, map[string]any(nil)},
		{"struct missing", "path", `required argument "path" is missing`, struct{ Title string }{Title: "title"}},
		{"before caller injection", "_caller", `required argument "_caller" is missing`, map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &argumentSpyClient{}
			pool := &argumentSpyPool{client: client}
			original := &argumentTestTool{testTool: testTool{name: "load_file"}, required: tc.required}
			nt := NewNodeToolWithPool(original, pool)
			ctx := testToolContext{Context: context.Background(), sessionID: "session-args"}
			_, err := nt.Run(ctx, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Run error = %v, want %s", err, tc.want)
			}
			if pool.method != "" || client.ready || client.called {
				t.Errorf("invalid args touched sandbox: acquisition=%q ready=%v called=%v", pool.method, client.ready, client.called)
			}
			if args, ok := tc.args.(map[string]any); ok {
				if _, injected := args["_caller"]; injected {
					t.Error("invalid arguments were mutated with caller metadata")
				}
			}
		})
	}
}

func TestNodeToolRun_ClonesArgumentsBeforeCallerInjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		args any
	}{
		{"map", map[string]any{"path": "/sandbox/report.md"}},
		{"existing caller", map[string]any{"path": "/sandbox/report.md", "_caller": "original"}},
		{"struct", struct {
			Path string `json:"path"`
		}{Path: "/sandbox/report.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.args)
			client := &argumentSpyClient{}
			nt := NewNodeToolWithPool(&argumentTestTool{testTool: testTool{name: "load_file"}, required: "path"}, &argumentSpyPool{client: client})
			result, err := nt.Run(testToolContext{Context: context.Background(), sessionID: "session-args"}, tc.args)
			if err != nil || result["ok"] != true {
				t.Fatalf("Run = %v, %v", result, err)
			}
			if !client.ready || !client.called || client.args["path"] != "/sandbox/report.md" || client.args["_caller"] != "agent-1" {
				t.Errorf("incorrect dispatch: %+v", client)
			}
			after, _ := json.Marshal(tc.args)
			if string(before) != string(after) {
				t.Errorf("arguments mutated: before=%s after=%s", before, after)
			}
		})
	}
}

func TestNodeToolRun_NoSessionPreservesLocalFallback(t *testing.T) {
	original := &argumentTestTool{testTool: testTool{name: "load_file"}, required: "path"}
	pool := &argumentSpyPool{client: &argumentSpyClient{}}
	nt := NewNodeToolWithPool(original, pool)
	args := struct{ Title string }{Title: "unchanged"}
	result, err := nt.Run(nil, args)
	if err != nil || result["local"] != true || !reflect.DeepEqual(original.localArgs, args) {
		t.Fatalf("local fallback = %v, %v; args=%#v", result, err, original.localArgs)
	}
	if pool.method != "" {
		t.Fatal("local fallback acquired a sandbox client")
	}
}

func TestWrapTools_LoadFile(t *testing.T) {
	load := &argumentTestTool{testTool: testTool{name: "load_file"}, required: "path"}
	host := testTool{name: "memory_search"}
	pool := &spyPool{}
	client := &LazyNodeClient{}
	for _, tc := range []struct {
		name    string
		wrapped []tool.Tool
	}{
		{"pool", WrapToolsWithPool([]tool.Tool{load, host}, pool)},
		{"fleet", WrapToolsWithNodeClient([]tool.Tool{load, host}, client)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nt, ok := tc.wrapped[0].(*NodeTool)
			if !ok {
				t.Fatalf("load_file not sandbox wrapped: %T", tc.wrapped[0])
			}
			if nt.Tool != load || !reflect.DeepEqual(nt.Declaration(), load.Declaration()) {
				t.Error("wrapper did not preserve tool/schema")
			}
			if tc.name == "pool" && nt.pool != pool || tc.name == "fleet" && nt.lazyClient != client {
				t.Error("wrapper lost routing client")
			}
			if tc.wrapped[1] != host {
				t.Error("host tool unexpectedly wrapped")
			}
		})
	}
}

// spyPool records which ToolNodePool method was called and with what args.
type spyPool struct {
	method    string
	session   string
	template  string
	chain     []string
	image     string
	scopeSess string
	scopeOrg  string
	scopeTeam string
}

func (s *spyPool) GetOrCreate(sessionID string) ToolNodeClient {
	s.method = "GetOrCreate"
	s.session = sessionID
	return &stubClient{}
}

func (s *spyPool) GetOrCreateWithTemplate(sessionID, template string) ToolNodeClient {
	s.method = "GetOrCreateWithTemplate"
	s.session = sessionID
	s.template = template
	return &stubClient{}
}

func (s *spyPool) GetOrCreateWithChain(sessionID, template string, chain []string) ToolNodeClient {
	s.method = "GetOrCreateWithChain"
	s.session = sessionID
	s.template = template
	s.chain = chain
	return &stubClient{}
}

func (s *spyPool) GetOrCreateWithImage(sessionID, template string, chain []string, image string) ToolNodeClient {
	s.method = "GetOrCreateWithImage"
	s.session = sessionID
	s.template = template
	s.chain = chain
	s.image = image
	return &stubClient{}
}

func (s *spyPool) Cleanup() {}

func (s *spyPool) GetBackend() Backend { return nil }

func (s *spyPool) Alias(_, _ string) {}

func (s *spyPool) Remove(_ string) {}

func (s *spyPool) SetSessionScope(sessionID, orgSlug, teamSlug string) {
	s.scopeSess = sessionID
	s.scopeOrg = orgSlug
	s.scopeTeam = teamSlug
}

// stubClient satisfies ToolNodeClient for test purposes.
type stubClient struct{}

func (c *stubClient) BindSession(string)       {}
func (c *stubClient) EnsureReady(string) error { return nil }
func (c *stubClient) Call(string, string, map[string]interface{}) (json.RawMessage, error) {
	return nil, nil
}
func (c *stubClient) Close() error   { return nil }
func (c *stubClient) IsClosed() bool { return false }

type preseedSpyClient struct {
	stubClient
	allowed []NetworkAllowEndpoint
	bound   bool
	ready   bool
	called  bool
}

func (c *preseedSpyClient) SetNetworkAllowEndpoints(endpoints []NetworkAllowEndpoint) {
	c.allowed = append([]NetworkAllowEndpoint(nil), endpoints...)
}

func (c *preseedSpyClient) BindSession(string) {
	c.bound = true
}

func (c *preseedSpyClient) EnsureReady(string) error {
	c.ready = true
	return nil
}

func (c *preseedSpyClient) Call(string, string, map[string]interface{}) (json.RawMessage, error) {
	c.called = true
	return json.RawMessage(`{"ok":true}`), nil
}

type preseedSpyPool struct {
	client *preseedSpyClient
}

func (s *preseedSpyPool) GetOrCreate(string) ToolNodeClient                     { return s.client }
func (s *preseedSpyPool) GetOrCreateWithTemplate(string, string) ToolNodeClient { return s.client }
func (s *preseedSpyPool) GetOrCreateWithChain(string, string, []string) ToolNodeClient {
	return s.client
}
func (s *preseedSpyPool) GetOrCreateWithImage(string, string, []string, string) ToolNodeClient {
	return s.client
}
func (s *preseedSpyPool) Cleanup()                       {}
func (s *preseedSpyPool) GetBackend() Backend            { return nil }
func (s *preseedSpyPool) Alias(_, _ string)              {}
func (s *preseedSpyPool) Remove(_ string)                {}
func (s *preseedSpyPool) SetSessionScope(_, _, _ string) {}

type testNetworkPolicyStore struct {
	rules []store.NetworkPolicyRule
}

func (s *testNetworkPolicyStore) List(context.Context) ([]store.NetworkPolicyRule, error) {
	return s.rules, nil
}

func (s *testNetworkPolicyStore) Get(context.Context, string) (*store.NetworkPolicyRule, error) {
	return nil, nil
}

func (s *testNetworkPolicyStore) Save(context.Context, *store.NetworkPolicyRule) error {
	return nil
}

func (s *testNetworkPolicyStore) Delete(context.Context, string) error {
	return nil
}

type testTool struct {
	name string
}

func (t testTool) Name() string        { return t.name }
func (t testTool) Description() string { return "test tool" }
func (t testTool) IsLongRunning() bool { return false }

type testToolContext struct {
	context.Context
	sessionID string
}

func (c testToolContext) InvocationID() string                 { return "invocation-1" }
func (c testToolContext) AgentName() string                    { return "agent-1" }
func (c testToolContext) UserID() string                       { return "user-1" }
func (c testToolContext) AppName() string                      { return "astonish" }
func (c testToolContext) SessionID() string                    { return c.sessionID }
func (c testToolContext) Branch() string                       { return "" }
func (c testToolContext) UserContent() *genai.Content          { return nil }
func (c testToolContext) ReadonlyState() session.ReadonlyState { return nil }
func (c testToolContext) Artifacts() adkagent.Artifacts        { return nil }
func (c testToolContext) State() session.State                 { return nil }
func (c testToolContext) FunctionCallID() string               { return "call-1" }
func (c testToolContext) Actions() *session.EventActions       { return &session.EventActions{} }
func (c testToolContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (c testToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c testToolContext) RequestConfirmation(string, any) error                { return nil }

var _ tool.Context = (*testToolContext)(nil)

func TestNodeToolRun_AppliesAndPreSeedsNetworkPolicyFromContext(t *testing.T) {
	prev := NetworkPolicyPreSeeder
	t.Cleanup(func() { NetworkPolicyPreSeeder = prev })

	var preSeedCalled bool
	var preSeedSession string
	NetworkPolicyPreSeeder = func(ctx context.Context, sessionID string) {
		preSeedCalled = true
		preSeedSession = sessionID
		if nps := store.NetworkPolicyStoresFromContext(ctx); nps == nil || nps.Team == nil {
			t.Fatalf("expected team network policy store in pre-seed context, got %+v", nps)
		}
	}

	client := &preseedSpyClient{}
	nt := &NodeTool{Tool: testTool{name: "http_request"}, pool: &preseedSpyPool{client: client}}
	teamStore := &testNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "*.cloud.sap",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	baseCtx := store.WithNetworkPolicyStores(context.Background(), &store.NetworkPolicyStores{Team: teamStore})
	ctx := testToolContext{Context: baseCtx, sessionID: "flow-run-openstack"}

	result, err := nt.Run(ctx, map[string]interface{}{"url": "https://kubernikus.qa-de-1.cloud.sap/api/v1/clusters"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if ok, _ := result["ok"].(bool); !ok {
		t.Fatalf("expected ok result, got %#v", result)
	}
	if len(client.allowed) != 1 || client.allowed[0].Host != "*.cloud.sap" || client.allowed[0].Port != 443 {
		t.Fatalf("expected *.cloud.sap allow endpoint baked before run, got %+v", client.allowed)
	}
	if !client.ready || !client.called {
		t.Fatalf("expected client to be readied and called, ready=%v called=%v", client.ready, client.called)
	}
	if !preSeedCalled || preSeedSession != "flow-run-openstack" {
		t.Fatalf("expected network policy pre-seed for flow session, called=%v session=%q", preSeedCalled, preSeedSession)
	}
}

func TestNodeToolProcessRequest_AppliesNetworkPolicyBeforeBind(t *testing.T) {
	client := &preseedSpyClient{}
	nt := &NodeTool{Tool: testTool{name: "http_request"}, pool: &preseedSpyPool{client: client}}
	teamStore := &testNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "identity-3.qa-de-1.cloud.sap",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	baseCtx := store.WithNetworkPolicyStores(context.Background(), &store.NetworkPolicyStores{Team: teamStore})
	ctx := testToolContext{Context: baseCtx, sessionID: "flow-run-bind"}

	if err := nt.ProcessRequest(ctx, &model.LLMRequest{Config: &genai.GenerateContentConfig{}}); err != nil {
		t.Fatalf("ProcessRequest returned error: %v", err)
	}
	if len(client.allowed) != 1 || client.allowed[0].Host != "identity-3.qa-de-1.cloud.sap" {
		t.Fatalf("expected policy allow endpoint before bind, got %+v", client.allowed)
	}
	if !client.bound {
		t.Fatal("expected session to be bound")
	}
}

func TestGetClientFromContext_ChainWithoutTemplate(t *testing.T) {
	// When a layer chain is set but no template name exists (the @base-only
	// configured case), getClientFromContext must still pass the chain.
	spy := &spyPool{}
	nt := &NodeTool{pool: spy}

	ctx := context.Background()
	ctx = store.WithSandboxLayerChain(ctx, []string{"@base", "sha256:abc123"})
	// No template set — SandboxTemplateFromContext returns ""

	client := nt.getClientFromContext(ctx, "session-1")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if spy.method != "GetOrCreateWithImage" {
		t.Fatalf("expected GetOrCreateWithImage, got %s", spy.method)
	}
	if spy.template != "" {
		t.Fatalf("expected empty template, got %q", spy.template)
	}
	if len(spy.chain) != 2 || spy.chain[0] != "@base" || spy.chain[1] != "sha256:abc123" {
		t.Fatalf("unexpected chain: %v", spy.chain)
	}
	if spy.image != "" {
		t.Fatalf("expected empty image, got %q", spy.image)
	}
}

func TestGetClientFromContext_ChainWithTemplate(t *testing.T) {
	// When both template and chain are set (team-template case),
	// getClientFromContext passes both.
	spy := &spyPool{}
	nt := &NodeTool{pool: spy}

	ctx := context.Background()
	ctx = store.WithSandboxTemplate(ctx, "my-team-tpl")
	ctx = store.WithSandboxLayerChain(ctx, []string{"@base", "sha256:team123"})

	client := nt.getClientFromContext(ctx, "session-2")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if spy.method != "GetOrCreateWithImage" {
		t.Fatalf("expected GetOrCreateWithImage, got %s", spy.method)
	}
	if spy.template != "my-team-tpl" {
		t.Fatalf("expected template 'my-team-tpl', got %q", spy.template)
	}
	if len(spy.chain) != 2 {
		t.Fatalf("unexpected chain: %v", spy.chain)
	}
}

func TestGetClientFromContext_TemplateNoChain(t *testing.T) {
	// When template is set but no chain (e.g., Incus backend or unresolved),
	// getClientFromContext uses GetOrCreateWithTemplate.
	spy := &spyPool{}
	nt := &NodeTool{pool: spy}

	ctx := context.Background()
	ctx = store.WithSandboxTemplate(ctx, "my-tpl")

	client := nt.getClientFromContext(ctx, "session-3")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if spy.method != "GetOrCreateWithTemplate" {
		t.Fatalf("expected GetOrCreateWithTemplate, got %s", spy.method)
	}
	if spy.template != "my-tpl" {
		t.Fatalf("expected template 'my-tpl', got %q", spy.template)
	}
}

func TestGetClientFromContext_PropagatesTenantScope(t *testing.T) {
	// The caller's org/team from the live request context must be recorded on
	// the session (SetSessionScope) before the pool creates a client, so the
	// container record lands in the caller's own team schema. This is the fix
	// for the cross-tenant sandbox container leak.
	spy := &spyPool{}
	nt := &NodeTool{pool: spy}

	ctx := context.Background()
	ctx = store.WithOrgSlug(ctx, "myorg")
	ctx = store.WithTeamSlug(ctx, "test-b")

	if client := nt.getClientFromContext(ctx, "session-scoped"); client == nil {
		t.Fatal("expected non-nil client")
	}
	if spy.scopeSess != "session-scoped" {
		t.Fatalf("expected scope session 'session-scoped', got %q", spy.scopeSess)
	}
	if spy.scopeOrg != "myorg" {
		t.Fatalf("expected scope org 'myorg', got %q", spy.scopeOrg)
	}
	if spy.scopeTeam != "test-b" {
		t.Fatalf("expected scope team 'test-b', got %q", spy.scopeTeam)
	}
}

func TestGetClientFromContext_NoTenantScopeWhenSlugsAbsent(t *testing.T) {
	// Without tenant slugs in context (e.g. personal/localhost single tenant),
	// getClientFromContext must not call SetSessionScope, so the pool keeps its
	// default registry behavior.
	spy := &spyPool{}
	nt := &NodeTool{pool: spy}

	if client := nt.getClientFromContext(context.Background(), "session-plain"); client == nil {
		t.Fatal("expected non-nil client")
	}
	if spy.scopeSess != "" || spy.scopeOrg != "" || spy.scopeTeam != "" {
		t.Fatalf("expected no scope call, got sess=%q org=%q team=%q", spy.scopeSess, spy.scopeOrg, spy.scopeTeam)
	}
}

func TestNodeClientPool_SessionScopeSelectsResolvedRegistry(t *testing.T) {
	// SetSessionScope must resolve the team-scoped registry via the installed
	// resolver and use it (plus the org/team slugs) for the LazyNodeClient the
	// pool creates for that session — instead of the pool's default registry.
	defaultReg := newTestRegistry(t)
	teamReg := newTestRegistry(t)

	pool := NewNodeClientPool(defaultReg, nil, "", nil)
	var gotOrg, gotTeam string
	pool.SetRegistryResolver(func(orgSlug, teamSlug string) *SessionRegistry {
		gotOrg, gotTeam = orgSlug, teamSlug
		if orgSlug == "myorg" && teamSlug == "test-b" {
			return teamReg
		}
		return nil
	})

	pool.SetSessionScope("sess-1", "myorg", "test-b")
	if gotOrg != "myorg" || gotTeam != "test-b" {
		t.Fatalf("resolver got org=%q team=%q", gotOrg, gotTeam)
	}

	client := pool.GetOrCreate("sess-1")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.sessRegistry != teamReg {
		t.Fatal("expected client to use the resolved team registry, not the pool default")
	}
	if client.OrgSlug != "myorg" || client.TeamSlug != "test-b" {
		t.Fatalf("expected org/team slugs on client, got org=%q team=%q", client.OrgSlug, client.TeamSlug)
	}
}

func TestGetClientFromContext_NoTemplateNoChain(t *testing.T) {
	// When neither template nor chain (personal mode, fresh install),
	// getClientFromContext uses plain GetOrCreate.
	spy := &spyPool{}
	nt := &NodeTool{pool: spy}

	ctx := context.Background()

	client := nt.getClientFromContext(ctx, "session-4")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if spy.method != "GetOrCreate" {
		t.Fatalf("expected GetOrCreate, got %s", spy.method)
	}
}

func TestGetClientFromContext_ImageOnly(t *testing.T) {
	// When image is set but no chain (OpenShell with custom image, no layer chain),
	// getClientFromContext uses GetOrCreateWithImage.
	spy := &spyPool{}
	nt := &NodeTool{pool: spy}

	ctx := context.Background()
	ctx = store.WithSandboxImage(ctx, "ghcr.io/sap/custom-sandbox:v2")

	client := nt.getClientFromContext(ctx, "session-img")
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if spy.method != "GetOrCreateWithImage" {
		t.Fatalf("expected GetOrCreateWithImage, got %s", spy.method)
	}
	if spy.image != "ghcr.io/sap/custom-sandbox:v2" {
		t.Fatalf("expected custom image, got %q", spy.image)
	}
	if len(spy.chain) != 0 {
		t.Fatalf("expected no chain, got %v", spy.chain)
	}
}

func TestGetClientFromContext_LazyClientTakesPriority(t *testing.T) {
	// When a lazyClient is set (fleet sessions), it takes priority
	// regardless of context values.
	spy := &spyPool{}
	lnc := &LazyNodeClient{}
	nt := &NodeTool{pool: spy, lazyClient: lnc}

	ctx := context.Background()
	ctx = store.WithSandboxTemplate(ctx, "ignored")
	ctx = store.WithSandboxLayerChain(ctx, []string{"@base", "sha256:ignored"})

	client := nt.getClientFromContext(ctx, "session-5")
	if client != lnc {
		t.Fatal("expected lazyClient to be returned")
	}
	if spy.method != "" {
		t.Fatalf("pool should not have been called, but got %s", spy.method)
	}
}
