package astonish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotsCLIExplicitIntents(t *testing.T) {
	id := uuid.New()
	for _, tc := range []struct {
		command, endpoint string
		status            store.BotStatus
	}{{"test-once", "test-run", store.BotDraft}, {"conversation", "conversation", store.BotPaused}} {
		t.Run(tc.command, func(t *testing.T) {
			posts := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("scope") != "team" {
					t.Error("scope lost")
				}
				if r.Method == "GET" {
					_ = json.NewEncoder(w).Encode(store.Bot{ID: id, Version: 3, Status: tc.status})
					return
				}
				posts++
				if r.URL.Path != "/api/studio/bots/"+id.String()+"/"+tc.endpoint {
					t.Errorf("wrong intent %s", r.URL)
				}
				var q map[string]any
				_ = json.NewDecoder(r.Body).Decode(&q)
				if q["version"] != float64(3) || q["idempotency_key"] != "reviewed-key" || q["instructions"] != "observe" {
					t.Errorf("DTO %#v", q)
				}
				_ = json.NewEncoder(w).Encode(store.BotRun{ID: uuid.New(), Status: store.RunQueued})
			}))
			defer s.Close()
			c := client.NewWithConfig(s.URL)
			base := []string{tc.command, id.String(), "--scope", "team", "--instructions", "observe", "--key", "reviewed-key"}
			for _, flags := range [][]string{nil, {"--version", "3"}, {"--version", "3", "--confirm", "personal:" + id.String()}, {"--version", "2", "--confirm", "team:" + id.String()}} {
				err := runBotsCommand(context.Background(), c, append(append([]string{}, base...), flags...), io.Discard)
				if err == nil {
					t.Fatalf("unreviewed request accepted %v", flags)
				}
			}
			if posts != 0 {
				t.Fatalf("unreviewed mutations=%d", posts)
			}
			var out strings.Builder
			err := runBotsCommand(context.Background(), c, append(base, "--version", "3", "--confirm", "team:"+id.String(), "--json"), &out)
			if err != nil || posts != 1 || !strings.Contains(out.String(), "queued") {
				t.Fatalf("posts=%d out=%s err=%v", posts, out.String(), err)
			}
		})
	}
}

func TestBotsCLIIntentStateAndServerErrors(t *testing.T) {
	id := uuid.New()
	for _, command := range []string{"test-once", "conversation"} {
		for _, status := range []store.BotStatus{store.BotActive, store.BotDraft, store.BotPaused} {
			t.Run(command+"/"+string(status), func(t *testing.T) {
				posts := 0
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						_ = json.NewEncoder(w).Encode(store.Bot{ID: id, Version: 3, Status: status})
						return
					}
					posts++
					http.Error(w, "Bot changed; reload and review", http.StatusConflict)
				}))
				defer s.Close()
				err := runBotsCommand(context.Background(), client.NewWithConfig(s.URL), []string{command, id.String(), "--version", "3", "--confirm", "personal:" + id.String()}, io.Discard)
				allowed := command == "test-once" && status == store.BotDraft || command == "conversation" && status == store.BotPaused
				if err == nil {
					t.Fatal("expected state/server error")
				}
				if allowed {
					if posts != 1 || !strings.Contains(err.Error(), "409") {
						t.Fatalf("posts=%d err=%v", posts, err)
					}
				} else if posts != 0 || !strings.Contains(err.Error(), "no activation") {
					t.Fatalf("posts=%d err=%v", posts, err)
				}
			})
		}
	}
}

func TestBotsCLIUnknownCommandDoesNotResolve(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected", 500) }))
	defer s.Close()
	err := runBotsCommand(context.Background(), client.NewWithConfig(s.URL), []string{"typo", "Monitor"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unknown bots command") || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
