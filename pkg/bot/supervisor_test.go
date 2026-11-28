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

func TestBotSkillVersionSelection(t *testing.T) {
	v := store.DefaultBotVersion()
	v.BotID = store.BotID{1}
	v.Charter = "News"
	id := store.BotID{2}
	v.Triggers = []store.TriggerSpec{{SkillID: &id, Invocation: "Send update"}}
	if err := ValidateVersion(v); err != nil {
		t.Fatal(err)
	}
	v.Triggers[0].Invocation = ""
	if err := ValidateVersion(v); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("missing intent accepted: %v", err)
	}
	v.Triggers[0] = store.TriggerSpec{Instructions: "legacy"}
	if err := ValidateVersion(v); err != nil {
		t.Fatal(err)
	}
}

func TestBotSkillSupervisorFailureRetainsUncommittedLearning(t *testing.T) {
	learning := &store.BotSkillLearning{SourceRunID: store.RunID{1}}
	result := supervisorOutcome(RunResult{Status: store.RunSucceeded, SkillLearning: learning}, errors.New("failed"))
	if result.Status != store.RunFailed || result.SkillLearning != learning || result.Report.Skill != nil {
		t.Fatal("uncommitted learning was erased or presented as committed")
	}
}

func TestSupervisorMissingDependencies(t *testing.T) {
	if _, err := NewSupervisor(SupervisorConfig{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("missing configuration accepted: %v", err)
	}
}
