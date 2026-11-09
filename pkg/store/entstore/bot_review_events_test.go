package entstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func reviewTranscript(t *testing.T, ctx context.Context, r *botRepository, b store.Bot) []store.BotTranscriptEvent {
	t.Helper()
	var reader store.BotTranscriptReader
	switch db := r.db.(type) {
	case personalBotDB:
		reader = &personalSessionStore{client: db.client}
	case teamBotDB:
		reader = &teamSessionStore{client: db.client}
	default:
		t.Fatal("unsupported fixture")
	}
	rows, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestBotReviewReferencesPersistOnce(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, worker, run, lease, now := botRunFixture(t, scope)
			b, _ := r.Get(ctx, run.BotID)
			text := "Discuss reviewed changes"
			q := store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "review", ExpiresAt: now.Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "review", Responsibility: &text}}
			p, err := r.ProposeControl(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.ProposeControl(worker, q); err != nil {
				t.Fatal(err)
			}
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact"}`)}
			digest, _ := action.Digest()
			a, err := r.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			approved, err := r.ListApprovals(ctx, b.ID, store.BotPage{})
			if err != nil || len(approved) != 1 || approved[0].ID != a.ID || approved[0].Status != store.ApprovalApproved {
				t.Fatalf("reference hydration lost approved record: %+v %v", approved, err)
			}
			for replay := 0; replay < 2; replay++ {
				rows := reviewTranscript(t, ctx, r, *b)
				refs := map[string]int{}
				for _, row := range rows {
					raw, ok := row.Event.Actions.StateDelta["bot_runtime_event"]
					if !ok {
						continue
					}
					encoded, _ := json.Marshal(raw)
					var event botdomain.RuntimeEvent
					if err := json.Unmarshal(encoded, &event); err != nil {
						t.Fatal(err)
					}
					if event.Type != "bot_review" {
						continue
					}
					var ref struct {
						Kind string `json:"kind"`
						ID   string `json:"review_id"`
					}
					if err := json.Unmarshal(event.Payload, &ref); err != nil {
						t.Fatal(err)
					}
					refs[ref.Kind+":"+ref.ID]++
				}
				if len(refs) != 2 || refs["proposal:"+p.ID.String()] != 1 || refs["approval:"+a.ID.String()] != 1 {
					t.Fatalf("review replay: %v", refs)
				}
			}
		})
	}
}
