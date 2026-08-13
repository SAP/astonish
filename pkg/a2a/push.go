package a2a

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// PushNotifier delivers task updates to registered webhook URLs.
type PushNotifier struct {
	client     *http.Client
	maxRetries int
	baseDelay  time.Duration
	logger     *log.Logger
}

// NewPushNotifier creates a new push notification delivery service.
func NewPushNotifier(logger *log.Logger) *PushNotifier {
	if logger == nil {
		logger = log.Default()
	}
	return &PushNotifier{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		maxRetries: 3,
		baseDelay:  5 * time.Second,
		logger:     logger,
	}
}

// NotifyStatusUpdate sends a task status update to the push notification URL.
func (p *PushNotifier) NotifyStatusUpdate(cfg *PushNotificationConfig, event TaskStatusUpdateEvent) error {
	if cfg == nil {
		return nil
	}
	return p.deliver(cfg, event)
}

// NotifyArtifactUpdate sends an artifact update to the push notification URL.
func (p *PushNotifier) NotifyArtifactUpdate(cfg *PushNotificationConfig, event TaskArtifactUpdateEvent) error {
	if cfg == nil {
		return nil
	}
	return p.deliver(cfg, event)
}

func (p *PushNotifier) deliver(cfg *PushNotificationConfig, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal push payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			delay := p.baseDelay * time.Duration(1<<(attempt-1)) // exponential backoff
			time.Sleep(delay)
		}

		req, err := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create push request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			p.logger.Printf("[a2a] push notification attempt %d failed: %v", attempt+1, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("push notification returned status %d", resp.StatusCode)
		p.logger.Printf("[a2a] push notification attempt %d: status %d", attempt+1, resp.StatusCode)
	}

	return fmt.Errorf("push notification failed after %d attempts: %w", p.maxRetries+1, lastErr)
}
