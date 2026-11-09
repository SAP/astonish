package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestBotOperationViewSnapshotReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, s, owner, worker, q := authorizedScheduleFixture(t, scope)
			empty, err := r.Snapshot(owner, q.MandateRef.BotID)
			if err != nil || empty.Operations == nil || len(empty.Operations) != 0 {
				t.Fatalf("empty snapshot: %v", err)
			}
			notices := []store.BotOperationView{}
			worker = store.WithBotOperationNotifier(worker, func(_ context.Context, v store.BotOperationView) { notices = append(notices, v) })
			o, err := s.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if len(notices) != 1 || notices[0].OperationID != o.ID {
				t.Fatalf("notification: %+v", notices)
			}
			if _, err = s.EnqueueAuthorizedOperation(worker, q); err != nil || len(notices) != 1 {
				t.Fatalf("duplicate: %v", err)
			}
			_, view, err := r.ReadOperationForViewer(owner, q.MandateRef.BotID, o.ID)
			if err != nil || view.OperationID != o.ID {
				t.Fatalf("viewer lookup: %v", err)
			}
			if _, _, err = r.ReadOperationForViewer(context.Background(), q.MandateRef.BotID, o.ID); err == nil {
				t.Fatal("unauthorized viewer")
			}
			snapshot, err := r.Snapshot(owner, q.MandateRef.BotID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Operations) != 1 || snapshot.Operations[0] != notices[0] {
				t.Fatalf("snapshot drift: %+v", snapshot.Operations)
			}
			var reader store.BotTranscriptReader
			switch db := r.db.(type) {
			case personalBotDB:
				reader = &personalSessionStore{client: db.client}
			case teamBotDB:
				reader = &teamSessionStore{client: db.client}
			}
			rows, err := reader.ReadBotTranscript(owner, snapshot.Bot.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, row := range rows {
				if value, ok := row.Event.Actions.StateDelta[store.BotOperationKey]; ok {
					count++
					raw, _ := json.Marshal(value)
					var v store.BotOperationView
					if json.Unmarshal(raw, &v) != nil || v != notices[0] {
						t.Fatal("replay drift")
					}
				}
			}
			if count != 1 {
				t.Fatalf("replay count %d", count)
			}
			// An outer rollback discards both the durable envelope and queued wakeup.
			rollback := errors.New("rollback")
			err = r.db.Transaction(worker, func(tx botDraftDB) error {
				rec, e := tx.GetBot(worker, q.MandateRef.BotID)
				if e != nil {
					return e
				}
				copy := *o
				copy.Revision++
				copy.Status = store.BotOperationApplying
				if e = appendMandateRecord(worker, tx, rec, operationKey(copy.ID, copy.Revision), copy, s.repo.clock()); e != nil {
					return e
				}
				return rollback
			})
			if !errors.Is(err, rollback) || len(notices) != 1 {
				t.Fatalf("rollback: %v notices %d", err, len(notices))
			}
		})
	}
}
