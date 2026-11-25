package api

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotEmployeeFeedbackBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		feedback BotEmployeeRepairFeedback
		want     string
	}{
		{"kind", BotEmployeeRepairFeedback{Kind: "unknown"}, "invalid employee repair kind"},
		{"research", BotEmployeeRepairFeedback{Kind: botEmployeeRepairResearch}, "research repair requires"},
		{"count", BotEmployeeRepairFeedback{MissingDimensions: make([]string, 9)}, "missing_dimensions exceeds 8"},
		{"blank", BotEmployeeRepairFeedback{SourceNeeds: []string{"\t"}}, "source_needs[0] is blank"},
		{"bytes", BotEmployeeRepairFeedback{PublicQueries: []string{strings.Repeat("é", 513)}}, "public_queries[0] exceeds 1024 bytes"},
		{"boundary", BotEmployeeRepairFeedback{PublicQueries: []string{strings.Repeat("a", 1024)}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.feedback.validate()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %s", err, tc.want)
			}
		})
	}
	schema := botWorkResponseSchema(botWorkReviewPrompt + botEmployeeAssessmentGuidance)
	props := schema["properties"].(map[string]any)
	for _, name := range []string{"missing_dimensions", "source_needs", "public_queries"} {
		list := props[name].(map[string]any)
		item := list["items"].(map[string]any)
		if list["maxItems"] != 8 || item["minLength"] != 1 || item["maxLength"] != 1024 {
			t.Fatalf("bounds missing for %s", name)
		}
	}
}

func TestBotEmployeeFeedbackBoundedCorrection(t *testing.T) {
	for _, mode := range []string{"corrected", "invalid-both", "negative-preserved", "schema-then-feedback", "feedback-then-citation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Say hello"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			w.Criteria = botEmployeeReviewCriteria(w)
			w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "Hello."}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			original := w.Candidate.Digest
			calls := 0
			llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					calls++
					if mode == "schema-then-feedback" && calls == 1 {
						yield(&model.LLMResponse{Content: genai.NewContentFromText("{", "model")}, nil)
						return
					}
					if calls == 2 && !strings.Contains(q.Config.SystemInstruction.Parts[0].Text, "FORMAT CORRECTION ONLY") {
						t.Error("missing bounded correction instructions")
					}
					var in botWorkContentInput
					if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in); err != nil {
						t.Fatal(err)
					}
					r := botEmployeeAssessment{BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairNone}}
					r.CandidateDigest = in.Candidate.Digest
					r.Criteria = []store.BotCriterionAssessment{{CriterionID: in.Criteria[0].ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Greeting meets the request."}}
					for _, item := range in.Items {
						r.ContentAssessments = append(r.ContentAssessments, botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentNonfactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Greeting, no factual claim."})
					}
					if calls == 1 || mode == "invalid-both" || mode == "schema-then-feedback" {
						r.PublicQueries = []string{" "}
					}
					if mode == "negative-preserved" && calls == 1 {
						r.Criteria[0].Verdict = store.BotCriterionFailed
						r.Criteria[0].Explanation = "Original negative finding."
					}
					if mode == "feedback-then-citation" && calls == 2 {
						r.ContentAssessments = nil
					}
					raw, _ := json.Marshal(r)
					yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
				}
			}}
			review, _, err := reviewBotEmployeeCandidate(ctx, req, llm, w, &store.BotUsage{})
			if calls != 2 {
				t.Fatalf("calls=%d want 2, err=%v", calls, err)
			}
			if w.Candidate.Digest != original || len(w.Candidate.Artifacts) != 0 {
				t.Fatal("correction mutated candidate/artifacts")
			}
			if mode == "invalid-both" || mode == "schema-then-feedback" || mode == "feedback-then-citation" {
				if err == nil || review != nil || w.HasAcceptedEmployeeReview() {
					t.Fatal("malformed feedback accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			w.Review = review
			if mode == "corrected" && !w.HasAcceptedEmployeeReview() {
				t.Fatal("corrected review rejected")
			}
			if mode == "negative-preserved" && w.HasAcceptedEmployeeReview() {
				t.Fatal("negative finding promoted")
			}
		})
	}
}
