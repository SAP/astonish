package launcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotCanonicalWatchCursorIdentity(t *testing.T) {
	root := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("watch mutated")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(eventID, typ, data string, cursor int64) {
			f := canonicalBotFrame(t, root.HomeSessionID.String(), eventID, typ, data, cursor)
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", f.ID, f.Type, f.Data)
		}
		switch calls.Add(1) {
		case 1:
			send("one", "text", `{"text":"same"}`, 0)
			send("two", "text", `{"text":"same"}`, 0)
		case 2:
			if got := r.URL.Query().Get("after"); got != "" {
				t.Errorf("partial row committed: %q", got)
			}
			send("one", "text", `{"text":"same"}`, 0)
			send("two", "text", `{"text":"same"}`, 1) // duplicate still commits transport cursor
		case 3:
			if got := r.URL.Query().Get("after"); got != root.HomeSessionID.String()+":1" {
				t.Errorf("duplicate lost cursor: %q", got)
			}
			send("", "cursor", `{}`, 2)
			fmt.Fprintf(w, "id: %s:2\nevent: replay_complete\ndata: {}\n\n", root.HomeSessionID)
		case 4:
			if got := r.URL.Query().Get("after"); got != root.HomeSessionID.String()+":2" {
				t.Errorf("hidden row lost cursor: %q", got)
			}
			send("three", "text", `{"text":"same"}`, 2)
			send("done", "turn_complete", `{}`, 3)
		default:
			t.Error("unexpected reconnect")
		}
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &root
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	stop := errors.New("complete")
	texts, done := 0, 0
	err := b.WatchBot(ctx, func(e events.Event) error {
		switch e.Kind {
		case events.KindText:
			texts++
			if e.Text != "same" {
				t.Errorf("text %q", e.Text)
			}
		case events.KindDone:
			done++
			return stop
		default:
			t.Errorf("transport control rendered: %v", e.Kind)
		}
		return nil
	})
	if !errors.Is(err, stop) || texts != 3 || done != 1 || calls.Load() != 4 {
		t.Fatalf("err=%v texts=%d done=%d calls=%d", err, texts, done, calls.Load())
	}
}
