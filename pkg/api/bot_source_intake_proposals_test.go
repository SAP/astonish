package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"testing"
	"time"
)

func TestBotSourceIntakeProposalHTTPAcceptance(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			sid := uuid.New()
			if err := f.sessions(scope).AddSessionMeta(context.Background(), store.SessionMeta{ID: sid.String(), UserID: f.owner, Purpose: store.SessionPurposeChat, Title: "Source", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			suffix := "?scope=" + scope
			path := "/api/studio/bots/source-proposals"
			before, err := f.sessions(scope).GetSessionMeta(context.Background(), sid.String())
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"mode": "chat", "source_id": sid, "name": "Reviewed source", "charter": "Observe", "idempotency_key": uuid.NewString(), "carryover": map[string]any{"summary": "Selected summary", "memories": []map[string]string{{"content": "Selected memory", "category": "fact"}}, "goals": []map[string]string{{"title": "Selected goal", "completion_criteria": "Verified"}}, "artifacts": []map[string]string{{"title": "Selected text", "content": "Redacted artifact"}}}}
			var p map[string]any
			if err := json.Unmarshal(f.request("POST", path+suffix, body, 201), &p); err != nil {
				t.Fatal(err)
			}
			if p["status"] != "proposed" || p["bot_id"] != nil {
				t.Fatalf("not inert: %+v", p)
			}
			decision := path + "/" + p["id"].(string) + "/decision" + suffix
			f.request("POST", decision, map[string]any{"digest": p["digest"], "decision": "confirm", "actor_id": f.owner}, 400)
			f.request("POST", decision, map[string]any{"digest": "wrong", "decision": "confirm", "reviewed_for_team": true}, 409)
			f.request("POST", decision, map[string]any{"digest": p["digest"], "decision": "confirm", "carryover": map[string]any{"summary": "replacement"}}, 400)
			if scope == "team" {
				f.request("POST", decision, map[string]any{"digest": p["digest"], "decision": "confirm"}, 403)
			}
			var applied map[string]any
			q := map[string]any{"digest": p["digest"], "decision": "confirm", "reviewed_for_team": true}
			if err := json.Unmarshal(f.request("POST", decision, q, 200), &applied); err != nil {
				t.Fatal(err)
			}
			if applied["status"] != "applied" || applied["bot_id"] == nil {
				t.Fatalf("not applied: %+v", applied)
			}
			after, err := f.sessions(scope).GetSessionMeta(context.Background(), sid.String())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("source Chat changed: %+v %+v %v", before, after, err)
			}
			var bot store.Bot
			json.Unmarshal(f.request("GET", "/api/studio/bots/"+applied["bot_id"].(string)+suffix, nil, 200), &bot)
			if bot.Status != store.BotDraft {
				t.Fatal("promotion activated")
			}
			var retry map[string]any
			json.Unmarshal(f.request("POST", decision, q, 200), &retry)
			if retry["bot_id"] != applied["bot_id"] {
				t.Fatal("duplicate")
			}
			f.request("GET", path+"/"+p["id"].(string)+suffix, nil, 200)
			f.request("GET", "/api/studio/bots/intake/"+p["id"].(string)+suffix, nil, 409)
			f.request("PUT", "/api/studio/bots/intake/"+p["id"].(string)+suffix, store.BotUnfinishedPut{ExpectedRevision:1,IdempotencyKey:"overwrite",Content:store.BotUnfinishedContent{Mode:store.BotIntakeBlank,Name:"Overwrite"}},409)
			f.request("DELETE", "/api/studio/bots/intake/"+p["id"].(string)+suffix, store.BotUnfinishedDelete{ExpectedRevision:1,IdempotencyKey:"discard"},409)
			body["charter"] = "different"
			f.request("POST", path+suffix, body, 409)
			body["idempotency_key"] = uuid.NewString()
			var stale store.BotSourceIntakeProposal
			json.Unmarshal(f.request("POST", path+suffix, body, 201), &stale)
			if err := f.sessions(scope).UpdateSessionMeta(context.Background(), sid.String(), func(meta *store.SessionMeta) { meta.UpdatedAt = time.Now().Add(time.Second); meta.MessageCount++ }); err != nil {
				t.Fatal(err)
			}
			var rejected store.BotSourceIntakeProposal
			json.Unmarshal(f.request("POST", path+"/"+stale.ID.String()+"/decision"+suffix, store.BotSourceIntakeDecision{Digest: stale.Digest, Decision: "confirm", ReviewedForTeam: true}, 200), &rejected)
			if rejected.Status != store.BotSourceIntakeStale || rejected.BotID != nil {
				t.Fatalf("stale applied: %+v", rejected)
			}
		})
	}
}
