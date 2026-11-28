package launcher

import (
	"context"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

func TestBotSharedTaskSearchCapture(t *testing.T) {
	for _, scenario := range []string{"public", "private-query", "private-source", "empty", "no-sources", "wrong-provider", "spoof", "denied", "web", "private-web", "private-web-request", "missing-web-request", "failed-web", "spoof-web", "empty-web", "wrong-query", "nested-error", "changed-route"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			knowledgeSearchSettings(f)
			cfg, err := BotScopedChatConfig(f.ctx, knowledgeScope(f))
			if err != nil {
				t.Fatal(err)
			}
			native, err := tools.NewPerplexityWebSearchTool(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			workspace := &retainedResultWorkspace{}
			b := &botFactoryBinding{owner: store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services), req: f.req, verifyIsolation: func(context.Context) error { return nil }}
			b.tasks = &botTaskServices{binding: b, workspace: workspace}
			b.taskSearch = native.(botRunnableTool)
			b.taskSearchConfig = cfg
			args := map[string]any{"query": "public product updates"}
			result := map[string]any{"query": "public product updates", "provider": cfg.PerplexityWebSearch.Provider, "model": cfg.PerplexityWebSearch.Model, "answer": "Observed sourced answer", "citations": []string{"https://evidence.example/source"}}
			target := native.(botRunnableTool)
			var cause error
			switch scenario {
			case "private-query":
				args["query"] = "private owner instructions"
				result["query"] = args["query"]
			case "private-source":
				result["citations"] = []string{"http://127.0.0.1/private"}
			case "empty":
				result["answer"] = ""
			case "no-sources":
				result["citations"] = nil
			case "wrong-provider":
				result["provider"] = "unbound"
			case "spoof":
				spoof, _ := tools.NewPerplexityWebSearchTool(cfg, nil)
				target = spoof.(botRunnableTool)
			case "denied":
				cause = store.ErrBotForbidden
			case "wrong-query":
				result["query"] = "different query"
			case "nested-error":
				result = map[string]any{"result": map[string]any{"error": "unverified"}}
			case "changed-route":
				b.taskSearchConfig = nil
			case "web", "private-web", "private-web-request", "missing-web-request", "failed-web", "spoof-web", "empty-web":
				fetch, _ := functiontool.New(functiontool.Config{Name: "web_fetch", Description: "fixture"}, func(tool.Context, map[string]any) (map[string]any, error) { return nil, nil })
				target = &sandbox.NodeTool{Tool: fetch}
				if scenario == "spoof-web" {
					target = fetch.(botRunnableTool)
				}
				args = map[string]any{"url": "https://evidence.example/source"}
				result = map[string]any{"status": float64(200), "final_url": "https://evidence.example/source", "content": "Observed body"}
				if scenario == "private-web" {
					result["final_url"] = "http://127.0.0.1/private"
				}
				if scenario == "private-web-request" {
					args["url"] = "http://127.0.0.1/private"
				}
				if scenario == "missing-web-request" {
					delete(args, "url")
				}
				if scenario == "failed-web" {
					result["status"] = float64(403)
				}
				if scenario == "empty-web" {
					result["content"] = ""
				}
			}
			ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: b.owner}, sid: f.req.Run.SessionID.String()}
			if err := b.captureSharedTaskSearch(ctx, target, args, result, cause); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "public" || scenario == "web" {
				want = 1
			}
			if workspace.publications != want {
				t.Fatalf("publications=%d want=%d", workspace.publications, want)
			}
		})
	}
}
