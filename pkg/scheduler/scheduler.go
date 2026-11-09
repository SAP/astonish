package scheduler

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/robfig/cron/v3"
)

// backoffSteps defines the error backoff delays for consecutive failures.
var backoffSteps = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	60 * time.Minute,
}

// ExecuteFunc is the function called to execute a job.
// It receives the job and returns the result text and any error.
type ExecuteFunc func(ctx context.Context, job *Job) (string, error)

// DeliverFunc is the function called to deliver job results to a channel.
type DeliverFunc func(ctx context.Context, job *Job, result string, err error) error

// Scheduler manages the lifecycle and execution of scheduled jobs.
type Scheduler struct {
	store   JobStore
	execute ExecuteFunc
	deliver DeliverFunc
	logger  *log.Logger
	parser  cron.Parser

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex          // protects concurrent RunNow calls
	running map[string]struct{} // tracks in-flight job IDs to prevent double dispatch
	runMu   sync.Mutex          // protects the running map
}

// New creates a new Scheduler.
func New(store JobStore, execute ExecuteFunc, deliver DeliverFunc, logger *log.Logger) *Scheduler {
	if logger == nil {
		logger = log.Default()
	}
	return &Scheduler{
		store:   store,
		execute: execute,
		deliver: deliver,
		logger:  logger,
		parser:  cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		running: make(map[string]struct{}),
	}
}

// Start begins the scheduler tick loop. It blocks until Stop is called
// or the context is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Compute next-run times for all enabled jobs on startup
	s.refreshNextRuns()

	s.wg.Add(1)
	go s.loop()

	s.logger.Printf("[scheduler] Started with %d job(s)", len(s.store.List()))
}

// Stop gracefully shuts down the scheduler and waits for in-flight jobs.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.logger.Printf("[scheduler] Stopped")
}

// Store returns the underlying job store (for tools/API access).
func (s *Scheduler) Store() JobStore {
	return s.store
}

// RunNow triggers immediate execution of a job by ID.
// Returns the result text and any execution error.
func (s *Scheduler) RunNow(ctx context.Context, jobID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job := s.store.Get(jobID)
	if job == nil {
		return "", fmt.Errorf("job %s not found", jobID)
	}

	return s.executeJob(ctx, job)
}

// loop is the main tick loop. It checks for due jobs every 30 seconds.
func (s *Scheduler) loop() {
	defer s.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick checks all enabled jobs and dispatches those that are due.
func (s *Scheduler) tick() {
	now := time.Now()
	jobs := s.store.List()

	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		if job.NextRun == nil {
			continue
		}
		if now.Before(*job.NextRun) {
			continue
		}

		// Check backoff
		if job.ConsecutiveFailures > 0 && job.LastRun != nil {
			backoff := s.backoffDuration(job.ConsecutiveFailures)
			if now.Before(job.LastRun.Add(backoff)) {
				continue // Still in backoff
			}
		}

		// Skip if this job is already running (prevents double dispatch)
		s.runMu.Lock()
		if _, alreadyRunning := s.running[job.ID]; alreadyRunning {
			s.runMu.Unlock()
			s.logger.Printf("[scheduler] Job %q still running, skipping tick", job.Name)
			continue
		}
		s.running[job.ID] = struct{}{}
		s.runMu.Unlock()

		// Execute in a goroutine (non-blocking tick)
		s.wg.Add(1)
		go func(j *Job) {
			defer s.wg.Done()
			defer func() {
				s.runMu.Lock()
				delete(s.running, j.ID)
				s.runMu.Unlock()
			}()

			s.mu.Lock()
			result, err := s.executeJob(s.ctx, j)
			s.mu.Unlock()

			// Deliver results to configured channel.
			// Fleet poll jobs handle their own communication (via the fleet
			// channel), so we skip the scheduler's delivery pipeline for them.
			if s.deliver != nil && j.Mode != ModeFleetPoll {
				if deliverErr := s.deliver(s.ctx, j, result, err); deliverErr != nil {
					s.logger.Printf("[scheduler] Delivery failed for job %q: %v", j.Name, deliverErr)
				} else {
					s.logger.Printf("[scheduler] Delivered job %q", j.Name)
				}
			}
		}(job)
	}
}

// executeJob runs a single job, updates its runtime state, and returns the result.
func (s *Scheduler) executeJob(ctx context.Context, job *Job) (string, error) {
	s.logger.Printf("[scheduler] Executing job %q (mode: %s)", job.Name, job.Mode)

	now := time.Now()
	// Snapshot the occurrence before invoking a callback that may race edits.
	req, requestErr := botTriggerRequest(job, now)
	ctx, ack := WithBotAcknowledgement(ctx)
	result, execErr := s.execute(ctx, job)
	if job.Mode == ModeBotTrigger && *ack == BotScheduleAcknowledged {
		return result, execErr
	}
	if job.Mode == ModeBotTrigger && execErr != nil && requestErr == nil {
		if failures, ok := s.store.(store.BotScheduleFailureStore); ok {
			_, err := failures.FailBotSchedule(ctx, store.BotScheduledOccurrence{JobID: req.JobID, BotID: req.BotID, Version: req.Version, TriggerID: req.TriggerID, OccurrenceAt: req.OccurrenceAt}, now, execErr.Error(), store.BotScheduleRetry)
			if err != nil {
				s.logger.Printf("[scheduler] Failed to record Bot admission failure: %v", err)
			}
			return result, execErr
		}
	}

	// Update runtime state in the store
	stored := s.store.Get(job.ID)
	if stored == nil {
		return result, execErr
	}

	stored.LastRun = &now
	if execErr != nil {
		stored.LastStatus = StatusFailed
		stored.LastError = execErr.Error()
		stored.ConsecutiveFailures++
		s.logger.Printf("[scheduler] Job %q failed (%d consecutive): %v",
			stored.Name, stored.ConsecutiveFailures, execErr)
	} else {
		stored.LastStatus = StatusSuccess
		stored.LastError = ""
		stored.ConsecutiveFailures = 0
		s.logger.Printf("[scheduler] Job %q completed successfully (%d chars)",
			stored.Name, len(result))
	}

	// Failed Bot admission retries the same durable occurrence.
	if stored.Mode != ModeBotTrigger || execErr == nil {
		stored.NextRun = s.computeNextRun(stored, now)
	}

	if err := s.store.Update(stored); err != nil {
		s.logger.Printf("[scheduler] Failed to update job state for %q: %v", stored.Name, err)
	}

	return result, execErr
}

// refreshNextRuns computes and sets NextRun for all enabled jobs.
func (s *Scheduler) refreshNextRuns() {
	jobs := s.store.List()
	for _, job := range jobs {
		if !job.Enabled || (job.Mode == ModeBotTrigger && job.NextRun != nil) {
			continue
		}
		nextRun := s.computeNextRun(job, time.Now())
		if nextRun != nil {
			stored := s.store.Get(job.ID)
			if stored != nil {
				stored.NextRun = nextRun
				if err := s.store.Update(stored); err != nil {
					slog.Warn("failed to update scheduled job next run time", "job_id", stored.ID, "error", err)
				}
			}
		}
	}
}

// RefreshNextRun recomputes the next run time for a specific job from now.
// Call this after updating a job's schedule to ensure the new timing takes effect
// immediately instead of waiting for the next execution cycle.
func (s *Scheduler) RefreshNextRun(jobID string) {
	job := s.store.Get(jobID)
	if job == nil || !job.Enabled {
		return
	}
	nextRun := s.computeNextRun(job, time.Now())
	if nextRun != nil {
		stored := s.store.Get(jobID)
		if stored != nil {
			stored.NextRun = nextRun
			if err := s.store.Update(stored); err != nil {
				slog.Warn("failed to update scheduled job next run time", "job_id", stored.ID, "error", err)
			}
			s.logger.Printf("[scheduler] Refreshed next run for job %q: %s", stored.Name, nextRun.Format(time.RFC3339))
		}
	}
}

// computeNextRun calculates the next run time for a job based on its cron schedule.
// The base time determines the reference point: use time.Now() for schedule changes,
// or job.LastRun for post-execution scheduling.
func (s *Scheduler) computeNextRun(job *Job, base time.Time) *time.Time {
	if job.Mode == ModeBotTrigger && job.Schedule.Cron == store.BotDeadlineSchedule {
		return nil // The owned goal supplies its single persisted occurrence.
	}
	schedule, err := s.parser.Parse(job.Schedule.Cron)
	if err != nil {
		s.logger.Printf("[scheduler] Invalid cron for job %q: %v", job.Name, err)
		return nil
	}

	loc := time.Local
	if job.Schedule.Timezone != "" {
		if parsed, err := time.LoadLocation(job.Schedule.Timezone); err == nil {
			loc = parsed
		} else {
			s.logger.Printf("[scheduler] Invalid timezone %q for job %q, using local", job.Schedule.Timezone, job.Name)
		}
	}

	next := schedule.Next(base.In(loc))
	nextUTC := next.UTC()
	return &nextUTC
}

// backoffDuration returns the delay for a given failure count.
func (s *Scheduler) backoffDuration(failures int) time.Duration {
	idx := failures - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(backoffSteps) {
		idx = len(backoffSteps) - 1
	}
	return backoffSteps[idx]
}

// ValidateCron checks if a cron expression is valid.
func ValidateCron(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err := parser.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return nil
}

// ComputeNextRun calculates the next run time for a cron expression and timezone.
// This is a pure function usable without a Scheduler instance — used by API handlers
// and the multi-tenant scheduler to compute NextRun after create/update operations.
// Returns nil if the cron expression or timezone is invalid.
func ComputeNextRun(cronExpr, timezone string) *time.Time {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(cronExpr)
	if err != nil {
		return nil
	}

	loc := time.Local
	if timezone != "" {
		if parsed, err := time.LoadLocation(timezone); err == nil {
			loc = parsed
		}
	}

	next := schedule.Next(time.Now().In(loc))
	nextUTC := next.UTC()
	return &nextUTC
}
