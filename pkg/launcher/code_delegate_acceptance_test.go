package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adkagent "google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/tools"
)

// codeDelegateGraphToolset stands in only for the configured MCP transport.
// Tool selection, declarations, child execution and grep's rg process are real.
type codeDelegateGraphToolset struct{ graph tool.Tool }

func (codeDelegateGraphToolset) Name() string { return "codegraph" }
func (s codeDelegateGraphToolset) Tools(adkagent.ReadonlyContext) ([]tool.Tool, error) {
	return []tool.Tool{s.graph}, nil
}

type codeDelegateGraphArgs struct {
	Query string `json:"query"`
}

type codeDelegateGraphResult struct {
	Evidence string `json:"evidence"`
}

func codeDelegateResponse(req *adkmodel.LLMRequest, name string) map[string]any {
	for i := len(req.Contents) - 1; i >= 0; i-- {
		for _, p := range req.Contents[i].Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.Name == name {
				return p.FunctionResponse.Response
			}
		}
	}
	return nil
}

// TestCodeDelegateCoreOnlyGraphAndCappedGrepAcceptance runs a parent code-mode
// ChatAgent through the public delegate_tasks tool, the wired SubAgentManager,
// a real child ADK runner and a real ripgrep subprocess. No paid provider or
// external MCP server is used; the scripted providers inspect actual requests.
func TestCodeDelegateCoreOnlyGraphAndCappedGrepAcceptance(t *testing.T) {
	// Require an installed rg rather than silently testing an automatic download.
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("runtime acceptance requires real ripgrep on PATH")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	// Suppress standard-server process bootstrap; the configured transport is
	// replaced below with an in-process fixture before any delegation runs.
	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "mcp_config.json"), []byte(`{"mcpServers":{"codegraph":{"enabled":false}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	b := newFileStoreBackend(t, t.TempDir(), "code-delegate-owner")
	b.workingDir = t.TempDir()
	largeFile := filepath.Join(b.workingDir, "large.txt")
	// Below rg's 5MiB file limit, but enough matching JSON output to fill a
	// pipe many times. Waiting without terminating the capped producer hangs.
	line := "needle " + strings.Repeat("x", 88) + "\n"
	if err := os.WriteFile(largeFile, []byte(strings.Repeat(line, 32768)), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	appConfig := &config.AppConfig{}
	forceHostExecution(appConfig) // Match RunCodeTUI; CodeMode alone does not disable sandboxing.
	wired, err := NewWiredChatAgent(ctx, &ChatFactoryConfig{
		AppConfig: appConfig, CodeMode: true, AutoApprove: true,
		AllowMissingProvider: true, WorkspaceDir: b.workingDir,
		SessionService: b.sessionSvc,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer wired.Cleanup()
	mgr := wired.ChatAgent.SubAgentManager
	if mgr == nil || !mgr.Config.CodeMode {
		t.Fatal("factory did not wire a Code-mode SubAgentManager")
	}
	if wired.SwappableLLM == nil {
		t.Fatal("factory did not expose its swappable provider")
	}
	mgr.AppName, mgr.UserID = codeAppName, b.effectiveUserID()
	mgr.Config.MaxRetries = 1
	mgr.Config.TaskTimeout = 12 * time.Second
	mgr.Config.InactivityTimeout = 10 * time.Second

	var graphCalls atomic.Int32
	graph, err := functiontool.New(functiontool.Config{
		Name: "codegraph_explore", Description: "Explore configured code graph",
	}, func(_ tool.Context, args codeDelegateGraphArgs) (codeDelegateGraphResult, error) {
		graphCalls.Add(1)
		if args.Query != "needle" {
			return codeDelegateGraphResult{}, fmt.Errorf("unexpected graph query: %q", args.Query)
		}
		return codeDelegateGraphResult{Evidence: "configured graph reached"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Install an actually enumerable configured group, not a prompt-only name
	// or a tool injected directly into the child's preferred list.
	mgr.ToolGroups["mcp:codegraph"] = &agent.ToolGroup{
		Name: "mcp:codegraph", Toolsets: []tool.Toolset{codeDelegateGraphToolset{graph}},
	}

	call := func(id, name string, args map[string]any) *genai.Content {
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args},
		}}}
	}
	const headLimit = 4096
	var grepStarted time.Time
	var grepElapsed time.Duration
	var grepResult tools.GrepSearchResult
	var childSawGrep bool
	child := &revisionScriptModel{}
	child.next = func(req *adkmodel.LLMRequest) *genai.Content {
		switch child.calls {
		case 1:
			declared := map[string]bool{}
			var prompt strings.Builder
			if req.Config != nil {
				for _, group := range req.Config.Tools {
					for _, d := range group.FunctionDeclarations {
						declared[d.Name] = true
					}
				}
				if req.Config.SystemInstruction != nil {
					for _, p := range req.Config.SystemInstruction.Parts {
						prompt.WriteString(p.Text)
					}
				}
			}
			if !declared["codegraph_explore"] || !declared["grep_search"] {
				t.Errorf("initial child provider request lacks graph/grep declarations: %v", declared)
			}
			if !strings.Contains(prompt.String(), "Code Investigation") {
				t.Error("initial child provider request lacks Code Investigation guidance")
			}
			return call("graph-call", "codegraph_explore", map[string]any{"query": "needle"})
		case 2:
			response := codeDelegateResponse(req, "codegraph_explore")
			if response["evidence"] != "configured graph reached" || graphCalls.Load() != 1 {
				t.Errorf("configured graph execution did not reach child: %#v (calls %d)", response, graphCalls.Load())
			}
			grepStarted = time.Now()
			return call("grep-call", "grep_search", map[string]any{
				"pattern": "needle", "search_path": largeFile,
				"head_limit": headLimit, "max_results": 100000,
			})
		case 3:
			grepElapsed = time.Since(grepStarted)
			response := codeDelegateResponse(req, "grep_search")
			raw, marshalErr := json.Marshal(response)
			if marshalErr != nil {
				t.Errorf("encode grep response: %v", marshalErr)
			}
			if err := json.Unmarshal(raw, &grepResult); err != nil {
				t.Errorf("decode grep response: %v", err)
			}
			childSawGrep = true
			if !grepResult.Capped || !strings.Contains(grepResult.TruncatedReason, "4096 bytes") || grepResult.Total == 0 {
				t.Errorf("expected successful byte-capped grep, got %s", raw)
			}
			return genai.NewContentFromText("Graph and bounded grep verified.", genai.RoleModel)
		default:
			t.Errorf("unexpected child model call %d", child.calls)
			return genai.NewContentFromText("Unexpected extra child call.", genai.RoleModel)
		}
	}
	mgr.SetTaskLLM(child)

	var result tools.DelegateTasksResult
	parent := &revisionScriptModel{}
	parent.next = func(req *adkmodel.LLMRequest) *genai.Content {
		if parent.calls == 1 {
			return call("delegate-call", "delegate_tasks", map[string]any{"tasks": []any{map[string]any{
				"name": "code-investigator", "task": "Investigate needle and return bounded search evidence",
				"tools": []any{"core"},
			}}})
		}
		raw, err := json.Marshal(codeDelegateResponse(req, "delegate_tasks"))
		if err != nil {
			t.Errorf("encode delegation response: %v", err)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Errorf("decode delegation response: %v", err)
		}
		return genai.NewContentFromText("Delegation acceptance complete.", genai.RoleModel)
	}
	wired.SwappableLLM.Swap(parent)
	id := seedSession(t, b, "Investigate with a core-only Code delegate")
	root, err := adkagent.New(adkagent.Config{Name: codeAppName, Run: wired.ChatAgent.Run})
	if err != nil {
		t.Fatal(err)
	}
	rnr, err := runner.New(runner.Config{AppName: codeAppName, Agent: root, SessionService: b.sessionSvc})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range rnr.Run(ctx, b.effectiveUserID(), id, genai.NewContentFromText("Run the investigation", genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			t.Fatalf("parent runtime: %v", err)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("acceptance exceeded its deadline: %v", ctx.Err())
	}
	if len(result.Results) != 1 || result.Results[0].Status != "success" {
		t.Fatalf("delegate did not succeed: %+v", result)
	}
	if !childSawGrep || child.calls != 3 || graphCalls.Load() != 1 || result.Results[0].ToolCalls != 2 {
		t.Fatalf("expected graph -> grep -> answer without discovery: child calls=%d graph calls=%d grep=%v result=%+v", child.calls, graphCalls.Load(), childSawGrep, result)
	}
	if grepElapsed >= 5*time.Second || grepResult.DurationMs >= 5000 {
		t.Fatalf("capped grep was not prompt: wall=%s result=%+v", grepElapsed, grepResult)
	}
	for _, match := range grepResult.Matches {
		if match.File != largeFile || match.Kind != "match" || !strings.Contains(match.Content, "needle") {
			t.Fatalf("invalid real grep match: %+v", match)
		}
	}
	// Persisted child history proves the public delegate really ran only the
	// graph and grep tools, rather than just fabricating its final answer.
	childSession, err := b.sessionSvc.Get(ctx, &adksession.GetRequest{
		AppName: codeAppName, UserID: b.effectiveUserID(), SessionID: result.Results[0].ChildSessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for event := range childSession.Session.Events().All() {
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionCall != nil {
				calls = append(calls, part.FunctionCall.Name)
			}
		}
	}
	if strings.Join(calls, ",") != "codegraph_explore,grep_search" {
		t.Fatalf("persisted child tool sequence = %v", calls)
	}
	t.Logf("core-only Code child declared and ran graph first; real rg returned %d retained matches, capped at %d bytes, in %s", grepResult.Total, headLimit, grepElapsed)
}
