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
	"context"
	"fmt"
	"log"
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
		config:   cfg,
		logger:   logger,
		allowSet: allowSet,
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
	api := s.getAPIForTarget(target)
	if api == nil {
		return fmt.Errorf("slack: no API client available for target %s", target.ChatID)
	}

	channelID := target.ChatID
	threadTS := target.ThreadID

	// Always reply in thread if we have a thread_ts (channel messages)
	if threadTS == "" && msg.ReplyTo != "" {
		threadTS = msg.ReplyTo
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

// RefreshCommands is a no-op for Slack — commands are registered in the Slack app config,
// not via API at runtime (unlike Telegram's setMyCommands).
func (s *SlackChannel) RefreshCommands(commands *channels.CommandRegistry) {}

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

	// Strip the @mention from the text
	text := s.stripBotMention(ev.Text)
	if text == "" {
		return
	}

	// Handle /link before allowlist check
	if strings.HasPrefix(text, "/link ") {
		s.handleLinkCommand(ctx, ev.User, s.getUserDisplayName(ctx, ev.User, teamID), strings.TrimPrefix(text, "/link "), ev.Channel, ev.TimeStamp)
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
	if strings.HasPrefix(text, "/link ") {
		s.handleLinkCommand(ctx, ev.User, s.getUserDisplayName(ctx, ev.User, teamID), strings.TrimPrefix(text, "/link "), ev.Channel, ev.TimeStamp)
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
	response := s.handleSlashCommand(ctx, cmd)
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
	if cmd.Command != "/link" {
		s.logger.Printf("[slack] Ignoring unsupported slash command %q from user %s", cmd.Command, cmd.UserID)
		return "This Slack command is not handled by Astonish. Use /link CODE to connect your Astonish account."
	}
	return s.linkAccount(ctx, cmd.UserID, s.displayNameForSlashCommand(ctx, cmd), cmd.Text)
}

// handleLinkCommand processes a /link CODE message.
func (s *SlackChannel) handleLinkCommand(ctx context.Context, userID, displayName, code, channelID, threadTS string) {
	s.sendReply(ctx, channelID, threadTS, s.linkAccount(ctx, userID, displayName, code))
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
