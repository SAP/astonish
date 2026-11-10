package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

func TestBotSchedulePresentationWatchReplay(t *testing.T) {
	root := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Version: 2, Revision: 5, Status: store.BotActive}
	pending := store.BotOperationView{SchemaVersion: 1, BotID: root.ID, HomeSessionID: root.HomeSessionID, OperationID: uuid.New(), Revision: 1, StateSequence: 10, Operation: store.BotControlScheduleCreate, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Presentation: store.BotOperationPresentationInternal, Summary: "Change queued."}
	applying := pending
	applying.Revision++
	applying.StateSequence++
	applying.Status = store.BotOperationApplying
	applying.Summary = "Applying change."
	final := applying
	final.Revision++
	final.StateSequence++
	final.Status = store.BotOperationApplied
	final.Presentation = store.BotOperationPresentationConversation
	final.Summary = "I've created the schedule. It runs every minute."
	final.VerifiedSchedule = &store.BotOperationScheduleView{Enabled: true, Version: 1}
	journal := []store.BotOperationView{pending, applying, final, final, pending}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/snapshot"):
			_ = json.NewEncoder(w).Encode(store.BotSnapshot{Bot: root, Operations: []store.BotOperationView{pending}})
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			for i, view := range journal {
				row, err := store.NewBotOperationEvent(view, time.Now())
				if err != nil {
					t.Error(err)
					return
				}
				raw, err := json.Marshal(row)
				if err != nil {
					t.Error(err)
					return
				}
				fmt.Fprintf(w, "id: %s:%d\nevent: transcript\ndata: %s\n\n", root.HomeSessionID, i+1, raw)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(srv.URL)}, "personal")
	b.selected = &root
	tr := events.NewTranscript()
	tr.BotID, tr.SessionID = root.ID.String(), root.HomeSessionID.String()
	for pass := 0; pass < 2; pass++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		stop := errors.New("all operations observed")
		seen := 0
		err := b.WatchBot(ctx, func(e events.Event) error {
			if e.Kind != events.KindBotOperation {
				return nil
			}
			seen++
			tr.Apply(e)
			if pass == 0 && seen <= 2 {
				if e.BotOperation.Presentation != store.BotOperationPresentationInternal || len(tr.Items) != 0 || len(b.operationHeads.Items) != 0 || !tr.Streaming || !b.operationHeads.Streaming {
					t.Errorf("internal watch lost state or leaked message: %+v", tr)
				}
			}
			if seen == len(journal) {
				return stop
			}
			return nil
		})
		cancel()
		if !errors.Is(err, stop) {
			t.Fatalf("watch pass %d: %v (%d rows)", pass, err, seen)
		}
		if _, err := b.BotSnapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, reduced := range []*events.Transcript{tr, b.operationHeads} {
			if len(reduced.Items) != 1 || reduced.Items[0].Kind != events.ItemAgent || reduced.Items[0].Content != final.Summary || reduced.Items[0].BotOperationID != final.OperationID.String() || reduced.Streaming || reduced.BotOperations[final.OperationID.String()].Revision != final.Revision {
				t.Fatalf("watch/replay/snapshot regressed confirmation: %+v", reduced)
			}
		}
		if b.botComposerCaughtUpLocked() {
			t.Fatal("stale pending snapshot incorrectly satisfied final evidence")
		}
		if b.SelectedBot().Version != root.Version {
			t.Fatal("presentation changed composer authority")
		}
	}
}
