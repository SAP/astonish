package entstore

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotKnowledgeExtractionAtomicEnqueueHook(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, c, input, _ := knowledgeStorageFixture(t, scope)
			rollback := errors.New("result transaction aborted")
			err := r.db.Transaction(c, func(tx botDB) error {
				if _, err := r.enqueueBotKnowledgeExtractionTx(c, tx, input); err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			if _, err := r.ReadBotKnowledgeExtraction(c, input.Source); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("orphan queue after rollback: %v", err)
			}
			err = r.db.Transaction(c, func(tx botDB) error {
				_, err := r.enqueueBotKnowledgeExtractionTx(c, tx, input)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			record, err := r.ReadBotKnowledgeExtraction(c, input.Source)
			if err != nil || record.State != store.BotKnowledgeExtractionPending {
				t.Fatalf("committed queue %+v %v", record, err)
			}
			// The source is already terminal. Enqueue must not acquire or renew it.
			source, err := r.db.ReadRun(c, input.Source.BotID, input.Source.RunID)
			if err != nil || !source.Run.Status.Terminal() {
				t.Fatalf("source changed %+v %v", source, err)
			}
		})
	}
}
