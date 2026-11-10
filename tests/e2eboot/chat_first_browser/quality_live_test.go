package main

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func qualityFixture(t *testing.T) (employeeDiskRow, *store.BotWorkState, store.BotRun) {
	t.Helper()
	now := time.Now().UTC()
	bot, run := uuid.New(), uuid.New()
	g := store.BotGoal{ID: uuid.New(), BotID: bot, Revision: 1, Title: qualityGoal, CompletionCriteria: "Celsius source UTC Markdown"}
	w := store.NewBotWorkState(store.BotWorkContract{BotID: bot, RunID: run, Version: 1, Instructions: "report", Goals: []store.BotGoal{g}})
	w.Phase = store.BotWorkAccepted
	w.Criteria = []store.BotWorkCriterion{{ID: "c", SourceIDs: []string{"assignment", "charter", "goal:" + g.ID.String()}, Text: "report"}}
	w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "candidate", Text: "Orlando report", Observations: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "web_fetch:one", ObservedAt: now}}}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w.Review = &store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "checked"}}}
	r := store.BotRun{ID: run, BotID: bot, Status: store.RunSucceeded, StartedAt: &now}
	r.Report.Quality = &store.BotQualityReport{SchemaVersion: 1, WorkRevision: w.Revision, RequirementsDigest: w.RequirementsDigest, CandidateDigest: w.Candidate.Digest, Outcome: store.BotWorkAccepted, Criteria: w.Review.Criteria}
	row := qualityRow(t, w, r)
	return row, w, r
}
func qualityRow(t *testing.T, w *store.BotWorkState, r store.BotRun) employeeDiskRow {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"Run": r, "Work": w})
	if err != nil {
		t.Fatal(err)
	}
	return employeeDiskRow{ID: r.ID.String(), BotID: r.BotID.String(), Data: string(raw)}
}
func TestQualityWorkStrictIdentityAndSource(t *testing.T) {
	row, _, _ := qualityFixture(t)
	if _, _, err := qualityWork(row); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*store.BotWorkState, *store.BotRun){
		func(w *store.BotWorkState, r *store.BotRun) { r.Report.Quality.CandidateDigest = "old" },
		func(w *store.BotWorkState, r *store.BotRun) { w.SchemaVersion = 99 },
		func(w *store.BotWorkState, r *store.BotRun) { w.Contract.RunID = uuid.New() },
		func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Observations = nil
			w.Candidate.Digest = w.Candidate.ContentDigest()
		},
		func(w *store.BotWorkState, r *store.BotRun) { r.Report.Quality = nil },
	} {
		_, w, r := qualityFixture(t)
		mutate(w, &r)
		if _, _, err := qualityWork(qualityRow(t, w, r)); err == nil {
			t.Fatal("accepted invalid evidence")
		}
	}
}
func TestQualityFinalOnlyRejectsDraftButAllowsOwnerSample(t *testing.T) {
	_, _, r := qualityFixture(t)
	for _, id := range []string{r.ID.String(), r.ID.String() + ":repair"} {
		raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"type": "agent", "invocationId": id, "content": "draft"}}})
		if qualityFinalOnly(string(raw), []store.BotRun{r}) == nil {
			t.Fatal("draft passed")
		}
	}
	raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"type": "user", "content": qualitySample}}})
	if err := qualityFinalOnly(string(raw), []store.BotRun{r}); err != nil {
		t.Fatal(err)
	}
}
func TestQualityGoalMissingAndForeignRejected(t *testing.T) {
	_, w, _ := qualityFixture(t)
	if _, err := qualityGoalSource(w); err != nil {
		t.Fatal(err)
	}
	w.Contract.Goals[0].BotID = uuid.New()
	if _, err := qualityGoalSource(w); err == nil {
		t.Fatal("foreign source accepted")
	}
}
func TestQualityLegacyHooksRemainNoop(t *testing.T) {
	var p *qualityProbe
	if !p.offline(employeeDisk{}, uuid.Nil, uuid.Nil, time.Time{}) {
		t.Fatal("legacy gate changed")
	}
	p.cadence(employeeDisk{})
	p.finish(nil)
	p.restored(nil, "", "")
}
