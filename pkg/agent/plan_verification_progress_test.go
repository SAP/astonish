package agent

import (
	"sync"
	"testing"
	"time"
)

func TestPlanVerificationProgressHeartbeatAndFinish(t *testing.T) {
	var mu sync.Mutex
	var updates []PlanVerificationProgress
	finish := startPlanVerificationProgress(func(p PlanVerificationProgress) { mu.Lock(); updates = append(updates, p); mu.Unlock() }, PlanVerificationProgress{PlanID: "plan", Step: "verify", Command: "test"})
	time.Sleep(1100 * time.Millisecond)
	finish(PlanStepApplyResult{Applied: "failed", Code: PlanStepVerifyFailed, Message: "timeout"})
	mu.Lock()
	defer mu.Unlock()
	if len(updates) < 3 || updates[0].Stage != PlanVerificationRunning || updates[1].Elapsed <= 0 || updates[len(updates)-1].Stage != PlanVerificationFinished {
		t.Fatalf("missing lifecycle: %+v", updates)
	}
}
