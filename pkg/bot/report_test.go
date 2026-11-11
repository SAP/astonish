package bot

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotWorkQualityReport(t *testing.T) {
	if QualitySummary(nil) != "Not assessed" {
		t.Fatal("legacy inferred pass")
	}
	q := &store.BotQualityReport{SchemaVersion: store.BotWorkSchemaVersion, Outcome: store.BotWorkAccepted, Repairs: 1, Criteria: []store.BotCriterionAssessment{{CriterionID: "sources", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"observation:0"}, Explanation: "Sources inspected"}}}
	run := store.BotRun{ID: uuid.New(), Status: store.RunSucceeded, Report: store.BotReport{Summary: "Final result", Quality: q}}
	text := RenderDiagnosticReport(run)
	for _, want := range []string{"Final result", "Requirements checked", "1 correction attempts", "Model assessment", "Sources inspected", "observation:0"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing", want)
		}
	}
	q.Criteria[0].Verdict = store.BotCriterionFailed
	if strings.Contains(QualitySummary(q), "Requirements checked") {
		t.Fatal("failed criterion shown as pass")
	}
	q.SchemaVersion++
	if QualitySummary(q) != "Assessment unavailable" {
		t.Fatal("unknown schema")
	}
	run.Report.Quality = nil
	if strings.Contains(RenderDiagnosticReport(run), "Quality assessment") {
		t.Fatal("legacy output rewritten")
	}
}

func TestRenderReportEmployeeMessage(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunSucceeded, store.RunFailed, store.RunPartial, store.RunWaitingApproval, store.RunInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			run := store.BotRun{Status: status, Report: store.BotReport{Summary: "RAW provider response", Unverified: []string{"RAW secret"}, NeedsApproval: []uuid.UUID{uuid.New()}}}
			want := EmployeeMessageFallback(status, run.Report, nil).Text + "\n"
			if got := RenderReport(run); got != want || strings.Contains(got, "RAW") {
				t.Fatalf("unsafe legacy projection: %q", got)
			}
			message := EmployeeMessageFallback(status, run.Report, nil)
			run.Report.EmployeeMessage = &message
			if got := RenderReport(run); got != want {
				t.Fatalf("envelope not preserved: %q", got)
			}
			message.Text = "RAW forged envelope"
			if got := RenderReport(run); got != want {
				t.Fatalf("forged envelope projected: %q", got)
			}
			if run.Report.Summary != "RAW provider response" || message.Text != "RAW forged envelope" {
				t.Fatal("renderer mutated historical source")
			}
		})
	}
}

func TestRenderReport(t *testing.T) {
	next := time.Now().UTC()
	approval := uuid.New()
	run := store.BotRun{ID: uuid.New(), Status: store.RunPartial, Report: store.BotReport{Summary: "Observed a partial result", Changed: []store.BotEvidence{{Summary: "changed fact", Source: "source A"}}, Unchanged: []store.BotEvidence{{Summary: "stable fact"}}, Unverified: []string{"unverified fact"}, Artifacts: []store.BotArtifact{{Title: "Artifact", RelativePath: "results/a.txt", SHA256: "digest"}}, NeedsApproval: []uuid.UUID{approval}, NextRunAt: &next}}
	text := RenderDiagnosticReport(run)
	for _, want := range []string{"partial", "changed fact", "source A", "stable fact", "unverified fact", "results/a.txt", "digest", approval.String(), next.Format(time.RFC3339), run.ID.String()} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}
