package provider

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider/anthropic"
	copilot_oauth "github.com/SAP/astonish/pkg/provider/copilot_oauth"
	"github.com/SAP/astonish/pkg/provider/google"
	"github.com/SAP/astonish/pkg/provider/groq"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/provider/lmstudio"
	"github.com/SAP/astonish/pkg/provider/ollama"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	openai_compat "github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/provider/openrouter"
	"github.com/SAP/astonish/pkg/provider/poe"
	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/provider/xai"
	xai_oauth "github.com/SAP/astonish/pkg/provider/xai_oauth"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
)

// debugMode is a package-level flag that enables verbose HTTP logging for
// providers that support it (e.g. openai_compat). Set via SetDebugMode().
var debugMode bool

// SetDebugMode enables or disables debug HTTP logging for providers.
// Call this before GetProvider() to enable request/response logging.
func SetDebugMode(enabled bool) {
	debugMode = enabled
}

// confineProviderSecretsKey marks a context as belonging to a multi-tenant
// (platform) request, where a provider instance must NOT inherit process-wide
// environment-variable credentials (LITELLM_API_KEY, AICORE_*, OPENAI_API_KEY,
// …). In platform mode those env vars are the server operator's secrets; a
// tenant who creates a provider pointed at an attacker-controlled base_url and
// omits the key would otherwise have the operator's key dialed to that URL.
// Trusted local Code mode never sets this flag, so its legitimate env-var
// fallback is preserved.
type confineProviderSecretsKey struct{}

// WithConfinedProviderSecrets returns a context in which GetProvider refuses to
// fall back to process environment variables for a provider instance's secret
// fields. Set this on every multi-tenant (platform) request path.
func WithConfinedProviderSecrets(ctx context.Context) context.Context {
	return context.WithValue(ctx, confineProviderSecretsKey{}, true)
}

// providerSecretsConfined reports whether env-var secret fallback is disabled.
func providerSecretsConfined(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(confineProviderSecretsKey{}).(bool)
	return v
}

// IsConfinedProviderSecrets reports whether the context is a multi-tenant
// (platform) request in which a provider instance must not inherit the server
// operator's process-wide credentials — neither environment variables nor
// operator-owned credential-store entries keyed by a tenant-chosen provider
// name. API-layer secret resolvers (resolveProviderSecret) consult this so a
// tenant who creates a provider pointed at an attacker-controlled base_url and
// omits the key does not have the operator's key dialed to that URL.
func IsConfinedProviderSecrets(ctx context.Context) bool {
	return providerSecretsConfined(ctx)
}

// envFallback returns os.Getenv(key) unless the context is confined to a
// multi-tenant request, in which case it returns "" so a tenant provider never
// inherits the server operator's environment credentials.
func envFallback(ctx context.Context, key string) string {
	if providerSecretsConfined(ctx) {
		return ""
	}
	return os.Getenv(key)
}

// ProviderDisplayNames maps provider IDs to their proper display names.
// This is the centralized source of truth for how provider names should be displayed
// in both the CLI and UI.
var ProviderDisplayNames = map[string]string{
	"anthropic":     "Anthropic",
	"gemini":        "Google GenAI",
	"groq":          "Groq",
	"litellm":       "LiteLLM",
	"lm_studio":     "LM Studio",
	"ollama":        "Ollama",
	"openai":        "OpenAI",
	"openai_compat": "OpenAI Compatible",
	"openrouter":    "Openrouter",
	"poe":           "Poe",
	"sap_ai_core":   "SAP AI Core",
	"xai":           "xAI",
	"xai_oauth":     "xAI (OAuth)",
	"copilot_oauth": "GitHub Copilot (OAuth)",
}

// GetProviderDisplayName returns the proper display name for a provider ID.
// If the provider ID is not found, it returns the ID as-is.
func GetProviderDisplayName(providerID string) string {
	if name, ok := ProviderDisplayNames[providerID]; ok {
		return name
	}
	return providerID
}

// GetProviderIDs returns a list of all known provider IDs.
func GetProviderIDs() []string {
	ids := make([]string, 0, len(ProviderDisplayNames))
	for id := range ProviderDisplayNames {
		ids = append(ids, id)
	}
	return ids
}

// resolveProviderInstance looks up a provider in cfg.Providers with fallback
// matching. It first tries an exact key match, then falls back to:
//   - case-insensitive key match
//   - display-name-to-ID: requested ID matches the normalized form of a key
//     (e.g. "sap_ai_core" matches key "SAP AI Core")
//   - ID-to-display-name: requested display name matches the ID for a key
//
// Returns the resolved key, the provider config, and whether it was found.
func resolveProviderInstance(instanceName string, cfg *config.AppConfig) (string, config.ProviderConfig, bool) {
	// Exact match (fast path)
	if instance, ok := cfg.Providers[instanceName]; ok {
		return instanceName, instance, true
	}

	// Build the normalized form of the requested name for comparison:
	// lowercase, spaces replaced with underscores.
	normalizedReq := strings.ToLower(strings.ReplaceAll(instanceName, " ", "_"))

	for key, instance := range cfg.Providers {
		// Case-insensitive match
		if strings.EqualFold(key, instanceName) {
			return key, instance, true
		}
		// Normalized match: "SAP AI Core" -> "sap_ai_core" == "sap_ai_core"
		normalizedKey := strings.ToLower(strings.ReplaceAll(key, " ", "_"))
		if normalizedKey == normalizedReq {
			return key, instance, true
		}
		// Display name reverse lookup: requested "sap_ai_core" has display name
		// "SAP AI Core" which equals the key "SAP AI Core"
		if displayName, ok := ProviderDisplayNames[instanceName]; ok {
			if strings.EqualFold(key, displayName) {
				return key, instance, true
			}
		}
	}

	return "", nil, false
}

// GetProvider returns an LLM model based on a provider instance name.
func GetProvider(ctx context.Context, instanceName string, modelName string, cfg *config.AppConfig) (model.LLM, error) {
	resolvedName, instance, exists := resolveProviderInstance(instanceName, cfg)
	if !exists {
		return nil, fmt.Errorf("provider instance '%s' not found", instanceName)
	}
	instanceName = resolvedName

	providerType := config.GetProviderType(instanceName, instance)
	if providerType == "" {
		return nil, fmt.Errorf("provider instance '%s' has no 'type' field and name is not a known provider type", instanceName)
	}

	switch providerType {
	case "anthropic":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "ANTHROPIC_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
		}
		if modelName == "" {
			modelName = "claude-3-opus-20240229"
		}
		return anthropic.NewProvider(apiKey, modelName), nil

	case "google_genai", "gemini":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "GOOGLE_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("GOOGLE_API_KEY not set")
		}
		if modelName == "" {
			modelName = "gemini-1.5-flash"
		}
		return google.NewProvider(ctx, modelName, apiKey)

	case "openai":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "OPENAI_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY not set")
		}
		if modelName == "" {
			modelName = "gpt-4"
		}
		client := openai.NewClient(apiKey)
		return openai_provider.NewOpenAIProvider(client, modelName, true), nil

	case "openrouter":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "OPENROUTER_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("OPENROUTER_API_KEY not set")
		}
		if modelName == "" {
			return nil, fmt.Errorf("model name required for openrouter")
		}

		config := openai.DefaultConfig(apiKey)
		config.BaseURL = "https://openrouter.ai/api/v1"
		client := openai.NewClientWithConfig(config)

		maxTokens := openrouter.GetMaxCompletionTokens(ctx, apiKey, modelName)
		if maxTokens > 0 {
			slog.Info("setting max_completion_tokens", "component", "openrouter", "model", modelName, "max_tokens", maxTokens)
			return openai_provider.NewProviderWithMaxTokens(client, modelName, true, maxTokens), nil
		}
		return openai_provider.NewProvider(client, modelName, true), nil

	case "poe":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "POE_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("POE_API_KEY not set")
		}
		if modelName == "" {
			modelName = "gpt-4o"
		}

		config := openai.DefaultConfig(apiKey)
		config.BaseURL = poe.GetBaseURL()
		client := openai.NewClientWithConfig(config)
		return openai_provider.NewProvider(client, modelName, true), nil

	case "ollama":
		baseURL := "http://localhost:11434"
		if instance["base_url"] != "" {
			baseURL = instance["base_url"]
		}
		if modelName == "" {
			return nil, fmt.Errorf("model name required for ollama")
		}

		config := openai.DefaultConfig("ollama")
		config.BaseURL = fmt.Sprintf("%s/v1", baseURL)
		client := openai.NewClientWithConfig(config)
		return openai_provider.NewProvider(client, modelName, true), nil

	case "groq":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "GROQ_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("GROQ_API_KEY not set")
		}
		if modelName == "" {
			modelName = "llama3-70b-8192"
		}

		config := openai.DefaultConfig(apiKey)
		config.BaseURL = "https://api.groq.com/openai/v1"
		client := openai.NewClientWithConfig(config)
		return openai_provider.NewProvider(client, modelName, true), nil

	case "lm_studio":
		baseURL := "http://localhost:1234/v1"
		if instance["base_url"] != "" {
			baseURL = instance["base_url"]
		}
		if modelName == "" {
			return nil, fmt.Errorf("model name required for lm_studio")
		}

		config := openai.DefaultConfig("lm-studio")
		config.BaseURL = baseURL
		client := openai.NewClientWithConfig(config)
		return openai_provider.NewProvider(client, modelName, false), nil

	case "litellm":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "LITELLM_API_KEY")
		}

		baseURL := "http://localhost:4000"
		if instance["base_url"] != "" {
			baseURL = strings.TrimSuffix(instance["base_url"], "/v1")
		}
		if modelName == "" {
			return nil, fmt.Errorf("model name required for litellm")
		}
		return litellm.NewProvider(apiKey, baseURL, modelName), nil

	case "sap_ai_core":
		if modelName == "" {
			return nil, fmt.Errorf("model name required for sap_ai_core")
		}

		clientID := instance["client_id"]
		if clientID == "" {
			clientID = envFallback(ctx, "AICORE_CLIENT_ID")
		}
		clientSecret := instance["client_secret"]
		if clientSecret == "" {
			clientSecret = envFallback(ctx, "AICORE_CLIENT_SECRET")
		}
		authURL := instance["auth_url"]
		if authURL == "" {
			authURL = envFallback(ctx, "AICORE_AUTH_URL")
		}
		baseURL := instance["base_url"]
		if baseURL == "" {
			baseURL = envFallback(ctx, "AICORE_BASE_URL")
		}
		resourceGroup := instance["resource_group"]
		if resourceGroup == "" {
			resourceGroup = envFallback(ctx, "AICORE_RESOURCE_GROUP")
		}

		if clientID == "" || clientSecret == "" || authURL == "" || baseURL == "" {
			return nil, fmt.Errorf("SAP AI Core configuration incomplete")
		}
		return sap.NewProviderWithConfig(ctx, modelName, clientID, clientSecret, authURL, baseURL, resourceGroup)

	case "xai", "grok":
		apiKey := instance["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "XAI_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("XAI_API_KEY not set")
		}
		if modelName == "" {
			modelName = "grok-beta"
		}

		config := openai.DefaultConfig(apiKey)
		config.BaseURL = "https://api.x.ai/v1"
		client := openai.NewClientWithConfig(config)
		return openai_provider.NewProvider(client, modelName, true), nil

	case "xai_oauth":
		oauth, err := resolveOAuthConfig(ctx, providerType, instance)
		if err != nil {
			return nil, err
		}
		if modelName == "" {
			modelName = "grok-3"
		}
		return xai_oauth.NewProvider(oauth.clientID, oauth.accessToken, oauth.refreshToken, oauth.expiresAt, modelName, xaiOAuthRefreshCallback(cfg, instanceName)), nil

	case "copilot_oauth":
		oauth, err := resolveOAuthConfig(ctx, providerType, instance)
		if err != nil {
			return nil, err
		}
		if modelName == "" {
			modelName = "gpt-4o"
		}
		return copilot_oauth.NewProvider(oauth.githubToken, modelName), nil

	case "openai_compat":
		apiKey := instance["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("API key not set for OpenAI Compatible provider")
		}
		baseURL := instance["base_url"]
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		if modelName == "" {
			modelName = "gpt-4o"
		}
		return openai_compat.NewProvider(ctx, apiKey, baseURL, modelName, debugMode), nil

	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

func xaiOAuthRefreshCallback(cfg *config.AppConfig, instanceName string) func(string, string, time.Time) {
	if cfg == nil || cfg.ProviderTokenRefresh == nil {
		return nil
	}
	return func(accessToken, refreshToken string, expiresAt time.Time) {
		if err := cfg.ProviderTokenRefresh(instanceName, accessToken, refreshToken, expiresAt); err != nil {
			slog.Error("failed to persist refreshed OAuth tokens", "provider", instanceName, "error", err)
		}
	}
}

type oauthConfig struct {
	clientID, accessToken, refreshToken, githubToken string
	expiresAt                                        time.Time
}

// resolveOAuthConfig centralizes config precedence and tenant-safe env fallback.
func resolveOAuthConfig(ctx context.Context, providerType string, instance config.ProviderConfig) (oauthConfig, error) {
	get := func(field, env string) string {
		if value := instance[field]; value != "" {
			return value
		}
		return envFallback(ctx, env)
	}
	var resolved oauthConfig
	switch providerType {
	case "xai_oauth":
		resolved.accessToken = get("access_token", "XAI_OAUTH_ACCESS_TOKEN")
		resolved.refreshToken = get("refresh_token", "XAI_OAUTH_REFRESH_TOKEN")
		resolved.clientID = get("client_id", "XAI_OAUTH_CLIENT_ID")
		if resolved.clientID == "" {
			resolved.clientID = xai_oauth.DefaultClientID
		}
		resolved.expiresAt, _ = time.Parse(time.RFC3339, instance["expires_at"])
		if resolved.accessToken == "" {
			return resolved, fmt.Errorf("xAI OAuth requires access_token (run setup to authenticate)")
		}
	case "copilot_oauth":
		resolved.githubToken = get("github_token", "GITHUB_COPILOT_TOKEN")
		if resolved.githubToken == "" {
			return resolved, fmt.Errorf("Copilot OAuth requires github_token (run setup to authenticate)")
		}
	}
	return resolved, nil
}

// ListModelsForProvider fetches available models for a given provider instance.
// This is used by the API to provide model lists to the UI.
func ListModelsForProvider(ctx context.Context, providerID string, cfg *config.AppConfig) ([]string, error) {
	resolvedID, instance, exists := resolveProviderInstance(providerID, cfg)
	if !exists {
		return nil, fmt.Errorf("provider instance '%s' not found", providerID)
	}
	providerID = resolvedID

	providerType := config.GetProviderType(providerID, instance)
	if providerType == "" {
		return nil, fmt.Errorf("provider instance '%s' has no 'type' field and name is not a known provider type", providerID)
	}

	instanceConfig := instance

	switch providerType {
	case "anthropic":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "ANTHROPIC_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("Anthropic API key not configured")
		}
		return anthropic.ListModels(ctx, apiKey)

	case "google_genai", "gemini":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "GOOGLE_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("Google API key not configured")
		}
		return google.ListModels(ctx, apiKey)

	case "openai":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "OPENAI_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("OpenAI API key not configured")
		}
		return openai_provider.ListModels(ctx, apiKey)

	case "groq":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "GROQ_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("Groq API key not configured")
		}
		return groq.ListModels(ctx, apiKey)

	case "xai":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "XAI_API_KEY")
		}
		if apiKey == "" {
			return nil, fmt.Errorf("xAI API key not configured")
		}
		return xai.ListModels(ctx, apiKey)

	case "xai_oauth":
		oauth, err := resolveOAuthConfig(ctx, providerType, instanceConfig)
		if err != nil {
			return nil, err
		}
		return xai_oauth.ListModelsWithOAuth(ctx, oauth.clientID, oauth.accessToken, oauth.refreshToken, oauth.expiresAt, xaiOAuthRefreshCallback(cfg, providerID))

	case "copilot_oauth":
		oauth, err := resolveOAuthConfig(ctx, providerType, instanceConfig)
		if err != nil {
			return nil, err
		}
		return copilot_oauth.ListModels(ctx, oauth.githubToken)

	case "ollama":
		baseURL := "http://localhost:11434"
		if instanceConfig["base_url"] != "" {
			baseURL = instanceConfig["base_url"]
		}
		return ollama.ListModels(ctx, baseURL)

	case "lm_studio":
		baseURL := "http://localhost:1234/v1"
		if instanceConfig["base_url"] != "" {
			baseURL = instanceConfig["base_url"]
		}
		return lmstudio.ListModels(ctx, baseURL)

	case "litellm":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "LITELLM_API_KEY")
		}

		baseURL := "http://localhost:4000/v1"
		if instanceConfig["base_url"] != "" {
			baseURL = instanceConfig["base_url"]
		}
		return litellm.ListModels(ctx, apiKey, baseURL)

	case "openrouter":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "OPENROUTER_API_KEY")
		}
		models, err := openrouter.ListModels(apiKey)
		if err != nil {
			return nil, err
		}
		var modelNames []string
		for _, m := range models {
			modelNames = append(modelNames, m.ID)
		}
		return modelNames, nil

	case "poe":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			apiKey = envFallback(ctx, "POE_API_KEY")
		}
		return poe.ListModels(ctx, apiKey)

	case "sap_ai_core":
		clientID := instanceConfig["client_id"]
		if clientID == "" {
			clientID = envFallback(ctx, "AICORE_CLIENT_ID")
		}
		clientSecret := instanceConfig["client_secret"]
		if clientSecret == "" {
			clientSecret = envFallback(ctx, "AICORE_CLIENT_SECRET")
		}
		authURL := instanceConfig["auth_url"]
		if authURL == "" {
			authURL = envFallback(ctx, "AICORE_AUTH_URL")
		}
		baseURL := instanceConfig["base_url"]
		if baseURL == "" {
			baseURL = envFallback(ctx, "AICORE_BASE_URL")
		}
		resourceGroup := instanceConfig["resource_group"]
		if resourceGroup == "" {
			resourceGroup = envFallback(ctx, "AICORE_RESOURCE_GROUP")
		}

		if clientID == "" || clientSecret == "" || authURL == "" || baseURL == "" {
			return nil, fmt.Errorf("SAP AI Core configuration incomplete")
		}
		return sap.ListModels(ctx, clientID, clientSecret, authURL, baseURL, resourceGroup)

	case "openai_compat":
		apiKey := instanceConfig["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("API key not configured for OpenAI Compatible provider")
		}
		baseURL := instanceConfig["base_url"]
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return openai_compat.ListModels(ctx, apiKey, baseURL)

	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

// TestProviderConnection validates provider credentials by attempting to list
// available models. Accepts raw credentials (not from config) so it can be
// used by the API test endpoint before credentials are persisted.
// Returns the list of available models on success, or an error describing
// the connectivity/authentication failure.
func TestProviderConnection(ctx context.Context, providerType string, params map[string]string) ([]string, error) {
	switch providerType {
	case "anthropic":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return anthropic.ListModels(ctx, apiKey)

	case "google_genai", "gemini":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return google.ListModels(ctx, apiKey)

	case "openai":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return openai_provider.ListModels(ctx, apiKey)

	case "groq":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return groq.ListModels(ctx, apiKey)

	case "xai":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return xai.ListModels(ctx, apiKey)

	case "xai_oauth":
		oauth, err := resolveOAuthConfig(ctx, providerType, config.ProviderConfig(params))
		if err != nil {
			return nil, err
		}
		return xai_oauth.ListModelsWithOAuth(ctx, oauth.clientID, oauth.accessToken, oauth.refreshToken, oauth.expiresAt, nil)

	case "copilot_oauth":
		oauth, err := resolveOAuthConfig(ctx, providerType, config.ProviderConfig(params))
		if err != nil {
			return nil, err
		}
		return copilot_oauth.ListModels(ctx, oauth.githubToken)

	case "ollama":
		baseURL := params["base_url"]
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return ollama.ListModels(ctx, baseURL)

	case "lm_studio":
		baseURL := params["base_url"]
		if baseURL == "" {
			baseURL = "http://localhost:1234/v1"
		}
		return lmstudio.ListModels(ctx, baseURL)

	case "litellm":
		apiKey := params["api_key"]
		baseURL := params["base_url"]
		if baseURL == "" {
			baseURL = "http://localhost:4000/v1"
		}
		return litellm.ListModels(ctx, apiKey, baseURL)

	case "openrouter":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		models, err := openrouter.ListModels(apiKey)
		if err != nil {
			return nil, err
		}
		var modelNames []string
		for _, m := range models {
			modelNames = append(modelNames, m.ID)
		}
		return modelNames, nil

	case "poe":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return poe.ListModels(ctx, apiKey)

	case "sap_ai_core":
		clientID := params["client_id"]
		clientSecret := params["client_secret"]
		authURL := params["auth_url"]
		baseURL := params["base_url"]
		resourceGroup := params["resource_group"]
		if clientID == "" || clientSecret == "" || authURL == "" || baseURL == "" {
			return nil, fmt.Errorf("client_id, client_secret, auth_url, and base_url are required")
		}
		return sap.ListModels(ctx, clientID, clientSecret, authURL, baseURL, resourceGroup)

	case "openai_compat":
		apiKey := params["api_key"]
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		baseURL := params["base_url"]
		if baseURL == "" {
			return nil, fmt.Errorf("base_url is required for OpenAI Compatible providers")
		}
		return openai_compat.ListModels(ctx, apiKey, baseURL)

	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}
