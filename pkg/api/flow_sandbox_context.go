package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	adktool "google.golang.org/adk/tool"
)

var (
	resolveRuntimeTemplateLayerChain = resolveTemplateLayerChain
	resolveRuntimeTemplateImage      = resolveTemplateImage
	resolveRuntimeBaseLayerChain     = resolveBaseLayerChain
	resolveRuntimeBaseImage          = resolveBaseImage
)

func withRuntimeSandboxContext(ctx context.Context, r *http.Request) context.Context {
	if r != nil {
		if svc := store.FromRequest(r); svc != nil {
			ctx = store.WithServices(ctx, svc)
		}
	}
	return WithRuntimeSandboxContext(ctx)
}

// WithChannelRuntimeContext adds the request-scoped runtime capabilities used by
// channel and endpoint-owned dispatches. It keeps their tenant MCP catalog and
// selected web-search tool aligned with Studio chat without mutating the shared
// ChatAgent.
func WithChannelRuntimeContext(ctx context.Context, pool sandbox.ToolNodePool) context.Context {
	ctx = WithRuntimeSandboxContext(ctx)
	r := (&http.Request{}).WithContext(ctx)
	if groups := buildRequestMCPToolGroups(r, pool, false); len(groups) > 0 {
		ctx = agent.WithRequestMCPGroups(ctx, groups)
		r = r.WithContext(ctx)
	}
	return withPerRequestWebSearchContext(ctx, effectiveAppConfig(r))
}

func withPerRequestWebSearchContext(ctx context.Context, appCfg *config.AppConfig) context.Context {
	if appCfg == nil {
		return ctx
	}
	searchOK, searchServer, searchTool := IsWebSearchConfiguredWith(appCfg)
	extractOK, _, extractTool := IsWebExtractConfiguredWith(appCfg)

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

// WithRuntimeSandboxContext resolves the tenant's configured sandbox template,
// overlay layer chain, and image into ctx. HTTP handlers and endpoint-owned
// dispatchers share this path so they provision identical sandbox sessions.
func WithRuntimeSandboxContext(ctx context.Context) context.Context {
	if svc := store.FromContext(ctx); svc != nil && svc.Settings != nil {
		if settings, err := svc.Settings.Get(ctx); err == nil && settings != nil && settings.TemplateName != "" {
			ctx = store.WithSandboxTemplate(ctx, settings.TemplateName)
			if chain := resolveRuntimeTemplateLayerChain(ctx, settings.TemplateName); len(chain) > 0 {
				ctx = store.WithSandboxLayerChain(ctx, chain)
			}
			if img := resolveRuntimeTemplateImage(ctx, settings.TemplateName); img != "" {
				ctx = store.WithSandboxImage(ctx, img)
			}
		}
	}
	if store.SandboxLayerChainFromContext(ctx) == nil {
		if chain := resolveRuntimeBaseLayerChain(ctx); len(chain) > 0 {
			ctx = store.WithSandboxLayerChain(ctx, chain)
		}
	}
	if store.SandboxImageFromContext(ctx) == "" {
		if img := resolveRuntimeBaseImage(ctx); img != "" {
			ctx = store.WithSandboxImage(ctx, img)
		}
	}
	return ctx
}
