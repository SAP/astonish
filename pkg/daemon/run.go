package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/channels"
	emailchan "github.com/SAP/astonish/pkg/channels/email"
	slackchan "github.com/SAP/astonish/pkg/channels/slack"
	"github.com/SAP/astonish/pkg/channels/telegram"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	emailpkg "github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/mailer"
	"github.com/SAP/astonish/pkg/memory"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
	k8sbackend "github.com/SAP/astonish/pkg/sandbox/k8s"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/scheduler"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
)

// RunConfig holds the configuration for a daemon run.
type RunConfig struct {
	Port  int
	Debug bool
}

// Run starts the daemon in the foreground. It starts the Studio HTTP server,
// writes a PID file, handles signals for graceful shutdown, and cleans up on exit.
// This function blocks until a shutdown signal is received.
func Run(cfg RunConfig) error {
	// Determine runtime mode (default/api/worker) from ASTONISH_MODE env var.
	daemonMode := config.GetDaemonMode()

	// Load app config for provider/MCP setup
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Validate sandbox config early so invalid values are caught at startup
	// rather than producing cryptic Incus errors during container creation.
	if err := sandbox.ValidateSandboxConfig(&appCfg.Sandbox); err != nil {
		slog.Warn("invalid sandbox config, using defaults", "error", err)
	}

	// Resolve port
	port := cfg.Port
	if port <= 0 {
		if appCfg.Daemon.Port > 0 {
			port = appCfg.Daemon.Port
		} else {
			port = 9393
		}
	}

	// Set up logging
	// In api/worker modes, fall back to stdout if file logging is unavailable
	// (stateless containers may not have a writable config directory).
	logDir := appCfg.Daemon.GetLogDir()
	var logger *Logger
	if daemonMode != config.DaemonModeDefault {
		var logErr error
		logger, logErr = NewLogger(logDir + "/daemon.log")
		if logErr != nil {
			// Container mode: fall back to stdout (captured by orchestrator)
			logger = NewStdoutLogger()
		}
	} else {
		var logErr error
		logger, logErr = NewLogger(logDir + "/daemon.log")
		if logErr != nil {
			return fmt.Errorf("failed to initialize logger: %w", logErr)
		}
	}
	defer logger.Close()

	// Redirect standard log to file
	log.SetOutput(logger)
	log.SetFlags(0) // Logger adds its own timestamps

	// Back-compat for users upgrading from v2 (personal mode).
	// "file" and empty backend are no longer supported; default to sqlite (zero-config).
	if appCfg.Storage.Backend == "" || appCfg.Storage.Backend == "file" {
		appCfg.Storage.Backend = "sqlite"
		logger.Printf("Storage backend defaulted to 'sqlite' (zero-config platform mode)")
	}

	// Set up provider environment variables (credential store → config → env fallback)
	configDir, err := config.GetConfigDir()
	if err != nil {
		slog.Warn("failed to get config directory", "error", err)
	}
	var credStore *credentials.Store
	isPlatformMode := appCfg.Storage.Backend == "postgres" || appCfg.Storage.Backend == "sqlite"

	// In platform container modes (api/worker), skip the file-based credential store
	// entirely when ASTONISH_MASTER_KEY is set. All secrets come from PG — the file
	// store would only produce warnings about unwritable filesystem.
	skipFileCredStore := isPlatformMode && daemonMode != config.DaemonModeDefault && os.Getenv("ASTONISH_MASTER_KEY") != ""

	if configDir != "" && !skipFileCredStore {
		if cs, csErr := credentials.Open(configDir); csErr == nil {
			credStore = cs
			config.SetInstalledSecretGetter(cs.GetSecret)
			api.SetAPICredentialStore(cs)

			if !isPlatformMode {
				// Personal mode only: auto-migrate secrets from config.yaml (one-time)
				migrated, migrateErr := credentials.MigrateFromConfig(cs, appCfg, log.Default())
				if migrateErr != nil {
					logger.Printf("Warning: Credential migration error: %v", migrateErr)
				} else if migrated > 0 {
					if saveErr := config.SaveAppConfig(appCfg); saveErr != nil {
						logger.Printf("Warning: Failed to save scrubbed config: %v", saveErr)
					} else {
						logger.Printf("Migrated %d secrets from config.yaml to encrypted store", migrated)
					}
				}
				// Personal mode: set up provider env vars from config.yaml + filesystem credential store
				config.SetupAllProviderEnvFromStore(appCfg, cs.GetSecret)
			}
		} else {
			logger.Printf("Warning: Failed to open credential store: %v", csErr)
			if !isPlatformMode {
				config.SetupAllProviderEnv(appCfg)
			}
		}
	} else if !isPlatformMode {
		config.SetupAllProviderEnv(appCfg)
	}

	// --- Initialize store.Services for dependency injection ---
	// Backend selection: "postgres" or "sqlite" (both platform mode). Legacy "file"/empty defaulted above.
	var svc *store.Services
	var entStore *entstore.Store
	var backend platformDB // unified interface — assigned below

	// Both "postgres" and "sqlite" now use the unified entstore.
	entCfg := entstore.Config{
		InstanceSuffix:  appCfg.Storage.Postgres.InstanceSuffix,
		MaxOpenConns:    appCfg.Storage.Postgres.GetMaxOpenConns(),
		MaxIdleConns:    appCfg.Storage.Postgres.GetMaxIdleConns(),
		ConnMaxLifetime: appCfg.Storage.Postgres.GetConnMaxLifetime(),
	}

	switch appCfg.Storage.Backend {
	case "postgres":
		entCfg.DSN = appCfg.Storage.Postgres.GetPlatformDSN()
	case "sqlite":
		entCfg.DSN = "file:" + filepath.Join(appCfg.Storage.SQLite.GetDataDir(), "platform.db")
		entCfg.DataDir = appCfg.Storage.SQLite.GetDataDir()
	default:
		return fmt.Errorf("unsupported storage backend %q: must be 'postgres' or 'sqlite'", appCfg.Storage.Backend)
	}

	svc, entStore, err = entstore.NewPlatformServices(context.Background(), entCfg)
	if err != nil {
		// Auto-bootstrap for postgres mode
		if appCfg.Storage.Backend == "postgres" {
			logger.Printf("Platform store init failed, attempting auto-bootstrap: %v", err)
			if bootstrapErr := entstore.BootstrapPlatform(context.Background(), entCfg, nil); bootstrapErr != nil {
				return fmt.Errorf("failed to initialize platform storage: %w (auto-bootstrap also failed: %v)", err, bootstrapErr)
			}
			logger.Printf("Auto-bootstrap succeeded, retrying platform store init")
			svc, entStore, err = entstore.NewPlatformServices(context.Background(), entCfg)
			if err != nil {
				return fmt.Errorf("failed to initialize platform storage after auto-bootstrap: %w", err)
			}
		} else {
			return fmt.Errorf("failed to initialize storage: %w", err)
		}
	}
	defer entStore.Close()
	backend = entStore
	// Persist/learn per-model token limits from provider 400 responses (SAP Vertex).
	provider.SetSAPModelLimitsStore(entStore.ModelLimits())
	logger.Printf("Storage backend: %s (platform mode)", appCfg.Storage.Backend)

	// Run migrations
	if err := entStore.MigrateAllSchemas(context.Background()); err != nil {
		logger.Printf("Warning: migration errors: %v", err)
	}

	// Initialize embedding
	{
		embGetSecret := daemonSecretGetter(backend, credStore)
		embResult, embErr := memory.ResolveEmbeddingFunc(appCfg, &appCfg.Memory, cfg.Debug, embGetSecret)
		if embErr != nil {
			logger.Printf("Warning: embedding unavailable (keyword-only search): %v", embErr)
		} else {
			backend.SetEmbedFunc(func(ctx context.Context, text string) ([]float32, error) {
				return embResult.EmbeddingFunc(ctx, text)
			})
			if embResult.Cleanup != nil {
				defer embResult.Cleanup()
			}
			logger.Printf("Memory stores: hybrid vector+keyword search enabled")
		}
	}

	// In platform mode, cascade platform and default-org provider settings
	// into appCfg so the channel/fleet agent sees all configured providers.
	// This is the daemon-level equivalent of effectiveAppConfig() in HTTP handlers.
	// Provider env vars are set here (not earlier) because in platform mode
	// providers come exclusively from the database, not config.yaml.
	cascadePlatformProviders(context.Background(), backend, appCfg, logger)
	// Set up provider env vars from the DB-sourced config
	getSecret := daemonSecretGetter(backend, credStore)
	config.SetupAllProviderEnvFromStore(appCfg, getSecret)
	// Override the installed secret getter to use platform_secrets.
	// This ensures IsStandardServerInstalled() and mergeStandardServers()
	// resolve API keys from the DB in platform mode (not the file-based store).
	config.SetInstalledSecretGetter(getSecret)

	// Create the ToolVectorStore for platform mode.
	// This enables dynamic tool injection (semantic tool discovery) in platform mode.
	var platformToolVectorStore agent.ToolVectorStore
	var platformEmbedFunc agent.EmbedFunc
	if embedFunc := backend.GetEmbedFunc(); embedFunc != nil {
		vs, vsErr := backend.NewToolVectorStore(context.Background())
		if vsErr == nil && vs != nil {
			platformToolVectorStore = vs
			platformEmbedFunc = agent.EmbedFunc(embedFunc)
			logger.Printf("Tool discovery: vector-backed (platform mode)")
		} else if vsErr != nil && cfg.Debug {
			logger.Printf("Warning: failed to create tool vector store: %v", vsErr)
		}
	}

	// Set up MCP environment variables
	if mcpCfg, err := config.LoadMCPConfig(); err == nil {
		config.SetupMCPEnv(mcpCfg)
	}

	// Context for background goroutines
	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	// --- Initialize session persistence ---
	// Platform mode: sessions are in the database (per-team schema).
	logger.Printf("Session store: platform mode (%s)", appCfg.Storage.Backend)

	// --- Initialize platform authentication for Studio ---
	var platformAuth *api.PlatformAuth

	platformAuth = api.NewPlatformAuth(appCfg.Storage.Auth, backend, appCfg.Storage)
	api.SetPlatformAuth(platformAuth)
	// Wire up link code store for registration email verification
	platformAuth.SetLinkCodeStoreForAuth(backend.NewLinkCodeStore())
	if appCfg.Storage.Auth.GetJWTSecret() == "" {
		logger.Printf("WARNING: No JWT secret configured (ASTONISH_JWT_SECRET env or storage.auth.jwt_secret config)")
		logger.Printf("A random key has been generated — tokens will not survive daemon restarts")
	}
	logger.Printf("Platform authentication enabled (mode: %s)", appCfg.Storage.Auth.Mode)

	// --- Initialize shared ChatAgent if channels need it ---
	// The scheduler is always-on by default but doesn't require a ChatAgent at startup:
	// - Routine jobs use the headless runner (creates its own LLM)
	// - Adaptive jobs use the shared ChatAgent if available, or fail gracefully
	// The ChatAgent is expensive to create, so we only init it when channels are enabled.
	var channelMgr *channels.ChannelManager
	var factoryResult *launcher.ChatFactoryResult
	llmPool := provider.NewPool() // Shared LLM cache for per-message provider resolution
	defer func() {
		if factoryResult != nil {
			// Preserve sandbox containers for reconnection after restart,
			// then clean up everything else (LLM, embedder, MCP, etc.).
			// ShutdownSandbox marks containers as "already shut down" so
			// Cleanup() skips destructive container removal.
			if factoryResult.ShutdownSandbox != nil {
				factoryResult.ShutdownSandbox()
			}
			factoryResult.Cleanup()
		}
	}()

	// Channel configuration now lives exclusively in the platform DB (SQLite or Postgres).
	// We consult it early to decide whether we need a ChatAgent for channels/scheduler.
	dbChannelsAtStartup := loadChannelsConfigFromDB(backend, logger)
	needsChatAgent := anyChannelEnabled(dbChannelsAtStartup) && daemonMode != config.DaemonModeAPI

	if needsChatAgent {
		logger.Printf("Initializing ChatAgent for channels...")

		// Build a fully-wired ChatAgent for channel/scheduler use
		fr, factoryErr := launcher.NewWiredChatAgent(ctx, &launcher.ChatFactoryConfig{
			AppConfig:               appCfg,
			ProviderName:            appCfg.General.DefaultProvider,
			ModelName:               appCfg.General.DefaultModel,
			DebugMode:               cfg.Debug,
			AutoApprove:             true, // Channels/scheduler auto-approve all tools
			IsDaemon:                true, // We ARE the daemon — always run indexing/watchers.
			PlatformMode:            true,
			PlatformToolVectorStore: platformToolVectorStore,
			PlatformEmbedFunc:       platformEmbedFunc,
		})
		if factoryErr != nil {
			logger.Printf("Warning: Failed to initialize ChatAgent: %v", factoryErr)
		} else {
			factoryResult = fr
			// Make distillation available to LLM tools (for auto-distill during scheduling)
			tools.SetDistillAccess(newDistillBridge(fr.ChatAgent))
		}
	}

	// initChannels creates (or recreates) the ChannelManager.
	// Channel configuration is now read exclusively from the platform DB.
	// The *config.AppConfig parameter is kept only for sandbox settings (temporary).
	initChannels := func(freshCfg *config.AppConfig) (*channels.ChannelManager, error) {
		// Load the authoritative channel configuration from the DB (single source of truth).
		chCfg := loadChannelsConfigFromDB(backend, logger)
		if !anyChannelEnabled(chCfg) {
			return nil, nil
		}
		if factoryResult == nil {
			return nil, fmt.Errorf("channels enabled but no ChatAgent available — restart the daemon")
		}

		mgr := channels.NewChannelManager(factoryResult.ChatAgent, factoryResult.SessionService, log.Default(), &channels.ChannelManagerConfig{
			ProviderName: factoryResult.ProviderName,
			ModelName:    factoryResult.ModelName,
			ToolCount:    len(factoryResult.InternalTools),
		})

		if factoryResult.CredentialStore != nil {
			mgr.SetRedactor(factoryResult.CredentialStore.Redactor())
		}

		// Wire LLM provider pool for per-message provider resolution.
		// This ensures channel messages use the 3-tier DB cascade (Platform → Org → Team)
		// to resolve providers, matching the Studio chat behavior.
		mgr.SetLLMPool(llmPool)

		// Wire sandbox-aware file reader so channel document attachments can
		// read files from inside sandbox containers. Without this, os.ReadFile
		// fails for container-internal paths (e.g., /tmp/report.md).
		if sandbox.IsSandboxEnabled(&freshCfg.Sandbox) {
			mgr.SetReadFileFunc(func(sessionID, path string) ([]byte, error) {
				// Tier 1: try reading from the host filesystem.
				if data, err := os.ReadFile(path); err == nil {
					return data, nil
				}

				// Tier 2: pull from the session's sandbox container.
				registry, err := sandbox.NewSessionRegistry()
				if err != nil {
					return nil, fmt.Errorf("sandbox session registry unavailable: %w", err)
				}
				entry := registry.Get(sessionID)
				if entry == nil || entry.ContainerName == "" {
					return nil, fmt.Errorf("no sandbox container for session %s", sessionID)
				}
				client, err := incus.Connect(incus.DetectPlatform())
				if err != nil {
					return nil, fmt.Errorf("failed to connect to sandbox: %w", err)
				}
				reader, _, err := client.PullFile(entry.ContainerName, path)
				if err != nil {
					return nil, fmt.Errorf("failed to pull %s from container %s: %w", path, entry.ContainerName, err)
				}
				defer reader.Close()
				return io.ReadAll(reader)
			})
		}

		// Wire a browser provider for Chrome-based PDF generation. This gives
		// channel document attachments (e.g., Telegram) full Unicode/emoji support
		// and professional CSS-based layout when converting markdown to PDF.
		// Uses a dedicated headless browser to avoid interfering with the user's
		// browser session. Falls back to goldmark-pdf if Chrome is unavailable.
		pdfBrowserMgr := browser.NewManager(browser.DefaultConfig())
		mgr.BrowserPDF = pdfBrowserMgr
		defer pdfBrowserMgr.Cleanup()

		// Register Telegram if enabled (config comes exclusively from the platform DB)
		var tgConfigError string
		if chCfg.Telegram.IsTelegramEnabled() {
			botToken := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.telegram.bot_token")
			if botToken == "" {
				tgConfigError = "bot token not configured"
				logger.Printf("Warning: Telegram enabled but no bot token found")
			} else {
				// In platform mode, the DB is the sole authority for allowlists.
				// Pass empty AllowFrom here; the background refresh populates it
				// from user_channels immediately after channel init.
				tgAllowFrom := chCfg.Telegram.AllowFrom
				if backend != nil {
					tgAllowFrom = nil
				}
				tg := telegram.New(&telegram.Config{
					BotToken:  botToken,
					AllowFrom: tgAllowFrom,
					Commands:  mgr.Commands(),
				}, log.Default())
				mgr.Register(tg)
				logger.Printf("Telegram channel registered")
			}
		}

		// Register Email channel (inbound polling) only if explicitly enabled (DB is source of truth)
		var emailConfigError string
		if chCfg.Email.IsEmailEnabled() {
			pollInterval := time.Duration(chCfg.Email.GetPollInterval()) * time.Second
			emailAllowFrom := chCfg.Email.AllowFrom
			if backend != nil {
				emailAllowFrom = nil
			}

			if chCfg.Email.Provider == "msgraph" {
				// Microsoft Graph provider — uses OAuth token exchange
				tenantID := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.email.tenant_id")
				clientID := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.email.client_id")
				clientSecret := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.email.client_secret")
				refreshToken := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.email.refresh_token")
				if tenantID == "" || clientID == "" || refreshToken == "" {
					emailConfigError = "Microsoft Graph secrets not configured (tenant_id, client_id, refresh_token)"
					logger.Printf("Warning: Email channel enabled but msgraph secrets missing")
				} else if chCfg.Email.Address == "" {
					emailConfigError = "email address not configured"
					logger.Printf("Warning: Email channel enabled but no address configured")
				} else {
					tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
					var (
						cachedToken string
						tokenExpiry time.Time
						tokenMu     sync.Mutex
					)
					tokenFunc := func() (string, error) {
						tokenMu.Lock()
						defer tokenMu.Unlock()
						if cachedToken != "" && time.Now().Before(tokenExpiry.Add(-5*time.Minute)) {
							return cachedToken, nil
						}
						currentRefresh := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.email.refresh_token")
						if currentRefresh == "" {
							currentRefresh = refreshToken
						}
						tokenResp, err := emailpkg.ExchangeMSGraphToken(context.Background(), tokenURL, clientID, clientSecret, currentRefresh)
						if err != nil {
							return "", fmt.Errorf("msgraph token exchange: %w", err)
						}
						cachedToken = tokenResp.AccessToken
						tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
						if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != currentRefresh {
							if ps := api.GetPlatformSecrets(); ps != nil {
								_ = ps.SetSecret("channels.email.refresh_token", tokenResp.RefreshToken)
							}
						}
						return cachedToken, nil
					}

					em := emailchan.New(&emailchan.Config{
						Provider:     "msgraph",
						Address:      chCfg.Email.Address,
						TokenFunc:    tokenFunc,
						PollInterval: pollInterval,
						AllowFrom:    emailAllowFrom,
						Folder:       chCfg.Email.Folder,
						MarkRead:     chCfg.Email.IsMarkRead(),
						MaxBodyChars: chCfg.Email.MaxBodyChars,
						Commands:     mgr.Commands(),
					}, log.Default())
					em.SetThreadIndex(backend.NewThreadIndex())
					mgr.Register(em)
					logger.Printf("Email channel registered via Microsoft Graph (%s)", chCfg.Email.Address)
				}
			} else {
				// IMAP/SMTP provider
				emailPassword := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.email.password")
				if emailPassword == "" {
					emailConfigError = "password not configured"
					logger.Printf("Warning: Email channel enabled but no password found")
				} else if chCfg.Email.IMAPServer == "" || chCfg.Email.SMTPServer == "" {
					emailConfigError = "IMAP/SMTP servers not configured"
					logger.Printf("Warning: Email channel enabled but IMAP/SMTP servers not configured")
				} else {
					em := emailchan.New(&emailchan.Config{
						Provider:     chCfg.Email.Provider,
						IMAPServer:   chCfg.Email.IMAPServer,
						SMTPServer:   chCfg.Email.SMTPServer,
						Address:      chCfg.Email.Address,
						Username:     chCfg.Email.Username,
						Password:     emailPassword,
						PollInterval: pollInterval,
						AllowFrom:    emailAllowFrom,
						Folder:       chCfg.Email.Folder,
						MarkRead:     chCfg.Email.IsMarkRead(),
						MaxBodyChars: chCfg.Email.MaxBodyChars,
						Commands:     mgr.Commands(),
					}, log.Default())
					em.SetThreadIndex(backend.NewThreadIndex())
					mgr.Register(em)
					logger.Printf("Email channel registered (%s)", chCfg.Email.Address)
				}
			}
		}

		// Register Slack if enabled (DB is source of truth)
		var slackConfigError string
		if chCfg.Slack.IsSlackEnabled() {
			botToken := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.slack.bot_token")
			appToken := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.slack.app_token")
			signingSecret := resolveDaemonSecret(backend, factoryResult.CredentialStore, "channels.slack.signing_secret")

			mode := chCfg.Slack.GetMode()
			if mode == "socket" && botToken == "" {
				slackConfigError = "bot_token not configured"
				logger.Printf("Warning: Slack channel enabled (socket mode) but no bot token found")
			} else if mode == "socket" && appToken == "" {
				slackConfigError = "app_token not configured for socket mode"
				logger.Printf("Warning: Slack channel enabled (socket mode) but no app token found")
			} else if mode == "events" && signingSecret == "" {
				slackConfigError = "signing_secret not configured for events mode"
				logger.Printf("Warning: Slack channel enabled (events mode) but no signing secret found")
			} else {
				slAllowFrom := chCfg.Slack.AllowFrom
				if backend != nil {
					slAllowFrom = nil
				}
				sl := slackchan.New(&slackchan.Config{
					Mode:          mode,
					BotToken:      botToken,
					AppToken:      appToken,
					SigningSecret: signingSecret,
					AllowFrom:     slAllowFrom,
					Commands:      mgr.Commands(),
				}, log.Default())
				mgr.Register(sl)
				logger.Printf("Slack channel registered (mode: %s)", mode)
			}
		}

		if err := mgr.StartAll(ctx); err != nil {
			return mgr, fmt.Errorf("failed to start channels: %w", err)
		}
		logger.Printf("Channels started")

		// Report channel config statuses to the API layer so that
		// GET /api/channels/status includes enabled-but-not-started channels.
		cfgStatuses := map[string]api.ChannelConfigStatus{}
		if chCfg.Telegram.IsTelegramEnabled() {
			cfgStatuses["telegram"] = api.ChannelConfigStatus{Enabled: true, Error: tgConfigError}
		}
		if chCfg.Email.IsEmailEnabled() {
			cfgStatuses["email"] = api.ChannelConfigStatus{Enabled: true, Error: emailConfigError}
		}
		if chCfg.Slack.IsSlackEnabled() {
			cfgStatuses["slack"] = api.ChannelConfigStatus{Enabled: true, Error: slackConfigError}
		}
		api.SetChannelConfigStatuses(cfgStatuses)

		return mgr, nil
	}

	// initEmailTools initializes (or refreshes) email tools whenever valid
	// IMAP/SMTP credentials are configured (from the platform DB), regardless
	// of whether the email channel adapter itself is enabled.
	// Called both at startup and during reloads to pick up credential changes.
	//
	// NOTE: This must work even when factoryResult is nil (API-mode pods don't
	// build a ChatAgent). Secrets are resolved from the platform DB (backend)
	// with an optional fallback to the file-based credential store.
	initEmailTools := func() {
		if backend == nil {
			return
		}

		// Credential store fallback: nil-safe (platform DB is the primary source).
		var credStore *credentials.Store
		if factoryResult != nil {
			credStore = factoryResult.CredentialStore
		}

		emailCh := loadChannelsConfigFromDB(backend, logger).Email

		// Microsoft Graph provider uses platform secrets for OAuth tokens
		if emailCh.Provider == "msgraph" {
			if emailCh.Address == "" {
				return
			}
			// Resolve the 4 OAuth secrets from platform secrets
			tenantID := resolveDaemonSecret(backend, credStore, "channels.email.tenant_id")
			clientID := resolveDaemonSecret(backend, credStore, "channels.email.client_id")
			clientSecret := resolveDaemonSecret(backend, credStore, "channels.email.client_secret") // optional for public clients
			refreshToken := resolveDaemonSecret(backend, credStore, "channels.email.refresh_token")
			if tenantID == "" || clientID == "" || refreshToken == "" {
				logger.Printf("Warning: msgraph email provider missing required secrets (tenant_id, client_id, refresh_token)")
				return
			}
			tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)

			// Token cache: exchange once and reuse until near expiry
			var (
				cachedToken string
				tokenExpiry time.Time
				tokenMu     sync.Mutex
			)

			tokenFunc := func() (string, error) {
				tokenMu.Lock()
				defer tokenMu.Unlock()

				// Return cached token if still valid (with 5-minute buffer)
				if cachedToken != "" && time.Now().Before(tokenExpiry.Add(-5*time.Minute)) {
					return cachedToken, nil
				}

				// Get current refresh token (may have been rotated by the API test handler)
				currentRefresh := resolveDaemonSecret(backend, credStore, "channels.email.refresh_token")
				if currentRefresh == "" {
					currentRefresh = refreshToken
				}

				tokenResp, err := emailpkg.ExchangeMSGraphToken(context.Background(), tokenURL, clientID, clientSecret, currentRefresh)
				if err != nil {
					return "", fmt.Errorf("msgraph token exchange: %w", err)
				}

				cachedToken = tokenResp.AccessToken
				tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

				// Persist rotated refresh token via platform secrets API
				if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != currentRefresh {
					if ps := api.GetPlatformSecrets(); ps != nil {
						_ = ps.SetSecret("channels.email.refresh_token", tokenResp.RefreshToken)
					}
				}

				return cachedToken, nil
			}

			setupEmailTools(&emailToolConfig{
				Provider:     "msgraph",
				Address:      emailCh.Address,
				Folder:       emailCh.Folder,
				MaxBodyChars: emailCh.MaxBodyChars,
				TokenFunc:    tokenFunc,
			})
			logger.Printf("Email tools initialized via Microsoft Graph (%s)", emailCh.Address)
			return
		}

		// IMAP/SMTP provider — requires password
		emailPassword := resolveDaemonSecret(backend, credStore, "channels.email.password")
		if emailPassword == "" || emailCh.IMAPServer == "" ||
			emailCh.SMTPServer == "" || emailCh.Address == "" {
			return
		}
		setupEmailTools(&emailToolConfig{
			Provider:     emailCh.Provider,
			IMAPServer:   emailCh.IMAPServer,
			SMTPServer:   emailCh.SMTPServer,
			Address:      emailCh.Address,
			Username:     emailCh.Username,
			Password:     emailPassword,
			Folder:       emailCh.Folder,
			MaxBodyChars: emailCh.MaxBodyChars,
		})
		logger.Printf("Email tools initialized (%s)", emailCh.Address)
	}

	// --- Initialize email tools (independent of channel enabled state) ---
	// Email tools now read their config exclusively from the platform DB.
	initEmailTools()

	// --- Initialize channel manager (reads exclusively from platform DB) ---
	if mgr, err := initChannels(appCfg); err != nil {
		logger.Printf("Warning: %v", err)
	} else {
		channelMgr = mgr
	}
	api.SetChannelManager(channelMgr)

	// --- Dynamic allowlist from user_channels (platform mode) ---
	// In platform mode, channel allowlists are built exclusively from the
	// user_channels table (DB is the sole authority). Static config.yaml
	// allow_from entries are ignored — they are a personal-mode concept.
	// A background goroutine refreshes the allowlists every 60 seconds.
	var refreshAllowlistStop context.CancelFunc
	if backend != nil && channelMgr != nil {
		refreshAllowlist := func() {
			bgCtx := context.Background()
			allowlists := make(map[string][]string)

			// Telegram allowlist (from DB only)
			tgLinks, err := backend.UserChannels().ListByChannelType(bgCtx, "telegram")
			if err != nil {
				logger.Printf("[channels] Failed to refresh Telegram allowlist from DB: %v", err)
			} else {
				ids := make([]string, 0, len(tgLinks))
				for _, link := range tgLinks {
					ids = append(ids, link.ExternalID)
				}
				allowlists["telegram"] = ids
			}

			// Slack allowlist (from DB only)
			slLinks, err := backend.UserChannels().ListByChannelType(bgCtx, "slack")
			if err != nil {
				logger.Printf("[channels] Failed to refresh Slack allowlist from DB: %v", err)
			} else {
				ids := make([]string, 0, len(slLinks))
				for _, link := range slLinks {
					ids = append(ids, link.ExternalID)
				}
				allowlists["slack"] = ids
			}

			// Email allowlist (from DB only)
			emLinks, err := backend.UserChannels().ListByChannelType(bgCtx, "email")
			if err != nil {
				logger.Printf("[channels] Failed to refresh Email allowlist from DB: %v", err)
			} else {
				ids := make([]string, 0, len(emLinks))
				for _, link := range emLinks {
					ids = append(ids, link.ExternalID)
				}
				allowlists["email"] = ids
			}

			channelMgr.UpdateAllowlists(allowlists)
		}

		// Initial refresh
		refreshAllowlist()

		// Periodic refresh every 60s
		refreshCtx, refreshCancel := context.WithCancel(ctx)
		refreshAllowlistStop = refreshCancel
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-refreshCtx.Done():
					return
				case <-ticker.C:
					refreshAllowlist()
				}
			}
		}()
	}
	_ = refreshAllowlistStop // used at shutdown

	newChannelPlatformResolver := func(currentCfg *config.AppConfig) *channelPlatformResolver {
		resolver := &channelPlatformResolver{backend: backend}
		if currentCfg != nil && currentCfg.Sandbox.OpenShell.GatewayAddr != "" {
			gatewayConfig := openshell.GRPCClientConfig{
				Addr: currentCfg.Sandbox.OpenShell.GatewayAddr,
				TLS:  currentCfg.Sandbox.OpenShell.OpenShellGatewayTLS(),
			}
			resolver.gatewayConfig = &gatewayConfig
		}
		// Wire the cross-session memory merge function if PlatformReflector is available.
		if factoryResult != nil && factoryResult.ChatAgent != nil && factoryResult.ChatAgent.PlatformReflector != nil {
			resolver.memorySaveOrMerge = factoryResult.ChatAgent.PlatformReflector.MemorySaveOrMergeFunc()
		}
		return resolver
	}

	// Set platform resolver for inbound channel messages (platform mode only).
	// This enables user/channel-scoped context injection for linked channel users.
	if backend != nil && channelMgr != nil {
		channelMgr.SetPlatformResolver(newChannelPlatformResolver(appCfg))
	}

	// --- Link code store for code-based channel linking (platform mode) ---
	// Allows users to link their Telegram/Slack/Email via Studio or /link commands.
	// API pods (ASTONISH_MODE=api) don't run channel adapters but still serve
	// /api/user/channels/link-code — initialize the DB-backed store whenever we
	// have a platform backend, not only when a ChannelManager is running locally.
	if backend != nil {
		linkStore := api.NewDBLinkCodeBackend(backend.NewLinkCodeStore())
		api.SetLinkCodeStore(linkStore)

		if channelMgr != nil {
			// Set the link handler on the Telegram channel — bridges /link commands
			// to the link code store and user_channels DB.
			channelMgr.SetTelegramLinkHandler(buildTelegramLinkHandler(backend, linkStore, channelMgr))

			// Set the link handler on the Slack channel.
			channelMgr.SetSlackLinkHandler(buildSlackLinkHandler(backend, linkStore, channelMgr))
		}
	}

	// reloadChannels re-reads config, stops existing channels, and starts new
	// ones. Called by the POST /api/channels/reload endpoint so that CLI
	// commands like "astonish channels setup telegram" can activate changes
	// without a full daemon restart.
	var reloadMu sync.Mutex
	reloadChannels := func() error {
		reloadMu.Lock()
		defer reloadMu.Unlock()

		logger.Printf("Reloading channel configuration...")

		// Re-read non-provider config from disk (sandbox settings, etc.)
		freshCfg, err := config.LoadAppConfig()
		if err != nil {
			return fmt.Errorf("failed to reload config: %w", err)
		}
		// Re-apply the DB provider cascade so the factory uses current DB
		// settings (not stale config.yaml providers).
		cascadePlatformProviders(context.Background(), backend, freshCfg, logger)

		if factoryResult != nil && factoryResult.CredentialStore != nil {
			if err := factoryResult.CredentialStore.Reload(); err != nil {
				logger.Printf("Warning: failed to reload credential store: %v", err)
			}
		}

		// Stop existing channels
		if channelMgr != nil {
			shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := channelMgr.StopAll(shutCtx); err != nil {
				logger.Printf("Warning: error stopping channels: %v", err)
			}
		}

		// Lazy-init ChatAgent if channels are now enabled (in DB) but weren't at startup.
		// We check the platform DB directly instead of config.yaml.
		reloadChCfg := loadChannelsConfigFromDB(backend, logger)
		if anyChannelEnabled(reloadChCfg) && factoryResult == nil {
			logger.Printf("Initializing ChatAgent for newly enabled channels...")
			fr, factoryErr := launcher.NewWiredChatAgent(ctx, &launcher.ChatFactoryConfig{
				AppConfig:               freshCfg,
				ProviderName:            freshCfg.General.DefaultProvider,
				ModelName:               freshCfg.General.DefaultModel,
				DebugMode:               cfg.Debug,
				AutoApprove:             true,
				PlatformMode:            true,
				PlatformToolVectorStore: platformToolVectorStore,
				PlatformEmbedFunc:       platformEmbedFunc,
			})
			if factoryErr != nil {
				return fmt.Errorf("failed to initialize ChatAgent: %w", factoryErr)
			}
			factoryResult = fr
			// Make distillation available to LLM tools
			tools.SetDistillAccess(newDistillBridge(fr.ChatAgent))
			// Cleanup handled by the deferred closure in Run() that reads
			// the current factoryResult at shutdown time.
		}

		// Refresh email tools and channels from the platform DB (single source of truth).
		initEmailTools()

		// Create fresh channel manager with new config (reads channels from DB)
		mgr, err := initChannels(freshCfg)
		if err != nil {
			logger.Printf("Warning: %v", err)
		}
		channelMgr = mgr
		api.SetChannelManager(channelMgr)

		// Refresh dynamic allowlist after channel reload (platform mode).
		// DB is the sole authority — static config allow_from is ignored.
		if backend != nil && channelMgr != nil {
			bgCtx := context.Background()
			allowlists := make(map[string][]string)

			// Telegram
			tgLinks, linkErr := backend.UserChannels().ListByChannelType(bgCtx, "telegram")
			if linkErr == nil {
				ids := make([]string, 0, len(tgLinks))
				for _, link := range tgLinks {
					ids = append(ids, link.ExternalID)
				}
				allowlists["telegram"] = ids
			}

			// Slack
			slLinks, linkErr := backend.UserChannels().ListByChannelType(bgCtx, "slack")
			if linkErr == nil {
				ids := make([]string, 0, len(slLinks))
				for _, link := range slLinks {
					ids = append(ids, link.ExternalID)
				}
				allowlists["slack"] = ids
			}

			// Email
			emLinks, linkErr := backend.UserChannels().ListByChannelType(bgCtx, "email")
			if linkErr == nil {
				ids := make([]string, 0, len(emLinks))
				for _, link := range emLinks {
					ids = append(ids, link.ExternalID)
				}
				allowlists["email"] = ids
			}

			channelMgr.UpdateAllowlists(allowlists)
		}

		// Re-attach platform resolver after reload
		if backend != nil && channelMgr != nil {
			channelMgr.SetPlatformResolver(newChannelPlatformResolver(freshCfg))
		}

		// Initialize link code store if not yet created.
		if backend != nil && api.GetLinkCodeStore() == nil {
			linkStore := api.NewDBLinkCodeBackend(backend.NewLinkCodeStore())
			api.SetLinkCodeStore(linkStore)
		}

		// Re-attach link handlers after reload
		if backend != nil && channelMgr != nil {
			linkStore := api.GetLinkCodeStore()
			if linkStore != nil {
				channelMgr.SetTelegramLinkHandler(buildTelegramLinkHandler(backend, linkStore, channelMgr))
				channelMgr.SetSlackLinkHandler(buildSlackLinkHandler(backend, linkStore, channelMgr))
			}
		}

		logger.Printf("Channel reload complete")
		return nil
	}
	api.SetChannelReloadFunc(reloadChannels)

	// --- Start config file watcher for hot-reload ---
	configPath, configPathErr := config.GetConfigPath()
	if configPathErr != nil {
		logger.Printf("Warning: Failed to resolve config path for watcher: %v", configPathErr)
	} else {
		go func() {
			if err := WatchConfig(ctx, configPath, ConfigWatcherOpts{
				DebounceMs:     1500,
				Logger:         logger,
				GetManager:     func() *channels.ChannelManager { return channelMgr },
				ReloadChannels: reloadChannels,
				LastConfig:     appCfg,
			}); err != nil {
				logger.Printf("Warning: Config watcher stopped: %v", err)
			}
		}()
	}

	// --- Initialize scheduler if enabled ---
	// Skipped in API mode — only default and worker modes run the scheduler.
	var mtSched *MultiTenantScheduler
	var schedExec *scheduler.Executor

	// Register the headless runner for ALL modes (including API mode).
	// API pods need this to construct a local executor for schedule_job test
	// execution (test_first=true) without routing through the worker.
	api.SetRunHeadlessFunc(makeHeadlessRunner())

	// fleetSessionStore holds the PG-backed session store used for fleet
	// sessions in platform mode. In personal mode it remains nil, and
	// fleet session metadata is written to the file-based store instead.
	var fleetSessionStore store.SessionStore

	if appCfg.Scheduler.IsSchedulerEnabled() && daemonMode != config.DaemonModeAPI {
		// Platform mode: use the multi-tenant scheduler that iterates
		// all orgs → all teams on every tick. Individual job CRUD goes
		// through the request-scoped store.SchedulerStore in API handlers.

		// Create executor with injected headless runner
		schedExec = &scheduler.Executor{
			AppConfig:    appCfg,
			ProviderName: appCfg.General.DefaultProvider,
			ModelName:    appCfg.General.DefaultModel,
			DebugMode:    cfg.Debug,
			RunHeadless:  makeHeadlessRunner(),
			// FlowResolver is nil — multi-tenant path uses context-injected FlowStore
		}
		if factoryResult != nil {
			schedExec.ChatAgent = factoryResult.ChatAgent
			schedExec.SessionService = factoryResult.SessionService
		}
		// Host fallback only — report bodies normally come from write_file args
		// captured during the adaptive run (sandbox paths are not on the host).
		schedExec.ReadSessionFile = func(_, path string) ([]byte, error) {
			return os.ReadFile(path)
		}
		// Adaptive jobs: ephemeral OpenShell sandbox per run (destroy → create → destroy).
		schedExec.DestroySandbox = func(ctx context.Context, sessionID string) error {
			return sandbox.DestroySessionEverywhere(ctx, appCfg, sessionID, nil)
		}
		if factoryResult != nil && factoryResult.SandboxPool != nil {
			pool := factoryResult.SandboxPool
			schedExec.InvalidateSandboxClient = func(sessionID string) {
				pool.Remove(sessionID)
			}
		}

		// Create delivery function — uses a getter to always resolve the
		// current channelMgr, surviving channel reloads without stale closures.
		// In platform mode, use the full delivery resolver for owner/team/members modes.
		deliver := scheduler.NewPlatformDeliverFunc(func() *channels.ChannelManager {
			return channelMgr
		}, &deliveryResolver{backend: backend}, log.Default())

		// Create and start the multi-tenant scheduler
		mtSched = NewMultiTenantScheduler(backend, schedExec, deliver, log.Default())
		if appCfg.Sandbox.OpenShell.GatewayAddr != "" {
			mtSched.SetGatewayConfig(openshell.GRPCClientConfig{
				Addr: appCfg.Sandbox.OpenShell.GatewayAddr,
				TLS:  appCfg.Sandbox.OpenShell.OpenShellGatewayTLS(),
			})
		}
		mtSched.Start(ctx)

		// Register executor for API RunNow handler
		api.SetExecutor(schedExec)

		// Resolve default org/team for fleet session store (backward compat)
		orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
		if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
			teamStore := orgStore.ForTeam("general")
			fleetSessionStore = teamStore.Sessions()
		}

		logger.Printf("Scheduler: multi-tenant (all orgs/teams)")
	}

	// --- Initialize fleet plan activator ---
	// This bridges fleet plans to the scheduler for automated polling.
	// Requires the scheduler to be initialized.
	// In platform mode, fleet plans are read from the database.
	// Skipped in API mode — only default and worker modes run fleet monitors.
	if mtSched != nil && daemonMode != config.DaemonModeAPI {
		// Create a scheduler backed by the default team's store
		// for fleet plan job management.
		var fleetSchedBridge *fleetSchedulerBridge
		orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
		if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
			teamStore := orgStore.ForTeam("general")
			fleetJobStore := newPGSchedulerAdapter(teamStore.ScheduledJobs())
			fleetSched := scheduler.New(fleetJobStore, schedExec.Execute, nil, log.Default())
			fleetSchedBridge = newFleetSchedulerBridge(fleetSched)
		}

		if fleetSchedBridge != nil {
			fleetStarter := func(fCtx context.Context, fCfg fleet.HeadlessFleetConfig) (string, error) {
				// Resolve tenant-scoped stores for the fleet session so sub-agents
				// can access team drills, credentials, skills, etc. in platform mode.
				var fleetStores *api.FleetStores
				if backend != nil && fCfg.TeamSlug != "" {
					orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
					if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
						teamStore := orgStore.ForTeam(fCfg.TeamSlug)
						fleetStores = api.FleetStoresFromTeam(teamStore, orgStore, backend.PlatformMCPServers(), backend.PlatformSkills())
					}
				}
				return api.StartHeadlessFleetSession(fCtx, fCfg, fleetSessionStore, fleetStores)
			}
			// Wrap the plan registry getter to satisfy fleet.PlanAccess interface.
			// In personal mode, returns the file-based PlanRegistry.
			// In platform mode (when backend != nil), returns a DB-backed adapter.
			planAccessFn := func() fleet.PlanAccess {
				if backend != nil {
					orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
					if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
						teamStore := orgStore.ForTeam("general")
						return &dbPlanAccessAdapter{
							store:        teamStore.FleetPlans(),
							monitorStore: backend.NewMonitorStateStore(orgSlug, "general"),
						}
					}
				}
				reg := api.GetFleetPlanRegistry()
				if reg == nil {
					return nil
				}
				return reg
			}
			activator := fleet.NewPlanActivator(planAccessFn, fleetSchedBridge, fleetStarter)
			// Set the team slug so headless fleet sessions know which team's
			// stores to resolve. Matches the team used for plan storage above.
			activator.TeamSlug = "general"
			if backend != nil {
				orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
				if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
					if teamStore := orgStore.ForTeam("general"); teamStore != nil {
						activator.SetRunStateStore(teamStore.FleetRunStates())
					}
				}
			}

			// Wire the poll function into the scheduler executor
			if schedExec != nil {
				schedExec.FleetPoll = activator.Poll
			}

			// Wire the recover function for session recovery after restart
			activator.SetRecoverFunc(func(rCtx context.Context, rCfg fleet.RecoverFleetConfig) error {
				// Resolve tenant-scoped stores for the recovered fleet session.
				var fleetStores *api.FleetStores
				if backend != nil && rCfg.TeamSlug != "" {
					orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
					if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
						teamStore := orgStore.ForTeam(rCfg.TeamSlug)
						fleetStores = api.FleetStoresFromTeam(teamStore, orgStore, backend.PlatformMCPServers(), backend.PlatformSkills())
					}
				}
				return api.RecoverFleetSession(rCtx, rCfg, fleetSessionStore, fleetStores)
			})

			// Wire credential-based GitHub token resolver for fleet plans.
			// When a plan has credentials: { github: "some-store-entry" }, this
			// resolver extracts the token from the encrypted credential store so
			// it can be injected as GH_TOKEN into gh CLI commands.
			if backend != nil {
				// Platform mode: resolve from team credential store.
				activator.SetGHTokenResolver(func(plan *fleet.FleetPlan) string {
					orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
					orgStore, orgErr := backend.ForOrg(orgSlug)
					if orgErr != nil {
						slog.Warn("failed to get org store for fleet credentials", "plan", plan.Key, "error", orgErr)
						return ""
					}
					teamStore := orgStore.ForTeam("general")
					cs := teamStore.Credentials()
					resolved, err := fleet.ResolveCredentialsPlatform(context.Background(), plan, cs)
					if err != nil {
						slog.Warn("failed to resolve fleet credentials (platform)", "plan", plan.Key, "error", err)
					}
					return fleet.GitHubToken(resolved)
				})
			} else if credStore != nil {
				// Personal mode: resolve from file-based credential store.
				activator.SetGHTokenResolver(func(plan *fleet.FleetPlan) string {
					resolved, err := fleet.ResolveCredentials(plan, credStore)
					if err != nil {
						slog.Warn("failed to resolve fleet credentials", "plan", plan.Key, "error", err)
					}
					return fleet.GitHubToken(resolved)
				})
			}

			// Make activator available to API handlers
			api.SetPlanActivator(activator)

			// Wire auto-activation into save_fleet_plan tool so non-chat
			// plans start polling immediately after the wizard saves them.
			tools.SetPlanActivatorFunc(activator.Activate)

			// Wire the session registry so CheckForWork can detect active sessions
			activator.SetSessionRegistry(api.GetFleetSessionRegistry())

			// Restore previously activated plans (re-create monitors)
			if err := activator.RestoreActivated(); err != nil {
				logger.Printf("Warning: Failed to restore activated plans: %v", err)
			}

			logger.Printf("Fleet plan activator initialized")
		}
	}

	// --- Wire fleet commands into channels ---
	// Must happen after both channelMgr and fleet registries are initialized.
	if channelMgr != nil {
		channelMgr.SetFleetDeps(&channels.FleetDeps{
			GetSessionRegistry: func() channels.FleetSessionRegistry {
				return api.GetFleetSessionRegistry()
			},
			GetPlanRegistry: func() channels.FleetPlanRegistry {
				// Platform mode: use DB store
				if backend != nil {
					orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
					if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
						teamStore := orgStore.ForTeam("general")
						return &dbFleetPlanRegistryAdapter{store: teamStore.FleetPlans()}
					}
				}
				// Personal mode: use file-based registry
				reg := api.GetFleetPlanRegistry()
				if reg == nil {
					return nil
				}
				return &fleetPlanRegistryAdapter{reg: reg}
			},
			GetFleetRegistry: func() channels.FleetTemplateRegistry {
				// Platform mode: use DB store
				if backend != nil {
					orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
					if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
						teamStore := orgStore.ForTeam("general")
						return &dbFleetTemplateRegistryAdapter{store: teamStore.FleetTemplates()}
					}
				}
				// Personal mode: use file-based registry
				reg := api.GetFleetRegistry()
				if reg == nil {
					return nil
				}
				return &fleetTemplateRegistryAdapter{reg: reg}
			},
			StartSessionFromPlan: func(planKey, initialMessage string) (*channels.FleetSessionStartResult, error) {
				// Resolve fleet plan store for platform mode
				var fleetPlanStore store.FleetPlanStore
				var fleetStores *api.FleetStores
				if backend != nil {
					orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
					if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
						teamStore := orgStore.ForTeam("general")
						fleetPlanStore = teamStore.FleetPlans()
						fleetStores = api.FleetStoresFromTeam(teamStore, orgStore, backend.PlatformMCPServers(), backend.PlatformSkills())
					}
				}
				result, err := api.StartFleetSessionFromPlan(planKey, initialMessage, api.DefaultUserID(), "", nil, nil, "", fleetPlanStore, fleetStores)
				if err != nil {
					return nil, err
				}
				return &channels.FleetSessionStartResult{
					SessionID: result.Session.ID,
					FleetKey:  result.FleetKey,
					FleetName: result.FleetName,
					SetOnMessagePosted: func(fn func(sender, text string)) {
						if result.SetOnMessagePosted != nil {
							result.SetOnMessagePosted(func(msg fleet.Message) {
								fn(msg.Sender, msg.Text)
							})
						}
					},
					SetOnSessionDone: func(fn func(sessionID string, err error)) {
						if result.SetOnSessionDone != nil {
							result.SetOnSessionDone(fn)
						}
					},
				}, nil
			},
			StopSession: func(sessionID string) error {
				registry := api.GetFleetSessionRegistry()
				fs := registry.Get(sessionID)
				if fs == nil {
					return fmt.Errorf("fleet session %s not found", sessionID)
				}
				fs.Stop()
				registry.Unregister(sessionID)
				return nil
			},
		})
		logger.Printf("Fleet commands wired into channels")
	}

	// Start periodic cleanup goroutine (session expiry + orphan container pruning)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		runPeriodicCleanup(ctx, appCfg, nil, logger)
	}()

	// Start transient table cleanup in platform mode (device_sessions, pending_link_codes).
	// Runs every 5 minutes — these tables have short TTLs (5-10 min) and expired rows
	// accumulate if no cleanup runs. Safe to run on any/all pods (DELETE is idempotent).
	if backend != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					backend.CleanupExpired(context.Background())
				}
			}
		}()
	}

	// Start sandbox GC reconciler in platform mode with K8s backend.
	// Periodically reclaims orphan layers, uppers, and staging directories.
	if appCfg.Storage.Backend == "postgres" && sandbox.BackendKind(appCfg.Sandbox.BackendKind()) == sandbox.BackendKindK8s {
		platformPool, poolErr := pgxpool.New(ctx, entCfg.DSN)
		if poolErr == nil {
			defer platformPool.Close()
			cs, _, csErr := k8sbackend.NewClientFromOptions(k8sbackend.LoadConfigOptions{
				KubeconfigPath: appCfg.Sandbox.Kubernetes.KubeconfigPath,
				Context:        appCfg.Sandbox.Kubernetes.Context,
				InCluster:      appCfg.Sandbox.Kubernetes.InCluster,
			})
			if csErr == nil {
				gcNamespace := appCfg.Sandbox.Kubernetes.Namespace
				if gcNamespace == "" {
					gcNamespace = "astonish-sandbox"
				}
				go k8sbackend.RunGCReconciler(ctx, k8sbackend.GCReconcilerConfig{
					Interval:         time.Hour,
					LayerGracePeriod: 24 * time.Hour,
					UpperGracePeriod: time.Hour,
					Namespace:        gcNamespace,
					LayersPVCName:    appCfg.Sandbox.Kubernetes.LayersPVCName,
					UppersPVCName:    appCfg.Sandbox.Kubernetes.UppersPVCName,
					Client:           cs,
					PlatformPool:     platformPool,
					Layers:           entStore.SandboxLayers(),
					SandboxSessionsQuerier: func(ctx context.Context) (map[string]bool, error) {
						return entStore.AllSandboxSessionIDs(ctx)
					},
				})
			} else {
				logger.Printf("[gc-reconciler] Failed to create K8s client: %v (GC reconciler disabled)", csErr)
			}
		}
	}

	// --- OpenShell orphan reconciliation (periodic) ---
	// When the sandbox backend is OpenShell and we have a PG store, run
	// periodic orphan cleanup. First pass 30s after startup, then every 5min.
	// This handles pods orphaned from unclean shutdowns, failed DeleteSandbox
	// calls, or stale sandbox_sessions records from before the PG registry fix.
	if appCfg.Storage.Backend == "postgres" && sandbox.BackendKind(appCfg.Sandbox.BackendKind()) == sandbox.BackendKindOpenShell {
		go func() {
			// Initial delay: wait for pods to stabilize after a rolling restart.
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}

			// Run the first pass immediately, then every 5 minutes.
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()

			runOpenShellReconcile := func() {
				// Prune stale sandbox_sessions records whose chat session
				// has been deleted. Without this, AllSandboxSessionIDs returns
				// stale IDs and the reconciler thinks orphan pods are still active.
				pruned, pruneErr := entStore.PruneStaleSandboxSessions(ctx)
				if pruneErr != nil {
					slog.Warn("openshell orphan reconciler: failed to prune stale sandbox sessions", "error", pruneErr)
				} else if pruned > 0 {
					slog.Info("openshell orphan reconciler: pruned stale sandbox_sessions records", "count", pruned)
				}

				osCfg := appCfg.Sandbox.OpenShell
				gateway, gwErr := openshell.NewGatewayClientFromConfig(openshell.GRPCClientConfig{
					Addr:           osCfg.GatewayAddr,
					TLS:            osCfg.OpenShellGatewayTLS(),
					ClientCertPath: osCfg.ClientCertPath,
					ClientKeyPath:  osCfg.ClientKeyPath,
					CACertPath:     osCfg.CACertPath,
					AuthToken:      osCfg.AuthToken,
				})
				if gwErr != nil {
					slog.Warn("openshell orphan reconciler: failed to connect to gateway", "error", gwErr)
					return
				}
				defer gateway.Close()

				knownIDs, idsErr := entStore.AllSandboxSessionIDs(ctx)
				if idsErr != nil {
					slog.Warn("openshell orphan reconciler: failed to query session IDs", "error", idsErr)
					return
				}

				deleted, err := openshell.ReconcileOrphans(ctx, gateway, knownIDs, 10*time.Minute)
				if err != nil {
					slog.Warn("openshell orphan reconciler: reconciliation failed", "error", err)
					return
				}
				if len(deleted) > 0 {
					slog.Info("openshell orphan reconciler: cleanup complete",
						"deleted_count", len(deleted), "deleted_names", deleted)
				}
			}

			// First pass.
			runOpenShellReconcile()

			// Subsequent passes every 5 minutes.
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					runOpenShellReconcile()
				}
			}
		}()
	}

	// --- OpenShell idle timeout watchdog (periodic) ---
	// Evicts sandbox pods that have been idle (no exec/push/pull) for longer
	// than the configured idle timeout. Evicted sessions are transparently
	// recreated on the next tool call (see exec.go ensureSessionRunning).
	if appCfg.Storage.Backend == "postgres" && sandbox.BackendKind(appCfg.Sandbox.BackendKind()) == sandbox.BackendKindOpenShell {
		idleTimeout := appCfg.Sandbox.OpenShell.OpenShellIdleTimeout()
		if idleTimeout > 0 {
			go func() {
				// Initial delay: let the system stabilize.
				select {
				case <-ctx.Done():
					return
				case <-time.After(60 * time.Second):
				}

				ticker := time.NewTicker(5 * time.Minute)
				defer ticker.Stop()

				runIdleEviction := func() {
					osCfg := appCfg.Sandbox.OpenShell
					gateway, gwErr := openshell.NewGatewayClientFromConfig(openshell.GRPCClientConfig{
						Addr:           osCfg.GatewayAddr,
						TLS:            osCfg.OpenShellGatewayTLS(),
						ClientCertPath: osCfg.ClientCertPath,
						ClientKeyPath:  osCfg.ClientKeyPath,
						CACertPath:     osCfg.CACertPath,
						AuthToken:      osCfg.AuthToken,
					})
					if gwErr != nil {
						slog.Warn("openshell idle watchdog: failed to connect to gateway", "error", gwErr)
						return
					}
					defer gateway.Close()

					cutoff := time.Now().Add(-idleTimeout)
					sessions, listErr := entStore.ListIdleSandboxSessions(ctx, cutoff)
					if listErr != nil {
						slog.Warn("openshell idle watchdog: failed to list idle sessions", "error", listErr)
						return
					}

					var evicted int
					for _, sess := range sessions {
						sandboxID := sess.ContainerName
						if sandboxID != "" {
							if delErr := gateway.DeleteSandbox(ctx, sandboxID); delErr != nil {
								slog.Warn("openshell idle watchdog: failed to delete sandbox",
									"session", sess.SessionID, "sandbox", sandboxID, "error", delErr)
								continue
							}
						}
						if markErr := entStore.MarkSandboxSessionEvicted(ctx, sess.SessionID); markErr != nil {
							slog.Warn("openshell idle watchdog: failed to mark session evicted",
								"session", sess.SessionID, "error", markErr)
							continue
						}
						evicted++
						slog.Info("openshell idle watchdog: evicted idle sandbox",
							"session", sess.SessionID[:min(8, len(sess.SessionID))],
							"idle_for", time.Since(sess.LastActiveAt).Round(time.Second).String())
					}
					if evicted > 0 {
						slog.Info("openshell idle watchdog: eviction pass complete", "evicted", evicted)
					}
				}

				// First pass.
				runIdleEviction()

				// Subsequent passes every 5 minutes.
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						runIdleEviction()
					}
				}
			}()
		}
	}

	// Create and start the Studio server
	var studioOpts []launcher.StudioOption
	studioOpts = append(studioOpts, launcher.WithServices(svc))
	if platformAuth != nil && backend != nil {
		studioOpts = append(studioOpts, launcher.WithPlatformAuth(platformAuth, entStore))
		studioOpts = append(studioOpts, launcher.WithBackend(entStore))
		studioOpts = append(studioOpts, launcher.WithTenantMiddleware(entstore.TenantMiddleware(entStore)))
		api.SetPlatformBackend(entStore)
		api.SetPlatformSecrets(entStore.Secrets())
	}
	studio, err := launcher.NewStudioServer(port, studioOpts...)
	if err != nil {
		logger.Printf("Failed to start HTTP server: %v", err)
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}

	// Pre-warm the Studio chat agent in the background so the first web request is fast.
	// Also register the context builder so that Reset() can auto-PreWarm after settings changes.
	buildPreWarmCtx := func() context.Context {
		// Build an enriched Services clone for pre-warm that includes the
		// default org/team stores. Without these, the chat factory cannot
		// resolve MCP server configs (e.g., Tavily) or team settings
		// (WebSearchTool) from the database — causing standard servers to be
		// missing from the tool groups until a Reset() is triggered.
		warmSvc := &store.Services{
			Mode:             svc.Mode,
			Platform:         svc.Platform,
			TenantRouter:     svc.TenantRouter,
			PlatformSettings: svc.PlatformSettings,
		}
		if backend != nil {
			warmSvc.PlatformMCPServers = backend.PlatformMCPServers()
			orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
			if orgSlug != "" {
				warmSvc.OrgSettings = backend.OrgSettings(orgSlug)
				if orgStore, orgErr := backend.ForOrg(orgSlug); orgErr == nil {
					warmSvc.MCPServers = orgStore.OrgMCPServers()
					teamStore := orgStore.ForTeam("general")
					warmSvc.TeamMCPServers = teamStore.MCPServers()
					warmSvc.Settings = teamStore.Settings()
				}
			}
		}
		return store.WithServices(ctx, warmSvc)
	}
	api.SetPreWarmContextFunc(buildPreWarmCtx)
	api.SetLLMPool(llmPool)

	go func() {
		warmCtx := buildPreWarmCtx()
		if err := api.GetChatManager().PreWarm(warmCtx); err != nil {
			logger.Printf("Studio chat pre-warm failed (will retry on first request): %v", err)
		} else {
			logger.Printf("Studio chat agent pre-warmed successfully")
		}
	}()

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Start serving in a goroutine
	errCh := make(chan error, 1)
	go func() {
		logger.Printf("HTTP server listening on http://localhost:%d", port)
		if err := studio.Serve(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Print to stdout for foreground mode
	fmt.Printf("Astonish daemon running (pid: %d, port: %d)\n", os.Getpid(), port)
	fmt.Printf("Log: %s/daemon.log\n", logDir)

	// Wait for signal or server error
	select {
	case sig := <-sigCh:
		logger.Printf("Received signal %v, shutting down...", sig)
		fmt.Printf("\nShutting down...\n")
	case err := <-errCh:
		if err != nil {
			logger.Printf("Server error: %v", err)
			return err
		}
	}

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Cancel the main context to stop background goroutines (cleanup, etc.)
	ctxCancel()

	// Stop scheduler first (finish in-flight jobs)
	if mtSched != nil {
		logger.Printf("Stopping multi-tenant scheduler...")
		mtSched.Stop()
	}

	// Stop channels (drain in-flight messages)
	if channelMgr != nil {
		logger.Printf("Stopping channels...")
		if err := channelMgr.StopAll(shutdownCtx); err != nil {
			logger.Printf("Warning: Channel shutdown error: %v", err)
		}
	}

	// Stop sandbox containers gracefully (preserve them for reconnection after restart).
	// Unlike Reset(), this does NOT destroy containers — sessions persist and will
	// get their containers back via EnsureSessionContainer on the next tool call.
	logger.Printf("Stopping sandbox containers...")
	api.GetChatManager().ShutdownContainers()

	if err := studio.Shutdown(shutdownCtx); err != nil {
		logger.Printf("Shutdown error: %v", err)
		return fmt.Errorf("shutdown error: %w", err)
	}

	logger.Printf("Daemon stopped cleanly")
	return nil
}

// makeHeadlessRunner creates a RunHeadlessFunc that bridges the scheduler's
// executor to the launcher's headless runner, breaking the import cycle
// (scheduler -> launcher -> api).
func makeHeadlessRunner() scheduler.RunHeadlessFunc {
	return func(ctx context.Context, cfg *scheduler.HeadlessRunConfig) (string, error) {
		var agentCfg *config.AgentConfig
		var err error

		if cfg.FlowYAML != "" {
			// Platform mode: flow YAML was resolved from the store.
			agentCfg, err = config.LoadAgentFromBytes([]byte(cfg.FlowYAML))
			if err != nil {
				return "", fmt.Errorf("failed to parse flow YAML: %w", err)
			}
		} else {
			// Personal mode: load from filesystem path.
			agentCfg, err = config.LoadAgent(cfg.FlowPath)
			if err != nil {
				return "", fmt.Errorf("failed to load flow %q: %w", cfg.FlowPath, err)
			}
		}

		return launcher.RunHeadless(ctx, &launcher.HeadlessConfig{
			AgentConfig:  agentCfg,
			AppConfig:    cfg.AppConfig,
			ProviderName: cfg.ProviderName,
			ModelName:    cfg.ModelName,
			Parameters:   cfg.Parameters,
			DebugMode:    cfg.DebugMode,
		})

	}
}

// distillBridge adapts agent.ChatAgent to tools.DistillAccess,
// bridging the two packages without creating import cycles.
type distillBridge struct {
	agent *agent.ChatAgent
}

func newDistillBridge(a *agent.ChatAgent) *distillBridge {
	return &distillBridge{agent: a}
}

func (b *distillBridge) PreviewDistill(ctx context.Context, ds tools.DistillSession) (string, error) {
	return b.agent.PreviewDistill(ctx, agent.DistillSession{
		SessionID: ds.SessionID,
		AppName:   ds.AppName,
		UserID:    ds.UserID,
	})
}

func (b *distillBridge) ConfirmAndDistill(ctx context.Context, ds tools.DistillSession, print func(string)) error {
	return b.agent.ConfirmAndDistill(ctx, agent.DistillSession{
		SessionID: ds.SessionID,
		AppName:   ds.AppName,
		UserID:    ds.UserID,
	}, print)
}

// emailToolConfig holds the info needed to create an email client for tools.
type emailToolConfig struct {
	Provider     string
	IMAPServer   string
	SMTPServer   string
	Address      string
	Username     string
	Password     string
	Folder       string
	MaxBodyChars int
	// TokenFunc provides a valid OAuth2 access token (for msgraph provider).
	TokenFunc emailpkg.TokenFunc
}

// setupEmailTools creates an email client and registers it for the email tools.
func setupEmailTools(cfg *emailToolConfig) {
	emailCfg := &emailpkg.Config{
		Provider:     cfg.Provider,
		IMAPServer:   cfg.IMAPServer,
		SMTPServer:   cfg.SMTPServer,
		Address:      cfg.Address,
		Username:     cfg.Username,
		Password:     cfg.Password,
		Folder:       cfg.Folder,
		MaxBodyChars: cfg.MaxBodyChars,
		TokenFunc:    cfg.TokenFunc,
	}
	client, err := emailpkg.NewClient(emailCfg)
	if err != nil {
		slog.Warn("failed to create email client for tools", "error", err)
		return
	}
	tools.SetEmailClient(client)
	mailer.Init(client)
}

// fleetSchedulerBridge adapts scheduler.Scheduler to fleet.SchedulerAccess,
// bridging the two packages without import cycles.
type fleetSchedulerBridge struct {
	sched *scheduler.Scheduler
}

func newFleetSchedulerBridge(s *scheduler.Scheduler) *fleetSchedulerBridge {
	return &fleetSchedulerBridge{sched: s}
}

func (b *fleetSchedulerBridge) AddJob(job *fleet.SchedulerJob) error {
	sj := &scheduler.Job{
		Name: job.Name,
		Mode: scheduler.JobMode(job.Mode),
		Schedule: scheduler.JobSchedule{
			Cron: job.Cron,
		},
		Payload: scheduler.JobPayload{
			Flow: job.Flow,
		},
		Enabled: job.Enabled,
	}
	slog.Info("adding fleet scheduler job", "component", "fleet-sched-bridge", "name", job.Name, "mode", job.Mode, "cron", job.Cron, "flow", job.Flow, "enabled", job.Enabled)
	if err := b.sched.Store().Add(sj); err != nil {
		return err
	}
	job.ID = sj.ID
	b.sched.RefreshNextRun(sj.ID)
	slog.Info("fleet scheduler job created", "component", "fleet-sched-bridge", "id", sj.ID)
	return nil
}

func (b *fleetSchedulerBridge) RemoveJob(id string) error {
	return b.sched.Store().Remove(id)
}

func (b *fleetSchedulerBridge) RemoveJobByName(name string) error {
	for _, j := range b.sched.Store().List() {
		if j.Name == name {
			return b.sched.Store().Remove(j.ID)
		}
	}
	return nil // job not found is not an error
}

func (b *fleetSchedulerBridge) GetJob(id string) *fleet.SchedulerJob {
	sj := b.sched.Store().Get(id)
	if sj == nil {
		return nil
	}
	return &fleet.SchedulerJob{
		ID:      sj.ID,
		Name:    sj.Name,
		Mode:    string(sj.Mode),
		Cron:    sj.Schedule.Cron,
		Flow:    sj.Payload.Flow,
		Enabled: sj.Enabled,
	}
}

func (b *fleetSchedulerBridge) GetJobByName(name string) *fleet.SchedulerJob {
	sj := b.sched.Store().GetByName(name)
	if sj == nil {
		return nil
	}
	return &fleet.SchedulerJob{
		ID:      sj.ID,
		Name:    sj.Name,
		Mode:    string(sj.Mode),
		Cron:    sj.Schedule.Cron,
		Flow:    sj.Payload.Flow,
		Enabled: sj.Enabled,
	}
}

func (b *fleetSchedulerBridge) ValidateCron(expr string) error {
	return scheduler.ValidateCron(expr)
}

// fleetPlanRegistryAdapter adapts fleet.PlanRegistry to channels.FleetPlanRegistry.
type fleetPlanRegistryAdapter struct {
	reg *fleet.PlanRegistry
}

func (a *fleetPlanRegistryAdapter) ListPlans() []channels.FleetPlanSummary {
	plans := a.reg.ListPlans()
	result := make([]channels.FleetPlanSummary, len(plans))
	for i, p := range plans {
		result[i] = channels.FleetPlanSummary{
			Key:         p.Key,
			Name:        p.Name,
			Description: p.Description,
			ChannelType: p.ChannelType,
			AgentNames:  p.AgentNames,
		}
	}
	return result
}

// fleetTemplateRegistryAdapter adapts fleet.Registry to channels.FleetTemplateRegistry.
type fleetTemplateRegistryAdapter struct {
	reg *fleet.Registry
}

func (a *fleetTemplateRegistryAdapter) ListFleets() []channels.FleetTemplateSummary {
	fleets := a.reg.ListFleets()
	result := make([]channels.FleetTemplateSummary, len(fleets))
	for i, f := range fleets {
		result[i] = channels.FleetTemplateSummary{
			Key:         f.Key,
			Name:        f.Name,
			Description: f.Description,
			AgentNames:  f.AgentNames,
		}
	}
	return result
}

func (a *fleetTemplateRegistryAdapter) GetFleet(key string) (channels.FleetTemplateWithWizard, bool) {
	cfg, ok := a.reg.GetFleet(key)
	if !ok {
		return channels.FleetTemplateWithWizard{}, false
	}
	wizardPrompt, pinnedGroups := fleet.WizardContextForTemplate(cfg, key)
	return channels.FleetTemplateWithWizard{
		Name:               cfg.Name,
		WizardSystemPrompt: wizardPrompt,
		PinnedToolGroups:   pinnedGroups,
	}, true
}

// dbPlanAccessAdapter wraps a store.FleetPlanStore to satisfy fleet.PlanAccess
// for use by the PlanActivator in platform mode.
type dbPlanAccessAdapter struct {
	store        store.FleetPlanStore
	monitorStore fleet.MonitorStateStore
}

func (a *dbPlanAccessAdapter) GetPlan(key string) (*fleet.FleetPlan, bool) {
	planAny, ok := a.store.GetPlan(context.Background(), key)
	if !ok {
		return nil, false
	}
	plan, ok := planAny.(*fleet.FleetPlan)
	return plan, ok
}

func (a *dbPlanAccessAdapter) Save(plan *fleet.FleetPlan) error {
	return a.store.Save(context.Background(), plan)
}

func (a *dbPlanAccessAdapter) ListPlans() []fleet.PlanSummary {
	summaries := a.store.ListPlans(context.Background())
	result := make([]fleet.PlanSummary, len(summaries))
	for i, s := range summaries {
		result[i] = fleet.PlanSummary{
			Key:         s.Key,
			Name:        s.Name,
			Description: s.Description,
			CreatedFrom: s.CreatedFrom,
			ChannelType: s.ChannelType,
			AgentCount:  s.AgentCount,
			AgentNames:  s.AgentNames,
		}
	}
	return result
}

func (a *dbPlanAccessAdapter) MonitorStateStore() fleet.MonitorStateStore {
	return a.monitorStore
}

// Compile-time check
var _ fleet.PlanAccess = (*dbPlanAccessAdapter)(nil)

// dbFleetPlanRegistryAdapter adapts store.FleetPlanStore to channels.FleetPlanRegistry.
// Used in platform mode when FleetDeps needs to list plans from the DB.
type dbFleetPlanRegistryAdapter struct {
	store store.FleetPlanStore
}

func (a *dbFleetPlanRegistryAdapter) ListPlans() []channels.FleetPlanSummary {
	plans := a.store.ListPlans(context.Background())
	result := make([]channels.FleetPlanSummary, len(plans))
	for i, p := range plans {
		result[i] = channels.FleetPlanSummary{
			Key:         p.Key,
			Name:        p.Name,
			Description: p.Description,
			ChannelType: p.ChannelType,
			AgentNames:  p.AgentNames,
		}
	}
	return result
}

// dbFleetTemplateRegistryAdapter adapts store.FleetTemplateStore to channels.FleetTemplateRegistry.
// Used in platform mode when FleetDeps needs to list/get fleet templates from the DB.
type dbFleetTemplateRegistryAdapter struct {
	store store.FleetTemplateStore
}

func (a *dbFleetTemplateRegistryAdapter) ListFleets() []channels.FleetTemplateSummary {
	fleets := a.store.ListFleets(context.Background())
	result := make([]channels.FleetTemplateSummary, len(fleets))
	for i, f := range fleets {
		result[i] = channels.FleetTemplateSummary{
			Key:         f.Key,
			Name:        f.Name,
			Description: f.Description,
			AgentNames:  f.AgentNames,
		}
	}
	return result
}

func (a *dbFleetTemplateRegistryAdapter) GetFleet(key string) (channels.FleetTemplateWithWizard, bool) {
	cfgAny, ok := a.store.GetFleet(context.Background(), key)
	if !ok {
		return channels.FleetTemplateWithWizard{}, false
	}
	cfg, ok := cfgAny.(*fleet.FleetConfig)
	if !ok {
		return channels.FleetTemplateWithWizard{}, false
	}
	wizardPrompt, pinnedGroups := fleet.WizardContextForTemplate(cfg, key)
	return channels.FleetTemplateWithWizard{
		Name:               cfg.Name,
		WizardSystemPrompt: wizardPrompt,
		PinnedToolGroups:   pinnedGroups,
	}, true
}

// runPeriodicCleanup runs session expiry and orphan container pruning on a timer.
// It runs once shortly after startup, then periodically based on config.
func runPeriodicCleanup(ctx context.Context, appCfg *config.AppConfig, sessionStore *persistentsession.FileStore, logger *Logger) {
	// Initial delay to avoid slowing down startup
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	// Run once immediately after the initial delay
	runCleanupCycle(appCfg, sessionStore, logger)

	// Then run periodically
	interval := time.Duration(appCfg.Sandbox.Prune.OrphanCheckHours) * time.Hour
	if interval <= 0 {
		interval = 6 * time.Hour // default
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCleanupCycle(appCfg, sessionStore, logger)
		}
	}
}

// runCleanupCycle performs a single cleanup pass: expired sessions + orphan containers.
func runCleanupCycle(appCfg *config.AppConfig, sessionStore *persistentsession.FileStore, logger *Logger) {
	// Reload config to pick up changes without restart
	freshCfg, err := config.LoadAppConfig()
	if err == nil {
		appCfg = freshCfg
	}

	// 1. Clean up expired sessions
	if sessionStore != nil {
		maxAge := appCfg.Sessions.Cleanup.EffectiveMaxAgeDays()
		if maxAge > 0 {
			deletedIDs := sessionStore.CleanupExpiredSessions(maxAge)
			// Destroy sandbox containers for deleted sessions
			for _, id := range deletedIDs {
				sandbox.TryDestroySession(appCfg, id, nil)
			}
		}
	}

	// 2. Prune orphan sandbox containers
	if sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		var liveSessionIDs map[string]bool
		if sessionStore != nil {
			liveSessionIDs = sessionStore.AllSessionIDs()
		}

		kind := sandbox.BackendKind(appCfg.Sandbox.BackendKind())
		switch kind {
		case sandbox.BackendKindK8s:
			b, cleanup, bErr := sandbox.BackendFromAppConfig(appCfg)
			if bErr == nil {
				if cleanup != nil {
					defer cleanup()
				}
				registry, regErr := sandbox.NewSessionRegistry()
				if regErr == nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					pruned, _ := sandbox.PruneOrphansForBackend(ctx, b, registry, liveSessionIDs)
					cancel()
					if pruned > 0 {
						logger.Printf("[cleanup] Pruned %d orphaned sandbox pod(s)", pruned)
					}
				}
			}
		default:
			// Incus path (backward-compatible default)
			platform := incus.DetectPlatform()
			if platform != incus.PlatformUnsupported {
				incus.SetActivePlatform(platform)
				client, connErr := incus.Connect(platform)
				if connErr == nil {
					registry, regErr := sandbox.NewSessionRegistry()
					if regErr == nil {
						pruned, _ := sandbox.PruneOrphans(client, registry, liveSessionIDs)
						if pruned > 0 {
							logger.Printf("[cleanup] Pruned %d orphaned sandbox container(s)", pruned)
						}
					}
				}
			}
		}
	}
}

// cascadePlatformProviders merges platform-level and default-org provider
// settings from the platform store into appCfg. This gives the daemon-level
// channel/fleet agent access to providers configured via the admin UI.
//
// Note: This only applies Platform + Org layers. Team-level provider overrides
// are resolved per-message via the provider.Pool in the channel manager.
func cascadePlatformProviders(ctx context.Context, backend store.PlatformBackend, appCfg *config.AppConfig, logger *Logger) {
	// Platform mode: providers come exclusively from the database.
	// Clear any config.yaml residue so the DB is the sole source of truth.
	appCfg.Providers = nil
	appCfg.General.DefaultProvider = ""
	appCfg.General.DefaultModel = ""

	// Determine org settings store for the default org (if configured).
	var orgSettings store.OrgSettingsStore
	orgSlug := appCfg.Storage.Auth.GetDefaultOrgSlug()
	if orgSlug != "" {
		orgSettings = backend.OrgSettings(orgSlug)
	}

	// Delegate to the shared provider resolution function (Platform + Org only;
	// team is nil since this is the daemon-wide fallback, not per-user).
	resolved := provider.ResolveEffectiveConfig(ctx, backend.PlatformSettings(), orgSettings, nil)
	// Daemon bootstrap has no user/session context; pass empty strings so the
	// overlay chain is a no-op. Keeps every ResolveEffectiveConfig call site
	// syntactically consistent with the per-request paths.
	resolved = provider.ApplyProviderOverride(resolved, "", "")
	appCfg.Providers = resolved.Providers
	appCfg.General.DefaultProvider = resolved.General.DefaultProvider
	appCfg.General.DefaultModel = resolved.General.DefaultModel

	if resolved.General.DefaultProvider != "" {
		logger.Printf("Platform providers cascaded: default=%s model=%s (%d providers)",
			resolved.General.DefaultProvider, resolved.General.DefaultModel, len(resolved.Providers))
	}
}
