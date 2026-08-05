// Package slack implements the Slack channel adapter for Astonish.
// It connects to Slack via Socket Mode (WebSocket) or Events API (HTTP webhook),
// normalizes inbound messages, and delivers outbound responses with
// Slack mrkdwn formatting, message chunking, and thread-based replies.
//
// Multi-workspace support: When using Events API with OAuth, multiple Slack
// workspaces can install the app. Each workspace's bot token is stored in
// the slack_installations table and looked up by team_id on each event.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/channels"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Config holds configuration for the Slack channel adapter.
type Config struct {
	// Mode selects the transport: "socket" (default) or "events".
	Mode string

	// BotToken is the primary workspace bot token (xoxb-...).
	// Required for Socket Mode. For Events API multi-workspace, this is
	// the "default" workspace token (or empty if using only OAuth installs).
	BotToken string

	// AppToken is the app-level token (xapp-...) for Socket Mode.
	// Required only when Mode == "socket".
	AppToken string

	// SigningSecret is used to verify incoming HTTP requests in Events API mode.
	// Required only when Mode == "events".
	SigningSecret string

	// AppID is the Slack App ID used for App Manifest command synchronization.
	AppID string

	// ConfigToken is a Slack app configuration token used for App Manifest APIs.
	ConfigToken string

	// CommandURL is the HTTPS endpoint Slack should call for slash commands.
	CommandURL string

	// AllowFrom is a list of allowed Slack user IDs. Empty blocks all (safe default).
	// In platform mode, this is dynamically refreshed from user_channels.
	AllowFrom []string

	// Commands is the slash command registry shared across all channels.
	Commands *channels.CommandRegistry
}

// SlackChannel implements the channels.Channel interface for Slack.
type SlackChannel struct {
	config   *Config
	api      *slack.Client
	smClient *socketmode.Client // nil if Events API mode
	handler  channels.MessageHandler
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	logger   *log.Logger
	mu       sync.RWMutex
	status   channels.ChannelStatus
	msgCount atomic.Int64

	// botUserID is the bot's Slack user ID (e.g., "U0KRQLJ9H").
	// Used to detect @mentions and ignore the bot's own messages.
	botUserID string

	// allowSet is built from config.AllowFrom for fast lookup.
	allowMu  sync.RWMutex
	allowSet map[string]bool

	// workspaces holds per-workspace API clients (for multi-workspace mode).
	// Key: team_id. If nil, uses the single t.api client.
	workspaces   map[string]*slack.Client
	workspacesMu sync.RWMutex

	// LinkHandler is called when a user sends /link <code>.
	// Bridges the Slack channel with the platform link code store.
	LinkHandler func(ctx context.Context, senderID, senderName, code string) (bool, string)

	commandsMu sync.RWMutex
	commands   *channels.CommandRegistry

	responseURLsMu sync.Mutex
	responseURLs   map[string]string
}

// New creates a new Slack channel adapter.
func New(cfg *Config, logger *log.Logger) *SlackChannel {
	if logger == nil {
		logger = log.Default()
	}
	if cfg.Mode == "" {
		cfg.Mode = "socket"
	}

	allowSet := make(map[string]bool, len(cfg.AllowFrom))
	for _, id := range cfg.AllowFrom {
		allowSet[id] = true
	}

	return &SlackChannel{
		config:       cfg,
		logger:       logger,
		allowSet:     allowSet,
		commands:     cfg.Commands,
		responseURLs: make(map[string]string),
	}
}

// ID returns the channel identifier.
func (s *SlackChannel) ID() string { return "slack" }

// Name returns a human-readable name.
func (s *SlackChannel) Name() string { return "Slack Bot" }

// BotUsername returns the bot's Slack user ID.
func (s *SlackChannel) BotUsername() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.botUserID
}

// SetLinkHandler sets the callback for /link <code> commands.
func (s *SlackChannel) SetLinkHandler(fn func(ctx context.Context, senderID, senderName, code string) (bool, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LinkHandler = fn
}

// Start connects to Slack and begins processing events.
// In Socket Mode, it establishes a WebSocket connection.
// Blocks until ctx is cancelled or Stop is called.
func (s *SlackChannel) Start(ctx context.Context, handler channels.MessageHandler) error {
	s.handler = handler

	if s.config.Mode == "events" {
		return s.startEventsMode(ctx)
	}
	return s.startSocketMode(ctx)
}

// startSocketMode connects via WebSocket using the app-level token.
func (s *SlackChannel) startSocketMode(ctx context.Context) error {
	if s.config.BotToken == "" {
		s.setError("bot_token not configured")
		return fmt.Errorf("slack: bot_token not configured")
	}
	if s.config.AppToken == "" {
		s.setError("app_token not configured for socket mode")
		return fmt.Errorf("slack: app_token not configured for socket mode")
	}

	// Create the Slack API client with the bot token
	api := slack.New(
		s.config.BotToken,
		slack.OptionAppLevelToken(s.config.AppToken),
	)

	// Verify connection and get bot identity
	authResp, err := api.AuthTest()
	if err != nil {
		s.setError(fmt.Sprintf("auth failed: %v", err))
		return fmt.Errorf("slack: auth test failed: %w", err)
	}

	s.mu.Lock()
	s.api = api
	s.botUserID = authResp.UserID
	s.status = channels.ChannelStatus{
		Connected:   true,
		AccountID:   authResp.UserID,
		ConnectedAt: time.Now(),
	}
	s.mu.Unlock()

	s.logger.Printf("[slack] Connected as %s (user: %s, team: %s) via Socket Mode",
		authResp.User, authResp.UserID, authResp.TeamID)
	s.refreshCommandsBestEffort(ctx, s.currentCommands())

	// Create Socket Mode client
	smClient := socketmode.New(api)
	s.smClient = smClient

	// Create cancellable context
	pollCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Run Socket Mode event loop
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := smClient.RunContext(pollCtx); err != nil {
			if pollCtx.Err() == nil {
				s.logger.Printf("[slack] Socket Mode error: %v", err)
				s.setError(fmt.Sprintf("socket mode error: %v", err))
			}
		}
	}()

	// Process events
	s.wg.Add(1)
	defer s.wg.Done()

	for {
		select {
		case <-pollCtx.Done():
			s.logger.Printf("[slack] Event processing stopped")
			return nil

		case evt, ok := <-smClient.Events:
			if !ok {
				return nil
			}
			s.handleSocketModeEvent(ctx, evt)
		}
	}
}

// startEventsMode prepares the adapter for HTTP-based events.
// The actual HTTP handler is registered externally via EventsHTTPHandler().
// This method just blocks until context is cancelled.
func (s *SlackChannel) startEventsMode(ctx context.Context) error {
	// In Events API mode, we may have a default workspace token
	if s.config.BotToken != "" {
		api := slack.New(s.config.BotToken)
		authResp, err := api.AuthTest()
		if err != nil {
			s.logger.Printf("[slack] Events mode: default bot token auth failed: %v (will rely on OAuth installs)", err)
		} else {
			s.mu.Lock()
			s.api = api
			s.botUserID = authResp.UserID
			s.status = channels.ChannelStatus{
				Connected:   true,
				AccountID:   authResp.UserID,
				ConnectedAt: time.Now(),
			}
			s.mu.Unlock()
			s.logger.Printf("[slack] Events mode: default workspace connected as %s (team: %s)", authResp.User, authResp.TeamID)
		}
	}

	if s.status.AccountID == "" {
		s.mu.Lock()
		s.status = channels.ChannelStatus{
			Connected:   true,
			AccountID:   "events-api",
			ConnectedAt: time.Now(),
		}
		s.mu.Unlock()
	}

	s.logger.Printf("[slack] Events API mode active (HTTP handler ready)")
	s.refreshCommandsBestEffort(ctx, s.currentCommands())

	// Block until cancelled
	pollCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	s.wg.Add(1)
	defer s.wg.Done()

	<-pollCtx.Done()
	return nil
}

// Stop gracefully shuts down the Slack adapter.
func (s *SlackChannel) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}

	// Wait for event processing to finish
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		s.logger.Printf("[slack] Forced stop (context deadline)")
	}

	s.mu.Lock()
	s.status.Connected = false
	s.mu.Unlock()

	s.logger.Printf("[slack] Stopped")
	return nil
}

// Send delivers an outbound message to a Slack channel or DM.
// In channels, always replies in a thread. In DMs, posts inline.
func (s *SlackChannel) Send(ctx context.Context, target channels.Target, msg channels.OutboundMessage) error {
	channelID := target.ChatID
	threadTS := slackThreadTimestamp(target.ThreadID, msg.ReplyTo)
	if responseURL := s.takeResponseURL(msg.ReplyTo); responseURL != "" && threadTS == "" {
		if err := s.sendViaResponseURL(ctx, responseURL, msg); err != nil {
			return err
		}
		if len(msg.Images) == 0 && len(msg.Documents) == 0 {
			return nil
		}
	}

	api := s.getAPIForTarget(target)
	if api == nil {
		return fmt.Errorf("slack: no API client available for target %s", target.ChatID)
	}

	// --- Phase 1: Send text ---
	for _, rendered := range renderOutboundMessage(msg) {
		opts := []slack.MsgOption{
			slack.MsgOptionText(rendered.Text, false),
		}
		if len(rendered.Blocks) > 0 {
			opts = append(opts, slack.MsgOptionBlocks(rendered.Blocks...))
		}
		if threadTS != "" {
			opts = append(opts, slack.MsgOptionTS(threadTS))
		}

		_, _, err := api.PostMessageContext(ctx, channelID, opts...)
		if err != nil {
			return fmt.Errorf("slack: send failed: %w", err)
		}
	}

	// --- Phase 2: Send images as file uploads ---
	for _, img := range msg.Images {
		ext := img.Format
		if ext == "" {
			ext = "png"
		}
		filename := fmt.Sprintf("image.%s", ext)
		title := img.Caption
		if title == "" {
			title = filename
		}

		params := slack.UploadFileParameters{
			Channel:         channelID,
			Reader:          strings.NewReader(string(img.Data)),
			Filename:        filename,
			Title:           title,
			FileSize:        len(img.Data),
			ThreadTimestamp: threadTS,
		}

		if _, err := api.UploadFileContext(ctx, params); err != nil {
			s.logger.Printf("[slack] Failed to upload image: %v", err)
			// Non-fatal
		}
	}

	// --- Phase 3: Send document attachments ---
	for _, doc := range msg.Documents {
		if len(doc.Data) == 0 {
			continue
		}
		filename := doc.Filename
		if filename == "" {
			filename = "file"
		}

		params := slack.UploadFileParameters{
			Channel:         channelID,
			Reader:          strings.NewReader(string(doc.Data)),
			Filename:        filename,
			Title:           doc.Caption,
			FileSize:        len(doc.Data),
			ThreadTimestamp: threadTS,
		}

		if _, err := api.UploadFileContext(ctx, params); err != nil {
			s.logger.Printf("[slack] Failed to upload document %s: %v", filename, err)
			// Non-fatal
		}
	}

	return nil
}

// SendTyping sends a typing indicator. Slack doesn't well support bot typing
// indicators, so this is a no-op.
func (s *SlackChannel) SendTyping(ctx context.Context, target channels.Target) error {
	return nil
}

// Status returns the current channel status.
func (s *SlackChannel) Status() channels.ChannelStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.status
	status.MessageCount = s.msgCount.Load()
	return status
}

// BroadcastTargets returns a Target for each allowed user (DM delivery).
func (s *SlackChannel) BroadcastTargets() []channels.Target {
	s.allowMu.RLock()
	defer s.allowMu.RUnlock()
	targets := make([]channels.Target, 0, len(s.allowSet))
	for id := range s.allowSet {
		targets = append(targets, channels.Target{
			ChannelID: "slack",
			ChatID:    id,
		})
	}
	return targets
}

// UpdateAllowlist replaces the sender allowlist at runtime.
// Implements the channels.AllowlistUpdater interface.
func (s *SlackChannel) UpdateAllowlist(allowFrom []string) {
	newSet := make(map[string]bool, len(allowFrom))
	for _, id := range allowFrom {
		newSet[id] = true
	}
	s.allowMu.Lock()
	s.allowSet = newSet
	s.allowMu.Unlock()
}

// RefreshCommands implements channels.CommandRefresher. It projects Astonish's
// shared command registry into Slack's App Manifest when manifest credentials are configured.
func (s *SlackChannel) RefreshCommands(commands *channels.CommandRegistry) {
	s.commandsMu.Lock()
	s.commands = commands
	s.commandsMu.Unlock()
	s.refreshCommandsBestEffort(context.Background(), commands)
}

func (s *SlackChannel) currentCommands() *channels.CommandRegistry {
	s.commandsMu.RLock()
	defer s.commandsMu.RUnlock()
	return s.commands
}

// --- Socket Mode event handling ---

// handleSocketModeEvent processes a single Socket Mode event envelope.
func (s *SlackChannel) handleSocketModeEvent(ctx context.Context, evt socketmode.Event) {
	s.logger.Printf("[slack] Socket Mode envelope received: type=%s", evt.Type)

	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		// Acknowledge the event
		s.smClient.Ack(*evt.Request)
		// Process the inner event
		s.handleEventsAPIEvent(ctx, eventsAPIEvent, "")

	case socketmode.EventTypeSlashCommand:
		cmd, ok := evt.Data.(slack.SlashCommand)
		if !ok {
			if evt.Request != nil {
				s.smClient.Ack(*evt.Request)
			}
			return
		}
		s.handleSocketModeSlashCommand(ctx, evt, cmd)

	case socketmode.EventTypeConnecting:
		s.logger.Printf("[slack] Socket Mode connecting...")

	case socketmode.EventTypeConnected:
		s.logger.Printf("[slack] Socket Mode connected")

	case socketmode.EventTypeDisconnect:
		s.logger.Printf("[slack] Socket Mode disconnected (will reconnect)")

	default:
		// Acknowledge unknown events to prevent retries
		if evt.Request != nil {
			s.smClient.Ack(*evt.Request)
		}
	}
}

// handleEventsAPIEvent processes a Slack Events API event (shared between
// Socket Mode and HTTP Events API).
func (s *SlackChannel) handleEventsAPIEvent(ctx context.Context, event slackevents.EventsAPIEvent, teamID string) {
	if teamID == "" {
		teamID = event.TeamID
	}

	s.logger.Printf("[slack] Events API event received: outer_type=%s inner_type=%s team=%s", event.Type, event.InnerEvent.Type, teamID)

	switch event.Type {
	case slackevents.CallbackEvent:
		s.handleCallbackEvent(ctx, event.InnerEvent, teamID)
	default:
		s.logger.Printf("[slack] Ignored Events API outer event type %q", event.Type)
	}
}

// handleCallbackEvent dispatches inner events (messages, app_mention).
func (s *SlackChannel) handleCallbackEvent(ctx context.Context, innerEvent slackevents.EventsAPIInnerEvent, teamID string) {
	switch ev := innerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		s.handleAppMention(ctx, ev, teamID)
	case *slackevents.MessageEvent:
		s.handleMessage(ctx, ev, teamID)
	default:
		s.logger.Printf("[slack] Ignored Events API inner event type %q (%T)", innerEvent.Type, innerEvent.Data)
	}
}

// handleAppMention processes an @mention of the bot in a channel.
func (s *SlackChannel) handleAppMention(ctx context.Context, ev *slackevents.AppMentionEvent, teamID string) {
	// Ignore bot's own messages
	if ev.User == s.botUserID {
		return
	}
	// Ignore bot messages (from integrations)
	if ev.BotID != "" {
		return
	}

	// Strip the @mention from the text. Slack custom slash commands do not work in
	// thread composers, so app mentions are the supported thread command entrypoint:
	// "@Astonish status" and "@Astonish /astonish-status" both become "/status".
	text := s.normalizeMentionCommandText(s.stripBotMention(ev.Text))
	if text == "" {
		return
	}

	// Handle /link before allowlist check
	if isSlashCommandText(text, "link") {
		s.handleLinkCommand(ctx, ev.User, s.getUserDisplayName(ctx, ev.User, teamID), slashCommandArgs(text, "link"), ev.Channel, ev.TimeStamp)
		return
	}

	// Allowlist check
	if !s.isAllowed(ev.User) {
		s.logger.Printf("[slack] Blocked @mention from unauthorized user %s", ev.User)
		return
	}

	s.msgCount.Add(1)

	// Determine thread — in channels, always reply in thread
	threadTS := ev.ThreadTimeStamp
	if threadTS == "" {
		threadTS = ev.TimeStamp // Start a new thread from this message
	}

	inbound := channels.InboundMessage{
		ID:         ev.TimeStamp,
		ChannelID:  "slack",
		SenderID:   ev.User,
		SenderName: s.getUserDisplayName(ctx, ev.User, teamID),
		ChatID:     ev.Channel,
		ChatType:   channels.ChatTypeChannel,
		Text:       text,
		ThreadID:   threadTS,
		Timestamp:  tsToTime(ev.TimeStamp),
		Raw:        ev,
	}

	if err := s.handler(ctx, inbound); err != nil {
		s.logger.Printf("[slack] Handler error for mention %s: %v", ev.TimeStamp, err)
	}
}

// handleMessage processes a DM (message.im) event.
func (s *SlackChannel) handleMessage(ctx context.Context, ev *slackevents.MessageEvent, teamID string) {
	// Only handle DMs — channel messages come via app_mention. Some Slack
	// payloads omit channel_type, so accept DM channel IDs as a fallback.
	if !isDirectMessageEvent(ev) {
		s.logger.Printf("[slack] Ignored non-DM message event from user %s in channel %s (channel_type=%q)", ev.User, ev.Channel, ev.ChannelType)
		return
	}

	// Ignore bot's own messages
	if ev.User == s.botUserID {
		return
	}
	// Ignore bot messages
	if ev.BotID != "" {
		return
	}
	// Ignore message subtypes (edits, deletes, etc.), but keep Slack App Agent
	// assistant-thread messages: those may carry the user's normal app DM text.
	if ev.SubType != "" && ev.SubType != slack.MsgSubTypeAssistantAppThread {
		s.logger.Printf("[slack] Ignored DM subtype %q from user %s in channel %s", ev.SubType, ev.User, ev.Channel)
		return
	}

	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return
	}

	// Handle /link before allowlist check
	if isSlashCommandText(text, "link") {
		s.handleLinkCommand(ctx, ev.User, s.getUserDisplayName(ctx, ev.User, teamID), slashCommandArgs(text, "link"), ev.Channel, ev.TimeStamp)
		return
	}

	// Allowlist check
	if !s.isAllowed(ev.User) {
		s.logger.Printf("[slack] Blocked DM from unauthorized user %s", ev.User)
		return
	}

	s.msgCount.Add(1)

	threadTS := ev.ThreadTimeStamp
	if threadTS == "" {
		threadTS = ev.TimeStamp
	}

	inbound := channels.InboundMessage{
		ID:         ev.TimeStamp,
		ChannelID:  "slack",
		SenderID:   ev.User,
		SenderName: s.getUserDisplayName(ctx, ev.User, teamID),
		ChatID:     ev.Channel,
		ChatType:   channels.ChatTypeDirect,
		Text:       text,
		ThreadID:   threadTS,
		Timestamp:  tsToTime(ev.TimeStamp),
		Raw:        ev,
	}

	if err := s.handler(ctx, inbound); err != nil {
		s.logger.Printf("[slack] Handler error for DM %s: %v", ev.TimeStamp, err)
	}
}

// handleSocketModeSlashCommand processes Slack slash commands delivered over Socket Mode.
func (s *SlackChannel) handleSocketModeSlashCommand(ctx context.Context, evt socketmode.Event, cmd slack.SlashCommand) {
	threadTS := slashCommandThreadTSFromSocketRequest(evt.Request)
	if threadTS == "" {
		s.logger.Printf("[slack] Slash command %s from %s has no thread_ts in Socket Mode payload; Slack may not provide thread context for slash commands", cmd.Command, cmd.UserID)
	} else {
		s.logger.Printf("[slack] Slash command %s from %s routed to thread %s", cmd.Command, cmd.UserID, threadTS)
	}
	response := s.handleSlashCommandWithThread(ctx, cmd, threadTS)
	if evt.Request != nil {
		if err := s.smClient.Ack(*evt.Request, map[string]string{"response_type": "ephemeral", "text": response}); err != nil {
			s.logger.Printf("[slack] Failed to acknowledge slash command %s: %v", cmd.Command, err)
		}
	}
}

// HandleSlashCommand processes a Slack slash-command HTTP request and returns the text
// Slack should show to the invoking user.
func (s *SlackChannel) HandleSlashCommand(ctx context.Context, cmd slack.SlashCommand) string {
	return s.handleSlashCommand(ctx, cmd)
}

func (s *SlackChannel) handleSlashCommand(ctx context.Context, cmd slack.SlashCommand) string {
	return s.handleSlashCommandWithThread(ctx, cmd, "")
}

func (s *SlackChannel) handleSlashCommandWithThread(ctx context.Context, cmd slack.SlashCommand, threadTS string) string {
	if cmd.Command == "/link" {
		return s.linkAccount(ctx, cmd.UserID, s.displayNameForSlashCommand(ctx, cmd), cmd.Text)
	}

	registry := s.currentCommands()
	name := slackCommandNameFromInvocation(cmd.Command, registry)
	registeredCommand := (*channels.Command)(nil)
	if registry != nil {
		registeredCommand = registry.Lookup(name)
	}
	if !shouldExposeSlackCommand(registeredCommand) {
		s.logger.Printf("[slack] Ignoring unsupported slash command %q from user %s", cmd.Command, cmd.UserID)
		return "This Slack command is not handled by Astonish. Use /astonish-help for available commands or /link CODE to connect your Astonish account."
	}
	if !s.isAllowed(cmd.UserID) {
		s.logger.Printf("[slack] Blocked slash command %q from unauthorized user %s", cmd.Command, cmd.UserID)
		return "Please link your Astonish account first with /link CODE. Get a link code from Astonish Settings → Channels."
	}
	if s.handler == nil {
		return "Astonish is not ready to process Slack commands yet. Please try again in a moment."
	}

	canonicalCommand := "/" + name
	text := strings.TrimSpace(strings.Join([]string{canonicalCommand, cmd.Text}, " "))
	inboundID := firstNonEmpty(cmd.TriggerID, cmd.Command+":"+cmd.UserID)
	s.storeResponseURL(inboundID, cmd.ResponseURL)
	inbound := channels.InboundMessage{
		ID:         inboundID,
		ChannelID:  "slack",
		SenderID:   cmd.UserID,
		SenderName: s.displayNameForSlashCommand(ctx, cmd),
		ChatID:     cmd.ChannelID,
		ChatType:   slackCommandChatType(cmd),
		Text:       text,
		ThreadID:   slackThreadTimestamp(threadTS, ""),
		Timestamp:  time.Now(),
		Raw:        cmd,
	}
	go func() {
		if err := s.handler(ctx, inbound); err != nil {
			s.logger.Printf("[slack] Handler error for slash command %s from %s: %v", cmd.Command, cmd.UserID, err)
		}
	}()
	return "Running " + cmd.Command + "…"
}

// handleLinkCommand processes a /link CODE message.
func (s *SlackChannel) handleLinkCommand(ctx context.Context, userID, displayName, code, channelID, threadTS string) {
	s.sendReply(ctx, channelID, threadTS, s.linkAccount(ctx, userID, displayName, code))
}

const slackResponseURLTTL = 30 * time.Minute

type slackResponseURLPayload struct {
	ResponseType string        `json:"response_type,omitempty"`
	Text         string        `json:"text,omitempty"`
	Blocks       []slack.Block `json:"blocks,omitempty"`
}

func (s *SlackChannel) storeResponseURL(messageID, responseURL string) {
	messageID = strings.TrimSpace(messageID)
	responseURL = strings.TrimSpace(responseURL)
	if messageID == "" || responseURL == "" {
		return
	}

	s.responseURLsMu.Lock()
	if s.responseURLs == nil {
		s.responseURLs = make(map[string]string)
	}
	s.responseURLs[messageID] = responseURL
	s.responseURLsMu.Unlock()

	time.AfterFunc(slackResponseURLTTL, func() {
		s.responseURLsMu.Lock()
		if s.responseURLs[messageID] == responseURL {
			delete(s.responseURLs, messageID)
		}
		s.responseURLsMu.Unlock()
	})
}

func (s *SlackChannel) takeResponseURL(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return ""
	}

	s.responseURLsMu.Lock()
	defer s.responseURLsMu.Unlock()
	responseURL := s.responseURLs[messageID]
	delete(s.responseURLs, messageID)
	return responseURL
}

func (s *SlackChannel) sendViaResponseURL(ctx context.Context, responseURL string, msg channels.OutboundMessage) error {
	renderedMessages := renderOutboundMessage(msg)
	if len(renderedMessages) == 0 {
		return nil
	}

	for _, rendered := range renderedMessages {
		payload := slackResponseURLPayload{
			ResponseType: "ephemeral",
			Text:         rendered.Text,
			Blocks:       rendered.Blocks,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("slack: response_url marshal failed: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("slack: response_url request failed: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("slack: response_url send failed: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			s.logger.Printf("[slack] Failed to close response_url response body: %v", closeErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("slack: response_url send failed: status %d", resp.StatusCode)
		}
	}
	return nil
}

func (s *SlackChannel) linkAccount(ctx context.Context, userID, displayName, code string) string {
	code = strings.TrimSpace(code)

	s.mu.RLock()
	linkHandler := s.LinkHandler
	s.mu.RUnlock()

	if linkHandler == nil {
		return "Account linking is not available."
	}

	if code == "" {
		return "Usage: /link CODE\n\nGet a link code from the Astonish Settings → Channels page."
	}

	success, msg := linkHandler(ctx, userID, displayName, code)

	// If successful, add to allowlist immediately
	if success {
		s.allowMu.Lock()
		s.allowSet[userID] = true
		s.allowMu.Unlock()
	}

	return msg
}

// --- Helpers ---

// stripBotMention removes the <@BOTID> mention from message text.
func (s *SlackChannel) stripBotMention(text string) string {
	mention := fmt.Sprintf("<@%s>", s.botUserID)
	text = strings.Replace(text, mention, "", 1)
	return strings.TrimSpace(text)
}

func (s *SlackChannel) normalizeMentionCommandText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	registry := s.currentCommands()
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return text
	}
	first := strings.TrimSpace(fields[0])
	if first == "" {
		return text
	}

	candidate := "/" + strings.TrimPrefix(first, "/")
	name := slackCommandNameFromInvocation(candidate, registry)
	if registry == nil || registry.Lookup(name) == nil {
		return text
	}

	fields[0] = "/" + name
	return strings.Join(fields, " ")
}

// isAllowed checks if a user ID is in the allowlist.
func (s *SlackChannel) isAllowed(userID string) bool {
	s.allowMu.RLock()
	defer s.allowMu.RUnlock()
	return s.allowSet[userID]
}

// getUserDisplayName fetches a user's display name from the Slack API.
// Falls back to the user ID if the lookup fails.
func (s *SlackChannel) getUserDisplayName(ctx context.Context, userID, teamID string) string {
	api := s.getAPIForTeam(teamID)
	if api == nil {
		return userID
	}

	user, err := api.GetUserInfoContext(ctx, userID)
	if err != nil {
		return userID
	}

	return slackUserDisplayName(user, userID)
}

func (s *SlackChannel) displayNameForSlashCommand(ctx context.Context, cmd slack.SlashCommand) string {
	if name := s.getUserDisplayName(ctx, cmd.UserID, cmd.TeamID); name != "" && name != cmd.UserID {
		return name
	}
	if cmd.UserName != "" {
		return cmd.UserName
	}
	return cmd.UserID
}

func isDirectMessageEvent(ev *slackevents.MessageEvent) bool {
	if ev == nil {
		return false
	}
	return ev.ChannelType == "im" || (ev.ChannelType == "" && strings.HasPrefix(ev.Channel, "D"))
}

func slackThreadTimestamp(threadID, replyTo string) string {
	if isSlackTimestamp(threadID) {
		return threadID
	}
	if isSlackTimestamp(replyTo) {
		return replyTo
	}
	return ""
}

func slashCommandThreadTSFromSocketRequest(req *socketmode.Request) string {
	if req == nil || len(req.Payload) == 0 {
		return ""
	}
	var payload struct {
		ThreadTS string `json:"thread_ts"`
		Message  struct {
			ThreadTS string `json:"thread_ts"`
			TS       string `json:"ts"`
		} `json:"message"`
		Container struct {
			ThreadTS  string `json:"thread_ts"`
			MessageTS string `json:"message_ts"`
		} `json:"container"`
	}
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return ""
	}
	return slackThreadTimestamp(
		firstNonEmpty(payload.ThreadTS, payload.Message.ThreadTS, payload.Container.ThreadTS),
		firstNonEmpty(payload.Message.TS, payload.Container.MessageTS),
	)
}

func isSlackTimestamp(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func slackCommandNameFromInvocation(command string, registry *channels.CommandRegistry) string {
	name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(command)), "/")
	prefix := slackCommandPrefix + "-"
	if !strings.HasPrefix(name, prefix) {
		return name
	}
	aliasSuffix := strings.TrimPrefix(name, prefix)
	if registry != nil {
		for _, cmd := range registry.List() {
			if cmd == nil {
				continue
			}
			if slackCommandAliasSuffix(cmd.Name) == aliasSuffix || strings.TrimPrefix(legacySlackCommandAlias(cmd.Name), "/"+slackCommandPrefix+"-") == aliasSuffix {
				return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cmd.Name)), "/")
			}
		}
	}
	return aliasSuffix
}

func isSlashCommandText(text, name string) bool {
	text = strings.TrimSpace(text)
	prefix := "/" + name
	return text == prefix || strings.HasPrefix(text, prefix+" ")
}

func slashCommandArgs(text, name string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "/"+name))
}

func slackCommandChatType(cmd slack.SlashCommand) channels.ChatType {
	if strings.HasPrefix(cmd.ChannelID, "D") || strings.EqualFold(cmd.ChannelName, "directmessage") {
		return channels.ChatTypeDirect
	}
	return channels.ChatTypeChannel
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return fmt.Sprintf("slack-command-%d", time.Now().UnixNano())
}

func slackUserDisplayName(user *slack.User, fallback string) string {
	if user == nil {
		return fallback
	}
	name := user.Profile.DisplayName
	if name == "" {
		name = user.Profile.RealName
	}
	if name == "" {
		name = user.Name
	}
	if name == "" {
		name = fallback
	}
	return name
}

// sendReply sends a simple text reply in a channel/thread.
func (s *SlackChannel) sendReply(ctx context.Context, channelID, threadTS, text string) {
	api := s.getAPIForTarget(channels.Target{ChatID: channelID})
	if api == nil {
		return
	}

	opts := []slack.MsgOption{
		slack.MsgOptionText(text, false),
	}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	if _, _, err := api.PostMessageContext(ctx, channelID, opts...); err != nil {
		s.logger.Printf("[slack] Failed to send reply: %v", err)
	}
}

// getAPIForTarget returns the appropriate API client for a target.
// For multi-workspace, looks up by team_id embedded in the target.
func (s *SlackChannel) getAPIForTarget(target channels.Target) *slack.Client {
	// For now, use the default client.
	// Multi-workspace support will look up by team_id.
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.api
}

// getAPIForTeam returns the API client for a specific workspace.
func (s *SlackChannel) getAPIForTeam(teamID string) *slack.Client {
	if teamID != "" {
		s.workspacesMu.RLock()
		if s.workspaces != nil {
			if api, ok := s.workspaces[teamID]; ok {
				s.workspacesMu.RUnlock()
				return api
			}
		}
		s.workspacesMu.RUnlock()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.api
}

// RegisterWorkspace adds or updates a workspace's API client.
// Used by the OAuth callback to register newly installed workspaces.
func (s *SlackChannel) RegisterWorkspace(teamID, botToken, botUserID string) {
	api := slack.New(botToken)
	s.workspacesMu.Lock()
	if s.workspaces == nil {
		s.workspaces = make(map[string]*slack.Client)
	}
	s.workspaces[teamID] = api
	s.workspacesMu.Unlock()

	s.logger.Printf("[slack] Registered workspace %s (bot: %s)", teamID, botUserID)
}

// UnregisterWorkspace removes a workspace's API client.
// Used when the app is uninstalled from a workspace.
func (s *SlackChannel) UnregisterWorkspace(teamID string) {
	s.workspacesMu.Lock()
	delete(s.workspaces, teamID)
	s.workspacesMu.Unlock()

	s.logger.Printf("[slack] Unregistered workspace %s", teamID)
}

// setError updates the status with an error message.
func (s *SlackChannel) setError(errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Error = errMsg
	s.status.Connected = false
}

// tsToTime converts a Slack timestamp (e.g., "1469470591.759709") to time.Time.
func tsToTime(ts string) time.Time {
	if ts == "" {
		return time.Now()
	}
	// Slack timestamps are unix seconds with a dot separator
	parts := strings.SplitN(ts, ".", 2)
	if len(parts) == 0 {
		return time.Now()
	}
	var sec int64
	for _, c := range parts[0] {
		if c >= '0' && c <= '9' {
			sec = sec*10 + int64(c-'0')
		}
	}
	if sec == 0 {
		return time.Now()
	}
	return time.Unix(sec, 0)
}
