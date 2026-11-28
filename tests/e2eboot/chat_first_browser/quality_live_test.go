package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
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
func TestEmployeeRunPrivateWorkIsNotPublicAuthority(t *testing.T) {
	_, w, r := qualityFixture(t)
	r.SessionID = uuid.New()
	r.Work = &store.BotWorkProjection{SchemaVersion: 1, Protocol: store.BotWorkProtocolAgentLedV1, BotID: r.BotID, RunID: r.ID, Phase: store.BotWorkWorking}
	private := map[string]any{
		"id": uuid.NewString(), "bot_id": uuid.NewString(), "session_id": uuid.NewString(),
		"status": store.RunFailed, "working_notes": "PRIVATE-NOTES", "contract": w.Contract,
	}
	for _, key := range []string{"run", "Run"} {
		for _, wrapped := range []bool{false, true} {
			encode := func(run any, extra map[string]any) employeeDiskRow {
				doc := map[string]any{key: run, "work": private}
				for k, v := range extra {
					doc[k] = v
				}
				raw := employeeDiskJSON(t, doc)
				if wrapped {
					raw = employeeDiskJSON(t, map[string]string{"bot_document": raw})
				}
				return employeeDiskRow{ID: r.ID.String(), BotID: r.BotID.String(), Data: raw}
			}
			got := employeeRun(encode(r, nil))
			if !reflect.DeepEqual(got, r) {
				t.Fatal("private work changed the public run or projection")
			}
			missing := r
			missing.SessionID = uuid.Nil
			missing.Work = nil
			got = employeeRun(encode(missing, nil))
			if got.SessionID != uuid.Nil || got.Work != nil {
				t.Fatal("private work supplied a missing public session or projection")
			}
			private["id"], private["bot_id"] = r.ID.String(), r.BotID.String()
			for _, missingID := range []string{"id", "bot_id"} {
				bad := r
				if missingID == "id" {
					bad.ID = uuid.Nil
				} else {
					bad.BotID = uuid.Nil
				}
				employeeDiskMustPanic(t, encode(bad, nil))
			}
			// The sole overlap exception is work. Every other public field,
			// including case aliases, remains an ambiguous flat/nested document.
			runType := reflect.TypeFor[store.BotRun]()
			for i := 0; i < runType.NumField(); i++ {
				field := strings.Split(runType.Field(i).Tag.Get("json"), ",")[0]
				if field == "-" || field == "work" {
					continue
				}
				for _, alias := range []string{field, strings.ToUpper(field)} {
					employeeDiskMustPanic(t, encode(r, map[string]any{alias: "conflict"}))
				}
			}
			employeeDiskMustPanic(t, encode(r, map[string]any{"Work": private}))
		}
	}
}

func TestQualityWorkRequiresPrivateEnvelope(t *testing.T) {
	row, w, r := qualityFixture(t)
	for _, wrapped := range []bool{false, true} {
		valid := row
		if wrapped {
			valid.Data = employeeDiskJSON(t, map[string]string{"bot_document": row.Data})
		}
		if _, _, err := qualityWork(valid); err != nil {
			t.Fatal(err)
		}
		// Even a full private snapshot placed in a flat public run cannot
		// masquerade as private evidence for an accepted quality report.
		var flat map[string]any
		if err := json.Unmarshal([]byte(employeeDiskJSON(t, r)), &flat); err != nil {
			t.Fatal(err)
		}
		flat["work"] = w
		invalid := row
		invalid.Data = employeeDiskJSON(t, flat)
		if wrapped {
			invalid.Data = employeeDiskJSON(t, map[string]string{"bot_document": invalid.Data})
		}
		if _, _, err := qualityWork(invalid); err == nil {
			t.Fatal("flat public work supplied private quality evidence")
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
