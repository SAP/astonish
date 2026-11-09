package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBotSessionSelectionRejectsSupersededSameBot(t *testing.T) {
	id := uuid.New()
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		json.NewEncoder(w).Encode(store.Bot{ID: id, Version: 1})
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: id, Version: 1}
	done := make(chan error, 1)
	go func() { done <- b.SelectBotSession(context.Background(), uuid.Nil) }()
	<-entered
	b.DetachBot()
	b.botMu.Lock()
	b.selected = &store.Bot{ID: id, Version: 2}
	b.botMu.Unlock()
	close(release)
	if err := <-done; err == nil || b.SelectedBot().Version != 2 {
		t.Fatal("stale home response overwrote newer selection")
	}
}

func TestBotCanceledSnapshotDoesNotOverwriteSelection(t *testing.T) {
	id := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		json.NewEncoder(w).Encode(store.BotSnapshot{Bot: store.Bot{ID: id, Version: 1}})
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: id, Version: 2}
	if _, err := b.BotSnapshot(ctx); err == nil || b.SelectedBot().Version != 2 {
		t.Fatal("canceled snapshot changed selection")
	}
}

func TestBotSnapshotUsesScopedAuthorityAndStudioRoute(t *testing.T) {
	id := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/studio/bots/"+id.String()+"/snapshot" || r.URL.Query().Get("scope") != "team" {
			t.Errorf("wrong snapshot route: %s", r.URL)
		}
		json.NewEncoder(w).Encode(store.BotSnapshot{Bot: store.Bot{ID: id, Status: store.BotPaused, Version: 3}, UnreadCount: 4, PendingApprovals: []store.BotApproval{{ID: uuid.New()}}})
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "team")
	b.selected = &store.Bot{ID: id}
	snap, err := b.BotSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.UnreadCount != 4 || len(snap.PendingApprovals) != 1 || b.SelectedBot().Status != store.BotPaused {
		t.Fatal("snapshot lost authority")
	}
	if got := b.BotStudioURL(); got != srv.URL+"/#/chat/bots/team/"+id.String() {
		t.Fatal(got)
	}
}

func TestBotWatchWaitsForNewHomeAdmission(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/studio/bots/"+id.String() {
			json.NewEncoder(w).Encode(store.Bot{ID: id, HomeSessionID: sid})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "id: %s:1\nevent: transcript\ndata: {\"author\":\"user\",\"content\":{\"parts\":[{\"text\":\"admitted\"}]}}\n\n", sid)
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: id}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stop := errors.New("received home")
	err := b.WatchBot(ctx, func(e events.Event) error {
		if e.Kind == events.KindUser && e.Text == "admitted" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || b.SelectedBot().HomeSessionID != sid {
		t.Fatalf("home admission not observed: %v", err)
	}
}

func TestBotWatchReplayDeduplicatesAllKinds(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		row := `{"author":"agent","content":{"role":"model","parts":[{"text":"hello"},{"functionCall":{"name":"read_file","id":"call","args":{}}},{"functionResponse":{"name":"read_file","id":"call","response":{"result":"ok"}}}]},"actions":{"artifactDelta":{"report.md":1}},"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`
		for i := 0; i < 2; i++ {
			fmt.Fprintf(w, "id: %s:1\nevent: transcript\ndata: %s\n\n", sid, row)
		}
		fmt.Fprintf(w, "id: %s:2\nevent: transcript\ndata: {\"author\":\"agent\",\"content\":{\"parts\":[{\"text\":\"sentinel\"}]}}\n\n", sid)
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: id, HomeSessionID: sid}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stop := errors.New("stop")
	counts := map[events.Kind]int{}
	err := b.WatchBot(ctx, func(e events.Event) error {
		if e.Text == "sentinel" {
			return stop
		}
		counts[e.Kind]++
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	for _, kind := range []events.Kind{events.KindText, events.KindToolCall, events.KindToolResult, events.KindArtifact, events.KindUsage} {
		if counts[kind] != 1 {
			t.Fatalf("%s count=%d", kind, counts[kind])
		}
	}
}
func TestBotSelectionCannotResurrectAfterDetach(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	id := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		json.NewEncoder(w).Encode(store.Bot{ID: id})
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	done := make(chan error, 1)
	go func() { _, err := b.SelectBot(context.Background(), id.String()); done <- err }()
	<-entered
	b.DetachBot()
	close(release)
	if err := <-done; err == nil || b.SelectedBot() != nil {
		t.Fatal("stale selection resurrected")
	}
}

func TestBotOwnerTurnIntentAndSingleChronology(t *testing.T) {
	for _, status := range []store.BotStatus{store.BotDraft, store.BotActive, store.BotPaused} {
		t.Run(string(status), func(t *testing.T) {
			id, runID, sessionID := uuid.New(), uuid.New(), uuid.New()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				want := "/api/studio/bots/" + id.String() + "/messages"
				if r.Method != "POST" || r.URL.Path != want {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				var q struct {
					Version      int64  `json:"version"`
					Instructions string `json:"instructions"`
					Key          string `json:"idempotency_key"`
				}
				json.NewDecoder(r.Body).Decode(&q)
				if q.Version != 7 || q.Instructions != "hello" || q.Key == "" {
					t.Errorf("bad request: %+v", q)
				}
				json.NewEncoder(w).Encode(store.BotMessageReceipt{Run: store.BotRun{ID: runID, BotID: id, SessionID: sessionID}, HomeSessionID: sessionID, UserEventID: "user-event", HistoryCursor: 1})
			}))
			defer srv.Close()
			b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
			b.selected = &store.Bot{ID: id, Status: status, Version: 7}
			ch, err := b.RunTurn(context.Background(), "hello", backend.TurnOptions{})
			if err != nil {
				t.Fatal(err)
			}
			notices := 0
			for e := range ch {
				if e.Kind != events.KindSystem || !strings.Contains(e.Text, "Message accepted") || strings.Contains(e.Text, "/bots runs") || strings.Contains(e.Text, "hello") {
					t.Fatalf("admission must only acknowledge returned run/session, never echo input: %+v", e)
				}
				notices++
			}
			if notices != 1 {
				t.Fatalf("want one admission notice, got %d", notices)
			}
			if calls != 1 {
				t.Fatal(calls)
			}
			b.sessionID = uuid.New()
			if _, err = b.RunTurn(context.Background(), "no", backend.TurnOptions{}); err == nil || calls != 1 {
				t.Fatal("run trace accepted input")
			}
		})
	}
}
