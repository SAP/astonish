package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotsExplicitRunIntents(t *testing.T) {
	id, rid := uuid.NewString(), uuid.New()
	for _, intent := range []string{"runs", "test-run", "conversation"} {
		t.Run(intent, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/studio/bots/"+id+"/"+intent || r.URL.Query().Get("scope") != "team" || r.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var q map[string]any
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
				}
				if len(q) != 3 || q["version"] != float64(7) || q["idempotency_key"] != "key" || q["instructions"] != "observe" {
					t.Errorf("DTO: %#v", q)
				}
				_ = json.NewEncoder(w).Encode(store.BotRun{ID: rid})
			}))
			defer s.Close()
			c := NewWithConfig(s.URL)
			c.cached = &Tokens{AccessToken: "token"}
			b := Bots{Client: c, Scope: store.BotScopeTeam}
			call := b.Run
			if intent == "test-run" {
				call = b.TestRun
			}
			if intent == "conversation" {
				call = b.Conversation
			}
			got, err := call(context.Background(), id, 7, "key", "observe")
			if err != nil || got.ID != rid {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestBotsAmbiguityListsCandidates(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"bots": []store.Bot{{ID: ids[0], Name: "same"}, {ID: ids[1], Name: "same"}, {ID: ids[2], Name: "same"}}})
	}))
	defer s.Close()
	b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopeTeam}
	_, err := b.Resolve(context.Background(), "same")
	if err == nil {
		t.Fatal("accepted ambiguity")
	}
	for _, id := range ids {
		if !strings.Contains(err.Error(), id.String()) {
			t.Errorf("candidate %s missing: %v", id, err)
		}
	}
}

func TestBotsRunsAllPagesAndGoals(t *testing.T) {
	ids := make([]store.BotRun, 201)
	for i := range ids {
		ids[i].ID = uuid.New()
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/goals") {
			_ = json.NewEncoder(w).Encode(map[string]any{"goals": []store.BotGoal{{ID: uuid.New()}}})
			return
		}
		page := ids[:200]
		if r.URL.Query().Get("after") == ids[199].ID.String() {
			page = ids[200:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runs": page})
	}))
	defer s.Close()
	b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
	runs, err := b.Runs(context.Background(), uuid.NewString())
	if err != nil || len(runs) != 201 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	goals, err := b.Goals(context.Background(), uuid.NewString())
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals=%d err=%v", len(goals), err)
	}
}
