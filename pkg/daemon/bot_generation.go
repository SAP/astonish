package daemon

import (
	"context"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"google.golang.org/adk/model"
)

// botGenerationModel carries only numeric capabilities with the request-local
// adapter. It retains no provider settings, credentials or mutable global map.
type botGenerationModel struct {
	model.LLM
	contextWindow  int
	outputCapacity int
}

// NewBotGenerationModel attaches already-resolved numeric capabilities to a model
// returned by a custom BotWorkerConfig.ResolveModel composition root. Resolve the
// values with provider.ResolveContextWindow and ResolveModelOutputCapacity before
// calling this function; no provider configuration or credentials are retained.
// Non-positive values preserve the worker's existing finite fallback behavior.
func NewBotGenerationModel(llm model.LLM, contextWindow, outputCapacity int) model.LLM {
	return &botGenerationModel{LLM: llm, contextWindow: contextWindow, outputCapacity: outputCapacity}
}

func newBotGenerationModel(ctx context.Context, llm model.LLM, name, modelName string, cfg *config.AppConfig) model.LLM {
	return NewBotGenerationModel(llm,
		provider.ResolveContextWindow(ctx, name, modelName, cfg),
		provider.ResolveModelOutputCapacity(ctx, name, modelName, cfg))
}

func botModelContextWindow(ctx context.Context, llm model.LLM) int {
	if configured, ok := llm.(*botGenerationModel); ok && configured.contextWindow > 0 {
		return configured.contextWindow
	}
	// Custom/test resolvers need no provider configuration. Missing metadata uses
	// ordinary Chat's curated context/default rather than the old Bot 32K limit.
	return provider.ResolveContextWindow(ctx, "", llm.Name(), nil)
}

func botModelBudgetOptions(llm model.LLM, options provider.BudgetLLMOptions) provider.BudgetLLMOptions {
	capacity := 0
	if configured, ok := llm.(*botGenerationModel); ok {
		capacity = configured.outputCapacity
	}
	if options.MaxOutputTokens == 0 {
		options.SupportsOutputLimit = true
	}
	options.MaxOutputTokens = provider.BudgetOutputTokens(capacity, options.MaxOutputTokens)
	return options
}
