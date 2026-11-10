package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// QualitySummary is shared by report publication and the remote terminal
// overview. Missing/unknown/incomplete evidence never becomes a green pass.
func QualitySummary(q *store.BotQualityReport) string {
	if q == nil {
		return "Not assessed"
	}
	if q.SchemaVersion != store.BotWorkSchemaVersion {
		return "Assessment unavailable"
	}
	if q.Outcome != store.BotWorkAccepted {
		return "Requirements remain unmet or unverified"
	}
	if len(q.Criteria) == 0 || len(q.Limitations) > 0 {
		return "Requirements remain unmet or unverified"
	}
	for _, c := range q.Criteria {
		if c.Verdict != store.BotCriterionSatisfied || len(c.EvidenceRefs) == 0 || (c.Method != store.BotAssessmentModel && c.Method != store.BotAssessmentMechanical) {
			return "Requirements remain unmet or unverified"
		}
	}
	return fmt.Sprintf("Requirements checked · %d correction attempts. Model assessments are judgments, not guarantees.", q.Repairs)
}

// RenderReport renders the committed employee message for Home and terminal.
// Detailed evidence stays on BotReport for diagnostics, not in the conversation.
func RenderReport(run store.BotRun) string {
	text := strings.TrimSpace(run.Report.Summary)
	if text == "" {
		switch run.Status {
		case store.RunSucceeded:
			text = "I've finished the requested work."
		case store.RunWaitingApproval:
			text = "I need your answer or authorization before I can continue."
		default:
			text = "I couldn't complete the requested work."
		}
	}
	if len(run.Report.Unverified) > 0 {
		text += "\n\nI couldn't verify everything, so please don't treat this as a fully completed result."
	}
	if len(run.Report.NeedsApproval) > 0 && run.Status != store.RunWaitingApproval {
		text += "\n\nI need your authorization before I can continue with the pending action."
	}
	return text + "\n"
}

// RenderDiagnosticReport is the explicit detailed representation, not Home prose.
func RenderDiagnosticReport(run store.BotRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Run report — %s\n\n%s\n", run.Status, strings.TrimSpace(run.Report.Summary))
	if q := run.Report.Quality; q != nil {
		fmt.Fprintf(&b, "\n### Quality assessment\n%s\n", QualitySummary(q))
		if q.SchemaVersion == store.BotWorkSchemaVersion {
			for _, c := range q.Criteria {
				method := "Assessment method unavailable"
				if c.Method == store.BotAssessmentModel {
					method = "Model assessment"
				}
				if c.Method == store.BotAssessmentMechanical {
					method = "Mechanical observation"
				}
				fmt.Fprintf(&b, "- %s: %s — %s (%s)\n", c.CriterionID, c.Verdict, c.Explanation, method)
				if len(c.EvidenceRefs) > 0 {
					fmt.Fprintf(&b, "  Evidence: %s\n", strings.Join(c.EvidenceRefs, ", "))
				}
			}
			for _, limitation := range q.Limitations {
				fmt.Fprintf(&b, "- Unresolved: %s\n", limitation)
			}
		}
	}
	for _, section := range []struct {
		name     string
		evidence []store.BotEvidence
	}{{"Changed", run.Report.Changed}, {"Unchanged", run.Report.Unchanged}} {
		if len(section.evidence) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n", section.name)
		for _, e := range section.evidence {
			fmt.Fprintf(&b, "- %s", e.Summary)
			if e.Source != "" {
				fmt.Fprintf(&b, " (source: %s)", e.Source)
			}
			b.WriteByte('\n')
		}
	}
	if len(run.Report.Unverified) > 0 {
		b.WriteString("\n### Unverified\n")
		for _, s := range run.Report.Unverified {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	if len(run.Report.Artifacts) > 0 {
		b.WriteString("\n### Artifacts\n")
		for _, a := range run.Report.Artifacts {
			fmt.Fprintf(&b, "- %s: %s (home %s; sha256 %s)\n", a.Title, a.RelativePath, a.HomeID, a.SHA256)
		}
	}
	if len(run.Report.NeedsApproval) > 0 {
		b.WriteString("\n### Needs approval\n")
		for _, id := range run.Report.NeedsApproval {
			fmt.Fprintf(&b, "- %s\n", id)
		}
	}
	if failure := run.Report.Failure; failure != nil && strings.TrimSpace(failure.TechnicalDetails) != "" {
		b.WriteString("\n### Technical details\n\n")
		// Indentation keeps native markdown fences inert.
		for _, line := range strings.Split(strings.ReplaceAll(failure.TechnicalDetails, "\r", ""), "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	if run.Report.NextRunAt != nil {
		fmt.Fprintf(&b, "\nNext run: %s\n", run.Report.NextRunAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "\nRun: %s\n", run.ID)
	return b.String()
}
