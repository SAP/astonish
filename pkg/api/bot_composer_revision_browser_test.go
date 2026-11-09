package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

// Production scoped HTTP, SQLite, worker commits and native built-SPA composer.
// The shared fixture controls only identity/model/home provisioning, not Bot APIs.
func TestBotComposerRevisionBrowserAcceptance(t *testing.T) { runEmployeeBrowser(t, "revision") }

type composerRevisionResponse struct {
	version int64
	status  int
}
type composerRevisionProbe struct {
	calls     atomic.Int64
	race      atomic.Bool
	responses chan composerRevisionResponse
}

func newComposerRevisionProbe() *composerRevisionProbe {
	return &composerRevisionProbe{responses: make(chan composerRevisionResponse, 10)}
}
func (p *composerRevisionProbe) serve(t *testing.T, w http.ResponseWriter, r *http.Request, next http.Handler, repo store.BotStore, ctx context.Context, id uuid.UUID) {
	p.calls.Add(1)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
		http.Error(w, "read failed", 500)
		return
	}
	var body struct {
		Version int64 `json:"version"`
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Error(err)
		http.Error(w, "decode failed", 500)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if p.race.Swap(false) {
		// A real concurrent owner mutation after the browser captured its version.
		b, e := repo.Get(ctx, id)
		if e == nil {
			b, e = repo.Pause(ctx, store.BotLifecycleRequest{BotID: id, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Concurrent owner edit"})
		}
		if e == nil {
			v, readErr := repo.GetVersion(ctx, id, b.Version)
			e = readErr
			if e == nil {
				v.Charter += "\nConcurrent owner clarification."
				_, e = repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: id, ExpectedRevision: b.Revision, Version: *v})
			}
		}
		if e != nil {
			t.Error(e)
			http.Error(w, "concurrent fixture failed", 500)
			return
		}
	}
	recorded := httptest.NewRecorder()
	next.ServeHTTP(recorded, r)
	for key, values := range recorded.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(recorded.Code)
	_, _ = w.Write(recorded.Body.Bytes())
	p.responses <- composerRevisionResponse{body.Version, recorded.Code}
}
func (p *composerRevisionProbe) check(t *testing.T, page *rod.Page, repo store.BotStore, ctx context.Context, initial store.Bot) {
	t.Helper()
	receive := func() composerRevisionResponse {
		select {
		case response := <-p.responses:
			return response
		case <-time.After(10 * time.Second):
			t.Fatal("composer request not received")
			return composerRevisionResponse{}
		}
	}
	_ = receive() // first schedule instruction
	current, err := repo.Get(ctx, initial.ID)
	if err != nil || current.Version <= initial.Version {
		t.Fatalf("schedule did not advance root: %+v %v", current, err)
	}
	// No reload, Details, synthetic click or forced refresh between Applied and send.
	page.MustElement(`[data-testid="chat-input"]`).MustInput("What should we do next?")
	page.MustElement(`[data-testid="send-button"]`).MustClick()
	sent := receive()
	if sent.version != current.Version || sent.status != http.StatusAccepted {
		t.Fatalf("post-applied send=%+v want version=%d status=202", sent, current.Version)
	}
	page.MustWait(`() => document.querySelector('[data-testid="chat-input"]').value === ''`)
	runs, err := repo.ListRuns(ctx, initial.ID, store.BotPage{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("next message not durably admitted: %v %v", runs, err)
	}
	p.race.Store(true)
	page.MustElement(`[data-testid="chat-input"]`).MustInput("Keep this concurrent instruction")
	page.MustElement(`[data-testid="send-button"]`).MustClick()
	conflict := receive()
	if conflict.version != current.Version || conflict.status != http.StatusConflict {
		t.Fatalf("concurrent conflict: %+v", conflict)
	}
	page.MustElementR(`[aria-label="Bot workspace"] [role="alert"]`, "Bot changed; reload and review before retrying").MustVisible()
	page.MustWait(`() => !document.querySelector('[data-testid="send-button"]').disabled`)
	if page.MustEval(`() => document.querySelector('[data-testid="chat-input"]').value`).Str() != "Keep this concurrent instruction" {
		t.Fatal("conflict erased draft")
	}
	if p.calls.Load() != 3 {
		t.Fatalf("automatic replay: %d POSTs", p.calls.Load())
	}
	runs, err = repo.ListRuns(ctx, initial.ID, store.BotPage{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("conflict admitted extra run: %v %v", runs, err)
	}
	t.Logf("PASS applied schedule -> next send version %d HTTP %d, 2 durable runs; concurrent mutation HTTP %d, draft retained, exactly 3 total POSTs, no refresh/replay", sent.version, sent.status, conflict.status)
}
