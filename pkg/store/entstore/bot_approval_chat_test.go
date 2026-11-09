package entstore_test

import (
	"context"
	"encoding/json"
	"io"
	"iter"
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

type approvalModel struct {
	calls  int
	report bool
}

func (*approvalModel) Name() string { return "controlled" }
func (m *approvalModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		part := &genai.Part{Text: "Exact receipt observed."}
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

func TestBotApprovalSQLiteReopenChatRunner(t *testing.T) {
	ctx, botID, runID, home, reopen := entstore.ApprovalReopenFixture(t)
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
			chat := agent.NewChatAgent(llm, []tool.Tool{action}, nil, ss, &agent.SystemPromptBuilder{}, false, false)
			return &api.BotChatResources{Agent: chat, SessionService: ss, AppName: "astonish", Cleanup: func() {}}, nil
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
	if err != nil || result.Report.Summary != "Exact receipt observed." || prior.Load() != 1 || sent.Load() != 1 || final.calls != 1 {
		t.Fatalf("result=%+v err=%v prior=%d sent=%d model=%d", result, err, prior.Load(), sent.Load(), final.calls)
	}
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
