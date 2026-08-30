package google

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/provider/httpool"

	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"
)

// Native API endpoint for model listing with full metadata
const nativeModelsURL = "https://generativelanguage.googleapis.com/v1beta/models"

// Cache for models metadata
var (
	modelCacheMu   sync.RWMutex
	modelCache     []ModelInfo
	modelCacheTime time.Time
	modelCacheTTL  = 1 * time.Hour
)

// Provider implements the model.LLM interface for Google GenAI.
type Provider struct {
	model model.LLM
}

func (p *Provider) Name() string {
	return p.model.Name()
}

func (p *Provider) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	canonicalReq, err := common.CanonicalizeRequestTools(req)
	if err != nil {
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(nil, fmt.Errorf("canonicalize tools: %w", err))
		}
	}
	req = canonicalReq
	// WORKAROUND: Google GenAI (Gemini) does not support function calling with response_mime_type: application/json
	// If tools are present, we must unset the response MIME type and schema to avoid 400 error.
	// The prompt instructions will still guide the model to produce JSON, and the agent will parse it manually.
	if req.Config != nil {
		if len(req.Config.Tools) > 0 {
			req.Config.ResponseMIMEType = ""
			req.Config.ResponseSchema = nil
		} else if req.Config.ResponseMIMEType == "application/json" && req.Config.ResponseSchema == nil {
			// WORKAROUND: Some Google models (e.g. gemma-3-27b-it) do not support "JSON mode" (MIME type without schema).
			// If we are requesting JSON but providing no schema, unset the MIME type to avoid 400 error.
			// The prompt instructions in ReAct FormatOutput are sufficient for these models.
			req.Config.ResponseMIMEType = ""
		}
	}
	return p.model.GenerateContent(ctx, req, stream)
}

// NewProvider creates a new Google GenAI provider.
func NewProvider(ctx context.Context, modelName string, apiKey string) (model.LLM, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("GOOGLE_API_KEY not set")
	}

	// Use ADK's gemini package to create the model
	// This handles the model.LLM interface implementation
	m, err := gemini.NewModel(ctx, modelName, &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		return nil, err
	}

	return &Provider{model: m}, nil
}

// ListModels fetches available models from Google GenAI API using the OpenAI-compatible endpoint.
func ListModels(ctx context.Context, apiKey string) ([]string, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("GOOGLE_API_KEY not set")
	}

	url := "https://generativelanguage.googleapis.com/v1beta/openai/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		return nil, fmt.Errorf("failed to fetch models: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var models []string
	for _, m := range result.Data {
		models = append(models, m.ID)
	}

	if len(models) == 0 {
		return nil, fmt.Errorf("no models found from Google AI API")
	}

	sort.Strings(models)
	return models, nil
}

// ModelInfo represents enhanced model metadata for Google AI
type ModelInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DisplayName      string `json:"display_name,omitempty"`
	Description      string `json:"description,omitempty"`
	InputTokenLimit  int    `json:"context_length,omitempty"`
	OutputTokenLimit int    `json:"max_completion_tokens,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
}

// nativeModelResponse represents a single model from the native API
type nativeModelResponse struct {
	Name                       string   `json:"name"` // e.g. "models/gemini-2.0-flash"
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// nativeModelsResponse represents the API response from native endpoint
type nativeModelsResponse struct {
	Models []nativeModelResponse `json:"models"`
}

// ListModelsWithMetadata fetches models from native Google AI API with full metadata
func ListModelsWithMetadata(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	// Check cache first
	modelCacheMu.RLock()
	if len(modelCache) > 0 && time.Since(modelCacheTime) < modelCacheTTL {
		cached := make([]ModelInfo, len(modelCache))
		copy(cached, modelCache)
		modelCacheMu.RUnlock()
		return cached, nil
	}
	modelCacheMu.RUnlock()

	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("GOOGLE_API_KEY not set")
	}

	// Use native API with API key as query parameter
	url := nativeModelsURL + "?key=" + apiKey
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	client := httpool.Client(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		return nil, fmt.Errorf("failed to fetch models: %s - %s", resp.Status, string(body))
	}

	var result nativeModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var models []ModelInfo
	for _, m := range result.Models {
		// Only include models that support generateContent (text generation)
		supportsGenerate := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				supportsGenerate = true
				break
			}
		}
		if !supportsGenerate {
			continue
		}

		// Extract model ID from name (e.g. "models/gemini-2.0-flash" -> "gemini-2.0-flash")
		modelID := m.Name
		if strings.HasPrefix(m.Name, "models/") {
			modelID = strings.TrimPrefix(m.Name, "models/")
		}

		models = append(models, ModelInfo{
			ID:               modelID,
			Name:             m.DisplayName,
			DisplayName:      m.DisplayName,
			Description:      m.Description,
			InputTokenLimit:  m.InputTokenLimit,
			OutputTokenLimit: m.OutputTokenLimit,
		})
	}

	// Sort by ID
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	// Update cache
	modelCacheMu.Lock()
	modelCache = models
	modelCacheTime = time.Now()
	modelCacheMu.Unlock()

	return models, nil
}
