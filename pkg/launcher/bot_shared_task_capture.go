package launcher

import (
	"encoding/json"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

// captureSharedTaskSearch runs only after the shared dispatch's authorization,
// execution and settlement. It retains observed work, never grants authority or
// attests factual correctness. It neither consumes the outcome ledger nor reserves
// a second tool/model call. Unsuccessful or source-less responses are not work.
func (b *botFactoryBinding) captureSharedTaskSearch(ctx tool.Context, target botRunnableTool, args any, result map[string]any, cause error) error {
	if b.tasks == nil || cause != nil || result == nil || result["error"] != nil {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	var observed map[string]any
	if json.Unmarshal(raw, &observed) != nil {
		return nil
	}
	if envelope, ok := observed["result"].(map[string]any); ok {
		observed = envelope
	}
	if observed["error"] != nil {
		return nil
	}
	if target == b.taskSearch && b.taskSearch != nil {
		// Dynamic MCP bindings are not the configured built-in search capability.
		if _, mcp := agent.LazyMCPToolBinding(target); mcp {
			return nil
		}
		current, err := botKnowledgeSearchConfig(ctx, store.FromContext(b.owner))
		if err != nil {
			return nil
		}
		prepared, err := botConfiguredSearchRoute(ctx, store.FromContext(b.owner), b.taskSearchConfig)
		if err != nil {
			return nil
		}
		route, err := botConfiguredSearchRoute(ctx, store.FromContext(b.owner), current)
		if err != nil || route.Engine != execution.BotSearchPerplexity || route.Fingerprint != prepared.Fingerprint {
			return nil
		}
		argsRaw, err := json.Marshal(args)
		if err != nil {
			return nil
		}
		var request tools.PerplexityWebSearchArgs
		var response tools.PerplexityWebSearchResult
		if json.Unmarshal(argsRaw, &request) != nil {
			return nil
		}
		body, _ := json.Marshal(observed)
		if json.Unmarshal(body, &response) != nil {
			return nil
		}
		query := strings.TrimSpace(request.Query)
		if query == "" || strings.Contains(strings.ToLower(query), "private") || strings.Contains(query, "{{") || strings.ContainsAny(query, "\r\n\x00") || query != response.Query || strings.TrimSpace(response.Answer) == "" || response.Provider != route.Provider || response.Model != route.Model {
			return nil
		}
		sources := append([]string(nil), response.Citations...)
		for _, hit := range response.SearchResults {
			sources = append(sources, hit.URL)
		}
		if len(sources) == 0 {
			return nil
		}
		for _, source := range sources {
			if !botPublicSearchURL(source) {
				return nil
			}
		}
		return b.tasks.observe(ctx, target.Name(), args, result, nil)
	}
	// Web evidence must come from the genuine shared node, not an arbitrary
	// same-named MCP/function tool. Require an observed public successful GET body.
	if _, ok := target.(*sandbox.NodeTool); !ok || target.Name() != "web_fetch" {
		return nil
	}
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil
	}
	var request tools.WebFetchArgs
	if json.Unmarshal(argsRaw, &request) != nil || !botPublicSearchURL(request.URL) {
		return nil
	}
	var response struct {
		Status   float64 `json:"status"`
		FinalURL string  `json:"final_url"`
		Content  string  `json:"content"`
	}
	body, _ := json.Marshal(observed)
	if json.Unmarshal(body, &response) != nil || response.Status < 200 || response.Status >= 300 || response.Status != float64(int(response.Status)) || strings.TrimSpace(response.Content) == "" || !botPublicSearchURL(response.FinalURL) {
		return nil
	}
	return b.tasks.observe(ctx, target.Name(), args, result, nil)
}
