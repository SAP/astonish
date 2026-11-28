package client

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBotProgressContinuationDTO(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/studio/bots/bot/runs/run/continue-interactive" || r.URL.Query().Get("scope") != string(scope) {
					t.Error("scope or route lost")
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if len(body) != 4 || body["kind"] != "progress_v1" || body["checkpoint_revision"] != float64(7) || body["expected_revision"] != float64(4) || body["idempotency_key"] != "reviewed" {
					t.Errorf("unbound DTO: %#v", body)
				}
				http.Error(w, "stale checkpoint", 409)
			}))
			defer server.Close()
			b := Bots{Client: NewWithConfig(server.URL), Scope: scope}
			_, err := b.ContinueProgress(context.Background(), "bot", "run", 4, 7, "reviewed")
			var response *BotResponseError
			if !errors.As(err, &response) || response.StatusCode != 409 || calls != 1 {
				t.Fatalf("error/retry: %v %d", err, calls)
			}
			if _, err = b.ContinueProgress(context.Background(), "bot", "run", 4, 0, "reviewed"); err == nil || calls != 1 {
				t.Fatal("unreviewed mutation")
			}
		})
	}
}
