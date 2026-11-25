package api

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// RunEmployeeOrdinaryChatTest executes ordinary Chat's actual runner without a Bot
// identity, authorization hooks, durable Bot sink, or work/reviewer orchestration.
// The external acceptance test supplies NewScopedChatRuntime's shared agent.
func RunEmployeeOrdinaryChatTest(t *testing.T, ctx context.Context, chat *agent.ChatAgent, ss session.Service, llm model.LLM, sid, uid, objective string) ChatCompletion {
	t.Helper()
	if _, ok := execution.BotRunIdentityFromContext(ctx); ok {
		t.Fatal("ordinary Chat inherited Bot identity")
	}
	if _, err := ss.Create(ctx, &session.CreateRequest{AppName: "astonish_chat", UserID: uid, SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	runner := newChatRunner(sid, uid, "astonish_chat", false)
	runner.ctx = context.WithValue(store.WithSessionID(ctx, sid), appNameContextKey, "astonish_chat")
	runner.Run(chat, ss, llm, nil, genai.NewContentFromText(objective, "user"), objective, true, "", nil, false)
	completion := runner.Completion()
	if completion.Err != nil || completion.Blocked || !completion.Final {
		t.Fatalf("ordinary Chat failed: %+v", completion)
	}
	return completion
}

func TestBotEmployeeReadOnlyFinalOverridesStaleBlockedWork(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	req.Run.Intent = store.BotIntentRegular
	req.Instructions = "Return the sourced update."
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	req.Work = w
	saved := &agentExecutionWorkStore{state: w}
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}, Guard: botChatTestGuard{}})
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read source"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(c, "read_file", args); err != nil {
			return nil, err
		}
		blocked := saved.state.Clone()
		blocked.AgentWork.Disposition = store.AgentWorkBlocked
		blocked.Phase = store.BotWorkWorking
		if _, err := saved.SaveWork(c, store.BotWorkUpdate{Lease: req.Run.Lease, ExpectedRevision: saved.state.Revision, State: *blocked}); err != nil {
			return nil, err
		}
		return map[string]any{"content": "Supported source."}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const answer = "Supported sourced answer."
	factory := botChatTestFactory(t, ss, NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "source.txt"}), TextTurn(answer)), []tool.Tool{action}, new(atomic.Int64))
	adapter, err := NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
		r, e := factory(ctx, req)
		if e == nil {
			r.Work = saved
		}
		return r, e
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := ChatCompletion{FinalText: answer, FinalEventID: "final", Final: true, toolResponses: 3}
	report := store.BotReport{Summary: answer, EffectState: store.BotReportEffectsReadOnly}
	if !botCanPromoteStaleBlockedReadOnlyFinal(completion, req, report) {
		t.Fatal("settled resumed read-only final was not promoted")
	}
	report.EffectState = store.BotReportEffectsUncertain
	if botCanPromoteStaleBlockedReadOnlyFinal(completion, req, report) {
		t.Fatal("uncertain responses promoted blocked final")
	}
	result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || result.Status != store.RunSucceeded || result.Report.Summary != answer || result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Text != answer || saved.state.AgentWork.Disposition != store.AgentWorkAnswered || saved.state.Phase != store.BotWorkFinished {
		t.Fatalf("stale blocked state overrode read-only final: result=%+v saved=%+v err=%v", result, saved.state, err)
	}
}

// Run the real adapter and tool journal, not an injected already-classified result.
func TestBotEmployeeReadOnlyPublishesWithoutReview(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	req.Run.Intent = store.BotIntentRegular
	req.Instructions = "Read the local source and report the delivery date."
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	saved := &agentExecutionWorkStore{state: w}
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}, Guard: botChatTestGuard{}})
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read the controlled source"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(c, "read_file", args); err != nil {
			return nil, err
		}
		return map[string]any{"content": "Delivery is Friday."}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const answer = "Delivery is Friday."
	turns := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "source.txt"}), TextTurn(answer))
	calls := 0
	llm := &agentCheckModel{run: func(ctx context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		calls++
		return turns.GenerateContent(ctx, q, false)
	}}
	var cleaned atomic.Int64
	factory := botChatTestFactory(t, ss, llm, []tool.Tool{action}, &cleaned)
	adapter, err := NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*BotChatResources, error) {
		r, e := factory(ctx, req)
		if e == nil {
			r.Work = saved
		}
		return r, e
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result.Status != store.RunSucceeded || result.Report.Summary != answer || result.Report.EffectState != store.BotReportEffectsReadOnly {
		t.Fatalf("ordinary read required review or lost candidate: calls=%d result=%+v", calls, result)
	}
	if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || result.Report.EmployeeMessage.Text != answer {
		t.Fatalf("candidate not delivered as employee message: %+v", result.Report.EmployeeMessage)
	}
	if saved.state.EmployeeReviewAttempts != 0 || saved.state.Review != nil || saved.state.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 {
		t.Fatalf("review history fabricated or policy removed: %+v", saved.state)
	}
	// Round-trip the persisted candidate and exercise the real commit gate.
	raw, err := json.Marshal(saved.state)
	if err != nil {
		t.Fatal(err)
	}
	var restored store.BotWorkState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateBotWorkCompletion(&restored, store.BotRunCommit{Lease: req.Run.Lease, Status: result.Status, Report: result.Report}); err != nil {
		t.Fatalf("delivered candidate cannot commit after restore: %v", err)
	}
	// A terminal candidate cannot replay its producer.
	_, _, err = runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, saved.state, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
		t.Fatal("replayed producer")
		return bot.RunResult{}, nil
	}, llm)
	if err == nil {
		t.Fatal("terminal work became executable")
	}
}

// The adapter-only matrix complements the production HTTP title journey.
func TestBotEmployeeAdaptiveAdapterInquiry(t *testing.T) {
	testBotEmployeeAdaptiveHTTP(t, "adaptive", 3)
}

func TestBotEmployeeAdaptiveResultSufficiency(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reads int
	}{
		{"adequate-first", 1}, {"non-news", 1}, {"read-error", 2}, {"no-results", 1}, {"useful-partial", 1}, {"scheduled", 1}, {"latest-owner", 1},
	} {
		t.Run(tc.name, func(t *testing.T) { testBotEmployeeAdaptiveHTTP(t, tc.name, tc.reads) })
	}
}

func testBotEmployeeAdaptiveHTTP(t *testing.T, mode string, expectedReads int) {
	t.Helper()
	ctx, req, ss := botChatTestRequest(t)
	req.Run.Intent = store.BotIntentRegular
	req.Interactive = false
	objective := "Apple News"
	refinement := "Apple Inc. product launches and material market developments."
	if mode == "non-news" {
		objective = "Analyze delivery variance"
		refinement = "Compare planned and actual delivery dates."
	}
	if mode == "scheduled" {
		req.Run.Trigger = store.TriggerCron
	}
	if mode == "latest-owner" {
		objective = "Apple Inc. market developments only"
		refinement = "Use retained product context but focus on current market revenue."
	}
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: objective})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	if mode == "useful-partial" {
		w.AgentWork.Disposition = store.AgentWorkPartial
		w.Unresolved = []string{"The current-quarter market figures are unavailable."}
	}
	saved := &agentExecutionWorkStore{state: w}
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botReportTestClassifier{effect: execution.EffectRead, verified: true}, Guard: botChatTestGuard{}})
	reads, modelCalls := 0, 0
	var queries []string
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Query controlled evidence"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
		if err := agent.AuthorizeBotTool(c, "read_file", args); err != nil {
			return nil, err
		}
		query, _ := args["query"].(string)
		queries = append(queries, query)
		reads++
		switch mode {
		case "non-news":
			return map[string]any{"content": "Planned Friday; actual Monday; variance 3 days. source: local://delivery"}, nil
		case "no-results":
			return map[string]any{"content": "No results returned."}, nil
		case "read-error":
			if reads == 1 {
				// Trusted pre-dispatch source resolution feedback permits a new
				// alternate-source call, never replay of this failed invocation.
				bot.RecordToolOutcome(c, c.FunctionCallID(), bot.NewToolNonexecutionOutcome(c, c.FunctionCallID(), "read_file", args, bot.ToolFeedbackUnresolvedTarget))
				return nil, fmt.Errorf("Temporary source read failure; use alternate source.")
			}
		case "adaptive":
			if query == "Apple News" {
				return map[string]any{"content": "Apple fruit harvest increases. source: https://fixture.test/fruit"}, nil
			}
			if query == "Apple Inc. product launches" {
				return map[string]any{"content": "Apple Inc. launched a new device. Market developments not covered. source: https://fixture.test/product"}, nil
			}
		}
		return map[string]any{"content": "Apple Inc. launched a new device; market revenue rose 5%. source: https://fixture.test/market"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	answer := ""
	llm := &agentCheckModel{run: func(ctx context.Context, q *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		modelCalls++
		if mode == "scheduled" || mode == "latest-owner" {
			raw, _ := json.Marshal(q)
			if !strings.Contains(string(raw), objective) || !strings.Contains(string(raw), "Retained context: Apple Inc. launched a new device.") {
				t.Fatal("shared loop lost current objective or retained relevant context")
			}
		}
		var evidence string
		for _, content := range q.Contents {
			for _, p := range content.Parts {
				if p.FunctionResponse != nil {
					raw, _ := json.Marshal(p.FunctionResponse.Response)
					evidence += string(raw)
				}
			}
		}
		var turn *MockTurn
		// React to observed tool responses, not a prerecorded index of turns.
		switch {
		case evidence == "":
			turn = ToolCallTurn("read_file", map[string]any{"query": objective})
		case strings.Contains(evidence, "Temporary source read failure") && !strings.Contains(evidence, "revenue rose"):
			turn = ToolCallTurn("read_file", map[string]any{"query": "Apple Inc. alternate source"})
		case strings.Contains(evidence, "fruit harvest") && !strings.Contains(evidence, "launched"):
			turn = ToolCallTurn("read_file", map[string]any{"query": "Apple Inc. product launches"})
		case strings.Contains(evidence, "Market developments not covered") && !strings.Contains(evidence, "revenue rose"):
			turn = ToolCallTurn("read_file", map[string]any{"query": "Apple Inc. material market revenue developments"})
		default:
			switch mode {
			case "useful-partial":
				answer = "Apple Inc. launched a new device. Source: https://fixture.test/product. The current-quarter market figures are unavailable."
			case "no-results":
				answer = "I found no usable sources, so I cannot substantiate an Apple Inc. update."
			case "non-news":
				answer = "Delivery was 3 days late: planned Friday, actual Monday. Source: local://delivery."
			default:
				answer = "Apple Inc. launched a new device and market revenue rose 5%. Sources: https://fixture.test/market."
			}
			turn = TextTurn(answer)
		}
		return NewMockLLM(turn).GenerateContent(ctx, q, false)
	}}
	var cleaned atomic.Int64
	factory := botChatTestFactory(t, ss, llm, []tool.Tool{action}, &cleaned)
	adapter, err := NewBotChatRuntimeAdapter(func(c context.Context, r bot.RunRequest) (*BotChatResources, error) {
		resources, e := factory(c, r)
		if e == nil {
			resources.Work = saved
		}
		return resources, e
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []bot.RuntimeEvent
	accepted := false
	if mode == "scheduled" || mode == "latest-owner" {
		req.Instructions = objective + "\nRetained context: Apple Inc. launched a new device.\n" + refinement
		result, err := adapter.Start(ctx, req, func(_ context.Context, event bot.RuntimeEvent) error { events = append(events, event); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Text != answer || reads != expectedReads || modelCalls != expectedReads+1 || saved.state.Review != nil || len(result.Report.Artifacts) != 0 {
			t.Fatalf("noninteractive result: %+v reads=%d calls=%d", result, reads, modelCalls)
		}
		if mode == "latest-owner" && queries[0] != objective {
			t.Fatal("stale objective selected")
		}
		if err := store.ValidateBotWorkCompletion(saved.state, store.BotRunCommit{Lease: req.Run.Lease, Status: result.Status, Report: result.Report}); err != nil {
			t.Fatal(err)
		}
		return
	}
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, in *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(in.Body).Decode(&body) != nil {
			out.WriteHeader(400)
			return
		}
		if in.URL.Path == "/request" {
			if body.Text != objective {
				out.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(out).Encode(map[string]any{"question": "Which objective?", "options": []string{refinement}})
			return
		}
		if in.URL.Path != "/answer" || body.Text != refinement || accepted {
			out.WriteHeader(409)
			return
		}
		accepted = true
		req.Instructions = objective + "\nOwner refinement: " + body.Text
		result, e := adapter.Start(ctx, req, func(_ context.Context, event bot.RuntimeEvent) error { events = append(events, event); return nil })
		if e != nil {
			t.Errorf("adapter: %v", e)
			out.WriteHeader(500)
		}
		_ = json.NewEncoder(out).Encode(result)
	}))
	defer server.Close()
	post := func(path, text string) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"text": text})
		response, e := server.Client().Post(server.URL+path, "application/json", strings.NewReader(string(raw)))
		if e != nil {
			t.Fatal(e)
		}
		return response
	}
	question := post("/request", objective)
	question.Body.Close()
	if question.StatusCode != 200 || reads != 0 {
		t.Fatalf("refinement executed prematurely: %d reads=%d", question.StatusCode, reads)
	}
	response := post("/answer", refinement)
	defer response.Body.Close()
	var result bot.RunResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || result.Report.EmployeeMessage.Text != answer {
		t.Fatalf("inline delivery: HTTP=%d result=%+v", response.StatusCode, result)
	}
	if reads != expectedReads || modelCalls != expectedReads+1 || saved.state.EmployeeReviewAttempts != 0 || saved.state.Review != nil || len(result.Report.Artifacts) != 0 || len(saved.state.AgentWork.Items) != 0 {
		t.Fatalf("unnecessary orchestration: reads=%d calls=%d work=%+v", reads, modelCalls, saved.state)
	}
	if saved.state.Contract.Instructions != objective {
		t.Fatal("refinement overwrote immutable objective")
	}
	if mode == "adaptive" && (len(queries) != 3 || queries[1] != "Apple Inc. product launches" || !strings.Contains(queries[2], "market")) {
		t.Fatalf("did not repair relevance and material coverage: %v", queries)
	}
	if err := store.ValidateBotWorkCompletion(saved.state, store.BotRunCommit{Lease: req.Run.Lease, Status: result.Status, Report: result.Report}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) {
			t.Fatal("event ordering")
		}
	}
	t.Logf("mode=%s HTTP request+answer=2 model=%d reads=%d review=0 file=0 mandatory-plan=0 final=%s", mode, modelCalls, reads, fmt.Sprintf("%q", answer))
}

// Keep an explicit reviewer model available for unsafe/unclassified results.
func TestBotEmployeeReviewBypassRequiresPositiveReadOnlyResult(t *testing.T) {
	for _, mode := range []string{"read-only", "unknown", "uncertain", "confirmed", "changed", "unverified", "artifact", "approval", "pending-control", "explicit-criteria"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Report observations"})
			w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			if mode == "explicit-criteria" {
				w.Criteria = []store.BotWorkCriterion{{ID: "owner-observations", SourceIDs: []string{"assignment", "charter"}, Text: "Report observations with source support."}}
			}
			saved := &agentExecutionWorkStore{state: w}
			calls := 0
			reviewer := &agentCheckModel{run: func(context.Context, *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					calls++
					yield(&model.LLMResponse{Content: genai.NewContentFromText("invalid review", "model")}, nil)
				}
			}}
			_, _, _ = runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, w, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				c.FinalEventID = "answer"
				r := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Observed source.", EffectState: store.BotReportEffectsReadOnly}}
				switch mode {
				case "unknown":
					r.Report.EffectState = store.BotReportEffectsUnknown
				case "uncertain":
					r.Report.EffectState = store.BotReportEffectsUncertain
				case "confirmed":
					r.Report.EffectState = store.BotReportEffectsConfirmed
				case "changed":
					r.Report.Changed = []store.BotEvidence{{Summary: "Write observed"}}
				case "unverified":
					r.Report.Unverified = []string{"Uncertain effect remains"}
				case "artifact":
					r.Report.Artifacts = []store.BotArtifact{{}}
				case "approval":
					r.Report.NeedsApproval = []store.RunID{req.Run.ID}
				case "pending-control":
					c.pendingControls = append(c.pendingControls, botWorkPendingControl{})
				}
				return r, nil
			}, reviewer)
			if mode == "explicit-criteria" && (len(saved.state.Criteria) != 1 || saved.state.Criteria[0].ID != "owner-observations" || calls == 0) {
				t.Fatalf("explicit criteria were replaced or reviewer bypassed: calls=%d criteria=%+v", calls, saved.state.Criteria)
			}
			if mode == "read-only" && calls != 0 {
				t.Fatalf("read-only result called reviewer %d times", calls)
			}
			if mode != "read-only" && saved.state.HasReadOnlyEmployeeResult() {
				t.Fatalf("%s bypassed review reservation", mode)
			}
			if mode != "read-only" && mode != "artifact" && saved.state.EmployeeReviewAttempts == 0 {
				t.Fatalf("%s did not reserve review", mode)
			}
		})
	}
}
