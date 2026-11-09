package entstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/docs/slides"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type questionModel struct {
	calls     int
	continued bool
	t         *testing.T
}

func (*questionModel) Name() string { return "question-fixture" }
func (m *questionModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.continued {
			var askID, questionID string
			var answer struct {
				Type         string                       `json:"type"`
				ApprovalID   string                       `json:"approval_id"`
				InvocationID string                       `json:"invocation_id"`
				ToolCallID   string                       `json:"tool_call_id"`
				Tool         string                       `json:"tool"`
				Response     store.BotInteractionResponse `json:"response"`
			}
			prior, originalTask, answers, writes := 0, 0, 0, 0
			for _, content := range req.Contents {
				for _, part := range content.Parts {
					if part.FunctionResponse != nil && part.FunctionResponse.Name == "read_file" {
						prior++
					}
					if part.FunctionResponse != nil && part.FunctionResponse.Name == "ask_user" {
						askID = part.FunctionResponse.ID
						questionID, _ = part.FunctionResponse.Response["questionId"].(string)
					}
					if content.Role == "user" && part.Text == "Read once, then send body exact." {
						originalTask++
					}
					if part.FunctionResponse != nil && part.FunctionResponse.Name == "write_file" {
						writes++
						if part.FunctionResponse.Response["written"] != true {
							m.t.Fatalf("final model did not receive successful post-answer tool result: %+v", part.FunctionResponse)
						}
					}
					if content.Role == "user" && strings.Contains(part.Text, `"bot_owner_answer"`) {
						answers++
						if err := json.Unmarshal([]byte(part.Text), &answer); err != nil {
							m.t.Fatal(err)
						}
					}
				}
			}
			if prior != 1 || originalTask != 1 || answers != 1 || writes != m.calls-1 || askID == "" || answer.ToolCallID != askID || answer.Response.QuestionID != questionID || answer.Response.Answer != "safe" || answer.Type != "bot_owner_answer" || answer.Tool != "ask_user" || answer.InvocationID == "" || answer.ApprovalID == "" {
				m.t.Fatalf("lost or replayed transcript: prior=%d task=%d answers=%d writes=%d ask=%s question=%s answer=%+v", prior, originalTask, answers, writes, askID, questionID, answer)
			}
			part := &genai.Part{Text: "Completed the safe destination work."}
			if m.calls == 1 {
				part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "write_file", Args: map[string]any{"body": "safe"}}}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, nil)
			return
		}
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "read_file", Args: map[string]any{"body": "prior"}}}}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "ask_user", Args: map[string]any{"kind": "select", "prompt": "Choose the destination", "options": []map[string]any{{"id": "safe", "label": "Safe"}, {"id": "other", "label": "Other"}}}}}}}}, nil)
	}
}

type questionClassifier struct{}

func (questionClassifier) DescribeAction(_ context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	effect := execution.EffectRead
	if name == "write_file" {
		effect = execution.EffectReversibleWrite
	}
	return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: effect, ConstraintsVerified: true}, nil
}

func TestBotQuestionSQLiteReopenChatRunner(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) { testBotQuestionSQLiteReopenChatRunner(t, scope, false) })
	}
}

func TestBotApproveContinueQuestionHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) { testBotQuestionSQLiteReopenChatRunner(t, scope, true) })
	}
}

func testBotQuestionSQLiteReopenChatRunner(t *testing.T, scope store.BotScope, combined bool) {
	ctx, botID, runID, home, reopen := entstore.ApprovalScopeReopenFixture(t, scope)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: questionClassifier{}, Guard: approvalGuard{}})
	scoped, ss := reopen()
	ts, err := slides.GetTools()
	if err != nil {
		t.Fatal(err)
	}
	var ask tool.Tool
	for _, candidate := range ts {
		if candidate.Name() == "ask_user" {
			ask = candidate
		}
	}
	if ask == nil {
		t.Fatal("missing real ask_user")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cleaned, constructed, prior, post atomic.Int64
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled prior read"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		if args["body"] != "prior" {
			return nil, store.ErrBotInvalid
		}
		prior.Add(1)
		return map[string]any{"read": "prior result"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	write, err := functiontool.New(functiontool.Config{Name: "write_file", Description: "Controlled reversible effect"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		if args["body"] != "safe" {
			return nil, store.ErrBotInvalid
		}
		post.Add(1)
		return map[string]any{"written": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var cards []json.RawMessage
	makeSupervisor := func(llm model.LLM) *bot.Supervisor {
		adapter, err := api.NewBotChatRuntimeAdapter(func(_ context.Context, _ bot.RunRequest) (*api.BotChatResources, error) {
			constructed.Add(1)
			chat := agent.NewChatAgent(llm, []tool.Tool{ask, read, write}, nil, ss, &agent.SystemPromptBuilder{}, false, false)
			return &api.BotChatResources{Agent: chat, SessionService: ss, AppName: "astonish", Cleanup: func() { cleaned.Add(1) }}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		supervisor, err := bot.NewSupervisor(bot.SupervisorConfig{Store: scoped, Runtime: adapter, Homes: approvalHome{home}, WorkerID: uuid.NewString(), Events: func(_ context.Context, e bot.RuntimeEvent) error {
			if e.Type == "chat_question" {
				cards = append(cards, e.Payload)
			}
			return nil
		}, Ready: func(ctx context.Context, _ store.BotRunInput) (bot.RunPreparation, error) {
			id, _ := execution.BotRunIdentityFromContext(ctx)
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomyReversible, AllowedTools: []string{"ask_user", "read_file", "write_file"}})
			return bot.RunPreparation{Policy: policy, Budget: approvalBudget{}}, err
		}})
		if err != nil {
			t.Fatal(err)
		}
		return supervisor
	}
	first := &questionModel{}
	waiting, err := makeSupervisor(first).Execute(ctx, botID, runID)
	if err != nil || waiting.Status != store.RunWaitingApproval || cleaned.Load() != 1 || len(cards) != 1 || prior.Load() != 1 || first.calls != 2 {
		t.Fatalf("waiting=%+v err=%v cleaned=%d cards=%d", waiting, err, cleaned.Load(), len(cards))
	}
	scoped, ss = reopen()
	approvals, err := scoped.ListApprovals(ctx, botID, store.BotPage{})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%v err=%v", approvals, err)
	}
	a := approvals[0]
	if a.Interaction == nil || string(a.Interaction.Payload) != string(cards[0]) {
		t.Fatalf("reconnected card changed: %+v", a)
	}
	saved, err := scoped.GetRun(ctx, botID, runID)
	if err != nil || saved.Status != store.RunWaitingApproval {
		t.Fatal("missing durable wait", err)
	}
	checkpoint, err := bot.DecodeApprovalCheckpoint(saved.Checkpoint, *saved)
	if err != nil || checkpoint.Format != bot.InteractionCheckpointFormat || !checkpoint.Matches(a) {
		t.Fatal("invalid restored checkpoint", err)
	}
	p, _ := execution.PrincipalFromContext(ctx)
	// Real HTTP transport and handlers, with authenticated middleware identity
	// supplied by this fixture; no caller-provided actor is trusted by the API.
	httpCall := func(principal execution.Principal, method, path string, body any, want int) []byte {
		t.Helper()
		router := mux.NewRouter()
		router.HandleFunc("/api/studio/bots/{botID}/approvals", api.BotApprovalsHandler).Methods(http.MethodGet)
		router.HandleFunc("/api/studio/bots/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler).Methods(http.MethodPost)
		router.HandleFunc("/api/studio/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods(http.MethodPost)
		router.HandleFunc("/api/studio/bots/{botID}/runs/{runID}/resume", api.BotResumeRunHandler).Methods(http.MethodPost)
		requestCtx, err := execution.WithPrincipal(ctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		services := &store.Services{PersonalBots: scoped}
		if scope == store.BotScopeTeam {
			services = &store.Services{Bots: scoped}
		}
		requestCtx = store.WithServices(requestCtx, services)
		requestCtx = store.WithTenantContext(requestCtx, &store.TenantContext{UserID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			router.ServeHTTP(w, r.WithContext(requestCtx))
		}))
		defer server.Close()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path+"?scope="+string(scope), bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != want {
			t.Fatalf("%s %s status=%d want=%d body=%s err=%v", method, path, response.StatusCode, want, data, err)
		}
		return data
	}
	base := "/api/studio/bots/" + botID.String()
	var restored struct {
		Approvals []store.BotApproval `json:"approvals"`
	}
	if err := json.Unmarshal(httpCall(p, http.MethodGet, base+"/approvals", nil, http.StatusOK), &restored); err != nil || len(restored.Approvals) != 1 || !bytes.Equal(restored.Approvals[0].Interaction.Payload, a.Interaction.Payload) {
		t.Fatalf("HTTP reconnect lost question: %+v err=%v", restored, err)
	}
	decisionPath := base + "/approvals/" + a.ID.String() + "/decision"
	decisionBody := map[string]any{"action_digest": a.ActionDigest, "status": store.ApprovalApproved, "response": &store.BotInteractionResponse{QuestionID: a.Interaction.QuestionID, Answer: "safe"}}
	decisionStatus := http.StatusOK
	if combined {
		decisionPath = base + "/approvals/" + a.ID.String() + "/approve-and-continue"
		delete(decisionBody, "status")
		decisionStatus = http.StatusAccepted
	}
	foreign := p
	foreign.Subject = uuid.NewString()
	httpCall(foreign, http.MethodPost, decisionPath, decisionBody, http.StatusForbidden)
	missingResponse := map[string]any{"action_digest": a.ActionDigest}
	if !combined {
		missingResponse["status"] = store.ApprovalApproved
	}
	httpCall(p, http.MethodPost, decisionPath, missingResponse, http.StatusBadRequest)
	if combined {
		httpCall(p, http.MethodPost, decisionPath, map[string]any{"action_digest": a.ActionDigest, "response": &store.BotInteractionResponse{QuestionID: a.Interaction.QuestionID, Answer: "invented"}}, http.StatusBadRequest)
		httpCall(p, http.MethodPost, decisionPath, map[string]any{"action_digest": a.ActionDigest, "response": &store.BotInteractionResponse{QuestionID: "foreign-question", Answer: "safe"}}, http.StatusBadRequest)
	}
	decision := store.BotApprovalDecision{BotID: botID, ApprovalID: a.ID, ActorID: uuid.MustParse(p.Subject), ActionDigest: a.ActionDigest, Status: store.ApprovalApproved}
	if _, err := scoped.DecideApproval(ctx, decision); err == nil {
		t.Fatal("boolean approval answered a typed question")
	}
	decision.Response = &store.BotInteractionResponse{QuestionID: a.Interaction.QuestionID, Answer: "invented"}
	if _, err := scoped.DecideApproval(ctx, decision); err == nil {
		t.Fatal("unknown choice accepted")
	}
	httpCall(p, http.MethodPost, decisionPath, decisionBody, decisionStatus)
	// Identical retries are idempotent; changing an accepted answer is not.
	httpCall(p, http.MethodPost, decisionPath, decisionBody, decisionStatus)
	decision.Response = &store.BotInteractionResponse{QuestionID: a.Interaction.QuestionID, Answer: "other"}
	if _, err := scoped.DecideApproval(ctx, decision); err == nil {
		t.Fatal("changed answer accepted")
	}
	if combined {
		httpCall(p, http.MethodPost, decisionPath, map[string]any{"action_digest": a.ActionDigest, "response": decision.Response}, http.StatusConflict)
	}
	saved, _ = scoped.GetRun(ctx, botID, runID)
	wantRunStatus := store.RunWaitingApproval
	if combined {
		wantRunStatus = store.RunQueued
	}
	if saved.Status != wantRunStatus {
		t.Fatal("unexpected decision admission status", saved.Status)
	}
	scoped, ss = reopen()
	resumePath := base + "/runs/" + runID.String() + "/resume"
	resumeBody := map[string]any{"approval_id": a.ID}
	if !combined {
		httpCall(p, http.MethodPost, resumePath, resumeBody, http.StatusAccepted)
		httpCall(p, http.MethodPost, resumePath, resumeBody, http.StatusAccepted)
	}
	final := &questionModel{continued: true, t: t}
	result, err := makeSupervisor(final).Execute(ctx, botID, runID)
	if err != nil || result.Status != store.RunPartial || result.Report.Summary != "Completed the safe destination work." || len(result.Report.Unverified) < len(waiting.Report.Unverified) || final.calls != 2 || constructed.Load() != 2 || prior.Load() != 1 || post.Load() != 1 || len(cards) != 1 {
		t.Fatalf("unexpected continuation %+v err=%v model=%d constructed=%d", result, err, final.calls, constructed.Load())
	}
	scoped, ss = reopen()
	saved, err = scoped.GetRun(ctx, botID, runID)
	if err != nil || saved.Status != result.Status || saved.Report.Summary != result.Report.Summary {
		t.Fatalf("final report did not survive reopen: %+v err=%v", saved, err)
	}
	approvals, _ = scoped.ListApprovals(ctx, botID, store.BotPage{})
	if approvals[0].Status != store.ApprovalConsumed || approvals[0].Response.Answer != "safe" || approvals[0].DecidedBy == nil || approvals[0].DecidedBy.String() != p.Subject {
		t.Fatal("answer not durable")
	}
	if err := scoped.(store.BotApprovalResumeStore).RequeueApprovedRun(ctx, botID, runID, a.ID); err == nil {
		t.Fatal("decision replayed")
	}
	httpCall(p, http.MethodPost, resumePath, resumeBody, http.StatusConflict)
	if combined {
		httpCall(p, http.MethodPost, decisionPath, decisionBody, http.StatusConflict)
	}
	if _, err := makeSupervisor(final).Execute(ctx, botID, runID); err == nil {
		t.Fatal("terminal question continuation executed again")
	}
	if prior.Load() != 1 || post.Load() != 1 || final.calls != 2 || cleaned.Load() != 2 || constructed.Load() != 2 {
		t.Fatal("question continuation repeated prior effects or held resources")
	}
}
