package bot

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestSupervisorEmployeeEnvelopeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status store.RunStatus
		kind   store.BotReportFailureKind
		cause  error
	}{
		{"non-work", store.RunSucceeded, "", nil},
		{"setup", store.RunFailed, store.BotReportFailureSetup, errors.New("RAW setup token")},
		{"recovery", store.RunInterrupted, store.BotReportFailureInterrupted, &RunError{Class: ErrorInterrupted, Cause: errors.New("RAW recovery")}},
		{"approval", store.RunWaitingApproval, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := RunResult{Status: tc.status, Report: store.BotReport{Summary: "RAW provider body"}}
			if tc.kind != "" {
				r.Report.Failure = &store.BotReportFailure{Kind: tc.kind, TechnicalDetails: "RAW native details"}
			}
			out := supervisorOutcome(r, tc.cause)
			if out.Report.ValidateEmployeeMessageIntegrity(out.Status) != nil || strings.Contains(RenderReport(store.BotRun{Status: out.Status, Report: out.Report}), "RAW") {
				t.Fatalf("unsafe report: %+v", out)
			}
		})
	}
	r := RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "private result"}}
	m := EmployeeMessageFallback(r.Status, r.Report, nil)
	r.Report.EmployeeMessage = &m
	unchanged := supervisorOutcome(r, nil)
	if unchanged.Report.EmployeeMessage != &m {
		t.Fatal("unchanged envelope replaced")
	}
	changed := supervisorOutcome(r, fmt.Errorf("RAW wrapped: %w", store.ErrBotForbidden))
	if changed.Status != store.RunFailed || changed.Report.ValidateEmployeeMessageIntegrity(changed.Status) != nil || changed.Report.EmployeeMessage.Reason != store.BotEmployeeReasonForbidden || changed.Report.EmployeeMessage == &m {
		t.Fatalf("changed source not recomposed: %+v", changed)
	}
	m.Text = "RAW forged"
	forged := supervisorOutcome(r, errors.New("execution failure"))
	if forged.Report.EmployeeMessage != &m || forged.Report.ValidateEmployeeMessageIntegrity(forged.Status) == nil {
		t.Fatal("forged envelope silently repaired")
	}
}
