package entstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

func TestBotHomeArtifactEventBoundary(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), SessionID: uuid.New()}
	for _, tc := range []struct {
		name, author, kind             string
		wrongRun, wrongSession, noPath bool
		want                           bool
	}{
		{name: "artifact", author: "bot_runtime", kind: "artifact", want: true},
		{name: "model impersonation", author: "chat", kind: "artifact"},
		{name: "internal state", author: "bot_runtime", kind: "session"},
		{name: "wrong run", author: "bot_runtime", kind: "artifact", wrongRun: true},
		{name: "wrong session", author: "bot_runtime", kind: "artifact", wrongSession: true},
		{name: "missing path", author: "bot_runtime", kind: "artifact", noPath: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := bot.RuntimeEvent{RunID: run.ID, SessionID: run.SessionID, Type: tc.kind, Payload: json.RawMessage(`{"path":"/root/proof.txt"}`)}
			if tc.wrongRun {
				e.RunID = uuid.New()
			}
			if tc.wrongSession {
				e.SessionID = uuid.New()
			}
			if tc.noPath {
				e.Payload = json.RawMessage(`{}`)
			}
			raw, _ := json.Marshal(e)
			event := &session.Event{Author: tc.author, Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": json.RawMessage(raw)}}}
			if got := botHomeArtifactEvent(run, event); got != tc.want {
				t.Fatalf("projection=%v want=%v", got, tc.want)
			}
		})
	}
}
func TestBotHomeArtifactProjection(t *testing.T)      { testBotHomeArtifactProjection(t, false) }
func TestBotWorkDraftArtifactProjection(t *testing.T) { testBotHomeArtifactProjection(t, true) }

func testBotHomeArtifactProjection(t *testing.T, workEnabled bool) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, initial, _ := runtimeFixture(t, scope)
			b, err := repo.Get(ctx, initial.BotID)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "attachment", Instructions: "Attach file"})
			if err != nil {
				t.Fatal(err)
			}
			run := &receipt.Run
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "attachment", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if !workEnabled {
				legacyWorkFixture(t, repo, worker, lease)
			}
			var reader store.BotTranscriptReader
			switch d := repo.db.(type) {
			case personalBotDB:
				reader = &personalSessionStore{client: d.client}
			case teamBotDB:
				reader = &teamSessionStore{client: d.client}
			}
			before, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			var cursor int64
			if len(before) > 0 {
				cursor = before[len(before)-1].Cursor
			}
			turn := uuid.New()
			raw, _ := json.Marshal(bot.RuntimeEvent{SessionID: run.SessionID, RunID: run.ID, TurnID: turn, Sequence: 1, Type: "artifact", Payload: json.RawMessage(`{"path":"/root/proof.txt","tool_name":"load_file"}`)})
			event := &session.Event{ID: uuid.NewSHA1(turn, []byte("1")).String(), Author: "bot_runtime", InvocationID: turn.String(), Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": json.RawMessage(raw)}}}
			for i := 0; i < 2; i++ {
				if err := repo.appendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
					t.Fatal(err)
				}
			}
			for _, sid := range []uuid.UUID{b.HomeSessionID, run.SessionID} {
				rows, err := reader.ReadBotTranscript(ctx, sid, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, row := range rows {
					if row.Event.ID == event.ID {
						count++
						if row.Event.Content != nil {
							t.Fatal("artifact became model content")
						}
					}
				}
				want := 1
				if workEnabled && sid == b.HomeSessionID {
					want = 0
				}
				if count != want {
					t.Errorf("session %s: expected %d durable artifacts, got %d", sid, want, count)
				}
			}
			live, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, cursor, 200)
			wantLive := 1
			if workEnabled {
				wantLive = 0
			}
			if err != nil || len(live) != wantLive {
				t.Fatalf("live home artifact count: got %d want %d error %v", len(live), wantLive, err)
			}
			otherVersion := store.DefaultBotVersion()
			otherVersion.Charter = "Unrelated Bot"
			other, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Other Bot", OwnerID: b.OwnerID, Version: otherVersion, IdempotencyKey: "other-artifact-bot"})
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.appendBotRunEvent(worker, lease, other.HomeSessionID, event); err == nil {
				t.Fatal("cross-Bot artifact accepted")
			}
			otherRows, err := reader.ReadBotTranscript(ctx, other.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range otherRows {
				if row.Event.ID == event.ID {
					t.Fatal("artifact leaked to other Bot")
				}
			}
			if err := repo.appendBotRunEvent(worker, lease, uuid.New(), event); err == nil {
				t.Fatal("cross-session artifact accepted")
			}
		})
	}
}
