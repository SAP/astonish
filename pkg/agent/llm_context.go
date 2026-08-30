package agent

import (
	"context"

	"google.golang.org/adk/model"
)

type llmOverrideKey struct{}

// WithLLM attaches an LLM override to the context. When present, ChatAgent.Run
// will use this LLM instead of the agent's default c.LLM field. This enables
// per-message provider resolution (e.g., channel messages using the team's
// configured provider) without mutating the shared ChatAgent struct.
func WithLLM(ctx context.Context, llm model.LLM) context.Context {
	return context.WithValue(ctx, llmOverrideKey{}, llm)
}

// LLMFromContext retrieves the LLM override from the context.
// Returns nil if no override is present (callers should fall back to their default).
func LLMFromContext(ctx context.Context) model.LLM {
	llm, _ := ctx.Value(llmOverrideKey{}).(model.LLM)
	return llm
}
