package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/config"
	"github.com/google/uuid"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// The embedded quota interface is unused: this regression exercises review
// boundaries and the real journal flush, not paid model/tool dispatch.
type recoveredEvidenceMandate struct {
	store.BotMandateRuntime
	mandate store.BotMandate
}

func (m recoveredEvidenceMandate) VerifyBotMandate(context.Context, store.BotMandateRef, store.BotMandatePurpose) (store.BotMandate, error) {
	return m.mandate.Clone(), nil
}

// A successful continuation must retain canonical recovered bytes even if its
// subsequent assessment fails. An earlier finding describes an earlier candidate.
func TestBotEmployeeRecoveredEvidenceReviewFailureDoesNotPublishStaleFinding(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"perplexity_web_search", "web_fetch"}})
	if err != nil {
		t.Fatal(err)
	}
	req.Policy = policy
	identity := policy.Identity()
	ref := store.BotMandateRef{ID: uuid.New(), BotID: identity.BotID, OwnerID: identity.ActorID, OrgSlug: identity.OrgSlug, TeamSlug: identity.TeamSlug, Revision: 1}
	runtime := recoveredEvidenceMandate{mandate: store.BotMandate{Ref: ref, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}
	authorization, e := execution.VerifyBotMandateAuthorization(ctx, runtime, ref, execution.BotMandatePurposeTask)
	if e != nil {
		t.Fatal(e)
	}
	req.Mandate = &bot.MandateExecution{Runtime: runtime, Authorization: authorization, Identity: identity}
	ctx = execution.WithRunPolicy(ctx, policy)
	ctx = execution.WithBotRunIdentity(ctx, policy.Identity())
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: employeeGuard{}, Classifier: employeeClassifierFunc(func(_ context.Context, n string, a json.RawMessage) (execution.ActionDescriptor, error) {
		return execution.ActionDescriptor{Tool: n, Arguments: a, Effect: execution.EffectRead, ConstraintsVerified: true}, nil
	})})
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Research FAANG news"})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	saved := &agentExecutionWorkStore{state: w}
	response := map[string]any{"content": "Meta's long-time infrastructure leader will leave the company in the new year.", "status": 200, "url": "https://www.cnbc.com/2026/11/11/meta-infrastructure-departure-aws-cloud.html"}
	canonical, _ := json.Marshal(response)
	reader := agentEvidenceReader{rows: []store.BotTranscriptEvent{{Cursor: 1, Event: &session.Event{ID: "recovered-source", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "recovered", Name: "web_fetch", Response: response}}}}}}}}}
	resources := &BotChatResources{Work: saved, EvidenceReader: reader}
	service := &botAgentWorkService{req: req, resources: resources}
	ctx = bot.WithBotWorkServices(ctx, service)
	draft := w.Clone()
	draft.Phase = store.BotWorkFinished
	draft.AgentWork.Disposition = store.AgentWorkAnswered
	draft.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "original", Text: "The market rose."}
	draft.Candidate.Digest = draft.Candidate.ContentDigest()
	continuations, reviews := 0, 0
	ctx = WithBotEmployeeResearchRunner(ctx, func(c context.Context, directed BotEmployeeResearchRequest) (BotEmployeeResearchResult, error) {
		continuations++
		if bot.BotWorkServicesFromContext(c) != service {
			t.Fatal("continuation lost shared evidence service")
		}
		// Production observers buffer references; the next model boundary flushes
		// them from the same-run canonical transcript under the existing lease/CAS.
		service.captureReadReference("recovered")
		service.mu.Lock()
		_, e := service.load(c)
		service.mu.Unlock()
		if e != nil {
			t.Fatal(e)
		}
		return BotEmployeeResearchResult{Result: bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Meta's infrastructure leader will leave.", EffectState: store.BotReportEffectsReadOnly}}, Completion: ChatCompletion{FinalEventID: "revised"}}, nil
	})
	llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			reviews++
			var in botWorkContentInput
			if e := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in); e != nil {
				t.Fatal(e)
			}
			if reviews > 1 {
				if in.Candidate.Bodies["tool-response:recovered"] != string(canonical) {
					t.Fatal("next reviewer lost canonical recovered source")
				}
				seen := false
				for _, body := range in.Sources {
					if strings.Contains(body, response["content"].(string)) {
						seen = true
					}
				}
				if !seen {
					t.Fatal("recovered substantive article absent from reviewer content sources")
				}
				yield(nil, errors.New("review transport failed"))
				return
			}
			r := botEmployeeAssessment{BotEmployeeRepairFeedback: BotEmployeeRepairFeedback{Kind: botEmployeeRepairResearch, MissingDimensions: []string{"news"}, SourceNeeds: []string{"article content"}, PublicQueries: []string{"FAANG news"}}}
			r.CandidateDigest = in.Candidate.Digest
			for _, criterion := range in.Criteria {
				r.Criteria = append(r.Criteria, store.BotCriterionAssessment{CriterionID: criterion.ID, Verdict: store.BotCriterionUnverified, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Captured source content is only block/rate-limit notices."})
			}
			for _, item := range in.Items {
				r.ContentAssessments = append(r.ContentAssessments, botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified, Explanation: "No inspected article supports this draft"})
			}
			raw, _ := json.Marshal(r)
			yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
		}
	}}
	result := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{EffectState: store.BotReportEffectsReadOnly}}
	next, err := reviewBotEmployeeResult(ctx, req, resources, llm, w, draft, &result)
	if err != nil {
		t.Fatal(err)
	}
	if continuations != 1 || reviews != 2 {
		t.Fatalf("sequence: continuations=%d reviews=%d", continuations, reviews)
	}
	if result.Status != store.RunPartial || next.HasAcceptedEmployeeReview() {
		t.Fatal("unreviewed recovered draft was published")
	}
	if next.Candidate.Bodies["tool-response:recovered"] != string(canonical) {
		t.Fatal("recovered evidence discarded on failure")
	}
	if strings.Contains(result.Report.Summary, "only block/rate-limit notices") {
		t.Fatal("published obsolete finding from pre-recovery candidate")
	}
}
