package api

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// A reviewer can correct a small text defect even after the two producer repairs.
// Semantic judgments are deterministic fixtures; source binding uses production code.
func TestBotEmployeeTextOneAssessmentAtRepairLimit(t *testing.T) {
	for _, mode := range []string{"pass", "unsupported-fact", "foreign-source", "invented-quote", "missing-content", "missing-criterion", "wrong-digest", "inadequate", "research", "effects", "mixed-fields", "missing-review", "foreign-criterion-ref", "duplicate-content", "execution-claim", "unknown-effect", "changed", "approval", "report-artifact"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Market News"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			w.Repairs = store.BotWorkMaxRepairs
			w.EmployeeReviewAttempts = 2
			w.Candidate = &store.BotWorkCandidate{Revision: 2, EventID: "previous", Text: "Earlier draft"}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			saved := &agentExecutionWorkStore{state: w}
			good := "Apple rose 2% as investors welcomed its results.\nAmazon fell 1% after its forecast disappointed investors."
			draft := w.Clone()
			draft.Phase = store.BotWorkFinished
			draft.AgentWork.Disposition = store.AgentWorkAnswered
			draft.Candidate = &store.BotWorkCandidate{Revision: 3, EventID: "final:employee-repair:2", Text: good + "\nThe article did not discuss Netflix.", Bodies: map[string]string{"tool-response:article": `{"content":"Apple rose 2% as investors welcomed its results.\nAmazon fell 1% after its forecast disappointed investors."}`}}
			draft.Candidate.Digest = draft.Candidate.ContentDigest()
			originalDigest := draft.Candidate.Digest
			calls := 0
			llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					calls++
					var in botWorkContentInput
					if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in); err != nil {
						t.Fatal(err)
					}
					if in.Contract.Instructions != "Market News" {
						t.Fatal("assignment changed")
					}
					assess := func(state *store.BotWorkState, original bool) botWorkContentReview {
						r := botWorkContentReview{BotWorkReview: store.BotWorkReview{CandidateDigest: in.Candidate.Digest}}
						verdict := store.BotCriterionSatisfied
						explanation := "Useful relevant selective briefing."
						if original || mode == "inadequate" || mode == "research" {
							verdict = store.BotCriterionFailed
							explanation = "Remove unsupported assertion that the article did not discuss Netflix."
						}
						for _, c := range in.Criteria {
							r.Criteria = append(r.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: explanation})
						}
						for _, item := range botContentInput(state).Items {
							a := botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Supported by actual captured article."}
							for ref, body := range in.Sources {
								if strings.Contains(body, item.Text) {
									a.Sources = append(a.Sources, botWorkContentSource{Ref: ref, Quote: item.Text})
								}
							}
							if len(a.Sources) == 0 {
								a.Support = store.BotCriterionUnverified
								a.Explanation = "No actual evidence for this assertion."
							}
							r.ContentAssessments = append(r.ContentAssessments, a)
						}
						return r
					}
					revised := in.BotWorkState.Clone()
					revised.Candidate.Text = good
					if mode == "unsupported-fact" {
						revised.Candidate.Text = "Apple rose 8%.\nAmazon fell 1% after its forecast disappointed investors."
					}
					rr := assess(revised, false)
					switch mode {
					case "foreign-source":
						rr.ContentAssessments[0].Sources[0].Ref = "body:foreign-run"
					case "invented-quote":
						rr.ContentAssessments[0].Sources[0].Quote = "Apple rose 8%."
					case "missing-content":
						rr.ContentAssessments = rr.ContentAssessments[:1]
					case "missing-criterion":
						rr.Criteria = nil
					case "wrong-digest":
						rr.CandidateDigest = "foreign-candidate"
					case "foreign-criterion-ref":
						rr.Criteria[0].EvidenceRefs = []string{"candidate:foreign"}
					case "duplicate-content":
						rr.ContentAssessments[1] = rr.ContentAssessments[0]
					case "execution-claim":
						rr.ContentAssessments[0].ClaimType = botContentExecution
					}
					base, _ := json.Marshal(assess(in.BotWorkState, true))
					var response map[string]any
					_ = json.Unmarshal(base, &response)
					response["repair_kind"] = "text"
					response["revised_text"] = revised.Candidate.Text
					response["revised_review"] = rr
					if mode == "missing-review" {
						delete(response, "revised_review")
					}
					if mode == "mixed-fields" {
						response["formatted_text"] = good
						response["formatted_review"] = rr
					}
					if mode == "research" {
						response["repair_kind"] = "research"
						response["missing_dimensions"] = []string{"Required closing breadth"}
						response["source_needs"] = []string{"Closing statistics"}
						response["public_queries"] = []string{"market breadth"}
						delete(response, "revised_text")
						delete(response, "revised_review")
					}
					raw, _ := json.Marshal(response)
					yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
				}
			}}
			result := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{EffectState: store.BotReportEffectsReadOnly}}
			if mode == "effects" {
				result.Report.EffectState = store.BotReportEffectsUncertain
				result.Report.Unverified = []string{"uncertain effect"}
			}
			if mode == "unknown-effect" {
				result.Report.EffectState = ""
			}
			switch mode {
			case "changed":
				result.Report.Changed = []store.BotEvidence{{}}
			case "approval":
				result.Report.NeedsApproval = append(result.Report.NeedsApproval, req.Run.ID)
			case "report-artifact":
				result.Report.Artifacts = []store.BotArtifact{{}}
			}
			next, err := reviewBotEmployeeResult(ctx, req, &BotChatResources{Work: saved}, llm, w, draft, &result)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "pass" {
				if calls != 1 || !next.HasAcceptedEmployeeReview() || result.Status != store.RunSucceeded || result.Report.Summary != good || next.Repairs != 2 || next.Candidate.Digest == originalDigest || next.Review.CandidateDigest != next.Candidate.Digest {
					t.Fatalf("corrected useful briefing withheld: calls=%d result=%+v next=%+v", calls, result, next)
				}
			} else if result.Status == store.RunSucceeded || next.HasAcceptedEmployeeReview() || next.Candidate.Digest != originalDigest {
				t.Fatalf("invalid revision accepted: mode=%s result=%+v", mode, result)
			}
		})
	}
}

func TestBotEmployeeTextRepairReceivesExactFindings(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Explain the limitation"})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	saved := &agentExecutionWorkStore{state: w}
	draft := w.Clone()
	draft.Phase = store.BotWorkFinished
	draft.AgentWork.Disposition = store.AgentWorkAnswered
	draft.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "A specific limitation."}
	draft.Candidate.Digest = draft.Candidate.ContentDigest()
	calls := 0
	finding := "Explain precisely that closing breadth remains unknown."
	llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			calls++
			var input struct {
				botWorkContentInput
				Findings *botWorkContentReview     `json:"review_findings"`
				Feedback BotEmployeeRepairFeedback `json:"review_feedback"`
			}
			if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &input); err != nil {
				t.Fatal(err)
			}
			if calls == 2 {
				var guidance struct {
					Findings *botWorkContentReview     `json:"review_findings"`
					Feedback BotEmployeeRepairFeedback `json:"review_feedback"`
				}
				if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &guidance); err != nil {
					t.Fatal(err)
				}
				input.Findings, input.Feedback = guidance.Findings, guidance.Feedback
				if input.Findings == nil || input.Findings.CandidateDigest != input.Candidate.Digest || input.Findings.Criteria[0].Explanation != finding || len(input.Findings.ContentAssessments) != 1 || input.Findings.ContentAssessments[0].Explanation != finding || input.Feedback.Kind != botEmployeeRepairText {
					t.Fatalf("repair lost exact criterion/content findings: %+v", input.Findings)
				}
				yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"text":"Closing breadth remains unknown."}`, "model")}, nil)
				return
			}
			verdict := store.BotCriterionSatisfied
			if calls == 1 {
				verdict = store.BotCriterionFailed
			}
			r := botEmployeeAssessment{BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairText}}
			r.CandidateDigest = input.Candidate.Digest
			r.Criteria = []store.BotCriterionAssessment{{CriterionID: input.Criteria[0].ID, Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + input.Candidate.Digest}, Explanation: finding}}
			r.ContentAssessments = []botWorkContentAssessment{{ItemID: input.Items[0].ID, ClaimType: botContentNonfactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: finding}}
			raw, _ := json.Marshal(r)
			yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
		}
	}}
	result := bot.RunResult{Status: store.RunSucceeded}
	next, err := reviewBotEmployeeResult(ctx, req, &BotChatResources{Work: saved}, llm, w, draft, &result)
	if err != nil || calls != 3 || !next.HasAcceptedEmployeeReview() || result.Report.Summary != "Closing breadth remains unknown." {
		t.Fatalf("repair sequence failed: err=%v calls=%d result=%+v", err, calls, result)
	}
}

func TestBotEmployeeTextRevisionGuidance(t *testing.T) {
	for _, required := range []string{"revised_text AND revised_review", "FULL original assignment", "input candidate digest", "actual line IDs", "body:0:line:0", "skipping empty lines without renumbering", "no effects, artifacts or controls", "removing or qualifying unsupported"} {
		if !strings.Contains(botEmployeeAssessmentGuidance, required) {
			t.Fatalf("reviewer never receives text revision contract: missing %q", required)
		}
	}
}

func TestBotEmployeeTextRevisionSchema(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	_ = ctx
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "News"})
	w.Criteria = botEmployeeReviewCriteria(w)
	w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "Original"}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	raw, _ := json.Marshal(botContentInput(w))
	s := botWorkResponseSchema(botWorkReviewPrompt+botEmployeeAssessmentGuidance, raw)
	props := s["properties"].(map[string]any)
	nested := props["revised_review"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
	p := nested["properties"].(map[string]any)
	digest := p["candidate_digest"].(map[string]any)["enum"].([]string)
	if len(digest) != 1 || digest[0] != w.Candidate.Digest || nested["additionalProperties"] != false {
		t.Fatal("revision schema identity not pinned")
	}
	a := p["content_assessments"].(map[string]any)
	item := a["items"].(map[string]any)["properties"].(map[string]any)["item_id"].(map[string]any)
	if a["maxItems"] != botWorkContentMaxItems || item["enum"] != nil || props["revised_text"] == nil {
		t.Fatal("revision must allow new line IDs then validate actual coverage")
	}
}
