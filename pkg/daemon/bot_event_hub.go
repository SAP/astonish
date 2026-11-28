package daemon

import (
	"context"
	"sync"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// BotEventHub carries coalesced wakeups only, never transcript data. A slow
// subscriber cannot block a worker or exhaust an unbounded event queue.
type BotEventHub struct {
	mu     sync.Mutex
	topics map[api.BotEventSubscription]map[chan struct{}]struct{}
}

func NewBotEventHub() *BotEventHub {
	return &BotEventHub{topics: make(map[api.BotEventSubscription]map[chan struct{}]struct{})}
}
func (h *BotEventHub) SubscribeBotEvents(key api.BotEventSubscription) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.topics == nil {
		h.topics = make(map[api.BotEventSubscription]map[chan struct{}]struct{})
	}
	if h.topics[key] == nil {
		h.topics[key] = make(map[chan struct{}]struct{})
	}
	h.topics[key][ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.topics[key], ch)
			if len(h.topics[key]) == 0 {
				delete(h.topics, key)
			}
			// Do not close: a concurrent HTTP select must not spin on a closed wake.
		})
	}
}

// Publish is called only after the fenced transaction commits. Duplicate
// publications are harmless because consumers replay strictly after their cursor.
func (h *BotEventHub) Publish(ctx context.Context, e bot.RuntimeEvent) error {
	id, ok := execution.BotRunIdentityFromContext(ctx)
	if !ok || id.RunID != e.RunID {
		return store.ErrBotForbidden
	}
	key := api.BotEventSubscription{OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, BotID: id.BotID, SessionID: e.SessionID}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.topics[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

var defaultBotEventHub = NewBotEventHub()

func installBotEventPublisher(custom bot.EventSink) bot.EventSink {
	api.SetBotEventSubscriber(defaultBotEventHub)
	return func(ctx context.Context, e bot.RuntimeEvent) error {
		if err := defaultBotEventHub.Publish(ctx, e); err != nil {
			return err
		}
		if custom != nil {
			return custom(ctx, e)
		}
		return nil
	}
}

// WakeBotEvents carries no data or authority; durable replay reauthorizes.
func (h *BotEventHub) WakeBotEvents(key api.BotEventSubscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.topics[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
