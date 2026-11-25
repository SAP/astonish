package launcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

type accountingToolContext struct{ botFactoryHandoffContext }

func (accountingToolContext) FunctionCallID() string { return "accounting-call" }
func (accountingToolContext) AgentName() string      { return "accounting" }
func (accountingToolContext) AppName() string        { return "astonish" }
func (accountingToolContext) UserID() string         { return "accounting" }

// All routes use shared Chat executables with the production interactive hook.
// In particular the outer authorization handoff must not reserve a second call,
// and ordinary reads must reach the node, never the retired Bot file adapter.
func TestInteractiveAccountingDispatchMatrix(t *testing.T) {
	for _, route := range []string{"direct", "execute_tool", "progressive-executor", "child", "child-progressive", "flow", "lazy-mcp"} {
		t.Run(route, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
			f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
			f.req.Authorization = interactiveFactoryAuthorization{}
			f.req.Policy = nil
			// Interactive execution intentionally has no autonomous policy.
			mcpStore := newMemMCPStore()
			f.services.TeamMCPServers = mcpStore
			meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget = meter
			path := filepath.Join(f.files.root, "receipt.txt")
			if err := os.WriteFile(path, []byte("observed"), 0600); err != nil {
				t.Fatal(err)
			}
			node := &fileOutcomeNode{}
			if route == "flow" {
				f.services.PersonalFlows = &productionFlowStore{yaml: "description: counted child\nnodes:\n  - name: read\n    type: tool\n    tools_selection: [read_file]\n    args:\n      path: " + path + "\nflow:\n  - from: START\n    to: read\n  - from: read\n    to: END\n"}
			}
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: node}}, WorkspaceDir: f.files.root, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			if route == "child" || route == "child-progressive" {
				childModel := &fileOutcomeModel{name: "read_file", args: map[string]any{"path": path}}
				if route == "child-progressive" {
					childModel.name, childModel.args = "execute_tool", map[string]any{"name": "read_file", "arguments": map[string]any{"path": path}}
				}
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return childModel, nil }
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			var target *chatRuntimeTool
			for _, candidate := range resources.Agent.Tools {
				if candidate.Name() == "read_file" {
					target = candidate.(*chatRuntimeTool)
				}
			}
			if target == nil {
				t.Fatal("missing bound read")
			}
			ctx, cancel := context.WithTimeout(target.binding.hooks.Context.(botSharedBinding).b.bind(f.ctx), 5*time.Second)
			defer cancel()
			tc := accountingToolContext{botFactoryHandoffContext: botFactoryHandoffContext{base: ctx, session: f.req.Run.SessionID.String()}}
			args := map[string]any{"path": path}
			readsBefore := f.files.calls
			want := int64(1)
			switch route {
			case "lazy-mcp":
				var calls atomic.Int32
				server := sdk.NewServer(&sdk.Implementation{Name: "accounting", Version: "1"}, nil)
				server.AddTool(&sdk.Tool{Name: "read_accounting", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					calls.Add(1)
					return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "observed"}}}, nil
				})
				endpoint := httptest.NewServer(sdk.NewSSEHandler(func(*http.Request) *sdk.Server { return server }, nil))
				defer func() { endpoint.CloseClientConnections(); endpoint.Close() }()
				if err := mcpStore.Save(ctx, &store.MCPServer{Name: "accounting", Transport: "sse", URL: endpoint.URL}); err != nil {
					t.Fatal(err)
				}
				lazy := agent.NewLazyMCPToolset("accounting", []cache.ToolEntry{{Name: "read_accounting"}}, config.MCPServerConfig{Transport: "sse", URL: endpoint.URL}, false)
				defer lazy.Cleanup()
				proxies, e := lazy.Tools(nil)
				if e != nil {
					t.Fatal(e)
				}
				wrapped, e := target.binding.wrap(proxies[0], nil)
				if e != nil {
					t.Fatal(e)
				}
				_, err = wrapped.(botRunnableTool).Run(tc, map[string]any{})
				if err != nil || calls.Load() != 1 || meter.Usage().ToolCalls != 1 {
					t.Fatalf("lazy calls=%d usage=%+v err=%v", calls.Load(), meter.Usage(), err)
				}
				return
			case "direct":
				_, err = target.Run(tc, args)
			case "execute_tool":
				bridge, e := agent.NewProgressiveToolBridge(resources.Agent.ToolIndex)
				if e != nil {
					t.Fatal(e)
				}
				_, err = bridge[1].(botRunnableTool).Run(tc, map[string]any{"name": "read_file", "arguments": args})
			case "progressive-executor":
				result := agent.NewProgressiveToolExecutor(resources.Agent).Execute(tc, "read_file", args)
				if result.State != agent.ProgressiveStateReady {
					t.Fatalf("dispatch: %+v", result)
				}
			case "child", "child-progressive":
				mgr := resources.Agent.SubAgentManager
				result := mgr.RunTask(ctx, agent.SubAgentTask{Name: "accounting-child", Description: "Read once", Instructions: "Read once", ToolSelectionMode: agent.ChildToolSelectionStrict, OverrideTools: []tool.Tool{target}})
				if node.calls != 1 || f.files.calls != readsBefore {
					t.Fatalf("child: %+v", result)
				}
			case "flow":
				f.services.PersonalFlows = &productionFlowStore{yaml: "description: counted child\nnodes:\n  - name: read\n    type: tool\n    tools_selection: [read_file]\n    args:\n      path: " + path + "\nflow:\n  - from: START\n    to: read\n  - from: read\n    to: END\n"}
				entry := resources.Agent.ToolIndex.GetToolEntry("run_flow")
				if entry == nil {
					t.Fatal("missing shared run_flow")
				}
				_, err = entry.Tool.(botRunnableTool).Run(tc, map[string]any{"flow_name": "counted-child"})
				want = 2 // one parent run_flow invocation plus one actual child read
			}
			if err != nil {
				t.Fatal(err)
			}
			if node.calls != 1 || f.files.calls != readsBefore || meter.Usage().ToolCalls != want {
				t.Fatalf("route=%s executions=%d usage=%+v want=%d", route, node.calls, meter.Usage(), want)
			}
		})
	}
}
