package api

import (
	"context"
	"log/slog"

	"net/http"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
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
	if principal, ok := execution.PrincipalFromContext(ctx); ok {
		r := (&http.Request{}).WithContext(ctx)
		inputs, err := chatruntime.Build(ctx, chatruntime.BuildOptions{
			Principal: principal, Services: store.FromContext(ctx), ResourceScope: chatruntime.ResourceScopePersonal,
			AppConfig: effectiveAppConfig(r), MCPConfig: loadMCPConfigForRequest(r), Pool: pool,
			CredentialResolver: credentialResolverForRequest(r), FallbackPolicy: chatruntime.FallbackAllowed,
			Templates: chatruntime.SandboxSelectors{TemplateLayerChain: resolveRuntimeTemplateLayerChain, TemplateImage: resolveRuntimeTemplateImage, BaseLayerChain: resolveRuntimeBaseLayerChain, BaseImage: resolveRuntimeBaseImage},
		})
		if err == nil {
			bound, bindErr := inputs.Bind(ctx)
			if bindErr == nil {
				return bound
			}
			err = bindErr
		}
		slog.Warn("channel runtime assembly failed; legacy fallback allowed", "error", err)
		return ctx
	}
	ctx = WithRuntimeSandboxContext(ctx)
	r := (&http.Request{}).WithContext(ctx)
	if groups := buildRequestMCPToolGroups(r, pool, false); len(groups) > 0 {
		ctx = agent.WithRequestMCPGroups(ctx, groups)
		r = r.WithContext(ctx)
	}
	return withPerRequestWebSearchContext(ctx, effectiveAppConfig(r))
}

// WithScopedChannelRuntimeContext is the error-returning boundary for strict
// dispatchers. Unlike the legacy wrapper it never returns the original context
// on assembly failure; callers must stop dispatch when err is non-nil.
func WithScopedChannelRuntimeContext(ctx context.Context, options chatruntime.BuildOptions) (context.Context, error) {
	inputs, err := chatruntime.BuildScopedChatRuntimeInputs(ctx, options)
	if err != nil {
		return nil, err
	}
	return inputs.Bind(ctx)
}

func withPerRequestWebSearchContext(ctx context.Context, appCfg *config.AppConfig) context.Context {
	return chatruntime.WithWebSearchContext(ctx, appCfg)
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
