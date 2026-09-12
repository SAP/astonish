package a2a

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func publicTestNotifier(t *testing.T, server *httptest.Server) *PushNotifier {
	t.Helper()
	notifier := NewPushNotifier(nil)
	notifier.baseDelay = time.Millisecond
	notifier.resolveHost = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	notifier.client = server.Client()
	notifier.client.Timeout = 30 * time.Second
	notifier.client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return notifier
}

func TestPushNotifier_Success(t *testing.T) {
	var received []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		received = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := publicTestNotifier(t, server)
	cfg := &PushNotificationConfig{URL: server.URL, Token: "test-token"}
	event := TaskStatusUpdateEvent{TaskID: "task-123", Status: TaskStatus{State: TaskStateCompleted, Timestamp: time.Now()}}
	if err := notifier.NotifyStatusUpdate(cfg, event); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	var got TaskStatusUpdateEvent
	if err := json.Unmarshal(received, &got); err == nil && got.TaskID != "task-123" {
		t.Fatalf("expected taskId 'task-123', got %q", got.TaskID)
	}
}

func TestPushNotifier_NilConfig(t *testing.T) {
	if err := NewPushNotifier(nil).NotifyStatusUpdate(nil, TaskStatusUpdateEvent{}); err != nil {
		t.Fatalf("expected nil for nil config, got: %v", err)
	}
}

func TestPushNotifier_Retries(t *testing.T) {
	attempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := publicTestNotifier(t, server)
	if err := notifier.NotifyStatusUpdate(&PushNotificationConfig{URL: server.URL}, TaskStatusUpdateEvent{TaskID: "t1"}); err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestPushNotifierRejectsNonPublicAndNonHTTPSURLs(t *testing.T) {
	notifier := NewPushNotifier(nil)
	notifier.resolveHost = func(_ context.Context, host string) ([]net.IP, error) {
		if host == "localhost" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	for _, rawURL := range []string{"http://example.com/hook", "https://localhost/hook", "https://user@example.com/hook"} {
		if err := notifier.ValidatePushURL(context.Background(), rawURL); err == nil {
			t.Fatalf("expected %q to be rejected", rawURL)
		}
	}
	notifier.resolveHost = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	if err := notifier.ValidatePushURL(context.Background(), "https://example.com/hook"); err == nil {
		t.Fatal("expected loopback target to be rejected")
	}
}

func TestSafePushTransportRejectsNonPublicAddressFromResolver(t *testing.T) {
	transport := safePushTransport(func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})

	conn, err := transport.DialContext(context.Background(), "tcp", "example.com:443")
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("expected dial-time resolver result to reject a loopback address")
	}
}

func TestPushNotifierRejectsRedirects(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, "https://example.com/next", http.StatusFound)
	}))
	defer server.Close()
	notifier := publicTestNotifier(t, server)
	if err := notifier.NotifyStatusUpdate(&PushNotificationConfig{URL: server.URL}, TaskStatusUpdateEvent{}); err == nil {
		t.Fatal("expected redirect to be rejected")
	}
}
