package entstore_test

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// approvalWorkAssessmentModel keeps assessment outside the scripted action turns.
// These fixtures prove durable authority/continuation, not external-effect quality:
// a controlled read/write receipt alone must never become verified completion.
type approvalWorkAssessmentModel struct{ model.LLM }

func (m approvalWorkAssessmentModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	var prompt string
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, part := range req.Config.SystemInstruction.Parts {
			prompt += part.Text
		}
	}
	if strings.HasPrefix(prompt, "BOT_EMPLOYEE_MESSAGE") {
		// Narration is an auxiliary, tool-free call, not a continuation turn.
		// This fixture deliberately declines it to exercise the safe fallback.
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, store.ErrBotUnsupported) }
	}
	prepare := strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1")
	if !prepare && !strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
		return m.LLM.GenerateContent(ctx, req, stream)
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 {
			yield(nil, store.ErrBotInvalid)
			return
		}
		input := []byte(req.Contents[0].Parts[0].Text)
		var output any
		if prepare {
			var contract store.BotWorkContract
			if err := json.Unmarshal(input, &contract); err != nil {
				yield(nil, err)
				return
			}
			sources := []string{"assignment", "charter"}
			for _, a := range contract.Attachments {
				sources = append(sources, a.SourceID)
			}
			for _, goal := range contract.Goals {
				sources = append(sources, "goal:"+goal.ID.String())
			}
			output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "approved-work", SourceIDs: sources, Text: contract.Instructions}}}
		} else {
			var work store.BotWorkState
			if err := json.Unmarshal(input, &work); err != nil {
				yield(nil, err)
				return
			}
			if work.Candidate == nil || len(work.Criteria) != 1 {
				yield(nil, store.ErrBotInvalid)
				return
			}
			var content struct {
				Items []struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				} `json:"content_items"`
				ExecutionEvidence map[string]string `json:"execution_evidence"`
			}
			if err := json.Unmarshal(input, &content); err != nil {
				yield(nil, err)
				return
			}
			assessments := []approvalContentAssessment{}
			for _, item := range content.Items {
				assessments = append(assessments, assessApprovalContent(item.ID, item.Text, content.ExecutionEvidence))
			}
			output = map[string]any{"candidate_digest": work.Candidate.Digest, "criteria": []store.BotCriterionAssessment{{CriterionID: work.Criteria[0].ID, Verdict: store.BotCriterionUnverified, Method: store.BotAssessmentModel, Explanation: "Controlled tool receipt does not independently verify the requested external outcome."}}, "content_assessments": assessments}
		}
		raw, err := json.Marshal(output)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true}, nil)
	}
}

type approvalContentClaimType string

const (
	approvalContentFactual   approvalContentClaimType = "factual"
	approvalContentExecution approvalContentClaimType = "execution"
	approvalReceiptText                               = "Exact receipt observed."
	questionReceiptText                               = "The write tool returned its receipt; the destination outcome remains unverified."
)

type approvalContentSource struct {
	Ref   string `json:"ref"`
	Quote string `json:"quote"`
}

type approvalContentAssessment struct {
	ItemID           string                    `json:"item_id"`
	ClaimType        approvalContentClaimType  `json:"claim_type"`
	Relevance        store.BotCriterionVerdict `json:"relevance"`
	Support          store.BotCriterionVerdict `json:"support"`
	Explanation      string                    `json:"explanation"`
	Sources          []approvalContentSource   `json:"sources"`
	ExecutionSources []approvalContentSource   `json:"execution_sources"`
}

func assessApprovalContent(id, text string, evidence map[string]string) approvalContentAssessment {
	a := approvalContentAssessment{
		ItemID: id, ClaimType: approvalContentFactual,
		Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified,
		Explanation: "No captured content passage supports this claim.",
		Sources:     []approvalContentSource{}, ExecutionSources: []approvalContentSource{},
	}
	var receipt string
	switch text {
	case approvalReceiptText:
		receipt = `{"receipt":"ok"}`
	case questionReceiptText:
		receipt = `{"written":true}`
	default:
		// In particular, completed destination work is not nonfactual formatting
		// and cannot be corroborated by a controlled transport receipt.
		return a
	}
	a.ClaimType = approvalContentExecution
	a.Explanation = "The receipt observation lacks matching captured execution evidence."
	refs := make([]string, 0, len(evidence))
	for ref := range evidence {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if strings.HasPrefix(ref, "tool-response:") && evidence[ref] == receipt {
			a.Support = store.BotCriterionSatisfied
			a.ExecutionSources = append(a.ExecutionSources, approvalContentSource{Ref: ref, Quote: evidence[ref]})
			a.Explanation = "Captured receipt supports only the reported tool return, not independently verified completion of the requested external outcome."
			break
		}
	}
	return a
}

func TestApprovalWorkAssessmentContentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, text, ref, receipt string
		claim                    approvalContentClaimType
		support                  store.BotCriterionVerdict
	}{
		{"approval receipt", approvalReceiptText, "tool-response:approval", `{"receipt":"ok"}`, approvalContentExecution, store.BotCriterionSatisfied},
		{"question receipt", questionReceiptText, "tool-response:question", `{"written":true}`, approvalContentExecution, store.BotCriterionSatisfied},
		{"missing receipt", approvalReceiptText, "", "", approvalContentExecution, store.BotCriterionUnverified},
		{"wrong receipt", questionReceiptText, "tool-response:question", `{"written":false}`, approvalContentExecution, store.BotCriterionUnverified},
		{"owner scope is not receipt", approvalReceiptText, "assignment", `{"receipt":"ok"}`, approvalContentExecution, store.BotCriterionUnverified},
		{"external completion", "Completed the safe destination work.", "tool-response:question", `{"written":true}`, approvalContentFactual, store.BotCriterionUnverified},
		{"external fact", "The destination contains ten records.", "tool-response:question", `{"written":true}`, approvalContentFactual, store.BotCriterionUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := map[string]string{}
			if tc.ref != "" {
				evidence[tc.ref] = tc.receipt
			}
			got := assessApprovalContent("body:0:line:0", tc.text, evidence)
			if got.ItemID != "body:0:line:0" || got.ClaimType != tc.claim || got.Support != tc.support || got.Relevance != store.BotCriterionSatisfied || got.Explanation == "" || got.Sources == nil || len(got.Sources) != 0 || got.ExecutionSources == nil {
				t.Fatalf("assessment=%+v", got)
			}
			if tc.support == store.BotCriterionSatisfied {
				if len(got.ExecutionSources) != 1 || got.ExecutionSources[0].Ref != tc.ref || got.ExecutionSources[0].Quote != evidence[tc.ref] {
					t.Fatalf("receipt citation=%+v", got.ExecutionSources)
				}
			} else if len(got.ExecutionSources) != 0 {
				t.Fatalf("unsupported claim cited receipt: %+v", got)
			}
		})
	}
}

type approvalModel struct {
	calls  int
	report bool
}

func (*approvalModel) Name() string { return "controlled" }
func (m *approvalModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		part := &genai.Part{Text: approvalReceiptText}
		if !m.report {
			args := map[string]any{"body": "prior"}
			if m.calls == 2 {
				args["body"] = "exact"
			}
			part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "read_file", Args: args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, nil)
	}
}

type approvalClassifier struct{}

func (approvalClassifier) DescribeAction(_ context.Context, n string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return execution.ActionDescriptor{}, err
	}
	effect := execution.EffectSend
	if args["body"] == "prior" {
		effect = execution.EffectRead
	}
	return execution.ActionDescriptor{Tool: n, Arguments: raw, Effect: effect, ConstraintsVerified: true}, nil
}

type approvalGuard struct{}

func (approvalGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if d.Kind == execution.PolicyConsumeGrant {
		d.Kind = execution.PolicyAllow
	}
	return d, nil
}

type approvalBudget struct{}

func (approvalBudget) Check(ctx context.Context) error                 { return ctx.Err() }
func (approvalBudget) ReserveTool(ctx context.Context, _ string) error { return ctx.Err() }
func (approvalBudget) ReserveModel(ctx context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	return r, ctx.Err()
}
func (approvalBudget) SettleModel(ctx context.Context, _ uuid.UUID, _ execution.BudgetUsage) error {
	return ctx.Err()
}

type approvalHome struct{ home store.BotHomeBinding }

func (h approvalHome) Ensure(context.Context, bot.HomeRequest) (store.BotHomeBinding, error) {
	return h.home, nil
}
func (h approvalHome) BindRun(_ context.Context, b bot.HomeRunBinding) error {
	if b.Home != h.home {
		return store.ErrBotConflict
	}
	return nil
}
func (approvalHome) ReadArtifact(context.Context, store.BotHomeBinding, store.BotArtifact) (io.ReadCloser, error) {
	return nil, store.ErrBotUnsupported
}
func (approvalHome) Delete(context.Context, store.BotHomeBinding) error {
	return store.ErrBotUnsupported
}

func assertContinuationProtocolReport(t *testing.T, report store.BotReport, protocol store.BotWorkProtocol, text string) {
	t.Helper()
	if (report.Work != nil && report.Work.Protocol != protocol) || (protocol == store.BotWorkProtocolAgentLedV1 && report.Work == nil) {
		t.Fatalf("continuation changed protocol: %+v", report.Work)
	}
	switch protocol {
	case store.BotWorkProtocolLegacyAssessment:
		if report.EmployeeMessage.Origin != store.BotEmployeeMessageFallback || report.Quality == nil || report.Quality.Repairs != 0 || len(report.Quality.Criteria) != 1 || report.Quality.Criteria[0].Verdict != store.BotCriterionUnverified {
			t.Fatalf("legacy receipt gained unsupported assessment: %+v", report)
		}
	case store.BotWorkProtocolAgentLedV1:
		// Unattested effects still require the safety fallback in agent mode;
		// the model's receipt prose is not an execution attestation.
		if report.EmployeeMessage.Origin != store.BotEmployeeMessageFallback || report.Quality != nil || report.Summary != text || len(report.Changed) != 0 {
			t.Fatalf("agent receipt gained unsupported completion: %+v", report)
		}
	default:
		t.Fatalf("unknown protocol %q", protocol)
	}
}

func TestBotApprovalSQLiteReopenChatRunner(t *testing.T) {
	for _, protocol := range []store.BotWorkProtocol{store.BotWorkProtocolLegacyAssessment, store.BotWorkProtocolAgentLedV1} {
		name := "legacy"
		if protocol == store.BotWorkProtocolAgentLedV1 {
			name = "agent"
		}
		t.Run(name, func(t *testing.T) { testBotApprovalSQLiteReopenChatRunner(t, protocol) })
	}
}

func testBotApprovalSQLiteReopenChatRunner(t *testing.T, protocol store.BotWorkProtocol) {
	ctx, botID, runID, home, reopen := entstore.ApprovalScopeReopenFixture(t, store.BotScopePersonal, protocol)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: approvalClassifier{}, Guard: approvalGuard{}})
	scoped, ss := reopen()
	var prior, sent atomic.Int64
	action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled serial action"}, func(_ tool.Context, args map[string]any) (map[string]any, error) {
		switch args["body"] {
		case "prior":
			prior.Add(1)
		case "exact":
			sent.Add(1)
		default:
			t.Error("action changed")
		}
		return map[string]any{"receipt": "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	makeSupervisor := func(llm model.LLM) *bot.Supervisor {
		adapter, err := api.NewBotChatRuntimeAdapter(func(_ context.Context, _ bot.RunRequest) (*api.BotChatResources, error) {
			chat := agent.NewChatAgent(approvalWorkAssessmentModel{LLM: llm}, []tool.Tool{action}, nil, ss, &agent.SystemPromptBuilder{}, false, false)
			return &api.BotChatResources{Work: scoped.(store.BotWorkStore), Agent: chat, SessionService: ss, AppName: "astonish", Cleanup: func() {}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		supervisor, err := bot.NewSupervisor(bot.SupervisorConfig{Store: scoped, Runtime: adapter, Homes: approvalHome{home}, WorkerID: uuid.NewString(), Events: func(context.Context, bot.RuntimeEvent) error { return nil }, Ready: func(ctx context.Context, _ store.BotRunInput) (bot.RunPreparation, error) {
			id, _ := execution.BotRunIdentityFromContext(ctx)
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file"}})
			return bot.RunPreparation{Policy: policy, Budget: approvalBudget{}}, err
		}})
		if err != nil {
			t.Fatal(err)
		}
		return supervisor
	}
	first := &approvalModel{}
	waiting, err := makeSupervisor(first).Execute(ctx, botID, runID)
	if err != nil || waiting.Status != store.RunWaitingApproval || prior.Load() != 1 || sent.Load() != 0 {
		t.Fatalf("waiting=%+v err=%v prior=%d sent=%d", waiting, err, prior.Load(), sent.Load())
	}
	if protocol == store.BotWorkProtocolAgentLedV1 && len(waiting.Report.Unverified) != 0 {
		t.Fatalf("ordinary read was treated as an unattested effect: %+v", waiting.Report)
	}
	saved, err := scoped.GetRun(ctx, botID, runID)
	if err != nil {
		t.Fatal(err)
	}
	oldLease := saved.Lease
	// Close all connections, discard supervisor/model/store/session objects; decide
	// and resume solely from reopened SQLite state and current owner authority.
	scoped, ss = reopen()
	approvals, err := scoped.ListApprovals(ctx, botID, store.BotPage{})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%v err=%v", approvals, err)
	}
	a := approvals[0]
	p, _ := execution.PrincipalFromContext(ctx)
	if _, err := scoped.DecideApproval(ctx, store.BotApprovalDecision{BotID: botID, ApprovalID: a.ID, ActorID: uuid.MustParse(p.Subject), ActionDigest: a.ActionDigest, Status: store.ApprovalApproved}); err != nil {
		t.Fatal(err)
	}
	saved, err = scoped.GetRun(ctx, botID, runID)
	if err != nil || saved.Status != store.RunWaitingApproval {
		t.Fatal("decision activated run", err)
	}
	resume := scoped.(store.BotApprovalResumeStore)
	for i := 0; i < 2; i++ {
		if err := resume.RequeueApprovedRun(ctx, botID, runID, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	final := &approvalModel{report: true}
	result, err := makeSupervisor(final).Execute(ctx, botID, runID)
	if err != nil || result.Status != store.RunPartial || result.Report.ValidateEmployeeMessageIntegrity(result.Status) != nil || len(result.Report.Unverified) == 0 || prior.Load() != 1 || sent.Load() != 1 || final.calls != 1 {
		t.Fatalf("result=%+v err=%v prior=%d sent=%d model=%d", result, err, prior.Load(), sent.Load(), final.calls)
	}
	assertContinuationProtocolReport(t, result.Report, protocol, approvalReceiptText)
	scoped, ss = reopen()
	saved, err = scoped.GetRun(ctx, botID, runID)
	if err != nil || saved.Status != store.RunPartial || saved.Lease.Fence <= oldLease.Fence {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	approvals, err = scoped.ListApprovals(ctx, botID, store.BotPage{})
	if err != nil || approvals[0].Status != store.ApprovalConsumed {
		t.Fatal("not consumed", err)
	}
	if err := scoped.(store.BotApprovalResumeStore).RequeueApprovedRun(ctx, botID, runID, a.ID); err == nil {
		t.Fatal("replayed consumed action")
	}
	if _, err := makeSupervisor(&approvalModel{report: true}).Execute(ctx, botID, runID); err == nil {
		t.Fatal("reexecuted completed run")
	}
	if prior.Load() != 1 || sent.Load() != 1 {
		t.Fatal("effects replayed")
	}
}
