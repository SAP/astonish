package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPushNotifier_Success(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		received = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewPushNotifier(nil)
	notifier.baseDelay = 10 * time.Millisecond // speed up tests

	cfg := &PushNotificationConfig{URL: server.URL, Token: "test-token"}
	event := TaskStatusUpdateEvent{
		TaskID: "task-123",
		Status: TaskStatus{State: TaskStateCompleted, Timestamp: time.Now()},
	}

	err := notifier.NotifyStatusUpdate(cfg, event)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	var got TaskStatusUpdateEvent
	if err := json.Unmarshal(received, &got); err == nil {
		if got.TaskID != "task-123" {
			t.Fatalf("expected taskId 'task-123', got %q", got.TaskID)
		}
	}
}

func TestPushNotifier_NilConfig(t *testing.T) {
	notifier := NewPushNotifier(nil)
	err := notifier.NotifyStatusUpdate(nil, TaskStatusUpdateEvent{})
	if err != nil {
		t.Fatalf("expected nil for nil config, got: %v", err)
	}
}

func TestPushNotifier_Retries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewPushNotifier(nil)
	notifier.baseDelay = 1 * time.Millisecond

	cfg := &PushNotificationConfig{URL: server.URL}
	err := notifier.NotifyStatusUpdate(cfg, TaskStatusUpdateEvent{TaskID: "t1"})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}
