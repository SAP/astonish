package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Local transport acceptance only: the real catalog, delegate_tasks, child ADK
// runner, NodeTool and ExecuteTool dispatch run here. The node transport is bounded
// to a test-owned file; it is not Docker/Kubernetes or HTTP/scheduler admission.
// Tool preferences are deliberately NOT treated as an authority allow-list.
type delegateExecutionPool struct {
	sandbox.ToolNodePool
	mu               sync.Mutex
	node             *delegateExecutionNode
	aliases          map[string]string
	created, removed []string
	scopes           map[string][2]string
	cleaned          bool
}

func (p *delegateExecutionPool) GetBackend() sandbox.Backend { return p.node.backend }
func (p *delegateExecutionPool) GetOrCreate(id string) sandbox.ToolNodeClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.cleaned && (id == p.node.session || p.aliases[id] == p.node.session) {
		return p.node
	}
	return nil
}
func (p *delegateExecutionPool) GetOrCreateWithTemplate(id, _ string) sandbox.ToolNodeClient {
	return p.GetOrCreate(id)
}
func (p *delegateExecutionPool) GetOrCreateWithChain(id, _ string, _ []string) sandbox.ToolNodeClient {
	return p.GetOrCreate(id)
}
func (p *delegateExecutionPool) GetOrCreateWithImage(id, _ string, _ []string, _ string) sandbox.ToolNodeClient {
	return p.GetOrCreate(id)
}
func (p *delegateExecutionPool) SetSessionScope(id, org, team string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes[id] = [2]string{org, team}
}
func (p *delegateExecutionPool) Alias(child, parent string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if parent != p.node.session && p.aliases[parent] != p.node.session {
		panic("foreign delegate parent")
	}
	p.aliases[child] = p.node.session
	p.created = append(p.created, child)
}
func (p *delegateExecutionPool) LeaseChildAlias(child, parent string) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.aliases[child]; exists || child == p.node.session {
		return nil
	}
	if parent != p.node.session && p.aliases[parent] != p.node.session {
		return nil
	}
	p.aliases[child] = p.node.session
	p.created = append(p.created, child)
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			delete(p.aliases, child)
			p.removed = append(p.removed, child)
		})
	}
}

func (p *delegateExecutionPool) Remove(id string) {
	panic("runtime used destructive pool Remove instead of alias release")
}

func (p *delegateExecutionPool) Cleanup() { p.mu.Lock(); defer p.mu.Unlock(); p.cleaned = true }

type delegateExecutionNode struct {
	sandbox.ToolNodeClient
	pool          *delegateExecutionPool
	logicalIDs    []string
	backend       sandbox.Backend
	session, path string
	mu            sync.Mutex
	calls         []string
	outputs       []map[string]any
	afterRead     func()
}

func (n *delegateExecutionNode) SandboxSessionID() string { return n.session }
func (n *delegateExecutionNode) BindSession(string)       {} // immutable owner; logical aliases never rebind it
func (n *delegateExecutionNode) EnsureReady(id string) error {
	if id != n.session {
		return fmt.Errorf("foreign readiness %s", id)
	}
	return nil
}
func (n *delegateExecutionNode) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	logical := id
	if n.pool.GetOrCreate(id) != n {
		return nil, fmt.Errorf("foreign logical session %s", id)
	}
	id = n.session // same owner-pinned transport semantics as backendNodeClient
	switch name {
	case "read_file":
		if args["path"] != n.path {
			return nil, fmt.Errorf("unconfined read %v", args)
		}
	case "shell_command":
		if args["command"] != "printf services-parity-ok" || args["working_dir"] != filepath.Dir(n.path) {
			return nil, fmt.Errorf("unconfined command %v", args)
		}
	default:
		return nil, fmt.Errorf("unexpected node operation %s", name)
	}
	result, err := tools.ExecuteTool(context.Background(), name, args, id)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.logicalIDs = append(n.logicalIDs, logical)
	n.calls = append(n.calls, name)
	n.outputs = append(n.outputs, out)
	after := n.afterRead
	n.mu.Unlock()
	if name == "read_file" && after != nil {
		after()
	}
	return raw, nil
}

type delegateExecutionModel struct {
	t                  *testing.T
	mode               string
	path, expected     string
	shell              map[string]any
	inspect            func(context.Context)
	beforeSecondRead   func()
	waitForCleanup     func(context.Context)
	calls, active      atomic.Int32
	sawRead, sawDenial atomic.Bool
}

func (*delegateExecutionModel) Name() string { return "offline-actual-delegate-read" }
func (m *delegateExecutionModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.active.Add(1)
		defer m.active.Add(-1)
		m.inspect(ctx)
		call := m.calls.Add(1)
		if m.waitForCleanup != nil {
			m.waitForCleanup(ctx)
			yield(nil, ctx.Err())
			return
		}
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse == nil {
					continue
				}
				response := part.FunctionResponse.Response
				if response["content"] == m.expected {
					m.sawRead.Store(true)
				}
				raw, _ := json.Marshal(response)
				if strings.Contains(string(raw), "outside the child scope") || strings.Contains(string(raw), "child consequential action prohibited") {
					m.sawDenial.Store(true)
				}
			}
		}
		var fc *genai.FunctionCall
		switch call {
		case 1:
			name, args := "delegate_tasks", map[string]any{"tasks": []any{}}
			if m.mode == "autonomous-bot" {
				name, args = "shell_command", m.shell
			}
			fc = &genai.FunctionCall{ID: "parent-only", Name: "execute_tool", Args: map[string]any{"name": name, "arguments": args}}
		case 2:
			fc = &genai.FunctionCall{ID: "first-read", Name: "read_file", Args: map[string]any{"path": m.path, "force": true}}
		case 3:
			if !m.sawRead.Load() {
				m.t.Error("child model did not receive exact first read bytes")
			}
			if m.beforeSecondRead != nil {
				m.beforeSecondRead()
				fc = &genai.FunctionCall{ID: "second-read", Name: "read_file", Args: map[string]any{"path": m.path, "force": true}}
			} else {
				yield(&model.LLMResponse{Content: genai.NewContentFromText("verified child read: "+m.expected, "model"), TurnComplete: true}, nil)
				return
			}
		default:
			m.t.Error("child model ran after fresh authority loss")
			yield(nil, fmt.Errorf("unexpected child model dispatch"))
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: fc}}}}, nil)
	}
}

// Reconstruct the real mode factory with an aliased pool and scripted model. The
// existing SQLite membership fixture installs production live authority checks.
func newDelegateExecutionRuntime(t *testing.T, scope chatruntime.ResourceScope, mode string) (*servicesParityRuntime, liveMembershipFixture, *delegateExecutionPool, *delegateExecutionModel, func()) {
	t.Helper()
	r := newServicesParityRuntime(t, scope, mode)
	membership := newLiveMembershipFixture(t, r, scope)
	// Installs the interactive registry/current-principal authorizer when required.
	liveMembershipReconstruct(t, r, membership, scope, mode)
	path := filepath.Join(r.node.root, "delegated.txt")
	const body = "delegated exact bytes: α\nsecond line\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	node := &delegateExecutionNode{backend: r.node.backend, session: r.node.session, path: path}
	pool := &delegateExecutionPool{node: node, aliases: map[string]string{}, scopes: map[string][2]string{}}
	node.pool = pool
	m := &delegateExecutionModel{t: t, mode: mode, path: path, expected: "1: delegated exact bytes: α\n2: second line", shell: map[string]any{"command": "printf services-parity-ok", "working_dir": r.node.root}}
	resources := ChatCatalogResources{Pool: pool, Backend: node.backend}
	var cleanup func()
	if mode == "ordinary-chat" {
		cfg := &config.AppConfig{}
		inputs, err := chatruntime.Build(r.f.ctx, chatruntime.BuildOptions{Principal: r.f.req.Principal, Services: r.f.services, ResourceScope: scope, AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = inputs.Close() })
		runtime, err := NewScopedChatRuntime(r.f.ctx, ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: r.node.root}, Resources: &resources, LLM: m, Sessions: r.f.sessions, SessionID: node.session, UseScopedEngines: true}, ChatRuntimeHooks{})
		if err != nil {
			t.Fatal(err)
		}
		r.chat, cleanup = runtime.ChatAgent, runtime.Cleanup
	} else {
		if mode == "autonomous-bot" {
			drillGrants(t, r.f, map[string][]map[string]any{"read_file": nil, "delegate_tasks": {{"tasks": []any{map[string]any{"name": "reader", "task": "Read the scoped file", "tools": []string{"read_file"}}}}}, "shell_command": {m.shell}})
		}
		prepare := r.f.cfg.Prepare
		r.f.cfg.Prepare = func(ctx context.Context, req bot.RunRequest, scope *BotChatScope) (*BotChatSafeResources, error) {
			safe, err := prepare(ctx, req, scope)
			if err == nil {
				safe.Catalog = &resources
			}
			return safe, err
		}
		r.f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return m, nil }
		runtime, err := r.f.construct(t)
		if err != nil {
			t.Fatal(err)
		}
		r.chat, cleanup = runtime.Agent, runtime.Cleanup
	}
	t.Cleanup(cleanup)
	parent, release, err := childAuthorityBinding(t, r).bind(r.f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	principal, _ := execution.PrincipalFromContext(parent)
	credentials := store.CredentialStoreFromContext(parent)
	m.inspect = func(ctx context.Context) {
		actual, ok := execution.PrincipalFromContext(ctx)
		if !ok || !reflect.DeepEqual(actual, principal) || store.CredentialStoreFromContext(ctx) != credentials {
			t.Error("child changed owner, tenant, or credential scope")
		}
		if credentials == nil {
			t.Error("child credentials missing")
		} else if credential := credentials.Get(ctx, "mail"); credential == nil || credential.Username != "owner" {
			t.Error("child resolved foreign credentials")
		}
		identity, hasIdentity := execution.BotRunIdentityFromContext(ctx)
		policy := execution.RunPolicyFromContext(ctx)
		_, interactive := execution.BotTurnAuthorizerFromContext(ctx)
		switch mode {
		case "ordinary-chat":
			if hasIdentity || policy != nil || interactive {
				t.Error("ordinary child acquired Bot authority")
			}
		case "interactive-bot":
			if !hasIdentity || policy != nil || !interactive {
				t.Error("interactive child authority changed")
			}
		case "autonomous-bot":
			if !hasIdentity || identity != r.f.req.Policy.Identity() || policy == nil || policy == r.f.req.Policy || interactive {
				t.Error("autonomous child policy not narrowed")
			}
		}
	}
	return r, membership, pool, m, cleanup
}

func TestChatRuntimeDelegateAliasCleanupConcurrent(t *testing.T) {
	r, _, pool, _, cleanup := newDelegateExecutionRuntime(t, chatruntime.ResourceScopePersonal, "ordinary-chat")
	pool.aliases["preexisting-child"] = pool.node.session
	callback := r.chat.SubAgentManager.OnChildSession
	callback(pool.node.session, "preexisting-child") // borrowed even when announced again
	var children sync.WaitGroup
	for i := range 32 {
		children.Add(1)
		go func() {
			defer children.Done()
			child := fmt.Sprintf("request-child-%d", i)
			callback(pool.node.session, child)
			callback(pool.node.session, child) // retries must not double-own aliases
		}()
	}
	children.Wait()
	var cleanups sync.WaitGroup
	for range 8 {
		cleanups.Add(1)
		go func() { defer cleanups.Done(); cleanup() }()
	}
	cleanups.Wait()
	callback(pool.node.session, "late-child")
	if _, err := r.invoke(t, r.f.ctx, "delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "reader", "task": "Read the scoped file", "tools": []string{"read_file"}}}}); err == nil {
		t.Error("delegate admitted after cleanup")
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.created) != 32 || len(pool.removed) != 32 || len(pool.aliases) != 1 || pool.aliases["preexisting-child"] != pool.node.session || pool.cleaned {
		t.Fatalf("incorrect concurrent alias cleanup: created=%v removed=%v aliases=%v closed=%v", pool.created, pool.removed, pool.aliases, pool.cleaned)
	}
	seen := map[string]bool{}
	for _, child := range pool.removed {
		if seen[child] {
			t.Errorf("duplicate removal: %s", child)
		}
		seen[child] = true
	}
}

func TestChatRuntimeDelegateCleanupJoinsBeforeAliasRemoval(t *testing.T) {
	r, _, pool, m, cleanup := newDelegateExecutionRuntime(t, chatruntime.ResourceScopePersonal, "ordinary-chat")
	pool.aliases["preexisting-child"] = pool.node.session
	started, canceled, allowReturn := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(allowReturn) })
	m.waitForCleanup = func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-allowReturn
	}
	ctx, cancel := context.WithTimeout(r.f.ctx, 10*time.Second)
	defer cancel()
	invoked := make(chan struct{})
	go func() {
		defer close(invoked)
		_, _ = r.invoke(t, ctx, "delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "reader", "task": "Read the scoped file", "tools": []string{"read_file"}}}})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("delegate did not start")
	}
	cleaned := make(chan struct{})
	go func() { cleanup(); close(cleaned) }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("cleanup did not cancel delegate")
	}
	pool.mu.Lock()
	if len(pool.created) != 1 || len(pool.removed) != 0 || len(pool.aliases) != 2 {
		t.Errorf("alias removed before child joined: created=%v removed=%v aliases=%v", pool.created, pool.removed, pool.aliases)
	}
	pool.mu.Unlock()
	select {
	case <-cleaned:
		t.Error("cleanup returned before child joined")
	default:
	}
	release.Do(func() { close(allowReturn) })
	select {
	case <-cleaned:
	case <-ctx.Done():
		t.Fatal("cleanup did not join delegate")
	}
	<-invoked
	cleanup()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if m.active.Load() != 0 || !reflect.DeepEqual(pool.removed, pool.created) || len(pool.aliases) != 1 || pool.aliases["preexisting-child"] != pool.node.session || pool.cleaned {
		t.Errorf("incorrect joined cleanup: active=%d created=%v removed=%v aliases=%v pool closed=%v", m.active.Load(), pool.created, pool.removed, pool.aliases, pool.cleaned)
	}
}

func TestChatRuntimeActualDelegateReadFreshAuthority(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			fences := []string{"none", "membership-before-model", "membership-before-tool"}
			if mode != "ordinary-chat" {
				fences = append(fences, "lease-before-model", "lease-before-tool")
			}
			for _, fence := range fences {
				t.Run(string(scope)+"/"+mode+"/"+fence, func(t *testing.T) {
					r, membership, pool, m, cleanup := newDelegateExecutionRuntime(t, scope, mode)
					pool.aliases["preexisting-child"] = pool.node.session
					originalLease := r.f.req.Run.Lease
					var revoked atomic.Bool
					revoke := func() {
						if !revoked.CompareAndSwap(false, true) {
							return
						}
						if strings.HasPrefix(fence, "membership") {
							membership.revoke()
						} else {
							r.f.req.Run.Lease.Fence++
						}
					}
					if strings.HasSuffix(fence, "before-model") {
						pool.node.afterRead = revoke
					}
					if strings.HasSuffix(fence, "before-tool") {
						m.beforeSecondRead = revoke
					}
					ctx, cancel := context.WithTimeout(r.f.ctx, 10*time.Second)
					defer cancel()
					out, err := r.invoke(t, ctx, "delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "reader", "task": "Read the scoped file", "tools": []string{"read_file"}}}})
					raw, _ := json.Marshal(out)
					var result tools.DelegateTasksResult
					if decodeErr := json.Unmarshal(raw, &result); decodeErr != nil {
						t.Fatal(decodeErr)
					}
					if err != nil || len(result.Results) != 1 {
						t.Fatalf("actual delegate invocation failed: %s err=%v", raw, err)
					}
					child := result.Results[0]
					if child.ChildSessionID == "" || child.ChildSessionID == r.node.session || child.Attempts != 1 || m.active.Load() != 0 {
						t.Fatalf("child did not join exactly once: %+v active=%d", child, m.active.Load())
					}
					if !m.sawDenial.Load() {
						t.Fatalf("missing actual parent-only child denial: %s", raw)
					}
					wantCalls := int32(3)
					if strings.HasSuffix(fence, "before-model") {
						wantCalls = 2
					}
					if m.calls.Load() != wantCalls {
						t.Errorf("model dispatches=%d want=%d: %s", m.calls.Load(), wantCalls, raw)
					}
					if fence == "none" {
						if child.Status != "success" || !m.sawRead.Load() || !strings.Contains(child.Result, m.expected) {
							t.Errorf("successful child read missing: %s", raw)
						}
					} else if child.Status == "success" || !revoked.Load() {
						t.Errorf("child escaped fresh authority loss: %s", raw)
					}
					pool.node.mu.Lock()
					if len(pool.node.logicalIDs) != 1 || pool.node.logicalIDs[0] != child.ChildSessionID {
						t.Errorf("read did not traverse the child alias: %v", pool.node.logicalIDs)
					}
					if len(pool.node.calls) != 1 || pool.node.calls[0] != "read_file" || pool.node.outputs[0]["content"] != m.expected {
						t.Errorf("expected exactly one real read with exact bytes: calls=%v outputs=%v", pool.node.calls, pool.node.outputs)
					}
					pool.node.afterRead = nil
					pool.node.mu.Unlock()
					wantGrants := 0
					if mode == "autonomous-bot" {
						wantGrants = 1
					} // parent delegate admission only
					if len(r.guard.consumed) != wantGrants {
						t.Errorf("child borrowed parent grant: %v", r.guard.consumed)
					}
					if fence != "none" {
						if _, parentErr := r.invoke(t, r.f.ctx, "read_file", map[string]any{"path": m.path, "force": true}); parentErr == nil {
							t.Error("parent escaped revoked authority")
						}
						if strings.HasPrefix(fence, "membership") {
							membership.restore()
						} else {
							r.f.req.Run.Lease = originalLease
						}
					}
					// Same parent action and same unused shell grant after child return.
					if _, err := r.invoke(t, r.f.ctx, "shell_command", m.shell); err != nil {
						t.Errorf("parent positive control: %v", err)
					}
					pool.node.mu.Lock()
					if len(pool.node.calls) != 2 || pool.node.calls[1] != "shell_command" || pool.node.outputs[1]["stdout"] != "services-parity-ok" || pool.node.logicalIDs[1] != r.node.session {
						t.Errorf("parent control was not owner-scoped or a denied operation reached the node: %v %v", pool.node.calls, pool.node.outputs)
					}
					pool.node.mu.Unlock()
					if mode == "autonomous-bot" && len(r.guard.consumed) != 2 {
						t.Error("parent's previously unused exact shell grant was not consumed")
					}
					if m.calls.Load() != wantCalls || m.active.Load() != 0 {
						t.Error("child continued after joined return")
					}
					cleanup()
					cleanup() // once-only removal, including t.Cleanup's later call
					pool.mu.Lock()
					if len(pool.created) != 1 || pool.created[0] != child.ChildSessionID {
						t.Errorf("child did not use production alias callback: %v", pool.created)
					}
					if len(pool.aliases) != 1 || pool.aliases["preexisting-child"] != pool.node.session {
						t.Errorf("runtime cleanup retained owned aliases or removed a borrowed alias: %v", pool.aliases)
					}
					if !reflect.DeepEqual(pool.removed, []string{child.ChildSessionID}) {
						t.Errorf("aliases not removed exactly once: %v", pool.removed)
					}
					if pool.cleaned {
						t.Error("runtime destroyed borrowed parent pool")
					}
					pool.mu.Unlock()
					if pool.GetOrCreate(r.node.session) != pool.node {
						t.Error("parent node lost after child cleanup")
					}
				})
			}
		}
	}
}
