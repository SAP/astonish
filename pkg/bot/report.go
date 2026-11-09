package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// RenderReport renders the committed structured outcome, not transport completion
// text or an inferred success. It deliberately keeps unknowns and evidence visible.
func RenderReport(run store.BotRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Run report — %s\n\n%s\n", run.Status, strings.TrimSpace(run.Report.Summary))
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
