package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotHomeMessageAndDurableDecisionContracts(t *testing.T) {
	id, pid, aid, sid, rid := uuid.NewString(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	proposal := store.BotControlProposal{ID: pid, Digest: "saved", Status: store.BotControlProposed}
	receipt := store.BotMessageReceipt{Run: store.BotRun{ID: rid}, HomeSessionID: sid, UserEventID: "user-event", HistoryCursor: 42}
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Query().Get("scope") != "team" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("auth/scope: %s", r.URL)
		}
		var body map[string]any
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		check := func(want map[string]any) {
			if r.Method != http.MethodPost || !reflect.DeepEqual(body, want) {
				t.Errorf("%s body %#v want %#v", r.Method, body, want)
			}
		}
		switch r.URL.Path {
		case "/api/studio/bots/" + id + "/messages":
			check(map[string]any{"version": float64(7), "idempotency_key": "key", "instructions": "hello"})
			_ = json.NewEncoder(w).Encode(receipt)
		case "/api/studio/bots/" + id + "/proposals":
			if r.Method != http.MethodGet {
				t.Error(r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"proposals": []store.BotControlProposal{proposal}})
		case "/api/studio/bots/" + id + "/proposals/" + pid.String() + "/decision":
			check(map[string]any{"digest": "saved", "decision": "confirm"})
			_ = json.NewEncoder(w).Encode(proposal)
		case "/api/studio/bots/" + id + "/approvals/" + aid.String() + "/approve-and-continue":
			check(map[string]any{"action_digest": "action"})
			_ = json.NewEncoder(w).Encode(store.BotApproveContinueResult{RunID: rid, Continuation: store.BotContinuationBlockedByPause})
		default:
			t.Errorf("unexpected mutation %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := NewWithConfig(s.URL)
	c.cached = &Tokens{AccessToken: "token"}
	b := Bots{Client: c, Scope: store.BotScopeTeam}
	ctx := context.Background()
	got, err := b.SendMessage(ctx, id, 7, "key", "hello")
	if err != nil || !reflect.DeepEqual(got, &receipt) {
		t.Fatalf("receipt %+v %v", got, err)
	}
	ps, err := b.Proposals(ctx, id)
	if err != nil || len(ps) != 1 || ps[0].Digest != "saved" {
		t.Fatalf("proposals %+v %v", ps, err)
	}
	p, err := b.DecideProposal(ctx, id, pid.String(), "saved", BotProposalConfirm)
	if err != nil || p.ID != pid {
		t.Fatalf("proposal %+v %v", p, err)
	}
	a, err := b.ApproveAndContinue(ctx, id, aid.String(), "action", nil)
	if err != nil || a.Continuation != store.BotContinuationBlockedByPause || a.RunID != rid {
		t.Fatalf("approval %+v %v", a, err)
	}
	if _, err = b.DecideProposal(ctx, id, pid.String(), "", BotProposalConfirm); err == nil {
		t.Fatal("empty digest accepted")
	}
	if _, err = b.DecideProposal(ctx, id, pid.String(), "saved", BotProposalDecision("activate")); err == nil {
		t.Fatal("invalid decision accepted")
	}
	if _, err = b.ApproveAndContinue(ctx, id, aid.String(), "", nil); err == nil {
		t.Fatal("empty action digest accepted")
	}
	if len(paths) != 4 {
		t.Fatalf("extra requests: %v", paths)
	}
}
