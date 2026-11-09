package bot

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestSupervisorOutcomeNeverClaimsSuccessAfterError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure *RunError
		want    store.RunStatus
	}{
		{"permanent", &RunError{Class: ErrorPermanent}, store.RunFailed},
		{"transient-no-retry", &RunError{Class: ErrorTransient}, store.RunFailed},
		{"uncertain", &RunError{Class: ErrorTransient, UncertainEffect: true}, store.RunPartial},
		{"interrupted", &RunError{Class: ErrorInterrupted}, store.RunInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := supervisorOutcome(RunResult{Status: store.RunSucceeded, Usage: store.BotUsage{InputTokens: 3}, Checkpoint: []byte(`{"step":1}`)}, tc.failure)
			if result.Status != tc.want || result.Report.Summary == "" || result.Usage.InputTokens != 3 || len(result.Checkpoint) == 0 {
				t.Fatalf("incorrect failure outcome: %+v", result)
			}
		})
	}
	result := supervisorOutcome(RunResult{}, errors.New("unclassified"))
	if result.Status != store.RunFailed {
		t.Fatal(result.Status)
	}
}

func TestSupervisorMissingDependencies(t *testing.T) {
	if _, err := NewSupervisor(SupervisorConfig{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("missing configuration accepted: %v", err)
	}
}
