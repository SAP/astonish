package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type publicationReconcileStore struct {
	store.BotStore
	store.BotPublicationStore
	activities []store.BotActivity
	seen       []store.BotPublicationRequest
}

func (s *publicationReconcileStore) ListActivity(_ context.Context, _ store.BotID, after int64, limit int) ([]store.BotActivity, error) {
	var out []store.BotActivity
	for _, a := range s.activities {
		if a.Sequence > after {
			out = append(out, a)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (s *publicationReconcileStore) PublishReport(_ context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	s.seen = append(s.seen, q)
	return &store.BotPublication{BotID: q.BotID, RunID: q.RunID, ReportSequence: q.ReportSequence}, nil
}
func TestBotPublicationReconciliation(t *testing.T) {
	id, run := uuid.New(), uuid.New()
	s := &publicationReconcileStore{}
	for i := int64(1); i <= 201; i++ {
		a := store.BotActivity{BotID: id, RunID: &run, Sequence: i, Kind: store.BotActivityReport, Payload: json.RawMessage(`{"text":"publication receipt, not an outcome"}`)}
		if i == 1 || i == 201 {
			a.Payload, _ = json.Marshal(map[string]any{"intent": store.BotOutboxPublish, "report": store.BotReport{Summary: "Committed outcome"}})
		}
		s.activities = append(s.activities, a)
	}
	for i := 0; i < 2; i++ {
		if err := ReconcileBotPublications(context.Background(), s, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.seen) != 4 {
		t.Fatalf("lost or misclassified outcomes: %+v", s.seen)
	}
	for i, q := range s.seen {
		want := int64(1)
		if i%2 == 1 {
			want = 201
		}
		if q.ReportSequence != want || q.RunID != run {
			t.Fatalf("wrong identity: %+v", q)
		}
	}
}
