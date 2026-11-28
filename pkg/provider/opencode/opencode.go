// Package opencode implements model.LLM for the OpenCode Go hosted API.
//
// OpenCode Go exposes an OpenAI-compatible endpoint but rejects any request
// that does not carry a stable x-opencode-session header (returning
// missing_session_id). The generic openai_compat provider has no way to attach
// that opencode-specific header, so opencode go gets its own provider type.
//
// This provider mirrors pkg/provider/copilot_oauth: it wraps the shared
// openai_provider.Provider but installs a custom http.RoundTripper that sets
// the x-opencode-session header (a stable, client-generated value reused for
// the lifetime of the provider instance) and a descriptive User-Agent on every
// outbound request. OpenCode asks clients to identify themselves and to send a
// session ID that stays constant across the messages, turns, and retries of one
// conversation; holding a single session ID per provider instance satisfies
// that requirement without any new session plumbing.
package opencode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"iter"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"

	"github.com/SAP/astonish/pkg/provider/httpool"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
)

// DefaultBaseURL is the documented OpenAI-compatible path for OpenCode Go.
// It already ends in /v1, so callers must not append another /v1 suffix.
const DefaultBaseURL = "https://opencode.ai/inference/go/openai/v1"

// userAgent identifies this client to OpenCode Go, which asks clients to use
// their own user agent rather than a generic SDK/HTTP-library name.
const userAgent = "astonish/1.0"

// openCodeTransport sets the opencode-required headers on every request. The
// sessionID is generated once per provider instance and reused for the life of
// that instance so one conversation always presents the same session value.
type openCodeTransport struct {
	base      http.RoundTripper
	sessionID string
}

func (t *openCodeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone before mutating: RoundTrippers must not modify the caller's request.
	clone := req.Clone(req.Context())
	clone.Header.Set("x-opencode-session", t.sessionID)
	clone.Header.Set("User-Agent", userAgent)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// newSessionID returns a stable opencode session identifier of the form
// "ses_<32 hex chars>". It uses crypto/rand and falls back to a UUID if the
// random read fails.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "ses_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return "ses_" + hex.EncodeToString(b[:])
}

// Provider implements model.LLM for OpenCode Go by wrapping the shared
// OpenAI-compatible provider with the opencode header transport.
type Provider struct {
	*openai_provider.Provider
}

// NewProvider creates a new OpenCode Go provider. It configures an
// OpenAI-compatible client against the fixed opencode endpoint with an HTTP
// transport that attaches the x-opencode-session and User-Agent headers to
// every request. The base URL is fixed — opencode is a specific hosted service,
// not a generic OpenAI-compatible endpoint — so callers do not supply a URL.
func NewProvider(apiKey, modelName string) model.LLM {
	return newProviderWithBaseURL(apiKey, DefaultBaseURL, modelName)
}

// newProviderWithBaseURL builds the provider against an explicit base URL. It
// exists so tests can point the client at an httptest server; production code
// always goes through NewProvider, which fixes the base URL to DefaultBaseURL.
func newProviderWithBaseURL(apiKey, baseURL, modelName string) model.LLM {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	config.HTTPClient = &http.Client{
		Transport: &openCodeTransport{base: httpool.Transport(), sessionID: newSessionID()},
	}

	client := openai.NewClientWithConfig(config)
	return &Provider{Provider: openai_provider.NewProviderWithMaxTokens(client, modelName, true, 64000)}
}

// Name implements model.LLM.
func (p *Provider) Name() string {
	return p.Provider.Name()
}

// GenerateContent implements model.LLM.
func (p *Provider) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return p.Provider.GenerateContent(ctx, req, streaming)
}

// ListModels returns available models from the OpenCode Go endpoint. It builds
// the same header transport so the models request is also routed correctly. The
// base URL is fixed to the opencode endpoint.
func ListModels(ctx context.Context, apiKey string) ([]string, error) {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = DefaultBaseURL
	config.HTTPClient = &http.Client{
		Transport: &openCodeTransport{base: httpool.Transport(), sessionID: newSessionID()},
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
