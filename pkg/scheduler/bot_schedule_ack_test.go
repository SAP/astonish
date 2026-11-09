package scheduler

import (
	"context"
	"github.com/google/uuid"
	"path/filepath"
	"testing"
	"time"
)

func TestBotScheduleAcknowledgementPreservesEdits(t *testing.T) {
	jobs, err := NewStore(filepath.Join(t.TempDir(), "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	job := botTriggerTestJob()
	if err := jobs.Add(job); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(24 * time.Hour)
	e := &Executor{BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) {
		saved := jobs.Get(job.ID)
		saved.Enabled = false
		saved.Payload.BotVersion++
		saved.NextRun = &future
		if err := jobs.Update(saved); err != nil {
			t.Fatal(err)
		}
		return BotAdmission{Status: BotAdmissionAdmitted, RunID: uuid.New(), Acknowledgement: BotScheduleAcknowledged}, nil
	}}
	s := New(jobs, e.Execute, nil, nil)
	if _, err := s.RunNow(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	saved := jobs.Get(job.ID)
	if saved.Enabled || saved.Payload.BotVersion != job.Payload.BotVersion+1 || !saved.NextRun.Equal(future) || saved.LastRun != nil {
		t.Fatal("outer scheduler overwrote Bot-managed acknowledgement")
	}
}
