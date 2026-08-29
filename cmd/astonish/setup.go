package astonish

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"github.com/charmbracelet/huh"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/anthropic"
	"github.com/SAP/astonish/pkg/provider/google"
	"github.com/SAP/astonish/pkg/provider/groq"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/provider/lmstudio"
	"github.com/SAP/astonish/pkg/provider/ollama"
	openai_provider "github.com/SAP/astonish/pkg/provider/openai"
	"github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/provider/openrouter"
	"github.com/SAP/astonish/pkg/provider/poe"
	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/provider/xai"
	xai_oauth "github.com/SAP/astonish/pkg/provider/xai_oauth"
	"github.com/SAP/astonish/pkg/sandbox"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/store/pgutil"
	"golang.org/x/crypto/bcrypt"
)

func handleSetupCommand() error {
	// Escalate to root on Linux upfront — the sandbox setup step at the end
	// of the wizard needs overlay mounts, UID shifting, and Incus socket
	// access. Asking for the sudo password at the start avoids a disruptive
	// process restart mid-wizard that would lose all prior wizard state.
	if sandbox.NeedsEscalation() {
		cfg, err := config.LoadAppConfig()
		if err != nil || cfg == nil {
			cfg = &config.AppConfig{}
		}
		if sandbox.IsSandboxEnabled(&cfg.Sandbox) {
			return sandbox.Escalate()
		}
	}

	// Load config
	cfg, err := config.LoadAppConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		// Initialize empty config if load fails
		cfg = &config.AppConfig{
			Providers: make(map[string]config.ProviderConfig),
			General:   config.GeneralConfig{},
		}
	}

	// --- DEPLOYMENT MODE STEP ---
	// Ask the user to choose between SQLite (default) and PostgreSQL backends.
	// Only show this if the backend isn't already configured.
	if cfg.Storage.Backend == "" {
		if err := handleDeploymentModeSetup(cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			return err
		}
		// Save config immediately after backend setup. This ensures that if the
		// user aborts during provider configuration (the next step), the backend
		// choice (sqlite/postgres) is persisted. On next run, the wizard won't
		// re-enter deployment mode setup and won't re-run Bootstrap.
		if err := config.SaveAppConfig(cfg); err != nil {
			return fmt.Errorf("error saving config after backend setup: %w", err)
		}
	}

	// Make provider setup optional (like web search tools).
	// First check if providers already exist in the platform DB (sqlite or postgres).
	hasExisting, _ := platformHasProviders(cfg)
	if !hasExisting {
		hasExisting = len(cfg.Providers) > 0
	}

	configureProvider := false
	if hasExisting {
		// Already configured in platform DB or config — offer reconfigure.
		var reconfigure bool
		clearScreen()
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("AI Provider Already Configured").
					Description("A provider is already set up.\nYou can reconfigure it now or keep the current settings.").
					Affirmative("Reconfigure").
					Negative("Keep current").
					Value(&reconfigure),
			),
		).Run()
		if err != nil {
			return err
		}
		if reconfigure {
			configureProvider = true
		} else {
			printSuccess("Keeping existing provider configuration.")
		}
	} else {
		// Nothing configured yet — offer to set one up now or skip.
		var setupProvider bool
		clearScreen()
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Configure AI Provider?").
					Description("An AI provider is required for the agent.\nYou can configure (or change) it later in the Studio under Settings → Providers.").
					Affirmative("Yes, configure now").
					Negative("Skip for now").
					Value(&setupProvider),
			),
		).Run()
		if err != nil {
			return err
		}
		if setupProvider {
			configureProvider = true
		} else {
			printSuccess("Provider setup skipped. You can configure it in the Studio after logging in.")
		}
	}

	if configureProvider {
		// Build list of existing providers with their types
		var existingOptions []huh.Option[string]
		existingProviderTypes := make(map[string]string)

		for name, pCfg := range cfg.Providers {
			providerType := config.GetProviderType(name, pCfg)
			displayName := provider.GetProviderDisplayName(providerType)
			if displayName == "" {
				displayName = name
			}
			label := fmt.Sprintf("%s (%s)", name, displayName)
			existingOptions = append(existingOptions, huh.NewOption(label, name))
			existingProviderTypes[name] = providerType
		}

		// Sort existing options by name
		sort.Slice(existingOptions, func(i, j int) bool {
			return existingOptions[i].Value < existingOptions[j].Value
		})

		// Add "Add new provider" option at the end
		const addNewValue = "__add_new__"
		existingOptions = append(existingOptions, huh.NewOption("+ Add new provider", addNewValue))

		// --- STEP 1: Select existing or add new ---
		var selectedInstance string
		clearScreen()
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Select a provider instance to configure").
					Description("Choose an existing provider or add a new one").
					Options(existingOptions...).
					Value(&selectedInstance),
			),
		).Run()
		if err != nil {
			return err
		}

		var isNewProvider bool
		var selectedProviderID string

		if selectedInstance == addNewValue {
			isNewProvider = true
			// --- STEP 2: Provider Type (for new provider) ---
		selectedProviderID = selectProviderType()
		// Initialize provider config for new provider
		cfg.Providers[addNewValue] = make(config.ProviderConfig)
	} else {
		// Editing existing provider
		selectedProviderID = existingProviderTypes[selectedInstance]
		if selectedProviderID == "" {
			// Fallback: try to infer from instance name
			selectedProviderID = selectedInstance
		}
		// Initialize provider config if nil
		if cfg.Providers[selectedInstance] == nil {
			cfg.Providers[selectedInstance] = make(config.ProviderConfig)
		}
	}

	pCfg := cfg.Providers[selectedInstance]

	// --- STEP 3: Configuration Form ---
	switch selectedProviderID {
	case "anthropic":
		runAPIKeyForm("Anthropic API Key", "api_key", pCfg)
		if err := fetchAndSelectAnthropicModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select Anthropic models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "gemini":
		runAPIKeyForm("Google API Key", "api_key", pCfg)
		if err := fetchAndSelectGoogleModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select Google models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "groq":
		runAPIKeyForm("Groq API Key", "api_key", pCfg)
		if err := fetchAndSelectGroqModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select Groq models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "lm_studio":
		runBaseURLForm("LM Studio Base URL", "http://localhost:1234/v1", pCfg)
		if err := fetchAndSelectLMStudioModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select LM Studio models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "litellm":
		runAPIKeyForm("LiteLLM API Key", "api_key", pCfg)
		runBaseURLForm("LiteLLM Base URL", "http://localhost:4000/v1", pCfg)
		if err := fetchAndSelectLiteLLMModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select LiteLLM models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "ollama":
		runOllamaForm(pCfg)
		if err := fetchAndSelectOllamaModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select Ollama models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "openai":
		runAPIKeyForm("OpenAI API Key", "api_key", pCfg)
		if err := fetchAndSelectOpenAIModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select OpenAI models %v\n", err)
		} else {
			goto SaveConfig
		}
	case "xai":
		runAPIKeyForm("xAI API Key", "api_key", pCfg)
		if err := fetchAndSelectXAIModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select xAI models %v\n", err)
		} else {
			goto SaveConfig
		}
	case "xai_oauth":
		// Use the well-known public Grok CLI OAuth client_id (not a secret)
		clientID := xai_oauth.DefaultClientID
		pCfg["client_id"] = clientID
		// Run device-code flow
		fmt.Println("\nInitiating xAI OAuth device authorization...")
		dcResp, err := xai_oauth.RequestDeviceCode(context.Background(), clientID)
		if err != nil {
			return fmt.Errorf("failed to request device code: %w", err)
		}
		fmt.Printf("\n\U0001f510 Please visit: %s\n", dcResp.VerificationURIComplete)
		fmt.Printf("   and enter code: %s\n\n", dcResp.UserCode)
		fmt.Println("Waiting for authorization...")
		tokenResp, err := xai_oauth.PollForToken(context.Background(), clientID, dcResp.DeviceCode, dcResp.Interval)
		if err != nil {
			return fmt.Errorf("OAuth authorization failed: %w", err)
		}
		// Store tokens in config
		pCfg["access_token"] = tokenResp.AccessToken
		pCfg["refresh_token"] = tokenResp.RefreshToken
		expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		pCfg["expires_at"] = expiresAt.Format(time.RFC3339)
		fmt.Println("\u2713 Successfully authenticated with xAI OAuth!")
		if err := fetchAndSelectXAIOAuthModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select xAI models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "openai_compat":
		runAPIKeyForm("API Key", "api_key", pCfg)
		runBaseURLForm("Base URL", "https://api.openai.com/v1", pCfg)
		if err := fetchAndSelectOpenAICompatModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "openrouter":
		runAPIKeyForm("OpenRouter API Key", "api_key", pCfg)
		if err := fetchAndSelectOpenRouterModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select OpenRouter models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "poe":
		runAPIKeyForm("Poe API Key", "api_key", pCfg)
		if err := fetchAndSelectPoeModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select Poe models: %v\n", err)
		} else {
			goto SaveConfig
		}
	case "sap_ai_core":
		runSAPAICoreForm(pCfg)
		// Special handling for SAP AI Core model selection
		if err := fetchAndSelectSAPModel(pCfg, cfg); err != nil {
			if isUserAborted(err) {
				fmt.Println("Setup aborted by user; no changes were saved.")
				return nil
			}
			fmt.Printf("Warning: Failed to fetch/select SAP models: %v\n", err)
		} else {
			// Skip generic model selection if we did it specifically for SAP
			goto SaveConfig
		}
	default:
		// Unknown provider type - ask for instance name to continue
		selectedProviderID = selectProviderType()
	}

	// --- STEP 4: Default Model Selection (Generic) ---
	// Only ask if not already handled (like in SAP AI Core)
	{
		var defaultModel string = cfg.General.DefaultModel
		clearScreen()
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Default Model").
					Description("Leave empty to keep current").
					Value(&defaultModel),
			),
		).Run()
		if err == nil && defaultModel != "" {
			cfg.General.DefaultModel = defaultModel
		}
	}

SaveConfig:
	// For new providers, ask for instance name at the end
	if isNewProvider {
		var instanceName string
		clearScreen()
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Provider Instance Name").
					Description("Unique name for this provider instance").
					Value(&instanceName),
			),
		).Run()
		if err != nil {
			return err
		}
		instanceName = strings.TrimSpace(instanceName)
		if instanceName == "" {
			return fmt.Errorf("instance name is required")
		}

		// Migrate config from temp key to final name
		if instanceName != selectedInstance {
			cfg.Providers[instanceName] = pCfg
			cfg.Providers[instanceName]["type"] = selectedProviderID
			delete(cfg.Providers, selectedInstance)
			selectedInstance = instanceName
		} else {
			cfg.Providers[instanceName]["type"] = selectedProviderID
		}
	} else if cfg.Providers[selectedInstance] != nil {
		// Ensure type is set for existing providers
		if cfg.Providers[selectedInstance]["type"] == "" {
			cfg.Providers[selectedInstance]["type"] = selectedProviderID
		}
	}

	// Set as default provider
	cfg.General.DefaultProvider = selectedInstance

	// In platform mode, save provider config to the platform database.
	// This must happen BEFORE saveProviderSecretsToStore, which scrubs secrets from pCfg.
	// The platform settings store has its own secret extraction/encryption pipeline.
	// Works for both SQLite (default) and Postgres.
	if cfg.Storage.Backend == "postgres" || cfg.Storage.Backend == "sqlite" {
		if err := saveProviderToPlatformDB(cfg, selectedInstance, pCfg); err != nil {
			fmt.Printf("Warning: Failed to save provider to platform database: %v\n", err)
			fmt.Println("You may need to configure the provider via Studio settings.")
		}
	}

	// Save secrets to encrypted credential store (instead of plaintext config.yaml)
	if err := saveProviderSecretsToStore(selectedInstance, selectedProviderID, pCfg); err != nil {
		fmt.Printf("Warning: Failed to save secrets to credential store: %v\n", err)
		fmt.Println("Secrets will be saved in config.yaml as fallback.")
	}

	// Save config (secrets have been scrubbed from pCfg by saveProviderSecretsToStore)
	if err := config.SaveAppConfig(cfg); err != nil {
		return fmt.Errorf("error saving config: %w", err)
	}

	displayName := provider.GetProviderDisplayName(selectedProviderID)
	if displayName == "" {
		displayName = selectedProviderID
	}
	printSuccess(fmt.Sprintf("%s (%s) configured successfully!", selectedInstance, displayName))
	}

	// --- Web Tool Setup ---
	if err := handleWebToolSetup(); err != nil {
		if !isUserAborted(err) {
			fmt.Printf("Warning: Web tool setup failed: %v\n", err)
		}
	}

	// --- Browser Setup ---
	if err := handleBrowserSetup(); err != nil {
		if !isUserAborted(err) {
			fmt.Printf("Warning: Browser setup failed: %v\n", err)
		}
	}

	// --- Sandbox Setup ---
	if err := handleSandboxSetup(); err != nil {
		if !isUserAborted(err) {
			fmt.Printf("Warning: Sandbox setup failed: %v\n", err)
		}
	}

	return nil
}

// selectProviderType shows the provider type selection and returns the selected type
func selectProviderType() string {
	var selectedProviderID string

	providerTypeOptions := []huh.Option[string]{
		huh.NewOption(provider.GetProviderDisplayName("anthropic"), "anthropic"),
		huh.NewOption(provider.GetProviderDisplayName("gemini"), "gemini"),
		huh.NewOption(provider.GetProviderDisplayName("groq"), "groq"),
		huh.NewOption(provider.GetProviderDisplayName("litellm"), "litellm"),
		huh.NewOption(provider.GetProviderDisplayName("lm_studio"), "lm_studio"),
		huh.NewOption(provider.GetProviderDisplayName("ollama"), "ollama"),
		huh.NewOption(provider.GetProviderDisplayName("openai"), "openai"),
		huh.NewOption(provider.GetProviderDisplayName("openai_compat"), "openai_compat"),
		huh.NewOption(provider.GetProviderDisplayName("openrouter"), "openrouter"),
		huh.NewOption(provider.GetProviderDisplayName("poe"), "poe"),
		huh.NewOption(provider.GetProviderDisplayName("sap_ai_core"), "sap_ai_core"),
		huh.NewOption(provider.GetProviderDisplayName("xai"), "xai"),
		huh.NewOption(provider.GetProviderDisplayName("xai_oauth"), "xai_oauth"),
	}

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select provider type").
				Options(providerTypeOptions...).
				Value(&selectedProviderID),
		),
	).Run()
	if err != nil {
		return ""
	}

	return selectedProviderID
}

// Helper functions for forms

func runAPIKeyForm(title string, key string, pCfg config.ProviderConfig) {
	clearScreen()
	val := pCfg[key]
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title(title).
				EchoMode(huh.EchoModePassword).
				Value(&val),
		),
	).Run()
	if err != nil {
		slog.Error("form input failed", "error", err)
		os.Exit(1)
	}
	pCfg[key] = strings.TrimSpace(val)
}

func runBaseURLForm(title string, defaultVal string, pCfg config.ProviderConfig) {
	clearScreen()
	val := pCfg["base_url"]
	if val == "" {
		val = defaultVal
	}
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title(title).
				Value(&val),
		),
	).Run()
	if err != nil {
		slog.Error("form input failed", "error", err)
		os.Exit(1)
	}
	pCfg["base_url"] = val
}

func runOllamaForm(pCfg config.ProviderConfig) {
	clearScreen()
	baseURL := pCfg["base_url"]
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Ollama Base URL").
				Value(&baseURL),
		),
	).Run()
	if err != nil {
		slog.Error("form input failed", "error", err)
		os.Exit(1)
	}
	pCfg["base_url"] = baseURL
}

func runSAPAICoreForm(pCfg config.ProviderConfig) {
	clearScreen()
	clientID := pCfg["client_id"]
	clientSecret := pCfg["client_secret"]
	authURL := pCfg["auth_url"]
	baseURL := pCfg["base_url"]
	resourceGroup := pCfg["resource_group"]
	if resourceGroup == "" {
		resourceGroup = "default"
	}

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Client ID").
				Value(&clientID),
			huh.NewInput().
				Title("Client Secret").
				EchoMode(huh.EchoModePassword).
				Value(&clientSecret),
			huh.NewInput().
				Title("Auth URL").
				Value(&authURL),
			huh.NewInput().
				Title("Base URL").
				Value(&baseURL),
			huh.NewInput().
				Title("Resource Group").
				Value(&resourceGroup),
		),
	).Run()
	if err != nil {
		slog.Error("form input failed", "error", err)
		os.Exit(1)
	}

	pCfg["client_id"] = clientID
	pCfg["client_secret"] = clientSecret
	pCfg["auth_url"] = authURL
	pCfg["base_url"] = baseURL
	pCfg["resource_group"] = resourceGroup
}

// platformHasProviders returns whether any providers are already configured
// in the platform database (for sqlite or postgres backends).
// It falls back to checking cfg.Providers for file-based legacy cases.
func platformHasProviders(cfg *config.AppConfig) (bool, error) {
	ctx := context.Background()

	var settingsStore store.PlatformSettingsStore

	if cfg.Storage.Backend == "postgres" {
		if cfg.Storage.Postgres.PlatformDSN == "" {
			return false, nil
		}
		_, es, err := entstore.NewPlatformServices(ctx, entstore.Config{
			DSN:            cfg.Storage.Postgres.PlatformDSN,
			InstanceSuffix: cfg.Storage.Postgres.InstanceSuffix,
		})
		if err != nil {
			return false, err
		}
		defer es.Close()
		settingsStore = es.PlatformSettings()
	} else if cfg.Storage.Backend == "sqlite" {
		dataDir := cfg.Storage.SQLite.GetDataDir()
		if dataDir == "" {
			home, _ := os.UserHomeDir()
			dataDir = filepath.Join(home, ".config", "astonish", "data")
		}
		_, es, err := entstore.NewPlatformServices(ctx, entstore.Config{
			DSN:     "file:" + filepath.Join(dataDir, "platform.db"),
			DataDir: dataDir,
		})
		if err != nil {
			return false, err
		}
		defer es.Close()
		settingsStore = es.PlatformSettings()
	} else {
		return false, nil
	}

	settings, err := settingsStore.Get(ctx)
	if err != nil || settings == nil {
		return false, err
	}
	if len(settings.Providers) > 0 {
		return true, nil
	}
	return false, nil
}

// saveProviderToPlatformDB saves the provider configuration to the platform
// database's platform_settings table. This is used in platform mode so that
// the daemon (which reads providers exclusively from the DB) can find them.
// The platform settings store handles its own secret extraction/encryption.
func saveProviderToPlatformDB(cfg *config.AppConfig, instanceName string, pCfg config.ProviderConfig) error {
	ctx := context.Background()

	var settingsStore store.PlatformSettingsStore

	if cfg.Storage.Backend == "postgres" {
		if cfg.Storage.Postgres.PlatformDSN == "" {
			return fmt.Errorf("postgres platform DSN not configured")
		}
		_, es, err := entstore.NewPlatformServices(ctx, entstore.Config{
			DSN:            cfg.Storage.Postgres.PlatformDSN,
			InstanceSuffix: cfg.Storage.Postgres.InstanceSuffix,
		})
		if err != nil {
			return fmt.Errorf("connect to platform DB: %w", err)
		}
		defer es.Close()
		settingsStore = es.PlatformSettings()
	} else if cfg.Storage.Backend == "sqlite" {
		dataDir := cfg.Storage.SQLite.GetDataDir()
		if dataDir == "" {
			home, _ := os.UserHomeDir()
			dataDir = filepath.Join(home, ".config", "astonish", "data")
		}

		_, es, err := entstore.NewPlatformServices(ctx, entstore.Config{
			DSN:     "file:" + filepath.Join(dataDir, "platform.db"),
			DataDir: dataDir,
		})
		if err != nil {
			return fmt.Errorf("open sqlite platform services: %w", err)
		}
		defer es.Close()

		settingsStore = es.PlatformSettings()
	} else {
		return fmt.Errorf("unsupported storage backend for platform provider save: %s", cfg.Storage.Backend)
	}

	// Load existing settings (preserve any already-configured providers).
	settings, err := settingsStore.Get(ctx)
	if err != nil {
		return fmt.Errorf("load platform settings: %w", err)
	}

	// Merge the new provider into existing settings.
	if settings.Providers == nil {
		settings.Providers = make(map[string]map[string]string)
	}

	// Copy pCfg so we don't affect the caller's map.
	provCopy := make(map[string]string, len(pCfg))
	for k, v := range pCfg {
		provCopy[k] = v
	}
	settings.Providers[instanceName] = provCopy
	settings.DefaultProvider = instanceName
	if cfg.General.DefaultModel != "" {
		settings.DefaultModel = cfg.General.DefaultModel
	}

	if err := settingsStore.Save(ctx, settings); err != nil {
		return fmt.Errorf("save platform settings: %w", err)
	}

	return nil
}

// saveProviderSecretsToStore saves sensitive fields from a provider config
// into the encrypted credential store and scrubs them from the pCfg map.
// Non-secret fields (base_url, resource_group, type, model) are left in pCfg.
func saveProviderSecretsToStore(instanceName, providerType string, pCfg config.ProviderConfig) error {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("get config dir: %w", err)
	}

	store, err := credentials.Open(configDir)
	if err != nil {
		return fmt.Errorf("open credential store: %w", err)
	}

	// Determine which keys are secret for this provider type
	secretKeys := []string{"api_key"} // default
	switch providerType {
	case "sap_ai_core":
		secretKeys = []string{"client_id", "client_secret", "auth_url"}
	case "ollama", "lm_studio":
		secretKeys = nil // no secrets for local providers
	case "xai_oauth":
		secretKeys = []string{"access_token", "refresh_token", "expires_at"}
	}

	secrets := make(map[string]string)
	for _, key := range secretKeys {
		val, ok := pCfg[key]
		if !ok || val == "" {
			continue
		}
		storeKey := "provider." + instanceName + "." + key
		secrets[storeKey] = val
	}

	if len(secrets) == 0 {
		return nil
	}

	if err := store.SetSecretBatch(secrets); err != nil {
		return fmt.Errorf("save secrets: %w", err)
	}

	// Scrub secrets from pCfg so they don't end up in config.yaml
	for _, key := range secretKeys {
		delete(pCfg, key)
	}

	return nil
}

func fetchAndSelectSAPModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	runSpinner("Connecting to SAP AI Core...")

	models, err := sap.ListModels(context.Background(),
		pCfg["client_id"],
		pCfg["client_secret"],
		pCfg["auth_url"],
		pCfg["base_url"],
		pCfg["resource_group"])

	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no running models found")
	}

	sort.Strings(models)

	var modelOptions []huh.Option[string]
	for _, m := range models {
		modelOptions = append(modelOptions, huh.NewOption(m, m))
	}

	var selectedModel string
	if appCfg.General.DefaultModel != "" {
		for _, m := range models {
			if m == appCfg.General.DefaultModel {
				selectedModel = m
				break
			}
		}
	}

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a model").
				Description("Type to filter list").
				Options(modelOptions...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

// fetchAndSelectGoogleModel fetches available models from Google GenAI and prompts user to select one
func fetchAndSelectGoogleModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	apiKey := pCfg["api_key"]
	if apiKey == "" {
		return fmt.Errorf("API key is missing")
	}

	runSpinner("Fetching models from Google GenAI...")

	models, err := google.ListModels(context.Background(), apiKey)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	// Create options for huh.Select
	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a Google GenAI Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}
func fetchAndSelectOpenRouterModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	runSpinner("Fetching models from OpenRouter...")

	models, err := openrouter.ListModels(pCfg["api_key"])
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	// Create Options dynamically
	var modelOptions []huh.Option[string]
	for _, m := range models {
		label := fmt.Sprintf("[%s] %s", m.Group, m.DisplayName)
		modelOptions = append(modelOptions, huh.NewOption(label, m.ID))
	}

	var selectedModel string
	if appCfg.General.DefaultModel != "" {
		for _, m := range models {
			if m.ID == appCfg.General.DefaultModel {
				selectedModel = m.ID
				break
			}
		}
	}

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a model").
				Description("Type to filter list").
				Options(modelOptions...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectOllamaModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	baseURL := pCfg["base_url"]
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	runSpinner("Fetching models from Ollama...")

	models, err := ollama.ListModels(context.Background(), baseURL)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select an Ollama Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectLMStudioModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	baseURL := pCfg["base_url"]
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}

	runSpinner("Fetching models from LM Studio...")

	models, err := lmstudio.ListModels(context.Background(), baseURL)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select an LM Studio Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectGroqModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	apiKey := pCfg["api_key"]
	if apiKey == "" {
		return fmt.Errorf("API key required")
	}

	runSpinner("Fetching models from Groq...")

	models, err := groq.ListModels(context.Background(), apiKey)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a Groq Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func isUserAborted(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "user aborted") || strings.Contains(msg, "aborted") || strings.Contains(msg, "interrupt")
}

func runSpinner(msg string) {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))

	// We can't easily run the bubbletea program and block here without more complex setup
	// For now, let's just print a message that looks nice
	fmt.Printf("%s %s\n", s.Style.Render("•"), msg)

	// In a real CLI app we might want to use tea.NewProgram to run the spinner properly
	// but since we're about to make a blocking network call, we can't update the spinner
	// unless we run the network call in a goroutine.
	// For simplicity in this setup script, we just print the message.
}

// clearScreen resets the terminal so each wizard page starts fresh.
func clearScreen() {
	fmt.Print("\033[2J\033[H")
}

func printSuccess(msg string) {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")). // Green
		Bold(true).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("42"))

	fmt.Println(style.Render("✓ " + msg))
}

func fetchAndSelectOpenAIModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	apiKey := pCfg["api_key"]
	if apiKey == "" {
		return fmt.Errorf("API key required")
	}

	runSpinner("Fetching models from OpenAI...")

	models, err := openai_provider.ListModels(context.Background(), apiKey)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select an OpenAI Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectAnthropicModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	apiKey := pCfg["api_key"]
	if apiKey == "" {
		return fmt.Errorf("API key required")
	}

	runSpinner("Fetching models from Anthropic...")

	models, err := anthropic.ListModels(context.Background(), apiKey)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	// Create options for huh.Select
	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select an Anthropic Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectXAIModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	apiKey := pCfg["api_key"]
	if apiKey == "" {
		return fmt.Errorf("API key required")
	}

	runSpinner("Fetching models from xAI...")

	models, err := xai.ListModels(context.Background(), apiKey)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select an xAI Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectXAIOAuthModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	accessToken := pCfg["access_token"]
	if accessToken == "" {
		return fmt.Errorf("access token required")
	}
	runSpinner("Fetching models from xAI...")
	models, err := xai_oauth.ListModels(context.Background(), accessToken)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}
	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}
	var selectedModel string
	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select an xAI Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()
	if err != nil {
		return err
	}
	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectPoeModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	runSpinner("Fetching models from Poe...")

	ctx := context.Background()
	models, err := poe.ListModels(ctx, pCfg["api_key"])
	if err != nil {
		return fmt.Errorf("failed to fetch models: %w", err)
	}

	if len(models) == 0 {
		return fmt.Errorf("no models available from Poe")
	}

	options := make([]huh.Option[string], len(models))
	for i, m := range models {
		options[i] = huh.NewOption(m, m)
	}

	var selectedModel string
	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a Poe Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectLiteLLMModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	apiKey := pCfg["api_key"]
	baseURL := pCfg["base_url"]
	if baseURL == "" {
		baseURL = "http://localhost:4000/v1"
	}

	runSpinner("Fetching models from LiteLLM...")

	models, err := litellm.ListModels(context.Background(), apiKey, baseURL)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a LiteLLM Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

func fetchAndSelectOpenAICompatModel(pCfg config.ProviderConfig, appCfg *config.AppConfig) error {
	apiKey := pCfg["api_key"]
	baseURL := pCfg["base_url"]
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	runSpinner("Fetching models...")

	models, err := openai_compat.ListModels(context.Background(), apiKey, baseURL)
	if err != nil {
		return err
	}

	if len(models) == 0 {
		return fmt.Errorf("no models found")
	}

	var options []huh.Option[string]
	for _, m := range models {
		options = append(options, huh.NewOption(m, m))
	}

	var selectedModel string

	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a Model").
				Options(options...).
				Value(&selectedModel),
		),
	).Run()

	if err != nil {
		return err
	}

	appCfg.General.DefaultModel = selectedModel
	return nil
}

// handleWebToolSetup prompts the user to configure a standard web tool MCP server.
func handleWebToolSetup() error {
	// Check if any web tool is already configured
	appCfg, err := config.LoadAppConfig()
	if err == nil && appCfg.General.WebSearchTool != "" {
		var reconfigure bool
		clearScreen()
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Web search is already configured").
					Description("Would you like to reconfigure web search tools?").
					Value(&reconfigure),
			),
		).Run()
		if err != nil || !reconfigure {
			return err
		}
	}

	// Ask if user wants to set up web tools
	var setupWeb bool
	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Configure Web Search Tools?").
				Description("Web tools let the AI search the web and extract content from URLs").
				Affirmative("Yes").
				Negative("Skip").
				Value(&setupWeb),
		),
	).Run()
	if err != nil {
		return err
	}
	if !setupWeb {
		return nil
	}

	// Show available standard servers
	servers := config.GetStandardServers()
	var serverOptions []huh.Option[string]
	for _, srv := range servers {
		label := srv.DisplayName
		if srv.IsDefault {
			label += " (recommended)"
		}
		if config.IsStandardServerInstalled(srv.ID) {
			label += " [installed]"
		}
		serverOptions = append(serverOptions, huh.NewOption(label, srv.ID))
	}

	var selectedServer string
	clearScreen()
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a web tool provider").
				Description("Choose which service to use for web search and content extraction").
				Options(serverOptions...).
				Value(&selectedServer),
		),
	).Run()
	if err != nil {
		return err
	}

	srv := config.GetStandardServer(selectedServer)
	if srv == nil {
		return fmt.Errorf("unknown server: %s", selectedServer)
	}

	// Collect env var values
	envValues := make(map[string]string)
	for _, ev := range srv.EnvVars {
		var val string
		clearScreen()
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title(ev.Name).
					Description(ev.Description).
					EchoMode(huh.EchoModePassword).
					Value(&val),
			),
		).Run()
		if err != nil {
			return err
		}
		val = strings.TrimSpace(val)
		if val == "" && ev.Required {
			return nil
		}
		envValues[ev.Name] = val
	}

	// Install the server
	clearScreen()
	runSpinner(fmt.Sprintf("Configuring %s...", srv.DisplayName))

	// Save API key to credential store
	storeKeyInConfig := true // fallback to config if store fails
	configDir, err := config.GetConfigDir()
	if err != nil {
		slog.Warn("failed to get config directory", "error", err)
	}
	if configDir != "" {
		if store, storeErr := credentials.Open(configDir); storeErr == nil {
			for _, ev := range srv.EnvVars {
				if val, ok := envValues[ev.Name]; ok && val != "" {
					storeKey := "web_servers." + selectedServer + ".api_key"
					if setErr := store.SetSecret(storeKey, val); setErr == nil {
						storeKeyInConfig = false
					}
					break
				}
			}
		}
	}

	if err := config.InstallStandardServer(selectedServer, envValues, storeKeyInConfig); err != nil {
		return fmt.Errorf("failed to configure %s: %w", srv.DisplayName, err)
	}

	clearScreen()
	msg := fmt.Sprintf("%s configured! Web search is now available.", srv.DisplayName)
	if srv.WebExtractTool != "" {
		msg = fmt.Sprintf("%s configured! Web search and content extraction are now available.", srv.DisplayName)
	}
	printSuccess(msg)

	return nil
}

// handleBrowserSetup is defined in browser_setup.go — it runs the interactive
// browser engine configuration flow (default Chromium, CloakBrowser, or custom).

// handleSandboxSetup initializes the sandbox (container isolation) as part of
// the main setup wizard. If Incus is not available, it guides the user to
// install it and warns about the security risk of running without sandbox.
func handleSandboxSetup() error {
	for {
		platform, reason := incus.DetectPlatformReason()

		if platform == incus.PlatformUnsupported {
			// Build description with install instructions and reason
			desc := "Sandbox runs AI tools inside isolated Linux containers,\n" +
				"preventing them from accessing your host system directly.\n\n"
			if reason != "" {
				desc += reason + "\n\n"
			}
			desc += "To enable sandbox:\n" +
				"  Linux:         sudo apt install incus && sudo incus admin init\n" +
				"  macOS/Windows: Install Docker Desktop\n\n" +
				"Docs: https://linuxcontainers.org/incus/docs/main/installing/"

			var action string
			clearScreen()
			err := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title("Sandbox Setup — Container runtime not available").
						Description(desc).
						Options(
							huh.NewOption("Continue — I've installed the runtime", "retry"),
							huh.NewOption("Skip — proceed without sandbox", "skip"),
						).
						Value(&action),
				),
			).Run()
			if err != nil {
				return err
			}

			if action == "retry" {
				continue
			}

			// User chose to skip — show security warning
			var acceptRisk bool
			clearScreen()
			err = huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title("Continue without sandbox?").
						Description("Without sandbox, AI tools will execute directly on your host\n" +
							"system with full access to your files, network, and system resources.\n" +
							"This is a security risk.").
						Affirmative("Yes, I accept the risk").
						Negative("Go back").
						Value(&acceptRisk),
				),
			).Run()
			if err != nil {
				return err
			}

			if !acceptRisk {
				continue
			}

			return nil
		}

		// Container runtime is available — set up the sandbox

		// On Docker+Incus (macOS/Windows), ensure the Docker container is running first
		if platform == incus.PlatformDockerIncus {
			clearScreen()
			fmt.Println("Setting up Docker+Incus sandbox runtime...")
			fmt.Println("This will pull the Incus Docker image and create a container.")
			fmt.Println("(This may take a few minutes on first run.)")
			fmt.Println()

			if err := incus.EnsureIncusDockerContainer(); err != nil {
				return fmt.Errorf("failed to set up Docker+Incus: %w", err)
			}
			fmt.Println("Docker+Incus runtime ready.")
		}

		incus.SetActivePlatform(platform)
		if appCfg, cfgErr := config.LoadAppConfig(); cfgErr == nil && appCfg != nil {
			sandbox.SetSandboxConfig(&appCfg.Sandbox)
		}

		// Detect nested LXC: unprivileged containers cannot run inside
		// another LXC container (mounting /proc in double-nested user
		// namespaces is blocked by the outer host). Ask the user whether
		// to enable privileged mode, which the outer LXC still isolates.
		if incus.IsInsideLXC() && !sandbox.IsPrivileged() {
			var action string
			clearScreen()
			err := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title("Sandbox — Nested LXC environment detected").
						Description(
							"This host is itself an LXC container. Unprivileged sandbox\n"+
								"containers cannot run here because mounting /proc in a\n"+
								"double-nested user namespace is not permitted by the outer host.\n\n"+
								"Privileged mode runs containers as root inside the sandbox.\n"+
								"The outer LXC container still provides the isolation boundary,\n"+
								"but a container escape would give access to this LXC host.\n\n"+
								"You can change this later in your config:\n"+
								"  sandbox:\n"+
								"    privileged: true",
						).
						Options(
							huh.NewOption("Enable privileged mode", "enable"),
							huh.NewOption("Skip sandbox setup", "skip"),
						).
						Value(&action),
				),
			).Run()
			if err != nil {
				return err
			}

			if action == "skip" {
				return nil
			}

			// User accepted — persist privileged: true in config
			cfg, cfgErr := config.LoadAppConfig()
			if cfgErr != nil {
				return fmt.Errorf("failed to load config: %w", cfgErr)
			}
			priv := true
			cfg.Sandbox.Privileged = &priv
			if err := config.SaveAppConfig(cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			sandbox.SetSandboxConfig(&cfg.Sandbox)
			fmt.Println("Privileged mode enabled in config.")
		}

		client, err := incus.Connect(platform)
		if err != nil {
			return fmt.Errorf("failed to connect to Incus: %w", err)
		}

		// Check if base template already exists.
		// In platform mode (especially Docker+Incus on macOS), the Docker volume
		// persists across image/container deletions, so the old astn-tpl-base may
		// still be present. Ask the user if they want to rebuild.
		containerName := incus.TemplateName(incus.BaseTemplate)
		if client.InstanceExists(containerName) {
			var rebuild bool
			clearScreen()
			err := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title("Base sandbox template already exists").
						Description(
							"A base template (astn-tpl-base) was found from a previous installation.\n"+
								"It may be stale, built for a different architecture, or incomplete.\n\n"+
								"Rebuild it from scratch now? This will delete the existing template\n"+
								"and re-install all core tools and browser support.",
						).
						Affirmative("Yes, rebuild").
						Negative("Keep existing").
						Value(&rebuild),
				),
			).Run()
			if err != nil {
				return err
			}

			if !rebuild {
				printSuccess("Keeping existing base template. You can update it later via the Studio (Platform Admin → Sandbox → Configure Base).")
				return nil
			}

			// User chose to rebuild — remove the existing container so InitBaseTemplate creates fresh.
			fmt.Println("Removing existing base template for fresh rebuild...")
			if err := client.StopAndDeleteInstance(containerName); err != nil {
				return fmt.Errorf("failed to remove existing base template: %w", err)
			}
		}

		registry, err := sandbox.NewTemplateRegistry()
		if err != nil {
			return err
		}

		opts := promptOptionalTools()

		// Wire browser engine into base template options so browser packages
		// (Chromium, KasmVNC, X11 deps) are installed in the base template.
		if appCfg, cfgErr := config.LoadAppConfig(); cfgErr == nil && appCfg != nil {
			bCfg := incus.BrowserContainerConfig{
				ChromePath:          appCfg.Browser.ChromePath,
				FingerprintSeed:     appCfg.Browser.FingerprintSeed,
				FingerprintPlatform: appCfg.Browser.FingerprintPlatform,
			}
			engine := incus.DetectBrowserEngine(bCfg)
			if incus.IsContainerCompatibleEngine(engine) {
				opts.BrowserEngine = engine
			}
		}

		if err := sandbox.InitBaseTemplate(client, registry, opts); err != nil {
			return fmt.Errorf("sandbox setup: %w", err)
		}

		clearScreen()
		printSuccess("Sandbox initialized! AI tools will run inside isolated containers.")
		return nil
	}
}

// ---------------------------------------------------------------------------
// Deployment Mode Setup
// ---------------------------------------------------------------------------

// handleDeploymentModeSetup asks the user to choose between SQLite (default)
// and PostgreSQL platform backends.
func handleDeploymentModeSetup(cfg *config.AppConfig) error {
	clearScreen()

	var mode string
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("How would you like to run Astonish?").
				Description("Choose a database backend. You can change this later.").
				Options(
					huh.NewOption("SQLite — Multi-user, zero infrastructure, built-in auth (recommended)", "sqlite"),
					huh.NewOption("PostgreSQL — Multi-user, scalable, external database", "platform"),
				).
				Value(&mode),
		),
	).Run()
	if err != nil {
		return err
	}

	switch mode {
	case "sqlite":
		return handleSQLitePlatformSetup(cfg)
	default:
		// Platform mode: collect PostgreSQL connection details.
		return handlePlatformModeSetup(cfg)
	}
}

// handlePlatformModeSetup collects PostgreSQL connection parameters and
// bootstraps the platform database.
func handlePlatformModeSetup(cfg *config.AppConfig) error {
	clearScreen()

	pgHost := "localhost"
	pgPort := "5432"
	pgUser := "postgres"
	pgPassword := ""
	pgSSLMode := "prefer"
	orgName := "My Organization"
	orgSlug := "my-org"

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("PostgreSQL Host").
				Description("Hostname or IP address of the PostgreSQL server").
				Value(&pgHost),
			huh.NewInput().
				Title("PostgreSQL Port").
				Value(&pgPort),
			huh.NewInput().
				Title("PostgreSQL Username").
				Description("Must have CREATEDB privilege to provision organization databases").
				Value(&pgUser),
			huh.NewInput().
				Title("PostgreSQL Password").
				EchoMode(huh.EchoModePassword).
				Value(&pgPassword),
			huh.NewSelect[string]().
				Title("SSL Mode").
				Options(
					huh.NewOption("disable", "disable"),
					huh.NewOption("prefer (recommended)", "prefer"),
					huh.NewOption("require", "require"),
				).
				Value(&pgSSLMode),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Organization Name").
				Description("Display name for the auto-created organization").
				Value(&orgName),
			huh.NewInput().
				Title("Organization Slug").
				Description("URL-safe identifier (lowercase, hyphens allowed)").
				Value(&orgSlug).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("slug cannot be empty")
					}
					for _, ch := range s {
						if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
							return fmt.Errorf("must be lowercase alphanumeric with hyphens/underscores")
						}
					}
					return nil
				}),
		),
	).Run()
	if err != nil {
		return err
	}

	// Validate
	if pgPassword == "" {
		return fmt.Errorf("PostgreSQL password is required")
	}

	// Parse port
	port := 5432
	if pgPort != "" {
		if _, scanErr := fmt.Sscanf(pgPort, "%d", &port); scanErr != nil {
			return fmt.Errorf("invalid port: %s", pgPort)
		}
	}

	// Generate a unique instance suffix for this deployment.
	suffix := config.GenerateInstanceSuffix()

	// Build a temporary DSN and open a single *sql.DB connection for the
	// entire admin phase. Using *sql.DB keeps the TCP socket pooled, which
	// is critical for kubectl port-forward tunnels that die when connections
	// are closed and re-opened.
	tempDSN := pgutil.BuildDSN(pgHost, port, pgUser, pgPassword, "postgres", pgSSLMode)
	ctx := context.Background()
	adminDB, connErr := sql.Open("pgx", tempDSN)
	if connErr != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", connErr)
	}
	adminDB.SetMaxOpenConns(1)
	adminDB.SetMaxIdleConns(1)
	if connErr = adminDB.PingContext(ctx); connErr != nil {
		adminDB.Close()
		return fmt.Errorf("failed to connect to PostgreSQL: %w", connErr)
	}

	// Check for suffix collision (extremely unlikely).
	for attempts := 0; attempts < 5; attempts++ {
		exists, checkErr := pgutil.PlatformDBExistsDB(ctx, adminDB, suffix)
		if checkErr != nil {
			adminDB.Close()
			return fmt.Errorf("failed to check database existence: %w", checkErr)
		}
		if !exists {
			break
		}
		suffix = config.GenerateInstanceSuffix()
	}

	// Build DSN with the actual platform DB name.
	platformDBName := config.PlatformDBName(suffix)
	platformDSN := pgutil.BuildDSN(pgHost, port, pgUser, pgPassword, platformDBName, pgSSLMode)

	clearScreen()
	fmt.Println()
	fmt.Printf("  Connecting to PostgreSQL at %s:%d...\n", pgHost, port)

	// Bootstrap the platform database. Pass adminDB so bootstrap reuses our
	// existing connection for admin ops instead of opening a new one.
	if err := entstore.BootstrapPlatform(ctx, entstore.Config{
		DSN:            platformDSN,
		InstanceSuffix: suffix,
	}, adminDB); err != nil {
		adminDB.Close()
		fmt.Println()
		fmt.Printf("  ✗ Failed: %v\n", err)
		fmt.Println()

		// Offer to retry or abort.
		var retry bool
		retryErr := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Would you like to try again with different settings?").
					Affirmative("Yes, try again").
					Negative("No, skip platform setup").
					Value(&retry),
			),
		).Run()
		if retryErr != nil {
			return retryErr
		}
		if retry {
			return handlePlatformModeSetup(cfg)
		}
		// User chose to skip — stay in personal mode.
		return nil
	}

	fmt.Println("  ✓ Platform database initialized")
	adminDB.Close()
	fmt.Println()

	// Generate JWT secret and save to config.
	jwtSecret := config.GenerateJWTSecret()

	cfg.Storage.Backend = "postgres"
	cfg.Storage.Postgres.PlatformDSN = platformDSN
	cfg.Storage.Postgres.InstanceSuffix = suffix
	cfg.Storage.Auth.Mode = "builtin"
	cfg.Storage.Auth.JWTSecret = jwtSecret
	cfg.Storage.Auth.DefaultOrgName = orgName
	cfg.Storage.Auth.DefaultOrgSlug = orgSlug

	fmt.Println("  Platform mode configured. The first user to register via Studio")
	fmt.Println("  will become the organization owner with full admin access.")
	fmt.Println()

	return nil
}

// handleSQLitePlatformSetup collects parameters for SQLite-backed platform mode:
// org name/slug, admin email/password. It bootstraps the platform database, provisions
// the first org and team, generates a JWT secret and master encryption key.
func handleSQLitePlatformSetup(cfg *config.AppConfig) error {
	clearScreen()

	orgName := "My Organization"
	orgSlug := "my-org"
	adminEmail := ""
	adminPassword := ""
	adminName := ""
	dataDir := cfg.Storage.SQLite.GetDataDir()

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Data Directory").
				Description("Where SQLite database files will be stored").
				Value(&dataDir),
			huh.NewInput().
				Title("Organization Name").
				Description("Display name for the auto-created organization").
				Value(&orgName),
			huh.NewInput().
				Title("Organization Slug").
				Description("URL-safe identifier (lowercase, hyphens allowed)").
				Value(&orgSlug).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("slug cannot be empty")
					}
					for _, ch := range s {
						if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
							return fmt.Errorf("must be lowercase alphanumeric with hyphens/underscores")
						}
					}
					return nil
				}),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Admin Email").
				Description("Email for the first (superadmin) user account").
				Value(&adminEmail).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("email is required")
					}
					if !strings.Contains(s, "@") {
						return fmt.Errorf("must be a valid email address")
					}
					return nil
				}),
			huh.NewInput().
				Title("Admin Display Name").
				Description("Display name for the admin user (optional)").
				Value(&adminName),
			huh.NewInput().
				Title("Admin Password").
				Description("Password for the admin user").
				EchoMode(huh.EchoModePassword).
				Value(&adminPassword).
				Validate(func(s string) error {
					if len(s) < 8 {
						return fmt.Errorf("password must be at least 8 characters")
					}
					return nil
				}),
		),
	).Run()
	if err != nil {
		return err
	}

	clearScreen()
	fmt.Println()
	fmt.Printf("  Initializing SQLite platform in %s...\n", dataDir)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	// Bootstrap the platform schema via entstore.
	ctx := context.Background()
	entCfg := entstore.Config{
		DSN:     "file:" + filepath.Join(dataDir, "platform.db"),
		DataDir: dataDir,
	}
	if err := entstore.BootstrapPlatform(ctx, entCfg, nil); err != nil {
		fmt.Println()
		fmt.Printf("  ✗ Failed to initialize database: %v\n", err)
		fmt.Println()
		return err
	}

	// Open the store and seed initial admin user, org, team, and personal databases.
	_, es, err := entstore.NewPlatformServices(ctx, entCfg)
	if err != nil {
		fmt.Println()
		fmt.Printf("  ✗ Failed to open platform services: %v\n", err)
		fmt.Println()
		return err
	}
	defer es.Close()

	now := time.Now()

	// Step 1: Create admin user (or retrieve existing one by email).
	var userID string
	existingUser, err := es.Users().GetByEmail(ctx, adminEmail)
	if err != nil {
		return fmt.Errorf("check existing user: %w", err)
	}
	if existingUser != nil {
		userID = existingUser.ID
	} else {
		hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		userID = uuid.New().String()
		user := &store.User{
			ID:           userID,
			Email:        adminEmail,
			DisplayName:  adminName,
			PasswordHash: string(hash),
			PlatformRole: "superadmin",
			Status:       "active",
			CreatedAt:    now,
		}
		if err := es.Users().Create(ctx, user); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
	}

	// Step 2: Create organization (or retrieve existing one by slug).
	var orgID string
	existingOrg, err := es.Organizations().GetBySlug(ctx, orgSlug)
	if err != nil {
		return fmt.Errorf("check existing org: %w", err)
	}
	if existingOrg != nil {
		orgID = existingOrg.ID
	} else {
		orgID = uuid.New().String()
		org := &store.Organization{
			ID:        orgID,
			Name:      orgName,
			Slug:      orgSlug,
			Status:    "active",
			CreatedAt: now,
		}
		if err := es.Organizations().Create(ctx, org); err != nil {
			return fmt.Errorf("create organization: %w", err)
		}
	}

	// Step 3: Create membership.
	if err := es.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		return fmt.Errorf("add org membership: %w", err)
	}

	// Step 4: Provision org data directory and database.
	if err := es.ProvisionOrg(ctx, orgID, orgSlug); err != nil {
		return fmt.Errorf("provision org: %w", err)
	}

	// Step 5: Open org store and create team if needed.
	orgStore, err := es.ForOrg(orgSlug)
	if err != nil {
		return fmt.Errorf("open org store: %w", err)
	}

	teamSlug := "general"
	teamName := "General"
	existingTeam, err := orgStore.Teams().GetTeamBySlug(ctx, teamSlug)
	if err != nil {
		return fmt.Errorf("check existing team: %w", err)
	}
	if existingTeam == nil {
		team := &store.Team{
			ID:         uuid.New().String(),
			Name:       teamName,
			Slug:       teamSlug,
			SchemaName: entstore.TeamSchemaName(teamSlug),
			CreatedAt:  now,
		}
		if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
			return fmt.Errorf("create team: %w", err)
		}
	}

	// Step 6: Provision team database.
	if err := orgStore.ProvisionTeam(ctx, teamSlug); err != nil {
		return fmt.Errorf("provision team: %w", err)
	}

	// Step 7: Provision personal database.
	if err := orgStore.ProvisionPersonalSchema(ctx, userID); err != nil {
		return fmt.Errorf("provision personal schema: %w", err)
	}

	fmt.Println("  ✓ Platform database initialized")
	fmt.Println("  ✓ Organization and team created")
	fmt.Println("  ✓ Admin user created")

	// Generate a master key for secret encryption and write to .store_key
	masterKey, err := generateMasterKey()
	if err != nil {
		fmt.Printf("  ⚠ Warning: could not generate master key: %v (secrets will be stored unencrypted)\n", err)
	} else {
		configDir, dirErr := config.GetConfigDir()
		if dirErr == nil {
			keyPath := configDir + "/.store_key"
			if writeErr := os.WriteFile(keyPath, []byte(masterKey), 0600); writeErr != nil {
				fmt.Printf("  ⚠ Warning: could not write master key to %s: %v\n", keyPath, writeErr)
			} else {
				fmt.Printf("  ✓ Encryption key saved to %s\n", keyPath)
			}
		}
	}

	// Generate JWT secret and save to config.
	jwtSecret := config.GenerateJWTSecret()

	cfg.Storage.Backend = "sqlite"
	cfg.Storage.SQLite.DataDir = dataDir
	cfg.Storage.Auth.Mode = "builtin"
	cfg.Storage.Auth.JWTSecret = jwtSecret
	cfg.Storage.Auth.DefaultOrgName = orgName
	cfg.Storage.Auth.DefaultOrgSlug = orgSlug

	fmt.Println()
	fmt.Println("  SQLite platform mode configured.")
	fmt.Printf("  Data directory: %s\n", dataDir)
	fmt.Println("  You can log in via Studio with the admin email and password.")
	fmt.Println()

	return nil
}

// generateMasterKey creates a 32-byte random key encoded as hex (64 chars).
func generateMasterKey() (string, error) {
	key := make([]byte, 32)
	if _, err := cryptorand.Read(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", key), nil
}
