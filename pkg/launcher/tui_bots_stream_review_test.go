package launcher

import (
	"context"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/tui/events"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotSnapshotCancelsInflightTransport(t *testing.T) {
	entered, released := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer srv.Close()
	defer close(released)
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: uuid.New(), Version: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.BotSnapshot(ctx); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("snapshot HTTP request ignored cancellation")
	}
	if b.SelectedBot().Version != 2 {
		t.Fatal("canceled snapshot changed selection")
	}
}

func TestBotWatchReconnectContentAndEnvelopeChronology(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	rows := []client.SSEEvent{}
	for i, wire := range []struct{ typ, payload string }{
		{"session", `{"sessionId":"` + sid.String() + `"}`},
		{"user_message", `{"text":"question"}`},
		{"text", `{"text":"answer"}`},
		{"tool_call", `{"name":"read_file","id":"c","args":{}}`},
		{"tool_result", `{"name":"read_file","id":"c","result":"ok"}`},
		{"artifact", `{"path":"report.md"}`},
		{"usage", `{"input_tokens":3,"output_tokens":2}`},
		{"approval", `{"name":"shell_command"}`},
		{"turn_complete", `{}`},
	} {
		// Every early frame deliberately shares the previous committed cursor.
		cursor := int64(0)
		if i == 8 {
			cursor = 1
		}
		rows = append(rows, canonicalBotFrame(t, sid.String(), fmt.Sprintf("row/%d", i), wire.typ, wire.payload, cursor))
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 2 && r.URL.Query().Get("after") != sid.String()+":1" {
			t.Errorf("reconnect cursor=%s", r.URL.Query().Get("after"))
		}
		if n == 3 && r.URL.Query().Get("after") != sid.String()+":2" {
			t.Errorf("duplicate did not advance cursor=%s", r.URL.Query().Get("after"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			for _, f := range rows {
				fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", f.ID, f.Type, f.Data)
			}
		}
		if n == 2 {
			// A replayed identity still advances transport once its row is committed.
			f := canonicalBotFrame(t, sid.String(), "row/8", "turn_complete", `{}`, 2)
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", f.ID, f.Type, f.Data)
		}
		if n == 3 {
			f := canonicalBotFrame(t, sid.String(), "sentinel", "text", `{"text":"sentinel"}`, 3)
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", f.ID, f.Type, f.Data)
		}
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: id, HomeSessionID: sid}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop := errors.New("observed reconnect")
	var order []events.Kind
	err := b.WatchBot(ctx, func(e events.Event) error {
		if e.Text == "sentinel" {
			return stop
		}
		order = append(order, e.Kind)
		return nil
	})
	if !errors.Is(err, stop) || calls.Load() != 3 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	want := []events.Kind{events.KindSession, events.KindUser, events.KindText, events.KindToolCall, events.KindStatus, events.KindToolResult, events.KindArtifact, events.KindUsage, events.KindDone}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order=%v want=%v", order, want)
	}
}

func TestBotSnapshotRefreshesInfoAuthority(t *testing.T) {
	id := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"bot":{"id":%q,"name":"Monitor","status":"paused","version":4}}`, id)
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: id, Name: "Monitor", Status: store.BotActive, Version: 3}
	if _, err := b.BotSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Info().Title, "[paused]") || b.SelectedBot().Version != 4 {
		t.Fatalf("stale info: %+v", b.Info())
	}
}

func TestBotTranscriptMetadataDoesNotCloseText(t *testing.T) {
	for _, typ := range []string{"approval", "auto_approved", "plan_approval", "cursor", "replay_complete"} {
		got, err := botTranscriptEvents(runtimeTranscriptFrame(t, typ, `{}`, 1))
		if err != nil || len(got) != 0 {
			t.Fatalf("metadata altered chronology: %+v %v", got, err)
		}
	}
}
