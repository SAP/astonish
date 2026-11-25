package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotArtifactTextRepairReviewedAgain(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Explain the design; qualify unverified implementation assumptions"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			saved := &agentExecutionWorkStore{state: w}
			bodies := map[string]string{}
			receipt := func(body string) store.BotArtifact {
				id := uuid.New()
				ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{BotID: req.Bot.ID, OwnerID: req.Bot.OwnerID.String()}, Kind: store.BotContentArtifact, Key: "content-v1/artifact-1-" + id.String(), Revision: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body))), SizeBytes: int64(len(body))}
				a := store.BotArtifact{ID: id, HomeID: uuid.New(), RelativePath: "report.md", ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}
				bodies[id.String()] = body
				return a
			}
			original := receipt("Assume implementation is verified.")
			draft := w.Clone()
			draft.Phase = store.BotWorkFinished
			draft.AgentWork.Disposition = store.AgentWorkAnswered
			draft.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "Attached design.", Artifacts: []store.BotArtifact{original}, Bodies: map[string]string{"tool-response:evidence": "Implementation was not inspected."}}
			resources := &BotChatResources{Work: saved, ArtifactBody: func(_ context.Context, a store.BotArtifact) ([]byte, error) {
				return []byte(bodies[a.ID.String()]), nil
			}, RepairFileDelivery: func(_ context.Context, text string, n int) ([]store.BotArtifact, error) {
				return []store.BotArtifact{receipt(text)}, nil
			}}
			calls := 0
			llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					calls++
					var input botWorkContentInput
					if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &input); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(q.Config.SystemInstruction.Parts[0].Text, "BOT_EMPLOYEE_REPAIR_V1") {
						if !strings.Contains(q.Contents[0].Parts[0].Text, "review_findings") {
							t.Fatal("findings missing")
						}
						yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"edits":[{"old":"Assume implementation is verified.","new":"Implementation details are unverified assumptions, not guarantees."}]}`, "model")}, nil)
						return
					}
					if calls == 3 {
						var artifactBody strings.Builder
						for _, item := range input.Items {
							if strings.HasPrefix(item.ID, "body:1:") {
								artifactBody.WriteString(item.Text)
							}
						}
						if artifactBody.String() != "Implementation details are unverified assumptions, not guarantees." {
							t.Fatal("second review did not receive full locally reconstructed artifact")
						}
					}
					r := botEmployeeAssessment{BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairText}}
					r.CandidateDigest = input.Candidate.Digest
					verdict := store.BotCriterionSatisfied
					if calls == 1 || reject {
						verdict = store.BotCriterionFailed
					}
					for _, c := range input.Criteria {
						r.Criteria = append(r.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + input.Candidate.Digest}, Explanation: "Qualify implementation assumptions."})
					}
					for _, item := range input.Items {
						r.ContentAssessments = append(r.ContentAssessments, botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentNonfactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Explicit proposal or limitation."})
					}
					raw, _ := json.Marshal(r)
					yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
				}
			}}
			result := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{EffectState: store.BotReportEffectsReadOnly}}
			next, err := reviewBotEmployeeResult(ctx, req, resources, llm, w, draft, &result)
			if err != nil {
				t.Fatalf("calls=%d state=%+v err=%v", calls, saved.state, err)
			}
			if bodies[original.ID.String()] != "Assume implementation is verified." {
				t.Fatal("original changed")
			}
			if reject {
				if next.HasAcceptedEmployeeReview() || len(result.Report.Artifacts) != 0 || next.Repairs != store.BotWorkMaxRepairs {
					t.Fatalf("forced acceptance: %+v", next)
				}
			} else {
				if calls != 3 || !next.HasAcceptedEmployeeReview() || next.Candidate.Artifacts[0].ID == original.ID || next.Candidate.Bodies["tool-response:evidence"] != "Implementation was not inspected." {
					t.Fatalf("repair failed calls=%d result=%+v", calls, result)
				}
			}
		})
	}
}
