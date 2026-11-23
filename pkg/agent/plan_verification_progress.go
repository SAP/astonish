package agent

import (
	"sync"
	"time"
)

type PlanVerificationStage string

const (
	PlanVerificationRunning  PlanVerificationStage = "running"
	PlanVerificationFinished PlanVerificationStage = "finished"
)

// PlanVerificationProgress is ephemeral: it never grants acceptance or changes saved evidence.
type PlanVerificationProgress struct {
	PlanID   string                `json:"plan_id"`
	Revision int64                 `json:"revision"`
	Step     string                `json:"step"`
	Command  string                `json:"command"`
	Stage    PlanVerificationStage `json:"stage"`
	Elapsed  time.Duration         `json:"elapsed"`
	Result   PlanStepApplyResult   `json:"result"`
}

func startPlanVerificationProgress(callback func(PlanVerificationProgress), progress PlanVerificationProgress) func(PlanStepApplyResult) {
	if callback == nil {
		return func(PlanStepApplyResult) {}
	}
	start := time.Now()
	progress.Stage = PlanVerificationRunning
	callback(progress)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				update := progress
				update.Elapsed = time.Since(start)
				callback(update)
			}
		}
	}()
	var once sync.Once
	return func(result PlanStepApplyResult) {
		once.Do(func() {
			close(stop)
			<-done
			progress.Stage, progress.Elapsed, progress.Result = PlanVerificationFinished, time.Since(start), result
			callback(progress)
		})
	}
}
