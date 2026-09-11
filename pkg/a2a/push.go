package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"
)

// PushNotifier delivers task updates to registered webhook URLs.
type PushNotifier struct {
	client      *http.Client
	maxRetries  int
	baseDelay   time.Duration
	logger      *log.Logger
	resolveHost func(context.Context, string) ([]net.IP, error)
}

// NewPushNotifier creates a new push notification delivery service.
func NewPushNotifier(logger *log.Logger) *PushNotifier {
	if logger == nil {
		logger = log.Default()
	}
	notifier := &PushNotifier{
		maxRetries:  3,
		baseDelay:   5 * time.Second,
		logger:      logger,
		resolveHost: defaultResolveHost,
	}
	notifier.client = &http.Client{
		Timeout: 30 * time.Second,
		Transport: safePushTransport(func(ctx context.Context, host string) ([]net.IP, error) {
			return notifier.resolveHost(ctx, host)
		}),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return notifier
}

// safePushTransport re-resolves each connection using the same resolver used by
// ValidatePushURL, so custom resolvers and production DNS receive identical SSRF checks.
func safePushTransport(resolveHost func(context.Context, string) ([]net.IP, error)) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolveHost(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !isPublicPushIP(ip) {
				return nil, fmt.Errorf("push URL host resolves to a non-public address")
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return transport
}

func defaultResolveHost(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, address.IP)
	}
	return ips, nil
}

// ValidatePushURL permits only HTTPS webhook targets whose DNS answers are public IP addresses.
// It is called when configuration is stored and again immediately before delivery so a later DNS
// change cannot silently turn an approved target into an internal request.
func (p *PushNotifier) ValidatePushURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil {
		return fmt.Errorf("invalid push URL")
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("push URL must be an HTTPS URL without userinfo")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("push URL host is required")
	}
	ips, err := p.resolveHost(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("resolve push URL host: %w", err)
	}
	for _, ip := range ips {
		if !isPublicPushIP(ip) {
			return fmt.Errorf("push URL host resolves to a non-public address")
		}
	}
	return nil
}

func isPublicPushIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		// Carrier-grade NAT and the AWS instance metadata address must not be reached.
		return !ip4.Equal(net.IPv4(100, 64, 0, 0)) && !ip4.Equal(net.IPv4(169, 254, 169, 254))
	}
	// IPv6 unique-local addresses are private even though net.IP.IsPrivate is unavailable on
	// older Go versions used by some supported builds.
	return !(len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc)
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
	if err := p.ValidatePushURL(context.Background(), cfg.URL); err != nil {
		return fmt.Errorf("validate push URL: %w", err)
	}
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
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return fmt.Errorf("push notification redirect denied: status %d", resp.StatusCode)
		}
		lastErr = fmt.Errorf("push notification returned status %d", resp.StatusCode)
		p.logger.Printf("[a2a] push notification attempt %d: status %d", attempt+1, resp.StatusCode)
	}

	return fmt.Errorf("push notification failed after %d attempts: %w", p.maxRetries+1, lastErr)
}
