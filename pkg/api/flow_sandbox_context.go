package api

import (
	"context"
	"net/http"

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
