package chatruntime

import (
	"context"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/tools"
	adktool "google.golang.org/adk/tool"
	"log/slog"
	"strings"
)

func WithWebSearchContext(ctx context.Context, appCfg *config.AppConfig) context.Context {
	if appCfg == nil {
		return ctx
	}
	searchOK, searchServer, searchTool := parseWebTool(appCfg.General.WebSearchTool)
	extractOK, _, extractTool := parseWebTool(appCfg.General.WebExtractTool)

	searchAvailable := searchOK && searchTool != ""
	searchName := strings.ReplaceAll(searchTool, "-", "_")
	if searchServer == "perplexity" {
		searchAvailable = searchAvailable && appCfg.PerplexityWebSearch.Provider != "" && appCfg.PerplexityWebSearch.Model != ""
	}
	extractAvailable := extractOK && extractTool != ""
	extractName := strings.ReplaceAll(extractTool, "-", "_")

	current := agent.PromptOverridesFromContext(ctx)
	var overrides agent.PromptOverrides
	if current != nil {
		overrides = *current
		overrides.AdditionalTools = append([]adktool.Tool(nil), current.AdditionalTools...)
	}
	overrides.WebSearchAvailable = &searchAvailable
	overrides.WebSearchToolName = searchName
	overrides.WebExtractAvailable = &extractAvailable
	overrides.WebExtractToolName = extractName

	if searchAvailable && searchServer == "perplexity" {
		perplexityTool, err := tools.NewPerplexityWebSearchTool(appCfg, provider.GetProvider)
		if err != nil {
			slog.Warn("failed to create request-scoped perplexity_web_search tool", "error", err)
		} else {
			overrides.AdditionalTools = append(overrides.AdditionalTools, perplexityTool)
		}
	}
	return agent.WithPromptOverrides(ctx, &overrides)
}
func parseWebTool(value string) (bool, string, string) {
	if value == "" {
		return false, "", ""
	}
	p := strings.SplitN(value, ":", 2)
	if len(p) == 2 {
		return true, p[0], strings.ReplaceAll(p[1], "-", "_")
	}
	return true, p[0], ""
}
