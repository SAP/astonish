package provider

import (
	"context"
	"fmt"
	"iter"

	"google.golang.org/adk/model"
)

// placeholderLLM is a stand-in model.LLM used when an agent is constructed
// before any provider/model has been configured (for example, `astonish code`
// launched on a fresh machine). It never talks to a real provider: any attempt
// to generate content yields a helpful error instructing the user to configure
// a model. It is meant to be swapped out via SwappableLLM.Swap once the user
// picks a provider/model.
type placeholderLLM struct{}

// NewPlaceholderLLM returns a model.LLM that produces a configuration-required
// error on use. Callers should wrap it in a SwappableLLM and replace it once a
// real provider is selected.
func NewPlaceholderLLM() model.LLM {
	return placeholderLLM{}
}

// Name implements model.LLM.
func (placeholderLLM) Name() string { return "unconfigured" }

// GenerateContent implements model.LLM by yielding a single configuration error.
func (placeholderLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, fmt.Errorf("no AI model configured — run /model to choose a provider and model"))
	}
}

var _ model.LLM = placeholderLLM{}
