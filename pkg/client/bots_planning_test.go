package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotPlanningClientRequestControls(t *testing.T) {
	mode := false
	runID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "personal" {
			t.Error("lost scope")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if value, ok := body["planMode"]; !ok || value != false {
			t.Errorf("explicit false omitted: %+v", body)
		}
		if r.URL.Path == "/api/studio/bots/bot/messages" {
			if body["instructions"] != "Plan" {
				t.Error(body)
			}
			_ = json.NewEncoder(w).Encode(store.BotMessageReceipt{Run: store.BotRun{ID: runID}})
		} else {
			if body["expected_revision"] != float64(3) || body["idempotency_key"] != "continue" {
				t.Error(body)
			}
			_ = json.NewEncoder(w).Encode(store.BotRun{ID: runID})
		}
	}))
	defer server.Close()
	c := NewWithConfig(server.URL)
	c.cached = &Tokens{AccessToken: "token"}
	b := Bots{Client: c, Scope: store.BotScopePersonal}
	receipt, err := b.SendMessageRequest(context.Background(), "bot", store.BotMessageRequest{Version: 1, IdempotencyKey: "message", Instructions: "Plan", PlanMode: &mode})
	if err != nil || receipt.Run.ID != runID {
		t.Fatalf("message: %+v %v", receipt, err)
	}
	run, err := b.ContinueInteractiveRequest(context.Background(), "bot", runID.String(), store.BotContinueInteractiveRequest{ExpectedRevision: 3, IdempotencyKey: "continue", PlanMode: &mode})
	if err != nil || run.ID != runID {
		t.Fatalf("continuation: %+v %v", run, err)
	}
}
