package xai_oauth

import (
	"context"
	"iter"
	"net/http"
	"time"

	"github.com/sashabaranov/go-openai"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	"google.golang.org/adk/model"
)

// Provider implements model.LLM for xAI using OAuth 2.0 authentication.
// It wraps the OpenAI-compatible provider with an auto-refreshing OAuth transport.
type Provider struct {
	*openai_provider.Provider
	transport *oauthTransport
}

// NewProvider creates a new xAI OAuth provider. It configures an OpenAI-compatible
// client against https://api.x.ai/v1 with an HTTP transport that manages OAuth
// Bearer tokens and refreshes them before expiry.
func NewProvider(clientID, accessToken, refreshToken string, expiresAt time.Time, modelName string, onRefresh func(string, string, time.Time)) model.LLM {
	transport := NewOAuthTransport(clientID, accessToken, refreshToken, expiresAt, onRefresh)

	config := openai.DefaultConfig("oauth") // Token is handled by transport
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
