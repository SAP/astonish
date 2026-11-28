package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func botTriggerTestJob() *Job {
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Minute)
	return &Job{
		ID: uuid.NewString(), Name: "Bot schedule", Mode: ModeBotTrigger, Enabled: true,
		Schedule: JobSchedule{Cron: "* * * * *", Timezone: "UTC"}, NextRun: &due,
		Payload: JobPayload{BotID: uuid.NewString(), BotTriggerID: uuid.NewString(), BotVersion: 1},
		Scope:   "personal", OwnerID: uuid.NewString(), TeamSlug: "team-a",
		Delivery: JobDelivery{Channel: "email", Target: "must-not-deliver"},
	}
}

func TestBotTriggerOccurrenceIdentity(t *testing.T) {
	job := botTriggerTestJob()
	now := time.Now()
	first, err := botTriggerRequest(job, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var restored Job
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	// Reload, delayed dispatch and duplicate rows retain the same occurrence.
	restored.ID = uuid.NewString()
	local := restored.NextRun.In(time.FixedZone("offset", 5*3600))
	restored.NextRun = &local
	replay, err := botTriggerRequest(&restored, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotencyKey != replay.IdempotencyKey || replay.OccurrenceAt.Location() != time.UTC {
		t.Fatal("occurrence identity changed on replay")
	}
	if len(first.IdempotencyKey) > 200 {
		t.Fatal("key exceeds durable admission limit")
	}
	for _, change := range []func(*Job){
		func(j *Job) { next := j.NextRun.Add(time.Minute); j.NextRun = &next },
		func(j *Job) { j.Payload.BotVersion++ },
		func(j *Job) { j.Payload.BotID = uuid.NewString() },
		func(j *Job) { j.Payload.BotTriggerID = uuid.NewString() },
	} {
		cp := *job
		change(&cp)
		req, err := botTriggerRequest(&cp, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if req.IdempotencyKey == first.IdempotencyKey {
			t.Fatal("distinct occurrence collided")
		}
	}
}

func TestBotTriggerFailsClosed(t *testing.T) {
	job := botTriggerTestJob()
	if _, err := (&Executor{}).Execute(context.Background(), job); err == nil {
		t.Fatal("missing callback accepted")
	}
	for name, change := range map[string]func(*Job){
		"disabled":           func(j *Job) { j.Enabled = false },
		"missing-row":        func(j *Job) { j.ID = "" },
		"missing-occurrence": func(j *Job) { j.NextRun = nil },
		"zero-occurrence":    func(j *Job) { j.NextRun = new(time.Time) },
		"future-occurrence":  func(j *Job) { future := time.Now().Add(time.Hour); j.NextRun = &future },
		"invalid-bot":        func(j *Job) { j.Payload.BotID = "bad" },
		"nil-bot":            func(j *Job) { j.Payload.BotID = uuid.Nil.String() },
		"invalid-trigger":    func(j *Job) { j.Payload.BotTriggerID = "bad" },
		"missing-version":    func(j *Job) { j.Payload.BotVersion = 0 },
		"instructions":       func(j *Job) { j.Payload.Instructions = "untrusted" },
		"flow":               func(j *Job) { j.Payload.Flow = "untrusted" },
		"params":             func(j *Job) { j.Payload.Params = map[string]string{"x": "untrusted"} },
	} {
		t.Run(name, func(t *testing.T) {
			cp := *job
			change(&cp)
			called := false
			e := &Executor{BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) {
				called = true
				return BotAdmission{}, nil
			}}
			result, err := e.Execute(context.Background(), &cp)
			if err == nil || called || result != "" {
				t.Fatalf("invalid job dispatched: %q %v called=%v", result, err, called)
			}
		})
	}
}

// Uses the real scheduler tick, file store, executor routing, runtime-state update
// and delivery composition, with only the admission boundary controlled.
func TestBotTriggerTickAdmissionAndDelivery(t *testing.T) {
	for _, status := range []BotAdmissionStatus{BotAdmissionAdmitted, BotAdmissionDuplicate, BotAdmissionSkipped, "invalid", "error", "missing-callback"} {
		t.Run(string(status), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			jobs, err := NewStore(path)
			if err != nil {
				t.Fatal(err)
			}
			job := botTriggerTestJob()
			if err := jobs.Add(job); err != nil {
				t.Fatal(err)
			}
			// Prove payload and occurrence survive the existing scheduled-row store.
			jobs, err = NewStore(path)
			if err != nil {
				t.Fatal(err)
			}
			calls, delivered := 0, 0
			e := &Executor{BotTrigger: func(ctx context.Context, req BotTriggerRequest) (BotAdmission, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("admission has no timeout")
				}
				if req.BotID.String() != job.Payload.BotID || !req.OccurrenceAt.Equal(*job.NextRun) || req.OwnerID != job.OwnerID {
					t.Error("lost scheduled occurrence/binding")
				}
				if status == "error" {
					return BotAdmission{}, errors.New("admission unavailable")
				}
				receipt := BotAdmission{Status: status}
				if status == BotAdmissionAdmitted || status == BotAdmissionDuplicate {
					receipt.RunID = uuid.New()
				}
				return receipt, nil
			}}
			if status == "missing-callback" {
				e.BotTrigger = nil
			}
			s := New(jobs, e.Execute, WithoutBotTriggerDelivery(func(context.Context, *Job, string, error) error { delivered++; return nil }), log.New(io.Discard, "", 0))
			s.ctx = context.Background()
			s.tick()
			s.wg.Wait()
			stored := jobs.Get(job.ID)
			want := StatusSuccess
			if status == "invalid" || status == "error" || status == "missing-callback" {
				want = StatusFailed
			}
			if stored.LastStatus != want || stored.LastRun == nil || stored.NextRun == nil {
				t.Fatalf("unexpected runtime state: %+v", stored)
			}
			if want == StatusSuccess && !stored.NextRun.After(*job.NextRun) {
				t.Fatal("acknowledged admission did not advance occurrence")
			}
			if want == StatusFailed && !stored.NextRun.Equal(*job.NextRun) {
				t.Fatal("failed admission discarded retry occurrence")
			}
			if want == StatusFailed && (stored.LastError == "" || stored.ConsecutiveFailures != 1) {
				t.Fatal("lost failure accounting")
			}
			wantCalls := 1
			if status == "missing-callback" {
				wantCalls = 0
			}
			if calls != wantCalls || delivered != 0 {
				t.Fatalf("calls=%d deliveries=%d", calls, delivered)
			}
		})
	}
}

func TestBotTriggerReplayUsesSameAdmission(t *testing.T) {
	job := botTriggerTestJob()
	seen := map[string]uuid.UUID{}
	var keys []string
	e := &Executor{BotTrigger: func(_ context.Context, req BotTriggerRequest) (BotAdmission, error) {
		keys = append(keys, req.IdempotencyKey)
		if id, ok := seen[req.IdempotencyKey]; ok {
			return BotAdmission{Status: BotAdmissionDuplicate, RunID: id}, nil
		}
		id := uuid.New()
		seen[req.IdempotencyKey] = id
		return BotAdmission{Status: BotAdmissionAdmitted, RunID: id}, nil
	}}
	jobs, err := NewStore(filepath.Join(t.TempDir(), "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Add(job); err != nil {
		t.Fatal(err)
	}
	s := New(jobs, e.Execute, WithoutBotTriggerDelivery(func(context.Context, *Job, string, error) error {
		t.Error("replayed Bot occurrence reached legacy delivery")
		return nil
	}), log.New(io.Discard, "", 0))
	s.ctx = context.Background()
	for i := 0; i < 2; i++ {
		// Simulate a replay of the persisted occurrence (e.g. state-update loss).
		if err := jobs.Update(job); err != nil {
			t.Fatal(err)
		}
		s.tick()
		s.wg.Wait()
		if jobs.Get(job.ID).LastStatus != StatusSuccess {
			t.Fatal("occurrence admission failed")
		}
	}
	result, err := e.Execute(context.Background(), job)
	if err != nil || result != "" {
		t.Fatalf("admission invented a report: %q %v", result, err)
	}
	if len(seen) != 1 || len(keys) != 3 || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatal("replayed occurrence was not deduplicated")
	}
}

func TestBotTriggerReceiptAndTimeout(t *testing.T) {
	for _, receipt := range []BotAdmission{{}, {Status: BotAdmissionAdmitted}, {Status: BotAdmissionDuplicate}, {Status: BotAdmissionSkipped, RunID: uuid.New()}} {
		e := &Executor{BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) { return receipt, nil }}
		if result, err := e.Execute(context.Background(), botTriggerTestJob()); err == nil || result != "" {
			t.Fatalf("invalid receipt accepted: %+v", receipt)
		}
	}
	previous := jobTimeout
	jobTimeout = 10 * time.Millisecond
	t.Cleanup(func() { jobTimeout = previous })
	e := &Executor{BotTrigger: func(ctx context.Context, _ BotTriggerRequest) (BotAdmission, error) {
		<-ctx.Done()
		// Even a late successful receipt cannot bypass the timeout.
		return BotAdmission{Status: BotAdmissionAdmitted, RunID: uuid.New()}, nil
	}}
	if result, err := e.Execute(context.Background(), botTriggerTestJob()); err == nil || !strings.Contains(err.Error(), "timed out") || result != "" {
		t.Fatalf("timeout lost: %q %v", result, err)
	}
	called := false
	e.BotTrigger = func(context.Context, BotTriggerRequest) (BotAdmission, error) {
		called = true
		return BotAdmission{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Execute(ctx, botTriggerTestJob()); !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled admission dispatched")
	}
}

func TestBotTriggerLegacyTickCompatibility(t *testing.T) {
	for _, mode := range []JobMode{ModeRoutine, ModeAdaptive, ModeFleetPoll} {
		t.Run(string(mode), func(t *testing.T) {
			jobs, err := NewStore(filepath.Join(t.TempDir(), "jobs.json"))
			if err != nil {
				t.Fatal(err)
			}
			job := botTriggerTestJob()
			job.Mode = mode
			job.Payload = JobPayload{Flow: "legacy-flow", Instructions: "legacy-task"}
			if err := jobs.Add(job); err != nil {
				t.Fatal(err)
			}
			calls, delivered := 0, 0
			e := &Executor{
				BotTrigger: func(context.Context, BotTriggerRequest) (BotAdmission, error) {
					t.Error("legacy job entered Bot admission")
					return BotAdmission{}, nil
				},
				FlowResolver: func(string) (string, error) { return "legacy-yaml", nil },
				RunHeadless:  func(context.Context, *HeadlessRunConfig) (string, error) { calls++; return "legacy-result", nil },
				FleetPoll:    func(context.Context, string) (string, error) { calls++; return "legacy-result", nil },
			}
			s := New(jobs, e.Execute, WithoutBotTriggerDelivery(func(_ context.Context, _ *Job, result string, err error) error {
				delivered++
				if mode == ModeAdaptive {
					if err == nil || !strings.Contains(err.Error(), "no ChatAgent") {
						t.Error("adaptive route changed")
					}
				} else if result != "legacy-result" || err != nil {
					t.Error("legacy result changed")
				}
				return nil
			}), log.New(io.Discard, "", 0))
			s.ctx = context.Background()
			s.tick()
			s.wg.Wait()
			wantStatus, wantCalls, wantDeliveries := StatusSuccess, 1, 1
			if mode == ModeAdaptive {
				wantStatus, wantCalls = StatusFailed, 0
			}
			if mode == ModeFleetPoll {
				wantDeliveries = 0
			}
			if got := jobs.Get(job.ID); got.LastStatus != wantStatus || calls != wantCalls || delivered != wantDeliveries {
				t.Fatalf("mode=%s status=%s calls=%d delivery=%d", mode, got.LastStatus, calls, delivered)
			}
		})
	}
}
