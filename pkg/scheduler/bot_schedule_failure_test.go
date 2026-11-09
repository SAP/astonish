package scheduler

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"path/filepath"
	"testing"
	"time"
)

func TestBotScheduleFailureCallbackRace(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "managed"}[managed], func(t *testing.T) {
			jobs, err := NewStore(filepath.Join(t.TempDir(), "jobs.json"))
			if err != nil {
				t.Fatal(err)
			}
			job := botTriggerTestJob()
			if err := jobs.Add(job); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			e := &Executor{BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) {
				close(entered)
				<-release
				err := errors.New("temporary")
				if managed {
					return BotAdmission{}, &store.BotScheduleRejected{Cause: err, Outcome: store.BotScheduleRetry}
				}
				return BotAdmission{}, err
			}}
			s := New(jobs, e.Execute, nil, nil)
			done := make(chan error, 1)
			go func() { _, err := s.RunNow(context.Background(), job.ID); done <- err }()
			<-entered
			saved := jobs.Get(job.ID)
			saved.Enabled = false
			saved.Payload.BotVersion++
			future := time.Now().Add(time.Hour)
			saved.NextRun = &future
			if err := jobs.Update(saved); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-done; err == nil {
				t.Fatal("missing admission error")
			}
			after := jobs.Get(job.ID)
			if after.Enabled || after.Payload.BotVersion != saved.Payload.BotVersion || !after.NextRun.Equal(future) || after.LastRun != nil {
				t.Fatalf("failure overwrote change: %+v", after)
			}
		})
	}
}

func TestBotScheduleRetryRetainsDueAndBackoff(t *testing.T) {
	jobs, err := NewStore(filepath.Join(t.TempDir(), "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	job := botTriggerTestJob()
	if err := jobs.Add(job); err != nil {
		t.Fatal(err)
	}
	s := New(jobs, func(context.Context, *Job) (string, error) { return "", errors.New("temporary") }, nil, nil)
	for i := 1; i <= 8; i++ {
		if _, err := s.RunNow(context.Background(), job.ID); err == nil {
			t.Fatal("missing error")
		}
		saved := jobs.Get(job.ID)
		if !saved.Enabled || !saved.NextRun.Equal(*job.NextRun) || saved.ConsecutiveFailures != i {
			t.Fatalf("retry lost occurrence: %+v", saved)
		}
	}
	if s.backoffDuration(8) != time.Hour {
		t.Fatal("backoff cap changed")
	}
}

func TestBotScheduleManagedErrorReceipt(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		e := &Executor{BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) {
			return BotAdmission{}, &store.BotScheduleRejected{Cause: store.ErrBotForbidden, Outcome: store.BotScheduleTerminal, Acknowledged: acknowledged}
		}}
		ctx, ack := WithBotAcknowledgement(context.Background())
		_, err := e.Execute(ctx, botTriggerTestJob())
		if !errors.Is(err, store.ErrBotForbidden) || *ack != BotScheduleAcknowledged {
			t.Fatalf("managed error lost ownership: %v %v", err, *ack)
		}
	}
}
