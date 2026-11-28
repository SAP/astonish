package provider

import (
	"context"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/provider/openrouter"
	"github.com/SAP/astonish/pkg/provider/sap"
)

const (
	// DefaultBudgetOutputTokens is a finite fallback, not a model capability.
	DefaultBudgetOutputTokens int32 = 16_384
	// BudgetOutputPolicyTokens leaves reasoning headroom without reserving the
	// entire (often 128K) output capacity on every tool/research call.
	BudgetOutputPolicyTokens int32 = 32_768
)

// ResolveBudgetOutputTokens selects a per-call reservation ceiling. Explicit
// caller limits bypass the default policy, but never a known model maximum.
// This does not grant tokens or spend: BudgetLLM still clamps each request to
// its smaller request limit and the shared run budget's remaining allowance.
func ResolveBudgetOutputTokens(ctx context.Context, providerName, modelName string, cfg *config.AppConfig, explicit int32) int32 {
	return BudgetOutputTokens(ResolveModelOutputCapacity(ctx, providerName, modelName, cfg), explicit)
}

// BudgetOutputTokens bounds a caller or default reservation by known capacity.
func BudgetOutputTokens(maximum int, explicit int32) int32 {
	limit := explicit
	if limit <= 0 {
		limit = DefaultBudgetOutputTokens
		if maximum > 0 {
			limit = BudgetOutputPolicyTokens
		}
	}
	if maximum > 0 && int64(maximum) < int64(limit) {
		limit = int32(maximum)
	}
	return limit
}

// ResolveModelOutputCapacity returns known scoped metadata or zero if unknown.
func ResolveModelOutputCapacity(ctx context.Context, providerName, modelName string, cfg *config.AppConfig) int {
	if cfg == nil {
		return 0
	}
	name, instance, ok := resolveProviderInstance(providerName, cfg)
	if !ok {
		return 0
	}
	switch config.GetProviderType(name, instance) {
	case "sap_ai_core":
		// Metadata includes durable learned output limits. Exact static entries
		// remain available when the deployment/catalog service is unavailable.
		models, err := sap.ListModelsWithMetadata(ctx, instance["client_id"], instance["client_secret"], instance["auth_url"], instance["base_url"], instance["resource_group"])
		if err == nil {
			for _, m := range models {
				if m.ID == modelName && m.MaxOutputTokens > 0 {
					return m.MaxOutputTokens
				}
			}
		}
		if m, ok := sap.ModelConfigs[modelName]; ok {
			return m.MaxTokens
		}
	case "openai_compat", "litellm":
		return openai_compat.FetchModelMetadata(ctx, instance["api_key"], instance["base_url"])[modelName].MaxOutputTokens
	case "openrouter":
		if key := instance["api_key"]; key != "" {
			return openrouter.GetMaxCompletionTokens(ctx, key, modelName)
		}
	}
	return 0
}
