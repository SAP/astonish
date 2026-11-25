package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

type threeModePool struct {
	capabilityHTTPPool
	client *threeModeNode
}

func (p *threeModePool) GetOrCreate(string) sandbox.ToolNodeClient { return p.client }
func (p *threeModePool) GetOrCreateWithTemplate(string, string) sandbox.ToolNodeClient {
	return p.client
}
func (p *threeModePool) GetOrCreateWithImage(string, string, []string, string) sandbox.ToolNodeClient {
	return p.client
}

type threeModeNode struct{ *capabilityHTTPNode }

func (n *threeModeNode) Call(sessionID, name string, args map[string]any) (json.RawMessage, error) {
	switch name {
	case "filter_json":
	case "edit_file":
		if args["path"] != filepath.Join(n.root, "dispatch.txt") {
			return nil, fmt.Errorf("unexpected file path: %v", args)
		}
	case "find_files":
		if args["search_path"] != n.root {
			return nil, fmt.Errorf("unexpected discovery path: %v", args)
		}
	case "file_tree":
		if args["path"] != n.root {
			return nil, fmt.Errorf("unexpected tree path: %v", args)
		}
	default:
		return n.capabilityHTTPNode.Call(sessionID, name, args)
	}
	if sessionID == "" {
		return nil, fmt.Errorf("missing session identity")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.names = append(n.names, name)
	n.sessions = append(n.sessions, sessionID)
	result, err := tools.ExecuteTool(context.Background(), name, args, sessionID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

type threeModeToolContext struct{ botFactoryHandoffContext }

func (threeModeToolContext) AgentName() string { return "three-mode-fixture" }

// This is a configured constructor/dispatch subset, not the full capability or
// physical-backend matrix. Bot modes use their actual factory and authorization
// adapters; only the node transport and authoritative repository are fixtures.
// The node executes real built-in file/discovery tools, a fixed harmless shell
// command and process listing in personal and team scopes. Progressive dispatch
// resolves the same indexed executable. Chat and interactive Bot mutate the file;
// autonomous writes/edits/shell/process listing require exact grants that this
// evaluation-only guard cannot consume, and must remain denied.
func testChatRuntimeConfiguredThreeModeSubset(t *testing.T) {
	for _, scope := range []chatruntime.ResourceScope{chatruntime.ResourceScopePersonal, chatruntime.ResourceScopeTeam} {
		for _, mode := range []string{"ordinary-chat", "interactive-bot", "autonomous-bot"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				if scope == chatruntime.ResourceScopeTeam {
					f.req.Bot.Scope = store.BotScopeTeam
				}
				dir := t.TempDir()
				t.Setenv("HOME", dir)
				t.Setenv("XDG_CONFIG_HOME", dir)
				path := filepath.Join(dir, "dispatch.txt")
				if err := os.WriteFile(path, []byte("three-mode-retained-bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				node := &threeModeNode{capabilityHTTPNode: &capabilityHTTPNode{root: dir}}
				pool := &threeModePool{client: node}
				cfg := &config.AppConfig{}
				var chat *agent.ChatAgent
				var cleanup func()
				var revoke func()
				cleaned := 0
				if mode == "ordinary-chat" {
					inputs, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: scope, AppConfig: cfg, Pool: pool, FallbackPolicy: chatruntime.FallbackForbidden})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = inputs.Close() })
					runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Inputs: inputs, Config: &ChatFactoryConfig{AppConfig: cfg, WorkspaceDir: dir}, LLM: provider.NewPlaceholderLLM(), Sessions: f.sessions, SessionID: f.req.Run.SessionID.String(), Resources: &ChatCatalogResources{Pool: pool}, UseScopedEngines: true, Cleanup: func() { cleaned++ }}, ChatRuntimeHooks{})
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = runtime.ChatAgent, runtime.Cleanup
				} else {
					identity := f.req.Policy.Identity()
					var err error
					f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file", "write_file", "edit_file", "find_files", "file_tree", "filter_json", "shell_command", "process_list"}})
					if err != nil {
						t.Fatal(err)
					}
					if mode == "interactive-bot" {
						f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
						f.ctx, err = execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: f.req.Principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
						if err != nil {
							t.Fatal(err)
						}
						registry := execution.NewBotTurnAuthorizationRegistry(2)
						admission, err := registry.Register(f.ctx, identity)
						if err != nil {
							t.Fatal(err)
						}
						ceiling := admission.CallerCeiling()
						f.req.Run.CallerCeiling = &ceiling
						f.req.Authorization, err = registry.Authorizer(identity, ceiling, func(_ context.Context, p execution.Principal) (execution.Principal, error) { return p, nil })
						if err != nil {
							t.Fatal(err)
						}
						f.req.Policy = nil
					}
					f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
						return &BotChatSafeResources{ChatConfig: cfg, Catalog: &ChatCatalogResources{Pool: pool}, WorkspaceDir: dir, PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { f.safeCleaned++ }}, nil
					}
					resources, err := f.construct(t)
					if err != nil {
						t.Fatal(err)
					}
					chat, cleanup = resources.Agent, resources.Cleanup
					revoke = func() { f.req.Bot.Status = store.BotPaused }
				}
				t.Cleanup(cleanup)
				if len(node.names) != 0 {
					t.Fatal("construction executed node work")
				}
				invoke := func(ctx tool.Context, name string, args map[string]any) (map[string]any, error) {
					t.Helper()
					entry := chat.ToolIndex.GetToolEntry(name)
					if entry == nil {
						t.Fatalf("configured shared catalog missing %s", name)
					}
					runnable, ok := entry.Tool.(botRunnableTool)
					if !ok {
						t.Fatalf("configured tool %s is not executable", name)
					}
					return runnable.Run(ctx, args)
				}
				ctx := threeModeToolContext{botFactoryHandoffContext{base: t.Context(), session: f.req.Run.SessionID.String(), functionCallID: "parity-read"}}
				readArgs := map[string]any{"path": path, "force": true}
				result, err := invoke(ctx, "read_file", readArgs)
				if err != nil || result["content"] != "1: three-mode-retained-bytes" {
					t.Fatalf("read_file result=%v error=%v", result, err)
				}
				result, err = invoke(ctx, "filter_json", map[string]any{"json_data": `{"visible":"shared-filter-ok","private":"must-not-survive"}`, "fields_to_extract": []string{"visible"}})
				raw, _ := json.Marshal(result)
				if err != nil || !strings.Contains(string(raw), "shared-filter-ok") || strings.Contains(string(raw), "must-not-survive") {
					t.Fatalf("filter_json result=%s error=%v", raw, err)
				}
				wantNames := "read_file,filter_json"
				for _, discovery := range []struct {
					name string
					args map[string]any
				}{
					{"find_files", map[string]any{"search_path": dir, "pattern": "dispatch.txt"}},
					{"file_tree", map[string]any{"path": dir, "max_depth": 1}},
				} {
					result, err := invoke(ctx, discovery.name, discovery.args)
					raw, _ := json.Marshal(result)
					if err != nil || !strings.Contains(string(raw), "dispatch.txt") {
						t.Fatalf("%s result=%s error=%v", discovery.name, raw, err)
					}
					wantNames += "," + discovery.name
				}
				// Exercise the same indexed executable through progressive dispatch,
				// rather than proving only direct access to its ToolIndex entry.
				bridge, err := fixedProviderTools(chat.ToolIndex)
				if err != nil {
					t.Fatal(err)
				}
				var execute botRunnableTool
				for _, candidate := range bridge {
					if candidate.Name() == "execute_tool" {
						execute = candidate.(botRunnableTool)
					}
				}
				if execute == nil {
					t.Fatal("missing deferred dispatch bridge")
				}
				result, err = execute.Run(ctx, map[string]any{"name": "read_file", "arguments": readArgs})
				raw, _ = json.Marshal(result)
				if err != nil || !strings.Contains(string(raw), "three-mode-retained-bytes") {
					t.Fatalf("deferred read result=%s error=%v", raw, err)
				}
				wantNames += ",read_file"
				for _, operation := range []struct {
					name string
					args map[string]any
					want string
				}{
					{"shell_command", map[string]any{"command": "printf runtime-shell-ok", "working_dir": dir}, "runtime-shell-ok"},
					{"process_list", map[string]any{}, "sessions"},
				} {
					result, err := invoke(ctx, operation.name, operation.args)
					if mode == "autonomous-bot" {
						if err == nil {
							t.Fatalf("unapproved autonomous %s executed", operation.name)
						}
						continue
					}
					raw, _ := json.Marshal(result)
					if err != nil || !strings.Contains(string(raw), operation.want) {
						t.Fatalf("%s result=%s error=%v", operation.name, raw, err)
					}
					wantNames += "," + operation.name
				}
				wantBytes := "three-mode-retained-bytes"
				writeArgs := map[string]any{"path": path, "content": "three-mode-written-bytes"}
				editArgs := map[string]any{"path": path, "old_string": "written", "new_string": "edited"}
				if mode == "autonomous-bot" {
					// The allowlist does not approve an unknown file effect. Keep the
					// real classifier/guard; do not manufacture a successful grant.
					for _, mutation := range []struct {
						name string
						args map[string]any
					}{{"write_file", writeArgs}, {"edit_file", editArgs}} {
						if _, err := invoke(ctx, mutation.name, mutation.args); err == nil {
							t.Fatalf("unapproved autonomous %s executed", mutation.name)
						}
					}
				} else {
					mutationCtx := ctx
					mutationCtx.functionCallID = "parity-write"
					result, err = invoke(mutationCtx, "write_file", writeArgs)
					if err != nil {
						t.Fatalf("write_file result=%v error=%v", result, err)
					}
					if _, _, verified := tools.VerifiedBuiltinFileReceipt("write_file", writeArgs, result); !verified {
						t.Fatalf("write_file did not return a verified built-in receipt: %v", result)
					}
					assertBytes := func(want string) {
						t.Helper()
						body, err := os.ReadFile(path)
						if err != nil || string(body) != want {
							t.Fatalf("actual file bytes=%q want=%q error=%v", body, want, err)
						}
					}
					assertBytes("three-mode-written-bytes")
					result, err = invoke(ctx, "read_file", map[string]any{"path": path, "force": true})
					if err != nil || result["content"] != "1: three-mode-written-bytes" {
						t.Fatalf("read after write result=%v error=%v", result, err)
					}
					mutationCtx.functionCallID = "parity-edit"
					result, err = invoke(mutationCtx, "edit_file", editArgs)
					if err != nil || result["success"] != true || result["replacements"] != float64(1) || result["path"] != path {
						t.Fatalf("edit_file result=%v error=%v", result, err)
					}
					wantBytes = "three-mode-edited-bytes"
					assertBytes(wantBytes)
					result, err = invoke(ctx, "read_file", map[string]any{"path": path, "force": true})
					if err != nil || result["content"] != "1: "+wantBytes {
						t.Fatalf("read after edit result=%v error=%v", result, err)
					}
					wantNames += ",write_file,read_file,edit_file,read_file"
				}
				cancelled, cancel := context.WithCancel(t.Context())
				cancel()
				bad := ctx
				bad.base = cancelled
				if _, err := invoke(bad, "read_file", readArgs); err == nil {
					t.Fatal("cancelled read dispatched")
				}
				if mode == "autonomous-bot" {
					// Tool membership is not permission: the real classifier requires
					// exact approval for shell, even when it is on the run allowlist.
					if _, err := invoke(ctx, "shell_command", map[string]any{"command": "printf runtime-shell-ok", "working_dir": dir}); err == nil {
						t.Fatal("unapproved autonomous shell executed")
					}
				}
				if mode != "ordinary-chat" {
					// The repository snapshot changes after construction. Retaining an
					// indexed tool must not retain authority from the earlier lease.
					f.req.Run.Lease.Fence++
					if _, err := invoke(ctx, "read_file", readArgs); err == nil {
						t.Fatal("stale lease read dispatched")
					}
					f.req.Run.Lease.Fence--
				}
				if revoke != nil {
					revoke()
					if _, err := invoke(ctx, "read_file", readArgs); err == nil {
						t.Fatal("paused Bot read dispatched")
					}
					if _, err := invoke(ctx, "write_file", writeArgs); err == nil {
						t.Fatal("paused Bot write dispatched")
					}
					if _, err := invoke(ctx, "edit_file", editArgs); err == nil {
						t.Fatal("paused Bot edit dispatched")
					}
					result, err := execute.Run(ctx, map[string]any{"name": "read_file", "arguments": readArgs})
					if err == nil && result["error"] == nil {
						t.Fatalf("paused Bot deferred read accepted: %v", result)
					}
				}
				if strings.Join(node.names, ",") != wantNames || len(node.sessions) != len(strings.Split(wantNames, ",")) {
					t.Fatalf("dispatch escaped session or denial: names=%v sessions=%v", node.names, node.sessions)
				}
				for _, sessionID := range node.sessions {
					if sessionID != f.req.Run.SessionID.String() {
						t.Fatalf("dispatch escaped pinned session: %v", node.sessions)
					}
				}
				if body, err := os.ReadFile(path); err != nil || string(body) != wantBytes {
					t.Fatalf("denied operation changed file bytes=%q want=%q error=%v", body, wantBytes, err)
				}
				cleanup()
				cleanup()
				if mode == "ordinary-chat" && cleaned != 1 {
					t.Fatalf("ordinary resource cleanup=%d want=1", cleaned)
				}
				if mode != "ordinary-chat" && (f.scopeCleaned != 1 || f.safeCleaned != 1) {
					t.Fatalf("cleanup not exactly once: scope=%d resources=%d", f.scopeCleaned, f.safeCleaned)
				}
			})
		}
	}
}
