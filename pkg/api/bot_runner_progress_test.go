package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// This proves the conservative rollout gate, NOT durable employee acceptance.
func TestBotProgressActivationGateHTTP(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Version.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
	calls := 0
	adapter, err := NewBotChatRuntimeAdapter(func(context.Context, bot.RunRequest) (*BotChatResources, error) {
		calls++
		return nil, errors.New("must not prepare resources")
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
		if !errors.Is(err, errBotProgressIntegrationUnavailable) {
			t.Errorf("wrong failure: %v", err)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || calls != 0 {
		t.Fatalf("status=%d factories=%d", response.StatusCode, calls)
	}
}

func TestBotProgressResumeDiscriminator(t *testing.T) {
	for _, raw := range []string{`{"kind":"bot_progress_v1"}`, `{"checkpoint":{"kind":"bot_progress_v1"}}`} {
		ctx, req, _ := botChatTestRequest(t)
		req.Run.Checkpoint = []byte(raw)
		adapter := &BotChatRuntimeAdapter{}
		_, err := adapter.resume(ctx, req, nil)
		if !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("untrusted progress checkpoint was not rejected before approval decoding: %v", err)
		}
	}
	req := bot.RunRequest{Progress: &store.BotProgressState{}}
	if !botHasProgressCheckpoint(req) {
		t.Fatal("typed progress ignored")
	}
	req = bot.RunRequest{}
	req.Run.Checkpoint = []byte(`{"format":"approval_v1"}`)
	if botHasProgressCheckpoint(req) {
		t.Fatal("legacy checkpoint reinterpreted")
	}
	if err := botProgressActivationGuard(req); err != nil {
		t.Fatalf("legacy gated: %v", err)
	}
}
