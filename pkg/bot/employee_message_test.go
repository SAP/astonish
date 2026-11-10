package bot

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotEmployeeMessageOmitsInternalReport(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunSucceeded, store.RunPartial, store.RunFailed} {
		t.Run(string(status), func(t *testing.T) {
			summary := "I pulled the US market headlines.\n\nHere are the key developments."
			if status != store.RunSucceeded {
				summary = "I couldn't retrieve today's headlines. Please try again later."
			}
			run := store.BotRun{ID: uuid.New(), Status: status, Report: store.BotReport{Summary: summary, Quality: &store.BotQualityReport{SchemaVersion: 1, Outcome: store.BotWorkBlocked, Criteria: []store.BotCriterionAssessment{{CriterionID: "c1", Explanation: "internal review", EvidenceRefs: []string{"candidate:secret"}}}}, Unchanged: []store.BotEvidence{{Summary: "HTTP 429", Source: "https://example.test"}}, Failure: &store.BotReportFailure{TechnicalDetails: "internal stack"}}}
			got := RenderReport(run)
			if strings.TrimSpace(got) != summary {
				t.Fatalf("expected just the employee's message, got %s", got)
			}
			for _, unwanted := range []string{"Run report", "Quality assessment", "Unchanged", "candidate:secret", run.ID.String(), "internal stack"} {
				if strings.Contains(got, unwanted) {
					t.Fatalf("leaked %s", unwanted)
				}
			}
		})
	}
}
