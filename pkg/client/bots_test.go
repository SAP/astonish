package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotsAuthenticatedDTOAndScope(t *testing.T) {
	id := uuid.New()
	var method, path string
	var body map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing auth")
		}
		if r.URL.Query().Get("scope") != "team" {
			t.Error("scope lost")
		}
		method, path = r.Method, r.URL.Path
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		_ = json.NewEncoder(w).Encode(store.Bot{ID: id})
	}))
	defer s.Close()
	c := NewWithConfig(s.URL)
	c.cached = &Tokens{AccessToken: "token"}
	b := Bots{Client: c, Scope: store.BotScopeTeam}
	if _, err := b.Lifecycle(context.Background(), id.String(), BotDelete, 7, "reviewed"); err != nil {
		t.Fatal(err)
	}
	if method != "DELETE" || path != "/api/studio/bots/"+id.String() || body["expected_revision"] != float64(7) || body["reason"] != "reviewed" || len(body) != 2 {
		t.Fatalf("wrong DTO %s %s %#v", method, path, body)
	}
	if _, err := b.Lifecycle(context.Background(), id.String(), BotStop, 0, ""); err == nil {
		t.Fatal("unreviewed mutation allowed")
	}
}
func TestBotsResolveAmbiguity(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"bots": []store.Bot{{ID: uuid.New(), Name: "same"}, {ID: uuid.New(), Name: "same"}}})
	}))
	defer s.Close()
	b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
	if _, err := b.Resolve(context.Background(), "same"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal(err)
	}
}
func TestBotsWatchReconnectCursorAndDetach(t *testing.T) {
	sid, id := uuid.NewString(), uuid.NewString()
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != "GET" {
			t.Error("watch mutated")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprintf(w, "id: %s:1\nevent: transcript\ndata: %s\n\n", sid, strings.Repeat("x", 90000))
			return
		}
		if r.URL.Query().Get("after") != sid+":1" {
			t.Error("cursor lost")
		}
		fmt.Fprintf(w, "id: %s:1\nevent: transcript\ndata: duplicate\n\nid: %s:2\nevent: transcript\ndata: next\n\n", sid, sid)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	count := 0
	err := b.Watch(ctx, id, sid, "", true, func(e SSEEvent) error {
		count++
		if count == 2 {
			if e.Data != "next" {
				t.Error("duplicate delivered")
			}
			cancel()
		}
		return nil
	})
	if err != context.Canceled || count != 2 || calls.Load() != 2 {
		t.Fatalf("%v count=%d calls=%d", err, count, calls.Load())
	}
	if err := b.Watch(context.Background(), id, sid, uuid.NewString()+":1", false, func(SSEEvent) error { return nil }); err == nil {
		t.Fatal("cross-session cursor accepted")
	}
}
func TestBotsWatchForbiddenNoRetry(t *testing.T) {
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "forbidden", 403) }))
	defer s.Close()
	b := Bots{Client: NewWithConfig(s.URL), Scope: store.BotScopePersonal}
	err := b.Watch(context.Background(), uuid.NewString(), uuid.NewString(), "", true, func(SSEEvent) error { return nil })
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
