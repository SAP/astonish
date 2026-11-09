package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotsSnapshotScopedAuthenticated(t *testing.T) {
	id := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/studio/bots/"+id.String()+"/snapshot" || r.URL.Query().Get("scope") != "team" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("incorrect scoped authenticated snapshot: %s %s", r.Method, r.URL)
		}
		json.NewEncoder(w).Encode(store.BotSnapshot{Bot: store.Bot{ID: id, Status: store.BotPaused}, UnreadCount: 4})
	}))
	defer srv.Close()
	c := NewWithConfig(srv.URL)
	c.cached = &Tokens{AccessToken: "token"}
	b := Bots{Client: c, Scope: store.BotScopeTeam}
	snap, err := b.Snapshot(context.Background(), id.String())
	if err != nil || snap.Bot.ID != id || snap.Bot.Status != store.BotPaused || snap.UnreadCount != 4 {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
	b.Scope = "invalid"
	if _, err := b.Snapshot(context.Background(), id.String()); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestBotsSnapshotCancelsInflightTransport(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	b := Bots{Client: NewWithConfig(srv.URL), Scope: store.BotScopePersonal}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.Snapshot(ctx, uuid.NewString()); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot did not cancel HTTP request")
	}
}
