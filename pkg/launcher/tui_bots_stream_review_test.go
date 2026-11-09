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
	rows := []string{
		runtimeTranscriptFrame(t, "session", `{"sessionId":"`+sid.String()+`"}`, 1).Data,
		`{"Author":"user","Content":{"role":"user","parts":[{"text":"question"}]}}`,
		`{"Author":"agent","Content":{"parts":[{"text":"answer"},{"functionCall":{"name":"read_file","id":"c1","args":{}}},{"functionResponse":{"name":"read_file","id":"c1","response":{"result":"ok"}}}]},"Actions":{"ArtifactDelta":{"report.md":1}},"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
	}
	for i, wire := range []struct{ typ, payload string }{
		{"text", `{"text":"answer"}`},
		{"tool_call", `{"name":"read_file","args":{}}`},
		{"tool_result", `{"name":"read_file","result":{"result":"ok"}}`},
		{"artifact", `{"path":"report.md","fileName":"report.md"}`},
		{"usage", `{"input_tokens":3,"output_tokens":2,"total_tokens":5}`},
		{"approval", `{"name":"shell_command","args":{}}`},
		{"done", `{"done":true}`},
	} {
		rows = append(rows, runtimeTranscriptFrame(t, wire.typ, wire.payload, int64(i+2)).Data)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 2 && r.URL.Query().Get("after") != fmt.Sprintf("%s:%d", sid, len(rows)) {
			t.Errorf("reconnect cursor=%s", r.URL.Query().Get("after"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i, row := range rows {
			fmt.Fprintf(w, "id: %s:%d\nevent: transcript\ndata: %s\n\n", sid, i+1, row)
		}
		if n > 1 {
			fmt.Fprintf(w, "id: %s:%d\nevent: transcript\ndata: %s\n\n", sid, len(rows)+1, runtimeTranscriptFrame(t, "text", `{"text":"sentinel"}`, 20).Data)
		}
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &store.Bot{ID: id, HomeSessionID: sid}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop := errors.New("observed reconnect")
	counts := map[events.Kind]int{}
	var order []events.Kind
	err := b.WatchBot(ctx, func(e events.Event) error {
		if e.Text == "sentinel" {
			return stop
		}
		counts[e.Kind]++
		order = append(order, e.Kind)
		return nil
	})
	if !errors.Is(err, stop) || calls.Load() != 2 {
		t.Fatalf("err=%v connections=%d", err, calls.Load())
	}
	for _, kind := range []events.Kind{events.KindUser, events.KindText, events.KindToolCall, events.KindToolResult, events.KindArtifact, events.KindUsage} {
		if counts[kind] != 1 {
			t.Errorf("%s count=%d", kind, counts[kind])
		}
	}
	wantOrder := []events.Kind{events.KindSession, events.KindUser, events.KindDone, events.KindText, events.KindToolCall, events.KindStatus, events.KindToolResult, events.KindArtifact, events.KindUsage, events.KindDone}
	if fmt.Sprint(order) != fmt.Sprint(wantOrder) {
		t.Fatalf("chronology %v want %v", order, wantOrder)
	}
	if counts[events.KindDone] != 2 || counts[events.KindApproval] != 0 {
		t.Fatalf("metadata altered chronology/decisions: %v", counts)
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
	for _, row := range []string{
		`{"author":"bot_runtime","actions":{"stateDelta":{"bot_runtime_event":{"type":"approval","payload":{"name":"shell_command"}}}}}`,
		`{"actions":{"stateDelta":{"status":"running"}}}`,
	} {
		got, err := botTranscriptEvents(client.SSEEvent{Type: client.BotTranscriptEvent, Data: row})
		if err != nil || len(got) != 0 {
			t.Fatalf("metadata altered chronology: %+v, %v", got, err)
		}
	}
}
