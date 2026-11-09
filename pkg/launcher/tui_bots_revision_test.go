package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

// Drive the production watch, not a parallel test-only operation reducer.
func revisionWatch(t *testing.T, b *botPlatformBackend, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop := errors.New("observed operations")
	seen := 0
	err := b.WatchBot(ctx, func(e events.Event) error {
		if e.Kind == events.KindBotOperation {
			seen++
			if seen == count {
				return stop
			}
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("watch: %v (%d events)", err, seen)
	}
}

func TestBotComposerOperationRevision(t *testing.T) {
	for _, mode := range []string{"applied", "stale-first-read", "missing-evidence", "read-error", "conflict", "pending", "foreign", "stopped", "paused", "draft", "duplicates", "snapshot-ahead"} {
		t.Run(mode, func(t *testing.T) {
			root := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Version: 2, Revision: 5, Status: store.BotActive}
			op := store.BotOperationView{SchemaVersion: 1, BotID: root.ID, HomeSessionID: root.HomeSessionID, OperationID: uuid.New(), Revision: 73, StateSequence: 81, Status: store.BotOperationApplied, Decision: store.BotOperationExecute}
			if mode == "pending" {
				op.Status = store.BotOperationPending
			}
			if mode == "foreign" {
				op.BotID = uuid.New()
			}
			fresh := root
			fresh.Version = 9
			fresh.Revision = 14
			if mode == "stopped" {
				fresh.Status = store.BotStopped
			}
			if mode == "paused" {
				fresh.Status = store.BotPaused
			}
			if mode == "draft" {
				fresh.Status = store.BotDraft
			}
			var reads, posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/events") {
					w.Header().Set("Content-Type", "text/event-stream")
					row := adksession.Event{Actions: adksession.EventActions{StateDelta: map[string]any{store.BotOperationKey: op}}}
					raw, _ := json.Marshal(row)
					fmt.Fprintf(w, "id: %s:1\nevent: transcript\ndata: %s\n\n", root.HomeSessionID, raw)
					if mode == "duplicates" {
						fmt.Fprintf(w, "id: %s:2\nevent: transcript\ndata: %s\n\n", root.HomeSessionID, raw)
						older := op
						older.Status = store.BotOperationPending
						older.Revision--
						older.StateSequence--
						row.Actions.StateDelta[store.BotOperationKey] = older
						raw, _ = json.Marshal(row)
						fmt.Fprintf(w, "id: %s:3\nevent: transcript\ndata: %s\n\n", root.HomeSessionID, raw)
					}
					return
				}
				if strings.HasSuffix(r.URL.Path, "/snapshot") {
					n := reads.Add(1)
					if mode == "read-error" {
						http.Error(w, "unavailable", 503)
						return
					}
					if mode == "missing-evidence" || (mode == "stale-first-read" && n == 1) {
						_ = json.NewEncoder(w).Encode(store.BotSnapshot{Bot: root})
						return
					}
					_ = json.NewEncoder(w).Encode(store.BotSnapshot{Bot: fresh, Operations: []store.BotOperationView{op}, HistoryCursor: 90})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/messages") {
					posts.Add(1)
					var q struct {
						Version int64 `json:"version"`
					}
					_ = json.NewDecoder(r.Body).Decode(&q)
					want := fresh.Version
					if mode == "pending" || mode == "foreign" {
						want = root.Version
					}
					if q.Version != want {
						t.Errorf("composer sent version %d, want authoritative %d (operation revision %d)", q.Version, want, op.Revision)
					}
					if mode == "conflict" {
						http.Error(w, "Bot changed; reload and review before retrying.", 409)
						return
					}
					_ = json.NewEncoder(w).Encode(store.BotMessageReceipt{})
					return
				}
				t.Errorf("unexpected route %s", r.URL)
				http.NotFound(w, r)
			}))
			defer srv.Close()
			b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
			b.selected = &root
			if mode == "snapshot-ahead" {
				if _, err := b.BotSnapshot(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			count := 1
			if mode == "duplicates" {
				count = 3
			}
			revisionWatch(t, b, count)
			_, err := b.RunTurn(context.Background(), "next instruction", backend.TurnOptions{})
			blocked := mode == "missing-evidence" || mode == "read-error" || mode == "stopped"
			if (err != nil) != (blocked || mode == "conflict") {
				t.Fatalf("error=%v", err)
			}
			wantPosts := int32(1)
			if blocked {
				wantPosts = 0
			}
			if posts.Load() != wantPosts {
				t.Fatalf("posts=%d, want %d; no replay", posts.Load(), wantPosts)
			}
			if mode == "pending" || mode == "foreign" {
				if reads.Load() != 0 {
					t.Fatal("non-applied/foreign operation refreshed authority")
				}
			} else if !blocked {
				if b.SelectedBot().Version != 9 || b.SelectedBot().Revision != 14 {
					t.Fatalf("not snapshot authority: %+v", b.SelectedBot())
				}
				before := reads.Load()
				_, _ = b.RunTurn(context.Background(), "another instruction", backend.TurnOptions{})
				if reads.Load() != before {
					t.Fatal("caught-up operation fetched again")
				}
			}
		})
	}
}

func TestBotComposerSelectionChangesDuringReconciliation(t *testing.T) {
	for _, mode := range []string{"detach", "run-trace", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Version: 2, Revision: 5, Status: store.BotActive}
			op := store.BotOperationView{SchemaVersion: 1, BotID: root.ID, HomeSessionID: root.HomeSessionID, OperationID: uuid.New(), Revision: 73, StateSequence: 81, Status: store.BotOperationApplied, Decision: store.BotOperationExecute}
			entered, release := make(chan struct{}), make(chan struct{})
			var posts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					return
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_ = json.NewEncoder(w).Encode(store.BotSnapshot{Bot: root, Operations: []store.BotOperationView{op}})
			}))
			defer srv.Close()
			b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
			b.selected = &root
			b.observeBotOperationLocked(&op)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := b.RunTurn(ctx, "do not send to replacement", backend.TurnOptions{}); done <- err }()
			<-entered
			switch mode {
			case "detach":
				b.DetachBot()
			case "run-trace":
				b.botMu.Lock()
				b.sessionID = uuid.New()
				b.selectionEpoch++
				b.botMu.Unlock()
			case "cancel":
				cancel()
			}
			close(release)
			if err := <-done; err == nil {
				t.Fatal("selection/cancellation accepted")
			}
			if posts.Load() != 0 {
				t.Fatal("admitted after selection/cancellation")
			}
		})
	}
}

func TestBotComposerSnapshotRejectsRegression(t *testing.T) {
	root := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Version: 9, Revision: 14, Status: store.BotActive}
	var snap store.BotSnapshot
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(snap) }))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &root
	snap = store.BotSnapshot{Bot: root, HistoryCursor: 90, LatestSequence: 100}
	if _, err := b.BotSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "revision", "history", "sequence"} {
		snap = store.BotSnapshot{Bot: root, HistoryCursor: 90, LatestSequence: 100}
		switch field {
		case "version":
			snap.Bot.Version--
		case "revision":
			snap.Bot.Revision--
		case "history":
			snap.HistoryCursor--
		case "sequence":
			snap.LatestSequence--
		}
		if _, err := b.BotSnapshot(context.Background()); err == nil {
			t.Errorf("accepted regressing %s", field)
		}
		if b.SelectedBot().Version != 9 || b.SelectedBot().Revision != 14 {
			t.Fatal("regressed selected authority")
		}
	}
}
