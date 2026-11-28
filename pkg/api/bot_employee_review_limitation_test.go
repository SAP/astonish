package api

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotEmployeeResearchLimitationHTTP(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Run.Intent = store.BotIntentRegular
			req.Instructions = "Explain the measured change and its causes from representative inspected evidence."
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: req.Instructions, Charter: req.Version.Charter})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			saved := &agentExecutionWorkStore{state: w}
			const gap = "The available source does not establish the measured change or representative coverage of its causes."
			candidate := "The measured change was eight percent."
			reviews, effects := 0, 0
			source := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					effects++
				}
				_ = json.NewEncoder(out).Encode(map[string]any{"content": "Navigation footer only; no measurements or causal evidence."})
			}))
			defer source.Close()
			assessor := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					text := `{"text":"` + candidate + `"}`
					if strings.Contains(q.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_REVIEW_V1") {
						reviews++
						var in botWorkContentInput
						if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in); err != nil {
							t.Error(err)
						}
						review := botWorkContentReview{BotWorkReview: store.BotWorkReview{CandidateDigest: in.Candidate.Digest}}
						for _, criterion := range in.Criteria {
							review.Criteria = append(review.Criteria, store.BotCriterionAssessment{CriterionID: criterion.ID, Verdict: store.BotCriterionUnverified, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: gap})
						}
						for _, item := range in.Items {
							review.ContentAssessments = append(review.ContentAssessments, botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified, Explanation: gap})
						}
						raw, _ := json.Marshal(review)
						text = string(raw)
					}
					yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model")}, nil)
				}
			}}
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			effect := execution.EffectRead
			if method == http.MethodPost {
				effect = execution.EffectUnknown
			}
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: effect, verified: true}})
			ctx, _ = bot.WithToolOutcomes(ctx)
			productions := 0
			final := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
				result, err := runBotWork(ctx, req, &BotChatResources{Work: saved}, assessor, true, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
					productions++
					journal := &botAgentReadJournal{}
					call := &genai.FunctionCall{ID: "actual-source", Name: "read_file", Args: map[string]any{"method": method, "url": source.URL}}
					journal.capture(ctx, call)
					request, _ := http.NewRequest(method, source.URL, nil)
					response, err := source.Client().Do(request)
					if err != nil {
						return bot.RunResult{}, err
					}
					defer response.Body.Close()
					var body map[string]any
					if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
						return bot.RunResult{}, err
					}
					cr := &ChatRunner{ctx: context.WithValue(ctx, botAgentReadJournalKey{}, journal)}
					cr.observeBotToolOutcome("tool", &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: body})
					*c = cr.completion
					c.FinalEventID = "actual-answer"
					c.FinalText = candidate
					report := botCompletionReport(*c, req)
					return bot.RunResult{Status: store.RunSucceeded, Report: report}, nil
				})
				if err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(out).Encode(result)
			}))
			defer final.Close()
			response, err := final.Client().Get(final.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var result bot.RunResult
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if method == http.MethodGet {
				// Positive read-only classification bypasses mandatory content
				// assessment. Publication is not a guarantee that claims are true.
				if reviews != 0 || productions != 1 || result.Status != store.RunSucceeded || !saved.state.HasReadOnlyEmployeeResult() {
					t.Fatalf("read-only result required review: reviews=%d productions=%d result=%+v", reviews, productions, result)
				}
				if !strings.Contains(result.Report.Summary, candidate) {
					t.Fatal("read-only delivery should include candidate text")
				}
				if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || result.Report.EmployeeMessage.Text != candidate {
					t.Fatalf("partial delivery employee message did not preserve candidate text: %+v", result.Report.EmployeeMessage)
				}
				if err := result.Report.ValidateEmployeeMessage(result.Status, saved.state); err != nil {
					t.Fatalf("partial delivery employee message failed validation: %v", err)
				}
				if result.Report.EffectState != store.BotReportEffectsReadOnly {
					t.Fatalf("read-only effect state lost: %+v", result.Report)
				}
				if len(result.Report.Unverified) != 0 || result.Report.ReviewLimitation != nil {
					t.Fatalf("invented review limitation: %+v", result.Report)
				}
			} else {
				if reviews != 3 || productions != 1 || result.Status != store.RunPartial {
					t.Fatalf("unbounded or missing review: reviews=%d productions=%d result=%+v", reviews, productions, result)
				}
				// POST: uncertain effects prevent graceful delivery; still withheld.
				limitation := result.Report.ReviewLimitation
				if limitation == nil || !strings.Contains(limitation.Text, gap) || limitation.Text != saved.state.Unresolved[0] || limitation.WorkRevision != saved.state.Revision {
					t.Fatalf("saved finding not bound: %+v", result.Report)
				}
				if strings.Contains(result.Report.Summary, candidate) {
					t.Fatal("rejected candidate leaked")
				}
				reason := store.BotEmployeeOutcomeReason(result.Status, result.Report)
				message := store.NewBotEmployeeFallback(result.Status, result.Report, reason)
				if result.Report.EmployeeMessage == nil || *result.Report.EmployeeMessage != message {
					t.Fatalf("active final did not use bound facts: %+v expected=%+v", result.Report.EmployeeMessage, message)
				}
				if effects != 1 || result.Report.EffectState != store.BotReportEffectsUncertain || reason != store.BotEmployeeReasonAgentUnverifiedEffects || !strings.Contains(message.Text, "destination") {
					t.Fatalf("uncertain effect warning lost: %+v message=%+v", result.Report, message)
				}
			}
		})
	}
}

func TestBotReportEffectReceiptsComplete(t *testing.T) {
	ctx := agent.WithBotPolicyRuntime(context.Background(), agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}})
	for _, mode := range []string{"empty", "read", "missing", "duplicate", "wrong-name", "unknown", "conflicting-effect"} {
		t.Run(mode, func(t *testing.T) {
			journal := &botAgentReadJournal{}
			want := store.BotReportEffectsUncertain
			if mode == "empty" {
				want = store.BotReportEffectsUnknown
			} else {
				callCtx := ctx
				if mode == "unknown" {
					callCtx = context.Background()
				}
				call := &genai.FunctionCall{ID: "source", Name: "memory_get", Args: map[string]any{"id": "scoped-memory"}}
				journal.capture(callCtx, call)
				if mode == "duplicate" {
					journal.capture(callCtx, call)
				}
				if mode != "missing" {
					name := call.Name
					if mode == "wrong-name" {
						name = "other"
					}
					journal.take(&genai.FunctionResponse{ID: call.ID, Name: name})
				}
				if mode == "conflicting-effect" {
					journal.observeEffect(bot.ToolOutcome{Changed: []store.BotEvidence{{Summary: "actual mutation"}}}, true)
				}
				if mode == "read" {
					want = store.BotReportEffectsReadOnly
				}
			}
			if got := journal.effectState(); got != want {
				t.Fatalf("state=%q want=%q", got, want)
			}
		})
	}
}
