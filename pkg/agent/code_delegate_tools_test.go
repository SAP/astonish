package agent

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func codeDelegateTestTools(t *testing.T, names ...string) []tool.Tool {
	t.Helper()
	var tools []tool.Tool
	for _, name := range names {
		fn, err := functiontool.New(functiontool.Config{Name: name, Description: name}, func(tool.Context, struct{}) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, fn)
	}
	return tools
}

func codeDelegateTestCatalog(t *testing.T, m *SubAgentManager) (*childCountingToolset, *childCountingToolset) {
	t.Helper()
	graph := &childCountingToolset{tools: codeDelegateTestTools(t, "codegraph_explore", "codegraph_search")}
	unrelated := &childCountingToolset{tools: codeDelegateTestTools(t, "mail_send")}
	m.ToolGroups = map[string]*ToolGroup{
		"core":                 {Name: "core", Tools: codeDelegateTestTools(t, "read_file", "code_definition", "code_references", "repo_map")},
		"web":                  {Name: "web", Tools: codeDelegateTestTools(t, "web_fetch")},
		codeDelegateGraphGroup: {Name: codeDelegateGraphGroup, Toolsets: []tool.Toolset{graph}},
		"mcp:mail":             {Name: "mcp:mail", Toolsets: []tool.Toolset{unrelated}},
	}
	return graph, unrelated
}

func TestCodeDelegateInitialModelRequestAndHandoff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter []string
	}{
		{"core", []string{"core"}},
		{"core-web", []string{"core", "web"}},
		{"web-only", []string{"web"}},
		{"omitted", nil},
		{"empty", []string{}},
		{"qualified-graph", []string{"core", "mcp:codegraph/codegraph_search"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, svc, task := handoffFixture(t)
			m.Config.CodeMode = true
			graph, unrelated := codeDelegateTestCatalog(t, m)
			task.ToolSelectionMode, task.ToolFilter = ChildToolSelectionPreferred, tc.filter
			called := false
			m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
				called = true
				var names []string
				for _, group := range req.Config.Tools {
					for _, decl := range group.FunctionDeclarations {
						names = append(names, decl.Name)
					}
				}
				for _, name := range []string{"codegraph_explore", "codegraph_search", "code_definition", "code_references", "repo_map"} {
					if !slices.Contains(names, name) {
						t.Errorf("initial request missing %s: %v", name, names)
					}
				}
				if slices.Contains(names, "mail_send") || unrelated.calls.Load() != 0 {
					t.Error("unrelated MCP loaded")
				}
				if len(svc.records) != 1 {
					t.Fatal("handoff missing before model")
				}
				h := decodeHandoff(t, svc.records[0])
				if !reflect.DeepEqual(names, h.InitialToolNames) {
					t.Fatalf("provider %v != handoff %v", names, h.InitialToolNames)
				}
				seen := map[string]bool{}
				for _, name := range names {
					if seen[name] {
						t.Errorf("duplicate %s", name)
					}
					seen[name] = true
				}
				text := handoffRequestText(req)
				for _, fragment := range []string{"## Code Investigation", "Use codegraph FIRST", "structural tools next", "Reserve grep_search for narrow", "counts as already read", "Stop exploring once", "Immutable Delegation Handoff", "prior findings: use exact path"} {
					if !strings.Contains(text, fragment) {
						t.Errorf("request missing guidance %q", fragment)
					}
				}
				return handoffDone(), nil
			}}
			result := m.RunTask(context.Background(), task)
			if result.Status != "success" || !called || graph.calls.Load() != 1 {
				t.Fatalf("result=%+v called=%v graph calls=%d", result, called, graph.calls.Load())
			}
		})
	}
}

func TestCodeDelegateBoundaries(t *testing.T) {
	for _, kind := range []string{"noncode", "strict", "custom", "override-tools", "override-toolsets"} {
		t.Run(kind, func(t *testing.T) {
			m := NewSubAgentManager(SubAgentConfig{CodeMode: true})
			graph, unrelated := codeDelegateTestCatalog(t, m)
			task := SubAgentTask{ToolSelectionMode: ChildToolSelectionPreferred, ToolFilter: []string{"read_file"}}
			switch kind {
			case "noncode":
				m.Config.CodeMode = false
			case "strict":
				task.ToolSelectionMode = ChildToolSelectionStrict
			case "custom":
				task.CustomPrompt = true
			case "override-tools":
				task.OverrideTools = codeDelegateTestTools(t, "read_file")
			case "override-toolsets":
				task.OverrideToolsets = []tool.Toolset{&childCountingToolset{tools: codeDelegateTestTools(t, "read_file")}}
			}
			scope, _, err := m.childToolScope(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if len(scope.initial) != 1 || scope.initial[0].tool.Name() != "read_file" {
				t.Fatalf("boundary widened: %+v", scope.initial)
			}
			if graph.calls.Load() != 0 || unrelated.calls.Load() != 0 {
				t.Fatal("ambient toolsets initialized")
			}
			if strings.Contains(m.buildChildPromptWithScope(context.Background(), task, scope), "## Code Investigation") {
				t.Fatal("code guidance leaked")
			}
			if kind == "strict" || strings.HasPrefix(kind, "override") {
				if _, _, err := scope.resolve(&minimalReadonlyContext{Context: context.Background()}, "mcp:codegraph/codegraph_explore"); err == nil {
					t.Fatal("strict scope widened")
				}
			}
		})
	}
}

type codeDelegateBrokenToolset struct{}

func (codeDelegateBrokenToolset) Name() string { return "broken-codegraph" }
func (codeDelegateBrokenToolset) Tools(adkagent.ReadonlyContext) ([]tool.Tool, error) {
	return nil, errors.New("offline")
}

func TestCodeDelegateUnavailableAndDisabled(t *testing.T) {
	for _, kind := range []string{"missing", "offline", "offline-explicit", "disabled", "inaccessible", "partly-disabled"} {
		t.Run(kind, func(t *testing.T) {
			m, _, task := handoffFixture(t)
			m.Config.CodeMode = true
			graph, unrelated := codeDelegateTestCatalog(t, m)
			task.ToolSelectionMode, task.ToolFilter = ChildToolSelectionPreferred, []string{"core"}
			ctx := context.Background()
			switch kind {
			case "missing":
				delete(m.ToolGroups, codeDelegateGraphGroup)
			case "offline", "offline-explicit":
				m.ToolGroups[codeDelegateGraphGroup].Toolsets = []tool.Toolset{codeDelegateBrokenToolset{}}
				if kind == "offline-explicit" {
					task.ToolFilter = append(task.ToolFilter, codeDelegateGraphGroup)
				}
			case "inaccessible":
				disabled := false
				ctx = store.WithMCPServerStores(ctx, &store.MCPServerStores{Team: &testMCPServerStore{servers: map[string]*store.MCPServer{
					"codegraph": {Name: "codegraph", Enabled: &disabled},
				}}})
			case "disabled", "partly-disabled":
				m.ToolIndex = &ToolIndex{toolRegistry: map[string]ToolEntry{
					"explore": {Name: "codegraph_explore", GroupName: codeDelegateGraphGroup},
					"search":  {Name: "codegraph_search", GroupName: codeDelegateGraphGroup},
				}}
				disabled := []string{"mcp:codegraph/codegraph_explore", "code_definition"}
				if kind == "disabled" {
					disabled = append(disabled, "codegraph_search")
				}
				ctx = store.WithDisabledTools(ctx, disabled)
			}
			called := false
			m.LLM = &handoffTestLLM{generate: func(_ context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
				called = true
				text := handoffRequestText(req)
				if kind != "partly-disabled" && !strings.Contains(text, "Codegraph is unavailable") {
					t.Error("missing honest fallback guidance")
				}
				for _, group := range req.Config.Tools {
					for _, d := range group.FunctionDeclarations {
						if d.Name == "codegraph_explore" || (kind != "partly-disabled" && d.Name == "codegraph_search") {
							t.Errorf("unavailable %s exposed", d.Name)
						}
						if (kind == "disabled" || kind == "partly-disabled") && d.Name == "code_definition" {
							t.Error("disabled structural tool exposed")
						}
					}
				}
				return handoffDone(), nil
			}}
			result := m.RunTask(ctx, task)
			if result.Status != "success" || !called {
				t.Fatalf("fallback failed: %+v", result)
			}
			if unrelated.calls.Load() != 0 {
				t.Error("unrelated toolset loaded")
			}
			if (kind == "disabled" || kind == "inaccessible") && graph.calls.Load() != 0 {
				t.Error("denied graph enumerated")
			}
		})
	}
}

func TestCodeDelegateResolverUsesInvocationInstances(t *testing.T) {
	m := NewSubAgentManager(SubAgentConfig{CodeMode: true})
	codeDelegateTestCatalog(t, m)
	delete(m.ToolGroups, codeDelegateGraphGroup)
	local := codeDelegateTestTools(t, "codegraph_explore")[0]
	calls := 0
	m.MCPGroupResolver = func(_ context.Context, server string) *ToolGroup {
		calls++
		if server != "codegraph" {
			t.Fatalf("unrelated resolver %s", server)
		}
		return &ToolGroup{Name: codeDelegateGraphGroup, Tools: []tool.Tool{local}}
	}
	scope, _, err := m.childToolScope(context.Background(), SubAgentTask{ToolFilter: []string{"core"}, ToolSelectionMode: ChildToolSelectionPreferred})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := scope.resolve(&minimalReadonlyContext{Context: context.Background()}, "codegraph_explore")
	if err != nil || got != local || calls != 1 {
		t.Fatalf("instance=%v calls=%d err=%v", got, calls, err)
	}
}
