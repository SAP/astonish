package api

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"reflect"
	"strings"
	"testing"
)

func TestBotWorkCitationRejectsSupport(t *testing.T) {
	for _, fault := range []string{"unknown", "mixed_refs", "verdict", "explanation", "digest", "identity", "coverage", "method"} {
		t.Run(fault, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			w.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment", "charter"}, Text: "requirement"}}
			w.Candidate = &store.BotWorkCandidate{Text: "actual", Revision: 1, EventID: "event"}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			w.Phase = store.BotWorkReviewing
			original := w.Clone()
			r := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c1", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"https://private-secret.example"}, Explanation: "private-secret claims success"}}}
			switch fault {
			case "mixed_refs":
				r.Criteria[0].EvidenceRefs = append(r.Criteria[0].EvidenceRefs, "candidate:"+w.Candidate.Digest)
			case "verdict":
				r.Criteria[0].Verdict = "invalid"
			case "explanation":
				r.Criteria[0].Explanation = ""
			case "digest":
				r.CandidateDigest = "wrong"
			case "identity":
				r.Criteria[0].CriterionID = "wrong"
			case "coverage":
				r.Criteria = append(r.Criteria, r.Criteria[0])
			case "method":
				r.Criteria[0].Method = store.BotAssessmentMechanical
			}
			raw, _ := json.Marshal(r)
			m := &formatRecoveryModel{t: t, texts: []string{string(raw)}}
			var usage store.BotUsage
			got, err := reviewBotCandidate(ctx, req, m, w, &usage)
			if m.calls != 1 || !reflect.DeepEqual(w, original) {
				t.Fatal("retry or candidate mutation")
			}
			if fault != "unknown" && fault != "mixed_refs" {
				if err == nil {
					t.Fatal("other semantic error hidden")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c := got.Criteria[0]
			if c.Verdict != store.BotCriterionUnverified || len(c.EvidenceRefs) != 0 || strings.Contains(c.Explanation, "private-secret") || !strings.Contains(c.Explanation, "No cited support") {
				t.Fatalf("unsupported verdict: %+v", c)
			}
			next := w.Clone()
			next.Review = got
			if err := next.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBotWorkCitationPendingNoReplay(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	w.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment", "charter"}, Text: "requirement"}}
	w.Candidate = &store.BotWorkCandidate{Text: "pending", Revision: 1, EventID: "event"}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w.Phase = store.BotWorkReviewing
	r := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c1", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"assignment"}, Explanation: "unsupported"}}}
	raw, _ := json.Marshal(map[string]any{"current_turn": r, "deferred_application": r})
	m := &formatRecoveryModel{t: t, texts: []string{string(raw)}}
	var usage store.BotUsage
	got, err := reviewBotPendingCandidate(ctx, req, m, w, []botWorkPendingControl{{Operation: store.BotControlScheduleCreate, Payload: json.RawMessage(`{"operation":"schedule_create"}`)}}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 || got.Criteria[0].Verdict != store.BotCriterionUnverified || len(got.Criteria[0].EvidenceRefs) != 0 {
		t.Fatal("citation accepted or retried")
	}
}

func TestBotWorkCitationGuidance(t *testing.T) {
	for _, p := range []string{botWorkReviewPrompt, botWorkPendingReviewPrompt} {
		if !strings.Contains(p, botWorkCitationGuidance) {
			t.Fatal("missing citation guidance")
		}
	}
	w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Digest: "bound"}}
	raw, _ := json.Marshal(w)
	schema := botWorkResponseSchema(botWorkReviewPrompt, raw)
	props := schema["properties"].(map[string]any)["criteria"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if props["evidence_refs"].(map[string]any)["description"] != botWorkCitationGuidance {
		t.Fatal("missing schema guidance")
	}
}
