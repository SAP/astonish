package openai_compat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"strings"

	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
)

// Provider implements model.LLM for OpenAI Compatible endpoints.
type Provider struct {
	*openai_provider.Provider
}

// NewProvider creates a new OpenAI Compatible provider. GPT-6 uses a
// positively detected same-origin Responses endpoint; all other cases retain
// Chat Completions on the configured URL.
func NewProvider(ctx context.Context, apiKey, baseURL, modelName string, debug bool) model.LLM {
	config := openai.DefaultConfig(apiKey)

	// Ensure baseURL has /v1 suffix
	if baseURL != "" {
		if !strings.HasSuffix(baseURL, "/v1") {
			if strings.HasSuffix(baseURL, "/") {
				baseURL = baseURL + "v1"
			} else {
				baseURL = baseURL + "/v1"
			}
		}
		config.BaseURL = baseURL
	}

	if debug {
		config.HTTPClient = &http.Client{
			Transport: &debugHTTPTransport{base: http.DefaultTransport},
		}
	}

	client := openai.NewClientWithConfig(config)
	wrapped := openai_provider.NewProviderWithMaxTokens(client, modelName, true, 64000)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(modelName)), "gpt-6") {
		if responsesBase, ok := DetectResponsesBaseURL(ctx, apiKey, baseURL, modelName); ok {
			responsesConfig := config
			responsesConfig.BaseURL = responsesBase
			wrapped = openai_provider.NewProviderWithResponses(openai.NewClientWithConfig(responsesConfig), modelName, true, 64000)
		}
	}

	return &Provider{Provider: wrapped}
}

// Name implements model.LLM.
func (p *Provider) Name() string {
	return p.Provider.Name()
}

// GenerateContent implements model.LLM.
func (p *Provider) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return p.Provider.GenerateContent(ctx, req, streaming)
}

// ListModels returns available models from an OpenAI compatible endpoint.
func ListModels(ctx context.Context, apiKey, baseURL string) ([]string, error) {
	config := openai.DefaultConfig(apiKey)

	if baseURL != "" {
		if !strings.HasSuffix(baseURL, "/v1") {
			if strings.HasSuffix(baseURL, "/") {
				baseURL = baseURL + "v1"
			} else {
				baseURL = baseURL + "/v1"
			}
		}
		config.BaseURL = baseURL
	}

	client := openai.NewClientWithConfig(config)

	resp, err := client.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	var models []string
	for _, m := range resp.Models {
		models = append(models, m.ID)
	}
	return models, nil
}

// GetRequiredFields returns the required configuration fields for this provider.
func GetRequiredFields() []string {
	return []string{"api_key", "base_url"}
}

// debugHTTPTransport wraps an http.RoundTripper to log request and response
// details when debug mode is enabled. It captures:
//   - Request URL and method
//   - Request body (for POST requests)
//   - Response status code
//   - Response body (for non-2xx responses)
type debugHTTPTransport struct {
	base http.RoundTripper
}

func (t *debugHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	slog.Debug("http request", "method", req.Method, "url", req.URL.String())

	// Log request headers (redact Authorization)
	for key, vals := range req.Header {
		if strings.EqualFold(key, "Authorization") {
			slog.Debug("http request header", "header", key, "value", "[REDACTED]")
		} else {
			slog.Debug("http request header", "header", key, "value", strings.Join(vals, ", "))
		}
	}

	// Capture request body
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("debug transport: failed to read request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		slog.Debug("http request body", "bytes", len(bodyBytes), "body", string(bodyBytes))
	}

	// Perform the actual request
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		slog.Debug("http transport error", "error", err)
		return resp, err
	}

	slog.Debug("http response", "statusCode", resp.StatusCode, "status", resp.Status)

	// For non-2xx responses, capture the response body
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			slog.Debug("failed to read error response body", "error", readErr)
		} else {
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			slog.Debug("http error response body", "body", string(bodyBytes))
		}
	}

	return resp, nil
}
