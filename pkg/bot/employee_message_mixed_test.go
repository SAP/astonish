package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestEmployeeMixedControlNeverPublishesRuntimeReceipt(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunSucceeded, store.RunPartial, store.RunFailed} {
		t.Run(string(status), func(t *testing.T) {
			report := store.BotReport{Summary: "SECRET runtime pending acknowledgment UUID"}
			w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: report.Summary, Bodies: map[string]string{"pending-control:0": "private receipt", "producer-final-draft": "SECRET mixed result"}}}
			var cause error
			if status == store.RunFailed {
				cause = errors.New("SECRET provider")
				report.Failure = &store.BotReportFailure{Kind: store.BotReportFailureExecutionFormat, TechnicalDetails: cause.Error()}
			}
			m := ComposeEmployeeMessage(context.Background(), status, report, cause, EmployeeMessageOptions{Work: w, Boundary: func(context.Context) error { t.Fatal("mixed receipt must not consume extra capacity"); return nil }})
			report.EmployeeMessage = &m
			if m.Origin != store.BotEmployeeMessageFallback || report.ValidateEmployeeMessage(status, w) != nil || strings.Contains(m.Text, "SECRET") || strings.Contains(m.Text, "pending") {
				t.Fatalf("unsafe mixed receipt: %+v", m)
			}
			if status == store.RunFailed && !strings.Contains(m.Text, "model response") {
				t.Fatal("lost actual failure")
			}
			if status != store.RunFailed && !strings.Contains(m.Text, "independent result") {
				t.Fatal("lost independent-work limitation")
			}
		})
	}
}
