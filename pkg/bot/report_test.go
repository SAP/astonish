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
	approval := uuid.New()
	next := time.Now().UTC()
	for _, tc := range []struct {
		name string
		run  store.BotRun
		want string
	}{
		{
			name: "summary without audit",
			run: store.BotRun{ID: uuid.New(), Status: store.RunSucceeded, Report: store.BotReport{
				Summary:   "  Finished the requested comparison.  ",
				Changed:   []store.BotEvidence{{Summary: "changed fact", Source: "source A"}},
				Unchanged: []store.BotEvidence{{Summary: "stable fact"}},
				Artifacts: []store.BotArtifact{{Title: "Artifact", RelativePath: "results/a.txt", SHA256: "digest"}},
				Quality:   &store.BotQualityReport{SchemaVersion: store.BotWorkSchemaVersion, Outcome: store.BotWorkAccepted, Repairs: 1, Criteria: []store.BotCriterionAssessment{{CriterionID: "sources", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"observation:0"}, Explanation: "Sources inspected"}}},
				NextRunAt: &next,
			}},
			want: "Finished the requested comparison.\n",
		},
		{
			name: "failure retains explanation not technical details",
			run:  store.BotRun{Status: store.RunFailed, Report: store.BotReport{Summary: "I couldn't finish because the request exceeded the remaining budget. Shorten the task before trying again.", Failure: &store.BotReportFailure{Kind: store.BotReportFailureBudget, TechnicalDetails: "limit=71 requested=90"}}},
			want: "I couldn't finish because the request exceeded the remaining budget. Shorten the task before trying again.\n",
		},
		{
			name: "uncertainty and pending authorization",
			run:  store.BotRun{Status: store.RunPartial, Report: store.BotReport{Summary: "I prepared a partial comparison.", Unverified: []string{"internal observation reference"}, NeedsApproval: []uuid.UUID{approval}}},
			want: "I prepared a partial comparison.\n\nI couldn't verify everything, so please don't treat this as a fully completed result.\n\nI need your authorization before I can continue with the pending action.\n",
		},
		{
			name: "waiting approval does not duplicate notice",
			run:  store.BotRun{Status: store.RunWaitingApproval, Report: store.BotReport{NeedsApproval: []uuid.UUID{approval}}},
			want: "I need your answer or authorization before I can continue.\n",
		},
		{name: "empty success", run: store.BotRun{Status: store.RunSucceeded}, want: "I've finished the requested work.\n"},
		{name: "empty failure", run: store.BotRun{Status: store.RunFailed}, want: "I couldn't complete the requested work.\n"},
		{name: "empty partial", run: store.BotRun{Status: store.RunPartial}, want: "I couldn't complete the requested work.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderReport(tc.run); got != tc.want {
				t.Fatalf("employee message = %q, want %q", got, tc.want)
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
