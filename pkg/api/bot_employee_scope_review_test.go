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

// Exercises the production employee reviewer and exact content/citation validator,
// not a topic-specific server acceptance override. Deterministic model judgments
// verify the supplied policy; live model adherence is a separate acceptance gate.
func TestBotEmployeeScopeContentReview(t *testing.T) {
	for _, mode := range []string{"useful-selective", "unsupported", "empty-limitation", "invented-quote"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "AI News"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			w.Criteria = botEmployeeReviewCriteria(w)
			w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "IBM released an AI model.\nThe UK published an AI consultation.\nSelected coverage; other providers and market developments were not verified.", Bodies: map[string]string{"tool-response:ibm": `{"content":"IBM released an AI model."}`, "tool-response:uk": `{"content":"The UK published an AI consultation."}`}}
			if mode == "empty-limitation" {
				w.Candidate.Text = "No briefing; sources unavailable."
			}
			if mode == "unsupported" {
				w.Candidate.Text = "IBM doubled its AI revenue."
			}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			calls := 0
			llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					calls++
					system := ""
					for _, p := range q.Config.SystemInstruction.Parts {
						system += p.Text
					}
					if !strings.Contains(system, botEmployeeScopeGuidance) {
						t.Error("production review missing calibrated original scope")
					}
					var in botWorkContentInput
					if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in); err != nil {
						t.Fatal(err)
					}
					if in.Contract.Instructions != "AI News" {
						t.Fatal("assignment replaced with inferred coverage")
					}
					response := botEmployeeAssessment{BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairNone}}
					response.CandidateDigest = in.Candidate.Digest
					verdict := store.BotCriterionSatisfied
					if mode == "empty-limitation" {
						verdict = store.BotCriterionFailed
					}
					response.Criteria = []store.BotCriterionAssessment{{CriterionID: in.Criteria[0].ID, Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Original broad title requires useful news, not every sector; empty limitations are inadequate."}}
					for _, item := range in.Items {
						a := botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Supported selected coverage."}
						quote := item.Text
						if strings.Contains(quote, "Selected coverage") || mode == "empty-limitation" {
							a.ClaimType = botContentNonfactual
						} else if mode == "unsupported" {
							a.Support = store.BotCriterionUnverified
							a.Explanation = "No evidence supports this revenue claim."
						} else {
							for ref, body := range in.Sources {
								if strings.Contains(body, quote) {
									a.Sources = append(a.Sources, botWorkContentSource{Ref: ref, Quote: quote})
								}
							}
							if mode == "invented-quote" {
								for i := range a.Sources {
									a.Sources[i].Quote = "An unseen source claim."
								}
							}
						}
						response.ContentAssessments = append(response.ContentAssessments, a)
					}
					raw, _ := json.Marshal(response)
					yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
				}
			}}
			review, _, err := reviewBotEmployeeCandidate(ctx, req, llm, w, &store.BotUsage{})
			if err == nil {
				w.Review = review
			}
			if mode == "useful-selective" {
				if err != nil || !w.HasAcceptedEmployeeReview() || calls != 1 {
					t.Fatalf("useful supported briefing rejected: %v %+v", err, review)
				}
			} else if err == nil && w.HasAcceptedEmployeeReview() {
				t.Fatal("inadequate or unsupported candidate passed")
			}
		})
	}
}
