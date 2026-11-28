package chatruntime

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
)

// SandboxSelectors are trusted composition-root lookups, never host fallbacks.
type SandboxSelectors struct {
	TemplateLayerChain func(context.Context, string) []string
	TemplateImage      func(context.Context, string) string
	BaseLayerChain     func(context.Context) []string
	BaseImage          func(context.Context) string
}
type sandboxSelection struct {
	template string
	chain    []string
	image    string
}

func resolveSandbox(ctx context.Context, template string, s SandboxSelectors) sandboxSelection {
	out := sandboxSelection{template: template}
	if template != "" {
		if s.TemplateLayerChain != nil {
			out.chain = s.TemplateLayerChain(ctx, template)
		}
		if s.TemplateImage != nil {
			out.image = s.TemplateImage(ctx, template)
		}
	}
	if len(out.chain) == 0 && s.BaseLayerChain != nil {
		out.chain = s.BaseLayerChain(ctx)
	}
	if out.image == "" && s.BaseImage != nil {
		out.image = s.BaseImage(ctx)
	}
	out.chain = append([]string(nil), out.chain...)
	return out
}
func (s sandboxSelection) bind(ctx context.Context) context.Context {
	if s.template != "" {
		ctx = store.WithSandboxTemplate(ctx, s.template)
	}
	if len(s.chain) > 0 {
		ctx = store.WithSandboxLayerChain(ctx, append([]string(nil), s.chain...))
	}
	if s.image != "" {
		ctx = store.WithSandboxImage(ctx, s.image)
	}
	return ctx
}
func cloneConfig[T any](value *T) (*T, error) {
	if value == nil {
		return nil, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out T
	err = json.Unmarshal(b, &out)
	return &out, err
}
