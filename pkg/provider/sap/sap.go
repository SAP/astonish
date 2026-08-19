package sap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/SAP/astonish/pkg/provider/bedrock"
	"github.com/SAP/astonish/pkg/provider/httpool"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/provider/openai"
	"github.com/SAP/astonish/pkg/provider/vertex"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

const sapAICoreProviderType = "sap_ai_core"

// modelLimitsStore is the optional durable store for learned per-model token
// limits (e.g. maxOutputTokens from Vertex 400 responses). Set at daemon boot.
var (
	modelLimitsStore   store.ModelLimitsStore
	modelLimitsStoreMu sync.RWMutex
)

// SetModelLimitsStore registers the platform store used to persist learned
// model token limits. Nil clears the store (tests / miswired daemon).
func SetModelLimitsStore(s store.ModelLimitsStore) {
	modelLimitsStoreMu.Lock()
	defer modelLimitsStoreMu.Unlock()
	modelLimitsStore = s
}

func getModelLimitsStore() store.ModelLimitsStore {
	modelLimitsStoreMu.RLock()
	defer modelLimitsStoreMu.RUnlock()
	return modelLimitsStore
}

// resolveMaxOutputTokens returns the effective maxOutputTokens for a model:
// learned override if present, otherwise GetModelConfig static/fallback.
func resolveMaxOutputTokens(ctx context.Context, modelName string) int {
	cfg := GetModelConfig(modelName)
	maxTokens := cfg.MaxTokens
	if s := getModelLimitsStore(); s != nil {
		if entry, err := s.Get(ctx, sapAICoreProviderType, modelName); err == nil && entry != nil && entry.MaxOutputTokens > 0 {
			return entry.MaxOutputTokens
		}
	}
	return maxTokens
}

// resolveOmitTools reports whether tools/toolConfig should be stripped from
// Vertex requests for this model (learned SupportsTools == false).
func resolveOmitTools(ctx context.Context, modelName string) bool {
	if s := getModelLimitsStore(); s != nil {
		if entry, err := s.Get(ctx, sapAICoreProviderType, modelName); err == nil && entry != nil &&
			entry.SupportsTools != nil && !*entry.SupportsTools {
			return true
		}
	}
	return false
}

// requestWithoutTools returns a shallow copy of req with Tools and ToolConfig
// cleared on Config. Contents and other fields are shared.
func requestWithoutTools(req *model.LLMRequest) *model.LLMRequest {
	if req == nil {
		return nil
	}
	cp := *req
	if req.Config != nil {
		cfg := *req.Config
		cfg.Tools = nil
		cfg.ToolConfig = nil
		cp.Config = &cfg
	}
	return &cp
}

// invalidateModelCache clears the in-memory ListModelsWithMetadata cache so
// the next UI fetch picks up newly learned limits.
func invalidateModelCache() {
	sapModelCacheMu.Lock()
	sapModelCache = nil
	sapModelCacheTime = time.Time{}
	sapModelCacheMu.Unlock()
}

// Provider implements the model.LLM interface for SAP AI Core.
type Provider struct {
	openaiProvider *openai.Provider
	httpClient     *http.Client
	baseURL        string
	deploymentID   string
	modelName      string
	authConfig     *sapTransport
}

// NewProvider creates a new SAP AI Core provider.
func NewProvider(ctx context.Context, modelName string) (model.LLM, error) {
	clientID := os.Getenv("AICORE_CLIENT_ID")
	clientSecret := os.Getenv("AICORE_CLIENT_SECRET")
	authURL := os.Getenv("AICORE_AUTH_URL")
	baseURL := os.Getenv("AICORE_BASE_URL")
	resourceGroup := os.Getenv("AICORE_RESOURCE_GROUP")

	if clientID == "" || clientSecret == "" || authURL == "" || baseURL == "" {
		return nil, fmt.Errorf("missing SAP AI Core environment variables")
	}

	return NewProviderWithConfig(ctx, modelName, clientID, clientSecret, authURL, baseURL, resourceGroup)
}

// NewProviderWithConfig creates a new SAP AI Core provider with explicit configuration.
func NewProviderWithConfig(ctx context.Context, modelName, clientID, clientSecret, authURL, baseURL, resourceGroup string) (model.LLM, error) {
	if !strings.HasSuffix(baseURL, "/v2") {
		if strings.HasSuffix(baseURL, "/") {
			baseURL += "v2"
		} else {
			baseURL += "/v2"
		}
	}

	// Resolve deployment ID
	deploymentID, err := resolveDeploymentIDWithConfig(ctx, modelName, clientID, clientSecret, authURL, baseURL, resourceGroup)
	if err != nil {
		return nil, err
	}

	transport := &sapTransport{
		base:          httpool.Transport(),
		clientID:      clientID,
		clientSecret:  clientSecret,
		authURL:       authURL,
		resourceGroup: resourceGroup,
	}

	// Initialize OpenAI provider for fallback/delegation
	// We construct the full URL for OpenAI provider: baseURL + /inference/deployments/{id}
	// go-openai appends /chat/completions
	deploymentURL := fmt.Sprintf("%s/inference/deployments/%s", baseURL, deploymentID)

	openaiConfig := goopenai.DefaultConfig(clientSecret) // Token is handled by transport, but config needs something
	openaiConfig.BaseURL = deploymentURL
	openaiConfig.HTTPClient = &http.Client{
		Transport: transport,
	}
	openaiConfig.APIType = goopenai.APITypeOpenAI

	// Create the wrapped OpenAI provider
	client := goopenai.NewClientWithConfig(openaiConfig)
	op := openai.NewProvider(client, modelName, true)

	return &Provider{
		openaiProvider: op,
		httpClient: &http.Client{
			Transport: transport,
			// No timeout for streaming - data flows continuously
		},
		baseURL:      baseURL,
		deploymentID: deploymentID,
		modelName:    modelName,
		authConfig:   transport,
	}, nil
}

// resolveDeploymentIDWithConfig finds the deployment ID for a given model name using explicit config.
func resolveDeploymentIDWithConfig(ctx context.Context, modelName, clientID, clientSecret, authURL, baseURL, resourceGroup string) (string, error) {
	// Check map first
	if mapped, ok := ModelIDMap[modelName]; ok {
		modelName = mapped
	}

	t := &sapTransport{
		base:          http.DefaultTransport,
		clientID:      clientID,
		clientSecret:  clientSecret,
		authURL:       authURL,
		resourceGroup: resourceGroup,
	}

	token, err := t.getToken()
	if err != nil {
		return "", fmt.Errorf("failed to get token for deployment lookup: %w", err)
	}

	if !strings.HasSuffix(baseURL, "/v2") {
		baseURL += "/v2"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/lm/deployments", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", resourceGroup)

	client := httpool.Client(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		return "", fmt.Errorf("failed to list deployments: %s, body: %s", resp.Status, string(body))
	}

	var result struct {
		Resources []struct {
			ID      string `json:"id"`
			Details struct {
				Resources struct {
					BackendDetails struct {
						Model struct {
							Name string `json:"name"`
						} `json:"model"`
					} `json:"backendDetails"`
				} `json:"resources"`
			} `json:"details"`
			Status string `json:"status"`
		} `json:"resources"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	for _, res := range result.Resources {
		if res.Status == "RUNNING" && res.Details.Resources.BackendDetails.Model.Name == modelName {
			return res.ID, nil
		}
	}

	return "", fmt.Errorf("no running deployment found for model: %s", modelName)
}

func (p *Provider) Name() string {
	return p.modelName
}

func (p *Provider) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	if strings.HasPrefix(p.modelName, "anthropic--") || strings.HasPrefix(p.modelName, "amazon--") {
		return p.generateBedrockContent(ctx, req, streaming)
	}
	if strings.HasPrefix(p.modelName, "gemini-") {
		return p.generateVertexContent(ctx, req, streaming)
	}
	return p.openaiProvider.GenerateContent(ctx, req, streaming)
}

type sapTransport struct {
	base          http.RoundTripper
	clientID      string
	clientSecret  string
	authURL       string
	resourceGroup string

	token     string
	expiresAt time.Time
	mu        sync.Mutex
}

func (t *sapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.getToken()
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", t.resourceGroup)

	// SAP AI Core requires specific path adjustment sometimes, but let's try standard OpenAI path first
	// Standard: BaseURL + /chat/completions
	// Constructed: .../inference/deployments/{id}/chat/completions
	// This should match SAP AI Core API

	// Inject api-version for Azure OpenAI models if missing
	if strings.Contains(req.URL.Path, "/chat/completions") {
		q := req.URL.Query()
		if q.Get("api-version") == "" {
			q.Set("api-version", "2024-12-01-preview")
			req.URL.RawQuery = q.Encode()
		}
	}

	return t.base.RoundTrip(req)
}

func (t *sapTransport) getToken() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && time.Now().Before(t.expiresAt) {
		return t.token, nil
	}

	// Fetch new token
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", t.clientID)
	data.Set("client_secret", t.clientSecret)

	authReq, err := http.NewRequest("POST", t.authURL+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	authReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := httpool.Client(10 * time.Second)
	resp, err := client.Do(authReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		return "", fmt.Errorf("failed to get token: %s, body: %s", resp.Status, string(body))
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	t.token = result.AccessToken
	t.expiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).Add(-30 * time.Second) // Buffer

	return t.token, nil
}

// ModelConfig contains configuration for a specific model
type ModelConfig struct {
	MaxTokens     int
	ContextWindow int
	NoTemperature bool
}

// ModelConfigs contains configuration for all supported models
var ModelConfigs = map[string]ModelConfig{
	// Anthropic models via Bedrock.
	// Claude 4.x and later: temperature is deprecated — must be omitted entirely.
	"anthropic--claude-4.8-opus":   {MaxTokens: 64000, ContextWindow: 200000, NoTemperature: true},
	"anthropic--claude-4.6-opus":   {MaxTokens: 64000, ContextWindow: 200000, NoTemperature: true},
	"anthropic--claude-4.5-sonnet": {MaxTokens: 64000, ContextWindow: 200000, NoTemperature: true},
	"anthropic--claude-4-sonnet":   {MaxTokens: 64000, ContextWindow: 200000, NoTemperature: true},
	"anthropic--claude-4-opus":     {MaxTokens: 64000, ContextWindow: 200000, NoTemperature: true},
	// Claude 3.x: temperature is still supported.
	"anthropic--claude-3.7-sonnet": {MaxTokens: 64000, ContextWindow: 200000},
	"anthropic--claude-3.5-sonnet": {MaxTokens: 8192, ContextWindow: 200000},
	"anthropic--claude-3-sonnet":   {MaxTokens: 4096, ContextWindow: 200000},
	"anthropic--claude-3-haiku":    {MaxTokens: 4096, ContextWindow: 200000},
	"anthropic--claude-3-opus":     {MaxTokens: 4096, ContextWindow: 200000},

	// Gemini models via Vertex
	"gemini-2.5-pro":   {MaxTokens: 65536, ContextWindow: 1048576},
	"gemini-2.5-flash": {MaxTokens: 65536, ContextWindow: 1048576},

	// OpenAI models
	"gpt-4":        {MaxTokens: 4096, ContextWindow: 200000},
	"gpt-4o":       {MaxTokens: 4096, ContextWindow: 200000},
	"gpt-4o-mini":  {MaxTokens: 4096, ContextWindow: 200000},
	"gpt-4.1":      {MaxTokens: 32768, ContextWindow: 1047576},
	"gpt-4.1-nano": {MaxTokens: 32768, ContextWindow: 1047576},
	"gpt-5":        {MaxTokens: 128000, ContextWindow: 272000},
	"gpt-5-nano":   {MaxTokens: 128000, ContextWindow: 272000},
	"gpt-5-mini":   {MaxTokens: 128000, ContextWindow: 272000},

	// Reasoning models
	"o1":      {MaxTokens: 4096, ContextWindow: 200000},
	"o3":      {MaxTokens: 100000, ContextWindow: 200000},
	"o3-mini": {MaxTokens: 4096, ContextWindow: 200000},
	"o4-mini": {MaxTokens: 100000, ContextWindow: 200000},

	// Perplexity models
	"sonar":     {MaxTokens: 128000, ContextWindow: 128000},
	"sonar-pro": {MaxTokens: 128000, ContextWindow: 200000},
}

// GetModelConfig returns the configuration for a model, with fallback defaults.
//
// Temperature handling for unknown anthropic--claude-X models:
//   - Major version ≤ 3 (e.g. claude-3.x): temperature IS supported → NoTemperature: false
//   - Major version ≥ 4 (e.g. claude-4.x, claude-5.x, …): temperature is deprecated
//     → NoTemperature: true. This covers any future model not yet in the static map.
//   - Non-Anthropic models: temperature included (NoTemperature: false)
func GetModelConfig(modelName string) ModelConfig {
	if config, ok := ModelConfigs[modelName]; ok {
		return config
	}
	// For unknown anthropic--claude-X models, infer from the major version:
	// only Claude 3.x and below support temperature; 4.x and above do not.
	if strings.HasPrefix(modelName, "anthropic--claude-") {
		rest := strings.TrimPrefix(modelName, "anthropic--claude-")
		// rest starts with the major version digits followed by "." or "-"
		dotIdx := strings.IndexAny(rest, ".-")
		if dotIdx > 0 {
			if major, err := strconv.Atoi(rest[:dotIdx]); err == nil {
				noTemp := major >= 4
				return ModelConfig{MaxTokens: 64000, ContextWindow: 200000, NoTemperature: noTemp}
			}
		}
	}
	// Default fallback for all other unknown models (OpenAI, Gemini, etc.)
	return ModelConfig{MaxTokens: 64000, ContextWindow: 200000}
}

// ModelIDMap maps friendly model names to SAP AI Core model names
var ModelIDMap = map[string]string{
	"gpt-4o":        "gpt-4o",
	"gpt-4o-mini":   "gpt-4o-mini",
	"gpt-4":         "gpt-4",
	"gpt-3.5-turbo": "gpt-3.5-turbo",
	"o1":            "o1",
	"o4-mini":       "o4-mini",
	"gpt-5":         "gpt-5",
}

// ResolveDeploymentID finds the deployment ID for a given model name.
func ResolveDeploymentID(ctx context.Context, modelName string) (string, error) {
	// Check map first
	if mapped, ok := ModelIDMap[modelName]; ok {
		modelName = mapped
	}

	// We need a temporary transport to get the token
	t := &sapTransport{
		base:          http.DefaultTransport,
		clientID:      os.Getenv("AICORE_CLIENT_ID"),
		clientSecret:  os.Getenv("AICORE_CLIENT_SECRET"),
		authURL:       os.Getenv("AICORE_AUTH_URL"),
		resourceGroup: os.Getenv("AICORE_RESOURCE_GROUP"),
	}

	token, err := t.getToken()
	if err != nil {
		return "", fmt.Errorf("failed to get token for deployment lookup: %w", err)
	}

	baseURL := os.Getenv("AICORE_BASE_URL")
	if !strings.HasSuffix(baseURL, "/v2") {
		baseURL += "/v2"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/lm/deployments", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", t.resourceGroup)

	client := httpool.Client(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		return "", fmt.Errorf("failed to list deployments: %s, body: %s", resp.Status, string(body))
	}

	var result struct {
		Resources []struct {
			ID      string `json:"id"`
			Details struct {
				Resources struct {
					BackendDetails struct {
						Model struct {
							Name string `json:"name"`
						} `json:"model"`
					} `json:"backendDetails"`
				} `json:"resources"`
			} `json:"details"`
			Status string `json:"status"`
		} `json:"resources"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	for _, res := range result.Resources {
		if res.Status == "RUNNING" && res.Details.Resources.BackendDetails.Model.Name == modelName {
			return res.ID, nil
		}
	}

	return "", fmt.Errorf("no running deployment found for model: %s", modelName)
}

// ListModels fetches the list of available models from running deployments.
func ListModels(ctx context.Context, clientID, clientSecret, authURL, baseURL, resourceGroup string) ([]string, error) {
	t := &sapTransport{
		base:          http.DefaultTransport,
		clientID:      clientID,
		clientSecret:  clientSecret,
		authURL:       authURL,
		resourceGroup: resourceGroup,
	}

	token, err := t.getToken()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	if !strings.HasSuffix(baseURL, "/v2") {
		if strings.HasSuffix(baseURL, "/") {
			baseURL += "v2"
		} else {
			baseURL += "/v2"
		}
	}

	requestURL := fmt.Sprintf("%s/lm/deployments?status=RUNNING&resourceGroup=%s", baseURL, resourceGroup)
	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", resourceGroup)
	// Token will be added by the transport

	client := httpool.Client(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch deployments: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		return nil, fmt.Errorf("%s, body: %s", resp.Status, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Resources []struct {
			Details struct {
				Resources struct {
					BackendDetails struct {
						Model struct {
							Name string `json:"name"`
						} `json:"model"`
					} `json:"backendDetails"`
				} `json:"resources"`
			} `json:"details"`
			Status string `json:"status"`
		} `json:"resources"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	modelSet := make(map[string]bool)
	for _, res := range result.Resources {
		if res.Status == "RUNNING" {
			name := res.Details.Resources.BackendDetails.Model.Name
			if name != "" {
				modelSet[name] = true
			}
		}
	}

	var models []string
	for m := range modelSet {
		models = append(models, m)
	}
	sort.Strings(models)
	return models, nil
}

// ModelInfo represents enhanced model metadata for SAP AI Core
type ModelInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContextLength   int    `json:"context_length,omitempty"`
	MaxOutputTokens int    `json:"max_completion_tokens,omitempty"`
	SupportsTools   *bool  `json:"supports_tools,omitempty"`
}

// Model cache for SAP AI Core
var (
	sapModelCacheMu   sync.RWMutex
	sapModelCache     []ModelInfo
	sapModelCacheTime time.Time
	sapModelCacheTTL  = 1 * time.Hour
)

// ListModelsWithMetadata fetches running deployments and enriches with ModelConfigs metadata
func ListModelsWithMetadata(ctx context.Context, clientID, clientSecret, authURL, baseURL, resourceGroup string) ([]ModelInfo, error) {
	// Check cache first
	sapModelCacheMu.RLock()
	if len(sapModelCache) > 0 && time.Since(sapModelCacheTime) < sapModelCacheTTL {
		cached := make([]ModelInfo, len(sapModelCache))
		copy(cached, sapModelCache)
		sapModelCacheMu.RUnlock()
		return cached, nil
	}
	sapModelCacheMu.RUnlock()

	// Fetch model IDs using existing ListModels
	modelIDs, err := ListModels(ctx, clientID, clientSecret, authURL, baseURL, resourceGroup)
	if err != nil {
		return nil, err
	}

	// Enrich with ModelConfigs metadata; learned limits override MaxOutputTokens
	// and SupportsTools when known.
	var models []ModelInfo
	limits := getModelLimitsStore()
	for _, id := range modelIDs {
		config := GetModelConfig(id)
		maxOut := config.MaxTokens
		var supportsTools *bool
		if limits != nil {
			if entry, err := limits.Get(ctx, sapAICoreProviderType, id); err == nil && entry != nil {
				if entry.MaxOutputTokens > 0 {
					maxOut = entry.MaxOutputTokens
				}
				if entry.SupportsTools != nil {
					supportsTools = entry.SupportsTools
				}
			}
		}
		models = append(models, ModelInfo{
			ID:              id,
			Name:            id,
			ContextLength:   config.ContextWindow,
			MaxOutputTokens: maxOut,
			SupportsTools:   supportsTools,
		})
	}

	// Update cache
	sapModelCacheMu.Lock()
	sapModelCache = models
	sapModelCacheTime = time.Now()
	sapModelCacheMu.Unlock()

	return models, nil
}

func (p *Provider) generateBedrockContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Get model-specific config
		config := GetModelConfig(p.modelName)

		// Convert request using bedrock protocol with model-specific maxTokens
		bedrockReq, err := bedrock.ConvertRequest(req, config.MaxTokens, config.NoTemperature)
		if err != nil {
			yield(nil, err)
			return
		}

		payload, err := json.Marshal(bedrockReq)
		if err != nil {
			yield(nil, err)
			return
		}

		var url string
		if streaming {
			url = fmt.Sprintf("%s/inference/deployments/%s/invoke-with-response-stream", p.baseURL, p.deploymentID)
		} else {
			url = fmt.Sprintf("%s/inference/deployments/%s/invoke", p.baseURL, p.deploymentID)
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("AI-Resource-Group", p.authConfig.resourceGroup)
		// Token is added by transport

		resp, err := p.httpClient.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
			}
			yield(nil, llmerror.NewFromResponse("sap-bedrock", resp, body))
			return
		}

		if streaming {
			// Handle streaming response using bedrock protocol
			for resp, err := range bedrock.ParseStream(resp.Body) {
				if !yield(resp, err) {
					return
				}
			}
		} else {
			// Non-streaming response
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
			}
			llmResp, err := bedrock.ParseResponse(body)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(llmResp, nil)
		}
	}
}

func (p *Provider) generateVertexContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		maxTokens := resolveMaxOutputTokens(ctx, p.modelName)
		omitTools := resolveOmitTools(ctx, p.modelName)
		retriedMaxOut := false
		retriedNoTools := false

		for {
			resp, body, err := p.doVertexRequest(ctx, req, streaming, maxTokens, omitTools)
			if err != nil {
				yield(nil, err)
				return
			}

			if resp.StatusCode == http.StatusOK {
				if streaming {
					for llmResp, parseErr := range vertex.ParseStream(resp.Body) {
						if !yield(llmResp, parseErr) {
							resp.Body.Close()
							return
						}
					}
					resp.Body.Close()
					return
				}
				llmResp, parseErr := vertex.ParseResponse(body)
				resp.Body.Close()
				if parseErr != nil {
					yield(nil, parseErr)
					return
				}
				yield(llmResp, nil)
				return
			}

			resp.Body.Close()
			llmErr := llmerror.NewFromResponse("sap-vertex", resp, body)
			bodyStr := string(body)

			if resp.StatusCode == http.StatusBadRequest {
				// Learn maxOutputTokens from the error and retry once.
				if !retriedMaxOut {
					if learned, ok := llmerror.ParseMaxOutputTokensLimit(bodyStr); ok && learned > 0 && learned < maxTokens {
						if s := getModelLimitsStore(); s != nil {
							if upsertErr := s.UpsertMaxOutput(ctx, sapAICoreProviderType, p.modelName, learned, "learned_400"); upsertErr != nil {
								slog.Warn("failed to persist learned maxOutputTokens",
									"component", "sap-vertex", "model", p.modelName, "max", learned, "error", upsertErr)
							} else {
								invalidateModelCache()
								slog.Info("learned maxOutputTokens from provider 400",
									"component", "sap-vertex", "model", p.modelName, "max", learned)
							}
						}
						maxTokens = learned
						retriedMaxOut = true
						continue
					}
				}

				// Learn that the model rejects function calling and retry once without tools.
				if !retriedNoTools && !omitTools && llmerror.IsNoFunctionCalling(bodyStr) && requestHasTools(req) {
					if s := getModelLimitsStore(); s != nil {
						if upsertErr := s.UpsertSupportsTools(ctx, sapAICoreProviderType, p.modelName, false, "learned_400"); upsertErr != nil {
							slog.Warn("failed to persist learned supports_tools=false",
								"component", "sap-vertex", "model", p.modelName, "error", upsertErr)
						} else {
							invalidateModelCache()
							slog.Info("learned model does not support function calling",
								"component", "sap-vertex", "model", p.modelName)
						}
					}
					omitTools = true
					retriedNoTools = true
					continue
				}
			}

			yield(nil, llmErr)
			return
		}
	}
}

func requestHasTools(req *model.LLMRequest) bool {
	return req != nil && req.Config != nil && (len(req.Config.Tools) > 0 || req.Config.ToolConfig != nil)
}

// doVertexRequest builds and executes one Vertex generateContent call.
// On HTTP 200 with streaming, the caller owns resp.Body.
// On HTTP 200 with non-streaming (or any error status), body is fully read
// and resp.Body is still open for the caller to Close.
func (p *Provider) doVertexRequest(ctx context.Context, req *model.LLMRequest, streaming bool, maxTokens int, omitTools bool) (*http.Response, []byte, error) {
	if omitTools {
		req = requestWithoutTools(req)
	}
	vertexReq, err := vertex.ConvertRequest(req, maxTokens)
	if err != nil {
		return nil, nil, err
	}
	payload, err := json.Marshal(vertexReq)
	if err != nil {
		return nil, nil, err
	}

	var url string
	if streaming {
		url = fmt.Sprintf("%s/inference/deployments/%s/models/%s:streamGenerateContent?alt=sse", p.baseURL, p.deploymentID, p.modelName)
	} else {
		url = fmt.Sprintf("%s/inference/deployments/%s/models/%s:generateContent", p.baseURL, p.deploymentID, p.modelName)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("AI-Resource-Group", p.authConfig.resourceGroup)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode == http.StatusOK && streaming {
		return resp, nil, nil
	}

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
	}
	return resp, body, nil
}
