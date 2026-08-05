package astonish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/daemon"
	emailPkg "github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	slackPkg "github.com/slack-go/slack"
)

func handleChannelsCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printChannelsUsage()
		return nil
	}

	subcommand := args[0]
	switch subcommand {
	case "status":
		return handleChannelsStatus()
	case "setup":
		if len(args) < 2 {
			fmt.Println("usage: astonish channels setup <channel>")
			fmt.Println("")
			fmt.Println("Available channels:")
			fmt.Println("  telegram      Set up Telegram bot integration")
			fmt.Println("  email         Set up Email integration (IMAP/SMTP)")
			fmt.Println("  slack         Set up Slack bot integration")
			return nil
		}
		switch args[1] {
		case "telegram":
			return handleTelegramSetup()
		case "email":
			return handleEmailSetup()
		case "slack":
			return handleSlackSetup()
		default:
			return fmt.Errorf("unknown channel: %s (available: telegram, email, slack)", args[1])
		}
	case "disable":
		if len(args) < 2 {
			fmt.Println("usage: astonish channels disable <channel>")
			return nil
		}
		return handleChannelDisable(args[1])
	default:
		fmt.Printf("Unknown channels subcommand: %s\n", subcommand)
		printChannelsUsage()
		return fmt.Errorf("unknown subcommand: %s", subcommand)
	}
}

// handleTelegramSetup runs the interactive Telegram bot setup flow.
func handleTelegramSetup() error {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Check if already configured
	if cfg.Channels.Telegram.IsTelegramEnabled() {
		fmt.Println()
		fmt.Printf("  Telegram is already configured (bot token: ...%s)\n",
			maskToken(cfg.Channels.Telegram.BotToken))
		if len(cfg.Channels.Telegram.AllowFrom) > 0 {
			fmt.Printf("  Allowed users: %s\n", strings.Join(cfg.Channels.Telegram.AllowFrom, ", "))
		}
		fmt.Println()

		var reconfigure bool
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Reconfigure Telegram?").
					Description("This will replace your current Telegram configuration").
					Affirmative("Yes, reconfigure").
					Negative("No, keep current").
					Value(&reconfigure),
			),
		).Run()
		if err != nil || !reconfigure {
			if err == nil {
				fmt.Println("Keeping current configuration.")
			}
			return err
		}
	}

	// Step 1: Introduction and readiness check
	fmt.Println()
	channelsPrintHeader("Telegram Bot Setup")
	fmt.Println()
	fmt.Println("  To connect Astonish to Telegram, you need a bot token.")
	fmt.Println()
	fmt.Println("  1. Open Telegram and search for @BotFather")
	fmt.Println("  2. Send /newbot and follow the instructions")
	fmt.Println("  3. Copy the bot token you receive")
	fmt.Println()

	var ready string
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Have you already created a bot?").
				Options(
					huh.NewOption("Yes, I have a token", "yes"),
					huh.NewOption("Not yet — I'll open BotFather first", "no"),
				).
				Value(&ready),
		),
	).Run()
	if err != nil {
		return nil // user aborted
	}

	if ready == "no" {
		fmt.Println()
		fmt.Println("  Open this link to create your bot:")
		fmt.Println()
		fmt.Printf("    \033[4;36mhttps://t.me/BotFather\033[0m\n")
		fmt.Println()
		fmt.Println("  Once you have your token, run this command again:")
		fmt.Println("    astonish channels setup telegram")
		fmt.Println()
		return nil
	}

	// Step 2: Token input + validation
	var botToken string
	var botUsername string

	for {
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Bot Token").
					Description("Paste the token from BotFather").
					EchoMode(huh.EchoModePassword).
					Value(&botToken),
			),
		).Run()
		if err != nil {
			return nil // user aborted
		}

		botToken = strings.TrimSpace(botToken)
		if botToken == "" {
			fmt.Println("  Token cannot be empty. Please try again.")
			continue
		}

		// Validate token by connecting to Telegram
		channelsRunSpinner("Connecting to Telegram...")
		bot, botErr := tgbotapi.NewBotAPI(botToken)
		if botErr != nil {
			fmt.Printf("  \033[31mInvalid token: %v\033[0m\n", botErr)
			fmt.Println("  Please check your token and try again.")
			fmt.Println()
			botToken = ""
			continue
		}

		botUsername = bot.Self.UserName
		fmt.Printf("  \033[32mConnected as @%s\033[0m\n\n", botUsername)
		break
	}

	// Determine if we're in platform mode
	isPlatform := cfg.Storage.Backend == "postgres" && cfg.Storage.Postgres.PlatformDSN != ""

	var allowFrom []string

	if isPlatform {
		// Platform mode: access control is managed via user channel linking.
		// No static allowlist needed — users link their accounts via /link <code>.
		fmt.Println()
		fmt.Println("  \033[36mPlatform mode detected.\033[0m")
		fmt.Println("  Access control is managed through user account linking.")
		fmt.Println("  Users link their Telegram via Settings → Connected Channels.")
		fmt.Println()
	} else {
		// Personal mode: require at least one allowed user (static allowlist).
		// The bot has full tool access with auto-approve, so open access is never safe.
		fmt.Println()
		fmt.Println("  Now let's register who can talk to your bot.")
		fmt.Println("  Open this link in Telegram and send /start to your bot:")
		fmt.Println()
		fmt.Printf("    \033[4;36mhttps://t.me/%s\033[0m\n", botUsername)
		fmt.Println()

		for {
			allowFrom, err = detectTelegramUsers(botToken)
			if err != nil {
				return err
			}

			if len(allowFrom) > 0 {
				break
			}

			fmt.Println("  \033[31mAt least one allowed user is required.\033[0m")
			fmt.Println("  The bot has full tool access — it cannot be left open to everyone.")
			fmt.Println()

			var retry bool
			retryErr := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title("Try detecting users again?").
						Affirmative("Yes, try again").
						Negative("Cancel setup").
						Value(&retry),
				),
			).Run()
			if retryErr != nil || !retry {
				fmt.Println("  Setup cancelled.")
				return nil
			}
			fmt.Println()
			fmt.Println("  Send /start to your bot in Telegram, then press Ctrl+C when done:")
			fmt.Println()
		}
	}

	// Step 4: Save configuration
	enabled := true
	cfg.Channels.Enabled = &enabled
	cfg.Channels.Telegram.Enabled = &enabled
	cfg.Channels.Telegram.AllowFrom = allowFrom

	// Save bot token to the appropriate store based on mode
	if isPlatform {
		// Platform mode: store in platform_secrets table (DB)
		entCfg := entstore.Config{DSN: cfg.Storage.Postgres.GetPlatformDSN(), InstanceSuffix: cfg.Storage.Postgres.InstanceSuffix}
		_, es, entErr := entstore.NewPlatformServices(context.Background(), entCfg)
		if entErr != nil {
			return fmt.Errorf("failed to connect to platform database: %w", entErr)
		}
		defer es.Close()
		if setErr := es.Secrets().SetSecret("channels.telegram.bot_token", botToken); setErr != nil {
			return fmt.Errorf("failed to store bot token in platform database: %w", setErr)
		}
		fmt.Println("  Bot token stored in platform database.")
	} else {
		// Personal mode: store in file-based encrypted credential store
		cfg.Channels.Telegram.BotToken = botToken // fallback: stays in config if store fails
		configDir, configDirErr := config.GetConfigDir()
		if configDirErr != nil {
			slog.Warn("failed to get config directory", "error", configDirErr)
		}
		if configDir != "" {
			if store, storeErr := credentials.Open(configDir); storeErr == nil {
				if setErr := store.SetSecret("channels.telegram.bot_token", botToken); setErr == nil {
					cfg.Channels.Telegram.BotToken = "" // scrub from config.yaml
				}
			}
		}
	}

	// Always scrub token from config.yaml (never store plaintext)
	cfg.Channels.Telegram.BotToken = ""

	if err := config.SaveAppConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Success message
	summary := fmt.Sprintf("Telegram bot @%s configured successfully!", botUsername)
	channelsPrintSuccess(summary)

	fmt.Println()
	if isPlatform {
		fmt.Printf("  Bot:       @%s\n", botUsername)
		fmt.Printf("  Deep link: https://t.me/%s\n", botUsername)
		fmt.Println()
		fmt.Println("  Users can link their accounts via the web UI.")
	} else {
		fmt.Printf("  Allowed users: %s\n", strings.Join(allowFrom, ", "))
		fmt.Printf("  Deep link:     https://t.me/%s\n", botUsername)
	}
	fmt.Println()

	// Activate the new config in the running daemon (if any).
	notifyDaemonChannelsChanged(cfg)
	fmt.Println()

	return nil
}

// detectTelegramUsers temporarily polls the bot for /start messages
// and auto-detects user IDs. Returns the list of allowed user ID strings.
func detectTelegramUsers(botToken string) ([]string, error) {
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	// Drain all pending updates so we only see new messages.
	// Use direct API calls (not channel) to reliably find the latest offset.
	drainOffset := 0
	drainConfig := tgbotapi.NewUpdate(0)
	drainConfig.Timeout = 0
	for {
		drained, drainErr := bot.GetUpdates(drainConfig)
		if drainErr != nil || len(drained) == 0 {
			break
		}
		drainOffset = drained[len(drained)-1].UpdateID + 1
		drainConfig.Offset = drainOffset
	}

	// Start long polling from the clean offset — only new messages arrive.
	updateConfig := tgbotapi.NewUpdate(drainOffset)
	updateConfig.Timeout = 30
	updates := bot.GetUpdatesChan(updateConfig)

	type detectedUser struct {
		ID   string
		Name string
	}

	var allowFrom []string
	seen := map[string]bool{}

	fmt.Println("  Waiting for messages... (press Ctrl+C when done)")
	fmt.Println()

	// Set up interrupt handler
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// Poll with a timeout — wait up to 5 minutes total
	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-sigCh:
			fmt.Println()
			bot.StopReceivingUpdates()
			return allowFrom, nil

		case <-timeout:
			fmt.Println("\n  Timed out waiting for messages.")
			bot.StopReceivingUpdates()
			return allowFrom, nil

		case update, ok := <-updates:
			if !ok {
				return allowFrom, nil
			}
			if update.Message == nil {
				continue
			}

			msg := update.Message
			senderID := strconv.FormatInt(msg.From.ID, 10)

			if seen[senderID] {
				continue
			}

			// Build display name
			name := msg.From.FirstName
			if msg.From.LastName != "" {
				name += " " + msg.From.LastName
			}
			if name == "" {
				name = msg.From.UserName
			}

			user := detectedUser{ID: senderID, Name: name}

			fmt.Printf("  \033[32mDetected: %s (ID: %s)\033[0m\n", user.Name, user.ID)

			var addUser bool
			addErr := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title(fmt.Sprintf("Add %s to allowed users?", user.Name)).
						Affirmative("Yes").
						Negative("No").
						Value(&addUser),
				),
			).Run()
			if addErr != nil {
				bot.StopReceivingUpdates()
				return allowFrom, nil
			}

			if addUser {
				allowFrom = append(allowFrom, senderID)
				seen[senderID] = true

				// Send confirmation to the user in Telegram
				reply := tgbotapi.NewMessage(msg.Chat.ID,
					fmt.Sprintf("You've been added to Astonish! (ID: %s)", senderID))
				bot.Send(reply)

				fmt.Printf("  Added %s (%s)\n\n", user.Name, senderID)

				// One instance = one owner. Done.
				bot.StopReceivingUpdates()
				return allowFrom, nil
			} else {
				seen[senderID] = true
				fmt.Println()
			}
		}
	}
}

// handleEmailSetup runs the interactive email channel setup flow.
func handleEmailSetup() error {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Check if already configured
	if cfg.Channels.Email.IsEmailEnabled() {
		fmt.Println()
		fmt.Printf("  Email is already configured (%s)\n", cfg.Channels.Email.Address)
		if len(cfg.Channels.Email.AllowFrom) > 0 {
			fmt.Printf("  Allowed senders: %s\n", strings.Join(cfg.Channels.Email.AllowFrom, ", "))
		}
		fmt.Println()

		var reconfigure bool
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Reconfigure email?").
					Value(&reconfigure),
			),
		).Run()
		if err != nil || !reconfigure {
			return nil
		}
	}

	channelsPrintHeader("Email Channel Setup")
	fmt.Println()

	var imapServer, smtpServer, address, username, password string
	var allowFromStr string

	// IMAP/SMTP details form
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("IMAP Server (e.g., imap.gmail.com:993)").
				Value(&imapServer).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("IMAP server is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("SMTP Server (e.g., smtp.gmail.com:587)").
				Value(&smtpServer).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("SMTP server is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Email Address").
				Value(&address).
				Validate(func(s string) error {
					if s == "" || !strings.Contains(s, "@") {
						return fmt.Errorf("valid email address is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Username (press Enter if same as email address)").
				Value(&username),
			huh.NewInput().
				Title("Password / App Password").
				EchoMode(huh.EchoModePassword).
				Value(&password).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("password is required")
					}
					return nil
				}),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Allowed senders (comma-separated emails, or * for anyone)").
				Value(&allowFromStr).
				Validate(func(s string) error {
					if s == "" {
						return fmt.Errorf("at least one sender address is required")
					}
					return nil
				}),
		),
	).Run()
	if err != nil {
		return err
	}

	if username == "" {
		username = address
	}

	// Parse allowFrom
	var allowFrom []string
	for _, a := range strings.Split(allowFromStr, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			allowFrom = append(allowFrom, a)
		}
	}

	fmt.Println()
	channelsRunSpinner("Testing IMAP connection...")

	// Test IMAP connection
	emailCfg := &emailTestConfig{
		IMAPServer: imapServer,
		Address:    address,
		Username:   username,
		Password:   password,
	}
	if err := testIMAPConnection(emailCfg); err != nil {
		fmt.Printf("  IMAP connection failed: %v\n", err)
		return fmt.Errorf("IMAP test failed: %w", err)
	}
	channelsRunSpinner("IMAP connection successful")

	// Save configuration
	enabled := true
	channelsEnabled := true
	cfg.Channels.Enabled = &channelsEnabled
	cfg.Channels.Email = config.EmailConfig{
		Enabled:    &enabled,
		Provider:   "imap",
		IMAPServer: imapServer,
		SMTPServer: smtpServer,
		Address:    address,
		Username:   username,
		AllowFrom:  allowFrom,
	}

	// Store password in encrypted credential store
	configDir, err := config.GetConfigDir()
	if err == nil {
		store, storeErr := credentials.Open(configDir)
		if storeErr == nil {
			if setErr := store.SetSecret("channels.email.password", password); setErr != nil {
				fmt.Printf("  Warning: Failed to save password to encrypted store: %v\n", setErr)
				fmt.Println("  Password will be saved in config.yaml (less secure)")
				cfg.Channels.Email.Password = password
			} else {
				channelsRunSpinner("Password stored in encrypted credential store")
			}
		} else {
			cfg.Channels.Email.Password = password
		}
	} else {
		cfg.Channels.Email.Password = password
	}

	// Scrub migrated secrets before saving
	credentials.ScrubAppConfig(cfg)

	if err := config.SaveAppConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	channelsPrintSuccess("Email channel configured!")
	fmt.Println()
	fmt.Println("  Run 'astonish daemon start' to activate, or")
	fmt.Println("  'astonish daemon run' for foreground mode.")
	fmt.Println()

	notifyDaemonChannelsChanged(cfg)

	return nil
}

// handleSlackSetup runs the interactive Slack bot setup flow.
func handleSlackSetup() error {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Check if already configured
	if cfg.Channels.Slack.IsSlackEnabled() {
		fmt.Println()
		fmt.Println("  Slack is already configured and enabled.")
		fmt.Println()

		var reconfigure bool
		reconfigErr := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Reconfigure Slack?").
					Description("This will overwrite the current Slack configuration.").
					Affirmative("Yes, reconfigure").
					Negative("Cancel").
					Value(&reconfigure),
			),
		).Run()
		if reconfigErr != nil || !reconfigure {
			fmt.Println("  Setup cancelled.")
			return nil
		}
	}

	fmt.Println()
	channelsPrintHeader("Slack Bot Setup")
	fmt.Println()
	fmt.Println("  To set up a Slack bot, you'll need to create a Slack App:")
	fmt.Println()
	fmt.Println("  1. Go to \033[4;36mhttps://api.slack.com/apps\033[0m")
	fmt.Println("  2. Click \"Create New App\" → \"From scratch\"")
	fmt.Println("  3. Name your app and select your workspace")
	fmt.Println()
	fmt.Println("  4. Under \033[1mOAuth & Permissions\033[0m → Bot Token Scopes, add:")
	fmt.Println("     • app_mentions:read")
	fmt.Println("     • chat:write")
	fmt.Println("     • im:history")
	fmt.Println("     • im:read")
	fmt.Println("     • im:write")
	fmt.Println("     • users:read")
	fmt.Println()
	fmt.Println("  5. Install the app to your workspace")
	fmt.Println("     → Copy the \033[1mBot User OAuth Token\033[0m (starts with xoxb-)")
	fmt.Println()
	fmt.Println("  6. Under \033[1mSocket Mode\033[0m, enable it")
	fmt.Println("     → Generate an \033[1mApp-Level Token\033[0m with 'connections:write' scope")
	fmt.Println("     → Copy the token (starts with xapp-)")
	fmt.Println()
	fmt.Println("  7. Under \033[1mEvent Subscriptions\033[0m, enable events and subscribe to:")
	fmt.Println("     • app_mention (in bot events)")
	fmt.Println("     • message.im (in bot events)")
	fmt.Println()
	fmt.Println("  8. Optional: To let Astonish register Slack slash commands automatically,")
	fmt.Println("     note your Slack App ID and create an App Configuration Token.")
	fmt.Println()

	// Mode selection
	mode := "socket"
	modeErr := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Connection mode").
				Description("Socket Mode is recommended for most setups. Events API requires a public HTTPS endpoint.").
				Options(
					huh.NewOption("Socket Mode (recommended)", "socket"),
					huh.NewOption("Events API (HTTP webhook)", "events"),
				).
				Value(&mode),
		),
	).Run()
	if modeErr != nil {
		return modeErr
	}

	// Bot Token (required for both modes)
	var botToken string
	tokenErr := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Bot User OAuth Token").
				Description("Found under OAuth & Permissions → Bot User OAuth Token (xoxb-...)").
				Placeholder("xoxb-...").
				EchoMode(huh.EchoModePassword).
				Value(&botToken),
		),
	).Run()
	if tokenErr != nil {
		return tokenErr
	}
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		return fmt.Errorf("bot token is required")
	}
	if !strings.HasPrefix(botToken, "xoxb-") {
		fmt.Println("  \033[33mWarning: Token doesn't start with 'xoxb-'. This might not be a Bot User OAuth Token.\033[0m")
	}

	// Validate bot token
	channelsRunSpinner("Validating bot token...")
	slackClient := slackPkg.New(botToken)
	authResp, authErr := slackClient.AuthTest()
	if authErr != nil {
		return fmt.Errorf("token validation failed: %w", authErr)
	}
	fmt.Printf("  \033[32m✓ Connected!\033[0m  Bot: %s  |  Workspace: %s\n", authResp.User, authResp.Team)
	fmt.Println()

	// Mode-specific secrets
	var appToken string
	var signingSecret string

	if mode == "socket" {
		// App-Level Token (Socket Mode)
		appTokenErr := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("App-Level Token (Socket Mode)").
					Description("Generate under Basic Information → App-Level Tokens with 'connections:write' scope (xapp-...)").
					Placeholder("xapp-...").
					EchoMode(huh.EchoModePassword).
					Value(&appToken),
			),
		).Run()
		if appTokenErr != nil {
			return appTokenErr
		}
		appToken = strings.TrimSpace(appToken)
		if appToken == "" {
			return fmt.Errorf("app-level token is required for Socket Mode")
		}
		if !strings.HasPrefix(appToken, "xapp-") {
			fmt.Println("  \033[33mWarning: Token doesn't start with 'xapp-'. This might not be an App-Level Token.\033[0m")
		}
	} else {
		// Signing Secret (Events API)
		signingErr := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Signing Secret").
					Description("Found under Basic Information → App Credentials → Signing Secret").
					Placeholder("abc123...").
					EchoMode(huh.EchoModePassword).
					Value(&signingSecret),
			),
		).Run()
		if signingErr != nil {
			return signingErr
		}
		signingSecret = strings.TrimSpace(signingSecret)
		if signingSecret == "" {
			return fmt.Errorf("signing secret is required for Events API mode")
		}
	}

	var syncCommands bool
	var slackAppID string
	var configToken string
	var commandURL string
	manifestErr := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Register Slack slash commands automatically?").
				Description("Uses Slack App Manifest APIs to register /link and Slack-safe Astonish commands. You can skip this and keep managing commands manually.").
				Affirmative("Yes, sync commands").
				Negative("Skip").
				Value(&syncCommands),
		),
	).Run()
	if manifestErr != nil {
		return manifestErr
	}
	if syncCommands {
		manifestErr = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Slack App ID").
					Description("Found under Basic Information → App Credentials (starts with A).").
					Placeholder("A1234567890").
					Value(&slackAppID),
				huh.NewInput().
					Title("App Configuration Token").
					Description("Slack configuration token for App Manifest APIs. This is not the xoxb bot token or xapp Socket Mode token.").
					EchoMode(huh.EchoModePassword).
					Value(&configToken),
				huh.NewInput().
					Title("Slash Command URL").
					Description("For Events API, use https://<host>/api/slack/commands. Optional for Socket Mode if Slack accepts omitted command URLs.").
					Placeholder("https://example.com/api/slack/commands").
					Value(&commandURL),
			),
		).Run()
		if manifestErr != nil {
			return manifestErr
		}
		slackAppID = strings.TrimSpace(slackAppID)
		configToken = strings.TrimSpace(configToken)
		commandURL = strings.TrimSpace(commandURL)
		if slackAppID == "" {
			return fmt.Errorf("Slack App ID is required for automatic command registration")
		}
		if configToken == "" {
			return fmt.Errorf("App Configuration Token is required for automatic command registration")
		}
		if strings.HasPrefix(configToken, "xapp-") {
			return fmt.Errorf("automatic command registration needs a Slack App Configuration Token, not an xapp App-Level Token")
		}
		if strings.HasPrefix(configToken, "xoxb-") {
			return fmt.Errorf("automatic command registration needs a Slack App Configuration Token, not an xoxb Bot User OAuth Token")
		}
		if mode != "socket" && commandURL == "" {
			return fmt.Errorf("Slash Command URL is required for automatic Slack command registration in Events API mode")
		}
		if commandURL != "" && !strings.HasPrefix(commandURL, "https://") {
			return fmt.Errorf("Slash Command URL must be a public HTTPS URL")
		}
	}

	// Determine if we're in platform mode
	isPlatform := cfg.Storage.Backend == "postgres" && cfg.Storage.Postgres.PlatformDSN != ""

	if isPlatform {
		fmt.Println()
		fmt.Println("  \033[36mPlatform mode detected.\033[0m")
		fmt.Println("  Access control is managed through user account linking.")
		fmt.Println("  Users link their Slack via Settings → Connected Channels.")
		fmt.Println()
	} else {
		// Personal mode: optionally allow specific Slack user IDs
		fmt.Println()
		fmt.Println("  \033[36mPersonal mode:\033[0m The bot will respond to all DMs by default.")
		fmt.Println("  You can restrict access to specific Slack user IDs later in config.")
		fmt.Println()
	}

	// Save configuration
	enabled := true
	cfg.Channels.Enabled = &enabled
	cfg.Channels.Slack.Enabled = &enabled
	cfg.Channels.Slack.Mode = mode
	cfg.Channels.Slack.AppID = slackAppID
	cfg.Channels.Slack.CommandURL = commandURL

	// Save secrets to the appropriate store based on mode
	if isPlatform {
		// Platform mode: store in platform_secrets table (DB)
		entCfg := entstore.Config{DSN: cfg.Storage.Postgres.GetPlatformDSN(), InstanceSuffix: cfg.Storage.Postgres.InstanceSuffix}
		_, es, entErr := entstore.NewPlatformServices(context.Background(), entCfg)
		if entErr != nil {
			return fmt.Errorf("failed to connect to platform database: %w", entErr)
		}
		defer es.Close()
		if setErr := es.Secrets().SetSecret("channels.slack.bot_token", botToken); setErr != nil {
			return fmt.Errorf("failed to store bot token in platform database: %w", setErr)
		}
		if mode == "socket" && appToken != "" {
			if setErr := es.Secrets().SetSecret("channels.slack.app_token", appToken); setErr != nil {
				return fmt.Errorf("failed to store app token in platform database: %w", setErr)
			}
		}
		if mode == "events" && signingSecret != "" {
			if setErr := es.Secrets().SetSecret("channels.slack.signing_secret", signingSecret); setErr != nil {
				return fmt.Errorf("failed to store signing secret in platform database: %w", setErr)
			}
		}
		if configToken != "" {
			if setErr := es.Secrets().SetSecret("channels.slack.config_token", configToken); setErr != nil {
				return fmt.Errorf("failed to store Slack app configuration token in platform database: %w", setErr)
			}
		}
		fmt.Println("  Secrets stored in platform database.")
	} else {
		// Personal mode: store in file-based encrypted credential store
		configDir, configDirErr := config.GetConfigDir()
		if configDirErr != nil {
			slog.Warn("failed to get config directory", "error", configDirErr)
		}
		if configDir != "" {
			if store, storeErr := credentials.Open(configDir); storeErr == nil {
				if setErr := store.SetSecret("channels.slack.bot_token", botToken); setErr != nil {
					slog.Warn("failed to store bot token in credential store", "error", setErr)
					cfg.Channels.Slack.BotToken = botToken // fallback
				}
				if mode == "socket" && appToken != "" {
					if setErr := store.SetSecret("channels.slack.app_token", appToken); setErr != nil {
						slog.Warn("failed to store app token in credential store", "error", setErr)
						cfg.Channels.Slack.AppToken = appToken // fallback
					}
				}
				if mode == "events" && signingSecret != "" {
					if setErr := store.SetSecret("channels.slack.signing_secret", signingSecret); setErr != nil {
						slog.Warn("failed to store signing secret in credential store", "error", setErr)
						cfg.Channels.Slack.SigningSecret = signingSecret // fallback
					}
				}
				if configToken != "" {
					if setErr := store.SetSecret("channels.slack.config_token", configToken); setErr != nil {
						slog.Warn("failed to store Slack app configuration token in credential store", "error", setErr)
						cfg.Channels.Slack.ConfigToken = configToken // fallback
					}
				}
			} else {
				// Credential store unavailable — save in config as fallback
				cfg.Channels.Slack.BotToken = botToken
				if mode == "socket" {
					cfg.Channels.Slack.AppToken = appToken
				}
				if mode == "events" {
					cfg.Channels.Slack.SigningSecret = signingSecret
				}
				cfg.Channels.Slack.ConfigToken = configToken
			}
		}
	}

	// Always scrub tokens from config.yaml
	cfg.Channels.Slack.BotToken = ""
	cfg.Channels.Slack.AppToken = ""
	cfg.Channels.Slack.SigningSecret = ""
	cfg.Channels.Slack.ConfigToken = ""

	if err := config.SaveAppConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Success message
	summary := fmt.Sprintf("Slack bot '%s' configured successfully! (workspace: %s, mode: %s)", authResp.User, authResp.Team, mode)
	channelsPrintSuccess(summary)

	fmt.Println()
	if isPlatform {
		fmt.Printf("  Bot:       %s\n", authResp.User)
		fmt.Printf("  Workspace: %s\n", authResp.Team)
		fmt.Printf("  Mode:      %s\n", mode)
		fmt.Println()
		fmt.Println("  Users can link their accounts via the web UI.")
	} else {
		fmt.Printf("  Bot:       %s\n", authResp.User)
		fmt.Printf("  Workspace: %s\n", authResp.Team)
		fmt.Printf("  Mode:      %s\n", mode)
	}
	fmt.Println()

	notifyDaemonChannelsChanged(cfg)
	fmt.Println()

	return nil
}

// emailTestConfig holds the minimal config for testing an IMAP connection.
type emailTestConfig struct {
	IMAPServer string
	Address    string
	Username   string
	Password   string
}

// testIMAPConnection tests if we can connect and authenticate to the IMAP server.
func testIMAPConnection(cfg *emailTestConfig) error {
	emailCfg := &emailClientConfig{
		IMAPServer: cfg.IMAPServer,
		Address:    cfg.Address,
		Username:   cfg.Username,
		Password:   cfg.Password,
	}
	return testEmailConnection(emailCfg)
}

// emailClientConfig is a minimal config for creating a test email client.
type emailClientConfig struct {
	IMAPServer string
	Address    string
	Username   string
	Password   string
}

// testEmailConnection creates a temporary email client and tests the connection.
func testEmailConnection(cfg *emailClientConfig) error {
	// We import the email package in the setup function since it's only
	// used during setup, not at module load time.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	emailPkgCfg := &emailPkg.Config{
		Provider:   "imap",
		IMAPServer: cfg.IMAPServer,
		Address:    cfg.Address,
		Username:   cfg.Username,
		Password:   cfg.Password,
	}
	client, err := emailPkg.NewClient(emailPkgCfg)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Connect(ctx)
}

// handleChannelDisable disables a channel in the config.
func handleChannelDisable(channel string) error {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	switch channel {
	case "telegram":
		if !cfg.Channels.Telegram.IsTelegramEnabled() {
			fmt.Println("Telegram is not currently enabled.")
			return nil
		}
		disabled := false
		cfg.Channels.Telegram.Enabled = &disabled
		if err := config.SaveAppConfig(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Println("Telegram channel disabled.")
		notifyDaemonChannelsChanged(cfg)
	case "email":
		if !cfg.Channels.Email.IsEmailEnabled() {
			fmt.Println("Email is not currently enabled.")
			return nil
		}
		disabled := false
		cfg.Channels.Email.Enabled = &disabled
		if err := config.SaveAppConfig(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Println("Email channel disabled.")
		notifyDaemonChannelsChanged(cfg)
	case "slack":
		if !cfg.Channels.Slack.IsSlackEnabled() {
			fmt.Println("Slack is not currently enabled.")
			return nil
		}
		disabled := false
		cfg.Channels.Slack.Enabled = &disabled
		if err := config.SaveAppConfig(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Println("Slack channel disabled.")
		notifyDaemonChannelsChanged(cfg)
	default:
		return fmt.Errorf("unknown channel: %s (available: telegram, email, slack)", channel)
	}

	return nil
}

// notifyDaemonChannelsChanged tells the running daemon to reload channel
// configuration. It tries three strategies in order:
//  1. API reload (POST /api/channels/reload) — works for both "daemon run"
//     and "daemon start".
//  2. Service restart (systemctl/launchctl) — works only for installed services.
//  3. Manual instructions — printed when both fail.
func notifyDaemonChannelsChanged(appCfg *config.AppConfig) {
	port := appCfg.Daemon.GetPort()
	reloadURL := fmt.Sprintf("http://localhost:%d/api/channels/reload", port)

	// Strategy 1: API reload (fast, works for foreground and service mode)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(reloadURL, "application/json", nil)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Println("  Channels reloaded — changes are live!")
			fmt.Println()
			return
		}
		// Read error body for diagnostics
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<unreadable: %v>", readErr))
		}
		if len(body) > 0 {
			var errResp map[string]string
			if json.Unmarshal(body, &errResp) == nil {
				if msg, ok := errResp["error"]; ok {
					fmt.Printf("  \033[33mWarning: Reload returned: %s\033[0m\n", msg)
				}
			}
		}
	}

	// Strategy 2: Service restart (for daemon start / installed services)
	svc, svcErr := daemon.NewService()
	if svcErr == nil {
		if running, _ := svc.IsRunning(); running {
			fmt.Println("  Restarting daemon...")
			if restartErr := svc.Restart(); restartErr == nil {
				fmt.Println("  Daemon restarted — changes are live!")
				fmt.Println()
				return
			}
		}
	}

	// Strategy 3: Manual instructions
	fmt.Println("  Start the daemon to activate:")
	fmt.Println("    astonish daemon start")
	fmt.Println()
}

func handleChannelsStatus() error {
	// Load config to get daemon port
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	port := appCfg.Daemon.GetPort()
	url := fmt.Sprintf("http://localhost:%d/api/channels/status", port)

	resp, err := http.Get(url)
	if err != nil {
		// Daemon is not running — channels require the daemon
		fmt.Println()
		fmt.Println("  Daemon is not running. Channels require the daemon to operate.")
		fmt.Println()
		fmt.Println("  Start the daemon:")
		fmt.Println("    astonish daemon start")
		fmt.Println()
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		Channels map[string]struct {
			Connected    bool   `json:"connected"`
			AccountID    string `json:"account_id"`
			ConnectedAt  string `json:"connected_at"`
			Error        string `json:"error"`
			MessageCount int64  `json:"message_count"`
		} `json:"channels"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Channels) == 0 {
		fmt.Println()
		fmt.Println("  No channels configured.")
		fmt.Println()
		fmt.Println("  Set up a channel:")
		fmt.Println("    astonish channels setup telegram")
		fmt.Println()
		return nil
	}

	fmt.Println()
	fmt.Println("  Channel Status:")
	fmt.Println()

	for name, ch := range result.Channels {
		status := "\033[31mdisconnected\033[0m"
		if ch.Connected {
			status = "\033[32mconnected\033[0m"
		}

		fmt.Printf("    %-12s %s", name, status)
		if ch.AccountID != "" {
			fmt.Printf("  @%s", ch.AccountID)
		}
		if ch.Error != "" {
			fmt.Printf("  \033[31m(%s)\033[0m", ch.Error)
		}
		if ch.Connected {
			fmt.Printf("  — %d messages", ch.MessageCount)
		}
		fmt.Println()
	}
	fmt.Println()

	return nil
}

func printChannelsUsage() {
	fmt.Println("usage: astonish channels {status,setup,disable}")
	fmt.Println("")
	fmt.Println("Manage communication channels (Telegram, Email, Slack)")
	fmt.Println("")
	fmt.Println("subcommands:")
	fmt.Println("  status              Show status of all channels")
	fmt.Println("  setup <channel>     Interactive channel setup")
	fmt.Println("  disable <channel>   Disable a channel")
	fmt.Println("")
	fmt.Println("available channels:")
	fmt.Println("  telegram            Telegram bot integration")
	fmt.Println("  email               Email integration (IMAP/SMTP)")
	fmt.Println("  slack               Slack bot integration (Socket Mode / Events API)")
}

// maskToken returns the last 8 chars of a token, with the rest replaced by dots.
func maskToken(token string) string {
	if len(token) <= 8 {
		return "****"
	}
	return "..." + token[len(token)-8:]
}

// channelsPrintHeader prints a styled header.
func channelsPrintHeader(title string) {
	style := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("63")).
		Padding(0, 2)
	fmt.Println(style.Render(title))
}

// channelsRunSpinner prints a spinner-style status message.
func channelsRunSpinner(msg string) {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	fmt.Printf("  %s %s\n", style.Render("•"), msg)
}

// channelsPrintSuccess prints a styled success message in a box.
func channelsPrintSuccess(msg string) {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("42")).
		Bold(true).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("42"))
	fmt.Println(style.Render("✓ " + msg))
}
