package scheduler

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotGoalDeadlineOneShotTick(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	job := botTriggerTestJob()
	job.Schedule.Cron = store.BotDeadlineSchedule
	if err := jobs.Add(job); err != nil {
		t.Fatal(err)
	}
	calls := 0
	executor := &Executor{BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) {
		calls++
		return BotAdmission{Status: BotAdmissionAdmitted, RunID: uuid.New()}, nil
	}}
	s := New(jobs, executor.Execute, WithoutBotTriggerDelivery(nil), log.New(io.Discard, "", 0))
	s.ctx = context.Background()
	s.tick()
	s.wg.Wait()
	if saved := jobs.Get(job.ID); calls != 1 || saved.NextRun != nil || saved.LastStatus != StatusSuccess {
		t.Fatalf("not consumed: %+v calls=%d", saved, calls)
	}
	jobs, err = NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s = New(jobs, executor.Execute, WithoutBotTriggerDelivery(nil), log.New(io.Discard, "", 0))
	s.ctx = context.Background()
	s.refreshNextRuns()
	s.tick()
	s.wg.Wait()
	if calls != 1 || jobs.Get(job.ID).NextRun != nil {
		t.Fatal("restart replayed consumed deadline")
	}
}
