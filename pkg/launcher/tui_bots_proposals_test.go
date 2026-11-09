package launcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotReviewAdaptersBoundToSelectedBot(t *testing.T) {
	id, pid, aid := uuid.New(), uuid.New(), uuid.New()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("scope") != "team" {
			t.Error("scope lost")
		}
		var q map[string]any
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&q)
		}
		switch r.URL.Path {
		case "/api/studio/bots/" + id.String() + "/proposals":
			_ = json.NewEncoder(w).Encode(map[string]any{"proposals": []store.BotControlProposal{{ID: pid, BotID: id}}})
		case "/api/studio/bots/" + id.String() + "/proposals/" + pid.String() + "/decision":
			if len(q) != 2 || q["digest"] != "proposal-digest" || q["decision"] != "reject" {
				t.Errorf("proposal: %#v", q)
			}
			_ = json.NewEncoder(w).Encode(store.BotControlProposal{ID: pid})
		case "/api/studio/bots/" + id.String() + "/approvals/" + aid.String() + "/approve-and-continue":
			if len(q) != 1 || q["action_digest"] != "action-digest" {
				t.Errorf("approval: %#v", q)
			}
			_ = json.NewEncoder(w).Encode(store.BotApproveContinueResult{Continuation: store.BotContinuationBlockedByPause})
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	b := newBotPlatformBackend(&platformBackend{client: client.NewWithConfig(server.URL)}, "team")
	b.selected = &store.Bot{ID: id, Status: store.BotPaused}
	ctx := context.Background()
	p := store.BotControlProposal{ID: pid, BotID: id, Digest: "proposal-digest"}
	a := store.BotApproval{ID: aid, BotID: id, ActionDigest: "action-digest"}
	ps, err := b.BotProposals(ctx)
	if err != nil || len(ps) != 1 {
		t.Fatalf("%+v %v", ps, err)
	}
	if err = b.DecideBotProposal(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	result, err := b.ApproveBotAndContinue(ctx, a, nil)
	if err != nil || result.Continuation != store.BotContinuationBlockedByPause || b.SelectedBot().Status != store.BotPaused {
		t.Fatalf("%+v %v", result, err)
	}
	p.BotID = uuid.New()
	a.BotID = p.BotID
	if b.DecideBotProposal(ctx, p, true) == nil {
		t.Fatal("cross-bot proposal")
	}
	if _, err = b.ApproveBotAndContinue(ctx, a, nil); err == nil {
		t.Fatal("cross-bot action")
	}
	b.DetachBot()
	if _, err = b.BotProposals(ctx); err == nil {
		t.Fatal("detached review")
	}
	if requests != 3 {
		t.Fatalf("unexpected lifecycle/continuation requests: %d", requests)
	}
}
