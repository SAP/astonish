package copilot_oauth

import (
	"context"
	"iter"
	"net/http"

	"github.com/sashabaranov/go-openai"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	"google.golang.org/adk/model"
)

// Provider implements model.LLM for GitHub Copilot using OAuth authentication.
// It wraps the OpenAI-compatible provider with a Copilot transport that manages
// the two-step token exchange (GitHub token → Copilot session token).
type Provider struct {
	*openai_provider.Provider
	transport *copilotTransport
}

// NewProvider creates a new GitHub Copilot provider. It configures an
// OpenAI-compatible client against https://api.githubcopilot.com with an HTTP
// transport that manages the two-step Copilot token exchange.
func NewProvider(githubToken, modelName string) model.LLM {
	transport := NewCopilotTransport(githubToken)

	config := openai.DefaultConfig("copilot") // Token is handled by transport
	config.BaseURL = apiBaseURL
	config.HTTPClient = &http.Client{
		Transport: transport,
	}

	client := openai.NewClientWithConfig(config)
	op := openai_provider.NewProvider(client, modelName, true)

	return &Provider{
		Provider:  op,
		transport: transport,
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
