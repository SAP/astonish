// Package channels defines the core interfaces and types for Astonish's
// communication channel system. Channels are plugins that receive inbound
// messages from external platforms (Telegram, Slack, etc.), normalize them
// into a common format, route them to the ChatAgent, and deliver responses
// back to the originating platform.
package channels

import (
	"context"
	"time"
)

// ChatType represents the kind of chat a message originates from.
type ChatType string

const (
	ChatTypeDirect  ChatType = "direct"
	ChatTypeGroup   ChatType = "group"
	ChatTypeChannel ChatType = "channel"
)

// MessageFormat describes the formatting of outbound message text.
type MessageFormat string

const (
	FormatText     MessageFormat = "text"
	FormatMarkdown MessageFormat = "markdown"
	FormatHTML     MessageFormat = "html"
)

// Channel is the interface that all channel adapters must implement.
// Each adapter handles platform-specific connection, message normalization,
// and outbound delivery.
type Channel interface {
	// ID returns a unique identifier for this channel (e.g., "telegram").
	ID() string

	// Name returns a human-readable name (e.g., "Telegram Bot").
	Name() string

	// Start begins listening for inbound messages. The handler is called
	// for each normalized inbound message. Start blocks until ctx is
	// cancelled or Stop is called.
	Start(ctx context.Context, handler MessageHandler) error

	// Stop gracefully shuts down the channel, draining in-flight messages.
	Stop(ctx context.Context) error

	// Send delivers an outbound message to the specified target.
	Send(ctx context.Context, target Target, msg OutboundMessage) error

	// BroadcastTargets returns all targets this channel can deliver to.
	// For example, Telegram returns one Target per allowed user (in direct
	// messages, chat ID == user ID). Used by the scheduler to broadcast
	// job results to all active recipients without needing per-job targeting.
	BroadcastTargets() []Target

	// SendTyping sends a "typing" indicator to the specified target.
	// Typing indicators are ephemeral — on Telegram they last ~5 seconds.
	// Callers should call this repeatedly for long-running operations.
	// Channels that don't support typing indicators should return nil.
	SendTyping(ctx context.Context, target Target) error

	// Status returns the current connection and operational status.
	Status() ChannelStatus
}

// MessageHandler is the callback invoked by a channel for each inbound message.
// The handler is responsible for routing the message to the appropriate agent
// and sending the response back via Channel.Send.
type MessageHandler func(ctx context.Context, msg InboundMessage) error

// InboundMessage is a platform-normalized inbound message.
type InboundMessage struct {
	ID          string       // Platform message ID
	MessageID   string       // RFC 2822 Message-ID header (email only, for thread tracking)
	ChannelID   string       // Channel adapter ID (e.g., "telegram")
	SenderID    string       // Normalized sender identifier
	SenderName  string       // Human-readable sender name
	ChatID      string       // Normalized chat/conversation ID
	ChatType    ChatType     // "direct", "group", "channel"
	Text        string       // Message text content
	ReplyTo     string       // ID of message being replied to (empty if not a reply)
	ThreadID    string       // Thread/topic ID (empty if not threaded)
	Timestamp   time.Time    // When the message was sent
	Raw         any          // Platform-specific raw message (for advanced use)
	RoutingHint *RoutingHint // Optional per-message org/team routing override (email +addressing)
}

// RoutingHint provides a per-message org/team routing override.
// Used by email plus-addressing (e.g., bot+org+team@gmail.com) and
// potentially other per-message routing mechanisms.
type RoutingHint struct {
	OrgSlug  string // Target org slug (required)
	TeamSlug string // Target team slug (optional — defaults to first team)
}

// OutboundMessage is the response to be delivered back to a channel.
type OutboundMessage struct {
	Text      string               // Response text
	ReplyTo   string               // ID of message to reply to
	ThreadID  string               // Thread/topic ID to post in
	Format    MessageFormat        // "text" or "markdown"
	Images    []ImageAttachment    // Optional image attachments (e.g., screenshots)
	Documents []DocumentAttachment // Optional document attachments (e.g., generated files)
}

// ImageAttachment is a binary image to send as part of an outbound message.
// Channels that support media (Telegram, Discord) will send these as photos.
// Channels that don't support media will ignore them.
type ImageAttachment struct {
	Data    []byte // Raw image bytes (decoded from base64)
	Format  string // Image format: "png" or "jpeg"
	Caption string // Optional caption (Telegram supports up to 1024 chars)
}

// DocumentAttachment is a binary file to send as part of an outbound message.
// Channels that support file uploads (Telegram, Discord) will send these as
// documents. Channels that don't support file uploads will ignore them.
type DocumentAttachment struct {
	Data     []byte // Raw file bytes
	Filename string // Display filename (e.g., "report.md")
	Caption  string // Optional caption
}

// Target identifies where to send an outbound message.
type Target struct {
	ChannelID string // Channel adapter ID (e.g., "telegram")
	ChatID    string // Chat/conversation to send to
	ThreadID  string // Thread/topic to post in (optional)
}

// ChannelStatus reports the operational state of a channel.
type ChannelStatus struct {
	Connected    bool      // Whether the channel is currently connected
	AccountID    string    // Bot/account identifier on the platform
	ConnectedAt  time.Time // When the connection was established
	Error        string    // Last error message (empty if healthy)
	MessageCount int64     // Total messages processed since start
}

// AllowlistUpdater is an optional interface that channels can implement to
// update their sender allowlist at runtime without requiring a full restart.
// This is called when config.yaml changes and only the allow_from list differs.
type AllowlistUpdater interface {
	UpdateAllowlist(allowFrom []string)
}

// CommandRefresher is an optional interface that channels can implement to
// re-register slash commands with the platform (e.g., Telegram's setMyCommands).
// This is called when new commands are added after the channel has started.
type CommandRefresher interface {
	RefreshCommands(commands *CommandRegistry)
}

// CommandPresenter is an optional interface for channels whose user-facing
// command syntax differs from the shared registry names.
type CommandPresenter interface {
	FormatCommandName(name string) string
	ShouldExposeCommand(cmd *Command) bool
}
