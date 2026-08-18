package a2achan

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/channels"
)

// Config holds configuration for the A2A channel adapter.
type Config struct {
	TaskStore    a2a.TaskStore
	PushNotifier *a2a.PushNotifier
	BaseURL      string
}

// A2AChannel implements channels.Channel for the A2A protocol.
// Unlike other channel adapters (Telegram, Slack, Email), this adapter:
// - Handles multiple external agents through a single instance
// - Is HTTP-driven (no polling loop)
// - Supports identity propagation (acting on behalf of users)
type A2AChannel struct {
	config   *Config
	handler  channels.MessageHandler
	logger   *log.Logger
	msgCount atomic.Int64

	mu        sync.RWMutex
	connected bool
	connAt    time.Time

	// pendingResponses maps taskID -> response channel.
	// When the ChannelManager calls Send(), the response is written here
	// so the HTTP handler can return it to the caller.
	pendingMu        sync.Mutex
	pendingResponses map[string]chan channels.OutboundMessage
}

// New creates a new A2A channel adapter.
func New(cfg *Config, logger *log.Logger) *A2AChannel {
	if logger == nil {
		logger = log.Default()
	}
	if cfg.PushNotifier == nil {
		cfg.PushNotifier = a2a.NewPushNotifier(logger)
	}
	return &A2AChannel{
		config:           cfg,
		logger:           logger,
		pendingResponses: make(map[string]chan channels.OutboundMessage),
	}
}

// ID returns the channel identifier.
func (c *A2AChannel) ID() string { return "a2a" }

// Name returns a human-readable name.
func (c *A2AChannel) Name() string { return "A2A Protocol" }

// Start stores the message handler and marks the channel as connected.
// Unlike polling-based channels, A2A is HTTP-driven so Start returns immediately.
func (c *A2AChannel) Start(ctx context.Context, handler channels.MessageHandler) error {
	c.handler = handler
	c.mu.Lock()
	c.connected = true
	c.connAt = time.Now()
	c.mu.Unlock()
	c.logger.Printf("[a2a] Channel started (HTTP-driven, multi-client)")
	return nil
}

// Stop marks the channel as disconnected.
func (c *A2AChannel) Stop(ctx context.Context) error {
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
	c.logger.Printf("[a2a] Channel stopped")
	return nil
}

// Send delivers an outbound message. This is called by ChannelManager after
// the ChatAgent produces a response. The target.ThreadID contains the taskId,
// which is used to route the response to the waiting HTTP handler.
func (c *A2AChannel) Send(ctx context.Context, target channels.Target, msg channels.OutboundMessage) error {
	taskID := target.ThreadID
	if taskID == "" {
		return nil
	}

	c.msgCount.Add(1)

	// Write response to the pending channel so the HTTP handler can return it
	c.pendingMu.Lock()
	ch, ok := c.pendingResponses[taskID]
	c.pendingMu.Unlock()

	if ok {
		select {
		case ch <- msg:
		default:
			c.logger.Printf("[a2a] Warning: response channel full for task %s", taskID)
		}
	}

	// Also handle push notification if configured
	if pushCfg := c.config.TaskStore.GetPushConfig(taskID); pushCfg != nil {
		statusEvent := a2a.TaskStatusUpdateEvent{
			TaskID: taskID,
			Status: a2a.TaskStatus{
				State:     a2a.TaskStateCompleted,
				Timestamp: time.Now(),
				Message: &a2a.Message{
					Role:  "agent",
					Parts: []a2a.Part{a2a.TextPart{Text: msg.Text}},
				},
			},
		}
		go func() {
			if err := c.config.PushNotifier.NotifyStatusUpdate(pushCfg, statusEvent); err != nil {
				c.logger.Printf("[a2a] Push notification failed for task %s: %v", taskID, err)
			}
		}()
	}

	return nil
}

// BroadcastTargets returns empty — A2A doesn't support broadcast.
func (c *A2AChannel) BroadcastTargets() []channels.Target {
	return nil
}

// SendTyping is a no-op for A2A (streaming handles progress via SSE events).
func (c *A2AChannel) SendTyping(ctx context.Context, target channels.Target) error {
	return nil
}

// Status returns the current channel status.
func (c *A2AChannel) Status() channels.ChannelStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return channels.ChannelStatus{
		Connected:    c.connected,
		AccountID:    "a2a-server",
		ConnectedAt:  c.connAt,
		MessageCount: c.msgCount.Load(),
	}
}

// --- Internal methods for the HTTP handler ---

// RegisterPending creates a response channel for a task and returns it.
// The HTTP handler calls this before dispatching to the ChatAgent,
// then waits on the returned channel for the response.
func (c *A2AChannel) RegisterPending(taskID string) <-chan channels.OutboundMessage {
	ch := make(chan channels.OutboundMessage, 1)
	c.pendingMu.Lock()
	c.pendingResponses[taskID] = ch
	c.pendingMu.Unlock()
	return ch
}

// UnregisterPending removes the response channel for a task.
func (c *A2AChannel) UnregisterPending(taskID string) {
	c.pendingMu.Lock()
	delete(c.pendingResponses, taskID)
	c.pendingMu.Unlock()
}

// Handler returns the stored message handler for use by the HTTP layer.
func (c *A2AChannel) Handler() channels.MessageHandler {
	return c.handler
}

// TaskStore returns the task store for use by the HTTP layer.
func (c *A2AChannel) TaskStore() a2a.TaskStore {
	return c.config.TaskStore
}

// PushNotifier returns the push notifier.
func (c *A2AChannel) PushNotifier() *a2a.PushNotifier {
	return c.config.PushNotifier
}

// BaseURL returns the configured base URL.
func (c *A2AChannel) BaseURL() string {
	return c.config.BaseURL
}
