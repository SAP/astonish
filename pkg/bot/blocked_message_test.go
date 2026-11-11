package bot

import (
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"testing"
)

func TestVerifiedBlockerEmployeeMessage(t *testing.T) {
	text := "I couldn't add email delivery because the email channel is unavailable. An administrator needs to enable it. Your briefing continues here."
	q := &store.BotQualityReport{SchemaVersion: store.BotWorkSchemaVersion, Outcome: store.BotWorkBlocked, ResponseOutcome: store.BotWorkResponseVerifiedBlocker, CandidateDigest: "exact", BlockedResponse: &store.BotWorkBlockedResponse{CandidateDigest: "exact", Text: text, Assessment: store.BotBlockedResponseAssessment{Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel}}}
	run := store.BotRun{Status: store.RunPartial, Report: store.BotReport{Summary: text, Quality: q, Unverified: []string{"internal criterion delivery not performed"}}}
	message := store.BotEmployeeMessage{Origin: store.BotEmployeeMessageAssessed, Text: text, CandidateDigest: "exact", SourceDigest: run.Report.EmployeeMessageSourceDigest(run.Status)}
	run.Report.EmployeeMessage = &message
	if got := RenderReport(run); strings.TrimSpace(got) != text {
		t.Fatalf("reviewed employee explanation overwritten or appended: %s", got)
	}
	q.BlockedResponse.Text = "different"
	if got := RenderReport(run); got != EmployeeMessageFallback(run.Status, run.Report, nil).Text+"\n" || strings.Contains(got, text) {
		t.Fatalf("unbound explanation suppressed safety fallback: %s", got)
	}
}
