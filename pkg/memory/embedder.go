package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

// EmbeddingFunc is a function that embeds a single text string into a vector.
// This is our own type alias, decoupled from any external library.
type EmbeddingFunc = func(ctx context.Context, text string) ([]float32, error)

// EmbedderResult holds the resolved embedding function and an optional cleanup
// function that must be called when the embedder is no longer needed.
type EmbedderResult struct {
	EmbeddingFunc EmbeddingFunc
	Cleanup       func() error // nil if no cleanup is needed (e.g., cloud API providers)
}

// ResolveEmbeddingFunc determines the embedding function to use for the vector store.
//
// Default behavior (no explicit provider configured):
//
//	Uses a local, in-process embedding model via Hugot (pure Go, no external
//	dependencies). The model (all-MiniLM-L6-v2, ~23 MB) is auto-downloaded on
//	first use and cached in ~/.config/astonish/models/.
//
// Explicit provider (memory.embedding.provider in config.yaml):
//
//	Users can opt in to cloud-based embeddings by setting the provider to
//	"openai", "ollama", or "openai-compat". This is the only way to use
//	cloud APIs for embeddings — auto-detection of cloud providers is intentionally
//	not performed to avoid unexpected costs.
//
// The optional getSecret function resolves secrets from the credential store.
// If nil, only config values and env vars are used (legacy behavior).
func ResolveEmbeddingFunc(appCfg *config.AppConfig, memoryCfg *config.MemoryConfig, debugMode bool, getSecret ...config.SecretGetter) (*EmbedderResult, error) {
	// If memory config explicitly specifies an embedding provider, use that
	if memoryCfg != nil && memoryCfg.Embedding.Provider != "" && memoryCfg.Embedding.Provider != "auto" && memoryCfg.Embedding.Provider != "local" {
		var sg config.SecretGetter
		if len(getSecret) > 0 {
			sg = getSecret[0]
		}
		ef, err := resolveExplicitProvider(memoryCfg, sg)
		if err != nil {
			return nil, err
		}
		return &EmbedderResult{EmbeddingFunc: ef}, nil
	}

	// Default: use local Hugot-based embeddings (pure Go, zero cost, no API calls)
	// resolveLocalEmbedder is defined in hugot_embedder.go (build tag: GO || ALL)
	// or hugot_embedder_stub.go (no build tags — returns error)
	return resolveLocalEmbedder(debugMode)
}

// resolveExplicitProvider creates an embedding function from explicit memory config.
// This is the opt-in path for users who want to use cloud-based embedding APIs.
// The optional getSecret function resolves keys from the credential store.
func resolveExplicitProvider(cfg *config.MemoryConfig, getSecret config.SecretGetter) (EmbeddingFunc, error) {
	// resolveEmbeddingAPIKey checks credential store, then config, then env var.
	resolveAPIKey := func(envVar string) string {
		if getSecret != nil {
			if val := getSecret("memory.embedding.api_key"); val != "" {
				return val
			}
		}
		if cfg.Embedding.APIKey != "" {
			return cfg.Embedding.APIKey
		}
		if envVar != "" {
			return os.Getenv(envVar)
		}
		return ""
	}

	switch cfg.Embedding.Provider {
	case "openai":
		apiKey := resolveAPIKey("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OpenAI embedding requires an API key")
		}
		model := cfg.Embedding.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		return newOpenAIEmbeddingFunc("https://api.openai.com/v1", apiKey, model, cfg.Embedding.Timeout()), nil

	case "ollama":
		baseURL := cfg.Embedding.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		// Normalize: strip trailing /api if present (we add it in the request)
		baseURL = strings.TrimSuffix(baseURL, "/api")
		baseURL = strings.TrimSuffix(baseURL, "/")
		model := cfg.Embedding.Model
		if model == "" {
			model = "nomic-embed-text"
		}
		return newOllamaEmbeddingFunc(baseURL, model, cfg.Embedding.Timeout()), nil

	case "openai-compat", "openai_compat":
		baseURL := cfg.Embedding.BaseURL
		if baseURL == "" {
			return nil, fmt.Errorf("OpenAI-compatible embedding requires a base_url")
		}
		baseURL = strings.TrimSuffix(baseURL, "/")
		apiKey := resolveAPIKey("")
		model := cfg.Embedding.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		return newOpenAIEmbeddingFunc(baseURL, apiKey, model, cfg.Embedding.Timeout()), nil

	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s (supported: openai, ollama, openai-compat)", cfg.Embedding.Provider)
	}
}

func embeddingRequestError(provider string, timeout time.Duration, err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Errorf("%s request timed out after %s: %w", provider, timeout, err)
	}
	return fmt.Errorf("%s request failed: %w", provider, err)
}

// --- OpenAI-compatible embedding client ---

// openAIEmbeddingRequest is the request body for POST /v1/embeddings.
type openAIEmbeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// openAIEmbeddingResponse is the response from POST /v1/embeddings.
type openAIEmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// newOpenAIEmbeddingFunc returns an EmbeddingFunc that calls an OpenAI-compatible
// /v1/embeddings endpoint. Works for OpenAI, Azure OpenAI, and any compatible API.
func newOpenAIEmbeddingFunc(baseURL, apiKey, model string, timeout time.Duration) EmbeddingFunc {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, text string) ([]float32, error) {
		reqBody := openAIEmbeddingRequest{
			Model: model,
			Input: text,
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
		}

		url := baseURL + "/embeddings"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create embedding request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, embeddingRequestError("embedding", timeout, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(respBody))
		}

		var result openAIEmbeddingResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode embedding response: %w", err)
		}
		if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
			return nil, fmt.Errorf("embedding response contained no data")
		}
		return result.Data[0].Embedding, nil
	}
}

// --- Ollama embedding client ---

// ollamaEmbeddingRequest is the request body for POST /api/embeddings.
type ollamaEmbeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// ollamaEmbeddingResponse is the response from POST /api/embeddings.
type ollamaEmbeddingResponse struct {
	Embedding []float32 `json:"embedding"`
}

// newOllamaEmbeddingFunc returns an EmbeddingFunc that calls Ollama's
// /api/embeddings endpoint.
func newOllamaEmbeddingFunc(baseURL, model string, timeout time.Duration) EmbeddingFunc {
	client := &http.Client{Timeout: timeout}
	return func(ctx context.Context, text string) ([]float32, error) {
		reqBody := ollamaEmbeddingRequest{
			Model:  model,
			Prompt: text,
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal ollama embedding request: %w", err)
		}

		url := baseURL + "/api/embeddings"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create ollama embedding request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, embeddingRequestError("ollama embedding", timeout, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("ollama embedding API returned status %d: %s", resp.StatusCode, string(respBody))
		}

		var result ollamaEmbeddingResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode ollama embedding response: %w", err)
		}
		if len(result.Embedding) == 0 {
			return nil, fmt.Errorf("ollama embedding response contained no data")
		}
		return result.Embedding, nil
	}
}
