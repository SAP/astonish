package entstore

import (
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func separateRunStates(t *testing.T, rows []store.BotTranscriptEvent, want int) []store.BotTranscriptEvent {
	t.Helper()
	out := []store.BotTranscriptEvent{}
	count := 0
	for _, row := range rows {
		raw, ok := row.Event.Actions.StateDelta[store.BotRunStateKey]
		if !ok {
			out = append(out, row)
			continue
		}
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var s store.BotRunState
		if json.Unmarshal(data, &s) != nil || s.SchemaVersion != 1 || s.StateSequence < 1 || row.Event.Content != nil || row.Event.Author != "bot_runtime" {
			t.Fatalf("invalid state: %+v", row)
		}
		count++
	}
	if count != want {
		t.Fatalf("state envelopes=%d want=%d", count, want)
	}
	return out
}

func TestBotRunStatePersistenceSnapshotRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, b := messageReviewDraft(t, scope)
			q := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "state", Instructions: "hello"}
			receipt, err := repo.AdmitMessage(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			reader := messageReviewReader(repo)
			rows, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			visible := separateRunStates(t, rows, 1)
			if len(visible) != 1 {
				t.Fatal("user message lost")
			}
			snapshot, err := repo.Snapshot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.RunStates) != 1 || snapshot.RunStates[0].Status != store.RunQueued || snapshot.HistoryCursor != rows[len(rows)-1].Cursor {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			sentinel := errors.New("rollback")
			err = repo.db.Transaction(ctx, func(tx botDraftDB) error {
				if e := appendBotRunStateTx(ctx, tx, *b, receipt.Run, snapshot.LatestSequence+1, repo.clock()); e != nil {
					return e
				}
				return sentinel
			})
			if !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err = repo.Snapshot(ctx, b.ID); err != nil {
					t.Fatal(err)
				}
			}
			after, err := reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			if err != nil || len(after) != len(rows) {
				t.Fatal("read or rollback wrote events", err)
			}
			if _, err = repo.AdmitMessage(ctx, q); err != nil {
				t.Fatal(err)
			}
			after, err = reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			if err != nil || len(after) != len(rows) {
				t.Fatal("replay wrote events", err)
			}
			if _, err = repo.CancelRun(ctx, b.ID, receipt.Run.ID); err != nil {
				t.Fatal(err)
			}
			after, err = reader.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			separateRunStates(t, after, 2)
		})
	}
}
