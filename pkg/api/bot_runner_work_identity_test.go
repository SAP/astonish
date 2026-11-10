package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotWorkIdentitySchema(t *testing.T) {
	_, req, _ := botChatTestRequest(t)
	w := &store.BotWorkState{Criteria: []store.BotWorkCriterion{{ID: "actual"}, {ID: "excluded", Excluded: true}}, Candidate: &store.BotWorkCandidate{Digest: "current", Observations: []store.BotEvidence{{}}, Artifacts: []store.BotArtifact{{ID: uuid.New(), SHA256: "hash"}}}}
	raw, err := botWorkAssessmentInput(req, w)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Refs []string `json:"available_evidence_refs"`
	}
	_ = json.Unmarshal(raw, &input)
	if !reflect.DeepEqual(input.Refs, w.Candidate.AvailableEvidenceRefs()) {
		t.Fatal("inventory differs")
	}
	check := func(schema map[string]any) {
		p := schema["properties"].(map[string]any)
		if !reflect.DeepEqual(p["candidate_digest"].(map[string]any)["enum"], []string{"current"}) {
			t.Fatal("digest not pinned")
		}
		item := p["criteria"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		if !reflect.DeepEqual(item["criterion_id"].(map[string]any)["enum"], []string{"actual"}) {
			t.Fatal("IDs not pinned")
		}
		if !reflect.DeepEqual(item["evidence_refs"].(map[string]any)["items"].(map[string]any)["enum"], input.Refs) {
			t.Fatal("refs not pinned")
		}
		if !reflect.DeepEqual(item["method"].(map[string]any)["enum"], []string{string(store.BotAssessmentModel)}) {
			t.Fatal("method not pinned")
		}
	}
	check(botWorkResponseSchema(botWorkReviewPrompt, raw))
	pending := botWorkResponseSchema(botWorkPendingReviewPrompt, raw)["properties"].(map[string]any)
	check(pending["current_turn"].(map[string]any))
	check(pending["deferred_application"].(map[string]any))
	contract := store.BotWorkContract{Goals: []store.BotGoal{{ID: uuid.New()}}, Attachments: []store.BotWorkAttachment{{SourceID: "attachment:actual"}}}
	raw, _ = json.Marshal(contract)
	p := botWorkResponseSchema(botWorkPreparePrompt, raw)["properties"].(map[string]any)["criteria"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	want := []string{"assignment", "charter", "goal_1", "attachment_1"}
	if !reflect.DeepEqual(p["source_ids"].(map[string]any)["items"].(map[string]any)["enum"], want) {
		t.Fatal("sources not pinned")
	}
}

func TestBotWorkIdentityNoSemanticRetry(t *testing.T) {
	for _, field := range []string{"criterion_id", "evidence_refs", "method", "coverage", "valid_failed"} {
		t.Run(field, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			w.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment", "charter"}, Text: "requirement"}}
			w.Candidate = &store.BotWorkCandidate{Text: "actual", Revision: 1, EventID: "event"}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			w.Phase = store.BotWorkReviewing
			review := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c1", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, Explanation: "private-secret"}}}
			switch field {
			case "criterion_id":
				review.Criteria[0].CriterionID = "private-secret"
			case "evidence_refs":
				review.Criteria[0].EvidenceRefs = []string{"private-secret"}
			case "method":
				review.Criteria[0].Method = "private-secret"
			case "coverage":
				review.Criteria = nil
			}
			raw, _ := json.Marshal(review)
			m := &formatRecoveryModel{t: t, texts: []string{string(raw)}}
			var usage store.BotUsage
			got, err := reviewBotCandidate(ctx, req, m, w, &usage)
			if m.calls != 1 {
				t.Fatal("semantic retry")
			}
			if field == "evidence_refs" {
				if err != nil || got.Criteria[0].Verdict != store.BotCriterionUnverified || len(got.Criteria[0].EvidenceRefs) != 0 || strings.Contains(got.Criteria[0].Explanation, "private-secret") {
					t.Fatalf("citation was accepted or leaked: %v", err)
				}
				return
			}
			if field == "valid_failed" {
				if err != nil || got.Criteria[0].Verdict != store.BotCriterionFailed {
					t.Fatalf("failed verdict changed: %v", err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestBotWorkPendingIdentityDiagnostics(t *testing.T) {
	for _, section := range []string{"current_turn", "deferred_application"} {
		t.Run(section, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			w.Phase = store.BotWorkReviewing
			w.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment", "charter"}, Text: "requirement"}}
			w.Candidate = &store.BotWorkCandidate{Text: "pending", Revision: 1, EventID: "event"}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			valid := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c1", Verdict: store.BotCriterionUnverified, Method: store.BotAssessmentModel, Explanation: "pending"}}}
			bad := valid
			bad.CandidateDigest = "private-secret"
			reply := map[string]store.BotWorkReview{"current_turn": valid, "deferred_application": valid}
			reply[section] = bad
			raw, _ := json.Marshal(reply)
			m := &formatRecoveryModel{t: t, texts: []string{string(raw)}}
			var usage store.BotUsage
			controls := []botWorkPendingControl{{Operation: store.BotControlScheduleCreate, Payload: json.RawMessage(`{"operation":"schedule_create"}`)}}
			_, err := reviewBotPendingCandidate(ctx, req, m, w, controls, &usage)
			if err == nil || !strings.Contains(err.Error(), section) || !strings.Contains(err.Error(), "candidate_digest") || strings.Contains(err.Error(), "private-secret") || m.calls != 1 {
				t.Fatalf("section diagnostic or retry: calls=%d err=%v", m.calls, err)
			}
		})
	}
}
