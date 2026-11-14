package api

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// An HTTP-driven real adapter regression, using deterministic local model turns.
// The delegated summary is deliberately unsupported by the actual source body.
func employeeReviewHTTP(t *testing.T, mode string) (bot.RunResult, *store.BotWorkState, int, int) {
	t.Helper()
	ctx, req, ss := botChatTestRequest(t)
	req.Run.Intent = store.BotIntentRegular
	req.Instructions = "Report the actual market move and explain material drivers from inspected sources."
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: req.Instructions, Charter: req.Version.Charter})
	raw, _ := json.Marshal(w)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["employee_review_policy"] = "evidence_bound_v1"
	raw, _ = json.Marshal(fields)
	_ = json.Unmarshal(raw, w)
	sourceResponse := map[string]any{"content": "Market prices unavailable: source body contains only a navigation footer."}
	if mode == "adequate" {
		sourceResponse = map[string]any{"content": "Inspected market close: index rose 2%; earnings upgrades drove the move."}
	}
	delegateResponse := map[string]any{"report": "The market rose 8% because of strong earnings. This was not independently verified."}
	sourceRaw, _ := json.Marshal(sourceResponse)
	delegateRaw, _ := json.Marshal(delegateResponse)
	refs := []store.BotWorkEvidenceReference{{Ref: "capture:source", RunID: req.Run.ID, EventID: "source-event", CallID: "source", Digest: botAgentDigest(sourceRaw)}, {Ref: "capture:delegate", RunID: req.Run.ID, EventID: "delegate-event", CallID: "delegate", Digest: botAgentDigest(delegateRaw)}}
	w.AgentWork.Evidence = refs
	reader := agentEvidenceReader{rows: []store.BotTranscriptEvent{
		{Cursor: 1, Event: &session.Event{ID: "source-event", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "source", Name: "read_file", Response: sourceResponse}}}}}}},
		{Cursor: 2, Event: &session.Event{ID: "delegate-event", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "delegate", Name: "delegate_tasks", Response: delegateResponse}}}}}}},
	}}
	saved := &agentExecutionWorkStore{state: w}
	productions, reviews := 0, 0
	llm := &agentCheckModel{run: func(_ context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			system := ""
			if q.Config != nil && q.Config.SystemInstruction != nil {
				for _, p := range q.Config.SystemInstruction.Parts {
					system += p.Text
				}
			}
			text := "The market rose 8% because of strong earnings."
			switch {
			case strings.Contains(system, "BOT_WORK_REVIEW_V1"):
				reviews++
				if mode == "unavailable" {
					yield(nil, errors.New("offline assessor"))
					return
				}
				var in botWorkContentInput
				if err := json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &in); err != nil {
					t.Error(err)
				}
				if in.Candidate.Bodies["tool-response:source"] != string(sourceRaw) || in.Candidate.Bodies["tool-response:delegate"] != string(delegateRaw) {
					t.Error("review did not inspect exact source and delegated bodies")
				}
				if in.Contract.Instructions != req.Instructions {
					t.Error("review lost complete assignment")
				}
				response := botWorkContentReview{BotWorkReview: store.BotWorkReview{CandidateDigest: in.Candidate.Digest}}
				pass := strings.HasPrefix(in.Candidate.Text, "I could not") || (mode == "adequate" && in.Candidate.Text == "The index rose 2%, driven by earnings upgrades.")
				if !saved.state.EmployeeReviewPending || saved.state.EmployeeReviewAttempts != reviews {
					t.Error("review call was not durably reserved")
				}
				verdict := store.BotCriterionUnverified
				if pass {
					verdict = store.BotCriterionSatisfied
				}
				for _, c := range in.Criteria {
					response.Criteria = append(response.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "The delegated report does not establish actual market figures; disclose the limitation."})
				}
				for _, item := range in.Items {
					claim := botContentFactual
					if pass && mode != "adequate" {
						claim = botContentNonfactual
					}
					assessment := botWorkContentAssessment{ItemID: item.ID, ClaimType: claim, Relevance: store.BotCriterionSatisfied, Support: verdict, Explanation: "Actual source evidence is missing; do not assert unsupported figures."}
					if pass && mode == "adequate" {
						for ref, body := range in.Sources {
							if strings.Contains(body, "Inspected market close: index rose 2%; earnings upgrades drove the move.") {
								assessment.Sources = append(assessment.Sources, botWorkContentSource{Ref: ref, Quote: "Inspected market close: index rose 2%; earnings upgrades drove the move."})
							}
						}
						assessment.Explanation = "Inspected close establishes both the exact move and earnings driver, unlike the unsupported delegate summary."
					}
					response.ContentAssessments = append(response.ContentAssessments, assessment)
				}
				b, _ := json.Marshal(response)
				text = string(b)
			case strings.Contains(system, "BOT_EMPLOYEE_REPAIR_V1"):
				if !saved.state.EmployeeRepairPending || saved.state.Repairs < 1 {
					t.Error("repair call was not durably reserved")
				}
				if mode == "adequate" {
					text = `{"text":"The index rose 2%, driven by earnings upgrades."}`
				} else if mode == "exhausted" || mode == "exact" {
					text = `{"text":"The market rose 8% because of strong earnings."}`
				} else {
					text = `{"text":"I could not substantiate the market move or drivers from the available evidence."}`
				}
			default:
				productions++
			}
			yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model")}, nil)
		}
	}}
	var cleaned atomic.Int64
	factory := botChatTestFactory(t, ss, llm, nil, &cleaned)
	adapter, _ := NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
		r, e := factory(ctx, req)
		r.Work = saved
		r.EvidenceReader = reader
		return r, e
	})
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		var result bot.RunResult
		var startErr error
		if mode == "exact" {
			result, startErr = runBotWork(ctx, req, &BotChatResources{Work: saved, EvidenceReader: reader}, llm, true, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				productions++ // stands for the already-authorized exact effect executor
				c.FinalEventID = "exact-effect-result"
				return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "The market rose 8% because of strong earnings."}}, nil
			})
		} else {
			result, startErr = adapter.Start(ctx, req, func(_ context.Context, event bot.RuntimeEvent) error {
				if event.Type == "text" && strings.Contains(string(event.Payload), "rose 8%") {
					t.Error("rejected draft leaked to live stream")
				}
				if event.Type == "done" {
					t.Error("producer closed stream before review")
				}
				return nil
			})
		}
		if mode == "unavailable" || mode == "exhausted" || mode == "adequate" {
			before := productions
			_, _, replayErr := runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, saved.state, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
				productions++
				return bot.RunResult{}, nil
			}, llm)
			if replayErr == nil || productions != before {
				t.Error("terminal/uncertain review replayed producer")
			}
		}
		if startErr != nil {
			t.Logf("adapter: %v", startErr)
		}
		result.Failure = nil
		_ = json.NewEncoder(out).Encode(result)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result bot.RunResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result, saved.state, productions, reviews
}
func TestBotEmployeeMemoryReadEvidence(t *testing.T) {
	for _, mode := range []string{"captured", "foreign-run", "stale-digest", "empty-call", "missing", "empty-content"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Report delivery"})
			response := map[string]any{"content": "Delivery is Friday", "attribution": map[string]any{"status": "inferred"}}
			if mode == "empty-content" {
				response["content"] = ""
			}
			raw, _ := json.Marshal(response)
			ref := store.BotWorkEvidenceReference{Ref: "capture:memory", RunID: req.Run.ID, EventID: "memory-event", CallID: "memory", Digest: botAgentDigest(raw)}
			switch mode {
			case "foreign-run":
				ref.RunID = uuid.New()
			case "stale-digest":
				ref.Digest = strings.Repeat("0", 64)
			case "empty-call":
				ref.CallID = ""
			}
			w.AgentWork.Evidence = []store.BotWorkEvidenceReference{ref}
			w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "answer", Text: "Delivery is Friday"}
			reader := agentEvidenceReader{rows: []store.BotTranscriptEvent{{Cursor: 1, Event: &session.Event{ID: "memory-event", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "memory", Name: "memory_get", Response: response}}}}}}}}}
			if mode == "missing" {
				reader.rows = nil
			}
			err := botEmployeeCaptureEvidence(ctx, req, &BotChatResources{EvidenceReader: reader}, w)
			if mode != "captured" && mode != "empty-content" {
				if err == nil {
					t.Fatal("unbound memory evidence accepted")
				}
				return
			}
			if err != nil || w.Candidate.Bodies["tool-response:memory"] != string(raw) {
				t.Fatalf("exact captured bytes missing: %v", err)
			}
			in := botContentInput(w)
			if mode == "captured" && len(in.Sources) == 0 {
				t.Fatal("memory body unavailable to assessor")
			}
			if mode == "empty-content" && len(in.Sources) != 0 {
				t.Fatal("empty memory content became factual support")
			}
		})
	}
}

func TestBotEmployeeReadReferenceWithoutHTTPFacts(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Report delivery"})
	response := map[string]any{"content": "Delivery is Friday"}
	reader := agentEvidenceReader{rows: []store.BotTranscriptEvent{{Cursor: 1, Event: &session.Event{ID: "memory-event", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "memory", Name: "memory_get", Response: response}}}}}}}}}
	saved := &agentExecutionWorkStore{state: w}
	service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: saved, EvidenceReader: reader}}
	service.captureReadReference("memory")
	if _, exists := service.accessPending["memory"]; !exists {
		t.Fatal("successful local read dropped because it has no HTTP facts")
	}
	if len(service.accessPending["memory"]) != 0 {
		t.Fatal("local memory read invented HTTP facts")
	}
	bound, err := service.flushAccess(ctx, w)
	if err != nil || len(bound.AgentWork.Evidence) != 1 || bound.AgentWork.Evidence[0].CallID != "memory" || len(bound.AgentWork.SourceAccess) != 0 {
		t.Fatalf("local read did not retain exact durable evidence without HTTP facts: %v", err)
	}
}

func TestBotEmployeeReviewAdequateRepairSucceeds(t *testing.T) {
	result, w, productions, reviews := employeeReviewHTTP(t, "adequate")
	if result.Status != store.RunSucceeded || result.Report.Summary != "The index rose 2%, driven by earnings upgrades." || productions != 1 || reviews != 2 || !w.HasAcceptedEmployeeReview() {
		t.Fatalf("adequate repair incorrectly incomplete: %+v productions=%d reviews=%d", result, productions, reviews)
	}
}

func TestBotEmployeeReviewRejectsUnsupportedDelegatedReport(t *testing.T) {
	result, _, _, reviews := employeeReviewHTTP(t, "exhausted")
	if reviews != 3 || strings.Contains(result.Report.Summary, "rose 8%") || len(result.Report.Artifacts) != 0 {
		t.Fatalf("unsupported candidate published: %+v reviews=%d", result, reviews)
	}
}
func TestBotEmployeeReviewRepairsBeforePublication(t *testing.T) {
	result, w, productions, reviews := employeeReviewHTTP(t, "repair")
	if reviews != 2 || productions != 1 || !strings.HasPrefix(result.Report.Summary, "I could not") || w.Candidate.Text != result.Report.Summary {
		t.Fatalf("repair not integrated: %+v productions=%d reviews=%d", result, productions, reviews)
	}
}
func TestBotEmployeeReviewUnavailableWithholdsCandidate(t *testing.T) {
	result, _, _, reviews := employeeReviewHTTP(t, "unavailable")
	if reviews != 1 || strings.Contains(result.Report.Summary, "rose 8%") || result.Status == store.RunSucceeded {
		t.Fatalf("unreviewed candidate published: %+v reviews=%d", result, reviews)
	}
}
func TestBotEmployeeReviewExactActionNeverReplaysEffects(t *testing.T) {
	_, _, productions, reviews := employeeReviewHTTP(t, "exact")
	if productions != 1 || reviews != 3 {
		t.Fatalf("producer replayed or review skipped: productions=%d reviews=%d", productions, reviews)
	}
}
