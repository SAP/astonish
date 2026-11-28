package daemon

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type hookAdmissionStore struct {
	schedulerAdmissionStore
	result *store.BotScheduledAdmission
}

func (s hookAdmissionStore) PollScheduledHook(context.Context, store.BotScheduledOccurrence) (bool, *store.BotScheduledAdmission, error) {
	return true, s.result, nil
}

func TestBotSchedulerPollDispatch(t *testing.T) {
	for _, changed := range []bool{false, true} {
		scoped := hookAdmissionStore{schedulerAdmissionStore: schedulerAdmissionStore{admit: func(context.Context, store.BotScheduledOccurrence) (*store.BotScheduledAdmission, error) {
			t.Fatal("poll fell through to cron admission")
			return nil, nil
		}}}
		if changed {
			scoped.result = &store.BotScheduledAdmission{Run: store.BotRun{ID: uuid.New()}}
		}
		callbacks := BotSchedulerCallbacks{Runtime: admissionOnlyRuntime{}, Resolve: func(ctx context.Context, _ scheduler.BotTriggerRequest) (context.Context, store.BotScheduledRunStore, error) {
			return ctx, scoped, nil
		}}
		receipt, err := callbacks.Admission()(context.Background(), scheduler.BotTriggerRequest{})
		if err != nil {
			t.Fatal(err)
		}
		want := scheduler.BotAdmissionSkipped
		if changed {
			want = scheduler.BotAdmissionAdmitted
		}
		if receipt.Status != want {
			t.Fatalf("got %s want %s", receipt.Status, want)
		}
	}
}
