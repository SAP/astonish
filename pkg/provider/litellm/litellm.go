package litellm

import (
	"context"
	"iter"
	"strings"

	"github.com/SAP/astonish/pkg/provider/httpool"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
)

// Provider implements model.LLM for LiteLLM.
// LiteLLM follows OpenAI API standard, so we reuse OpenAI provider.
type Provider struct {
	*openai_provider.Provider
}

// NewProvider creates a new LiteLLM provider.
func NewProvider(apiKey, baseURL, modelName string) model.LLM {
	if apiKey == "" {
		apiKey = "litellm"
	}
	config := openai.DefaultConfig(apiKey)
	config.HTTPClient = httpool.StreamingClient()

	// Ensure baseURL has /v1 suffix, but don't duplicate it
	if !strings.HasSuffix(baseURL, "/v1") {
		if strings.HasSuffix(baseURL, "/") {
			baseURL = baseURL + "v1"
		} else {
			baseURL = baseURL + "/v1"
		}
	}

	config.BaseURL = baseURL
	client := openai.NewClientWithConfig(config)

	// LiteLLM is a proxy for multiple providers with different capabilities.
	// Disable JSON mode to avoid incompatibility with models that expect 'json_schema' or 'text' instead of 'json_object'
	return &Provider{
		Provider: openai_provider.NewProvider(client, modelName, false),
	}
}

// Name implements model.LLM.
func (p *Provider) Name() string {
	return p.Provider.Name()
}

// GenerateContent implements model.LLM.
func (p *Provider) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return p.Provider.GenerateContent(ctx, req, streaming)
}
