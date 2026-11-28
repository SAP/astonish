package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type employeeChatReceiptService struct {
	result *bot.EmployeeChatResult
	err    error
	calls  int
}

func (s *employeeChatReceiptService) Send(context.Context, bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
	s.calls++
	return s.result, s.err
}

type employeeChatReceiptContext struct {
	tool.Context
	id string
}

func (c employeeChatReceiptContext) FunctionCallID() string { return c.id }

func TestEmployeeChatServiceReturnReceipt(t *testing.T) {
	worker := uuid.NewString()
	for _, tc := range []struct {
		name   string
		result *bot.EmployeeChatResult
		err    error
		want   bool
	}{
		{"completed", &bot.EmployeeChatResult{WorkerID: worker, Status: bot.EmployeeChatCompleted}, nil, true},
		{"partial", &bot.EmployeeChatResult{WorkerID: worker, Status: bot.EmployeeChatPartial}, nil, true},
		{"needs-input", &bot.EmployeeChatResult{WorkerID: worker, Status: bot.EmployeeChatNeedsInput}, nil, true},
		{"failed", &bot.EmployeeChatResult{WorkerID: worker, Status: bot.EmployeeChatFailed}, nil, true},
		{"cancelled", &bot.EmployeeChatResult{WorkerID: worker, Status: bot.EmployeeChatCancelled}, nil, true},
		{"nil", nil, nil, false},
		{"error", nil, errors.New("service unavailable"), false},
		{"result-and-error", &bot.EmployeeChatResult{WorkerID: worker, Status: bot.EmployeeChatCompleted}, errors.New("service failed"), false},
		{"missing-worker", &bot.EmployeeChatResult{Status: bot.EmployeeChatCompleted}, nil, false},
		{"invalid-worker", &bot.EmployeeChatResult{WorkerID: "invalid", Status: bot.EmployeeChatCompleted}, nil, false},
		{"zero-worker", &bot.EmployeeChatResult{WorkerID: uuid.Nil.String(), Status: bot.EmployeeChatCompleted}, nil, false},
		{"unknown-status", &bot.EmployeeChatResult{WorkerID: worker, Status: "success"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"native", "direct"} {
				t.Run(path, func(t *testing.T) {
					service := &employeeChatReceiptService{result: tc.result, err: tc.err}
					base := botPolicyContext(t, botBoundaryContext(t), nil, nil)
					identity, _ := execution.BotRunIdentityFromContext(base)
					identity.ActorID = uuid.NewString()
					base = execution.WithBotRunIdentity(base, identity)
					base, _ = bot.WithToolOutcomes(base)
					base = bot.WithEmployeeChatService(base, service)
					ctx := employeeChatReceiptContext{Context: &contextToolContext{minimalReadonlyContext{Context: base}}, id: "employee-return"}
					raw, err := functiontool.New(functiontool.Config{Name: "employee_chat", Description: "local service"}, func(c tool.Context, request bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
						return bot.EmployeeChatServiceFromContext(c).Send(c, request)
					})
					if err != nil {
						t.Fatal(err)
					}
					sealed, err := NewBotEmployeeChatTool(raw, func(c tool.Context) (tool.Context, error) { return c, nil })
					if err != nil {
						t.Fatal(err)
					}
					args := map[string]any{"message": "inspect"}
					var out map[string]any
					if path == "native" {
						out, err = botNativeCallbacks(nil, nil)[0](ctx, sealed, args)
					} else {
						out, err = sealed.(proposalCaptureRunnable).Run(ctx, args)
					}
					if service.calls != 1 {
						t.Fatalf("calls=%d", service.calls)
					}
					if tc.err != nil && err == nil {
						t.Fatal("service error lost")
					}
					outcome, found := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
					if found != tc.want {
						t.Fatalf("receipt=%t want=%t output=%#v error=%v", found, tc.want, out, err)
					}
					if found && (!outcome.Verified || len(outcome.Unchanged) != 1 || outcome.Unchanged[0].Kind != store.BotEvidenceObservation || len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || len(outcome.Unverified) != 0) {
						t.Fatalf("service return attested worker effects: %+v", outcome)
					}
				})
			}
		})
	}
}

type employeeChatTestService struct{}

func (employeeChatTestService) Send(context.Context, bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
	return &bot.EmployeeChatResult{Status: bot.EmployeeChatCompleted}, nil
}

func TestEmployeeChatSealedDispatchInteractive(t *testing.T) {
	base, registry, id := interactivePolicyContext(t, []string{"chat", "tool:execute"})
	base = bot.WithEmployeeChatService(base, employeeChatTestService{})
	ctx := &contextToolContext{minimalReadonlyContext{Context: base}}
	calls := 0
	raw, err := functiontool.New(functiontool.Config{Name: "employee_chat", Description: "local orchestration"}, func(tool.Context, bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
		calls++
		return &bot.EmployeeChatResult{Status: bot.EmployeeChatCompleted}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := NewBotEmployeeChatTool(raw, func(c tool.Context) (tool.Context, error) {
		auth, ok := execution.BotTurnAuthorizerFromContext(c)
		if !ok {
			return nil, errors.New("missing current authorizer")
		}
		if err := auth.Authorize(c, execution.CapabilityChat); err != nil {
			return nil, err
		}
		return c, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"message": "inspect"}
	cb := botNativeCallbacks(nil, nil)[0]
	if _, err := cb(ctx, sealed, args); err != nil || calls != 1 {
		t.Fatalf("nil-policy Employee: %v calls=%d", err, calls)
	}
	registry.Cancel(id)
	if _, err := cb(ctx, sealed, args); err == nil || calls != 1 {
		t.Fatal("cancelled interactive Employee dispatched")
	}
}

func TestEmployeeChatSealedDispatch(t *testing.T) {
	base := botPolicyContext(t, botBoundaryContext(t), nil, nil)
	base = bot.WithEmployeeChatService(base, employeeChatTestService{})
	ctx := &contextToolContext{minimalReadonlyContext{Context: base}}
	calls := 0
	raw, err := functiontool.New(functiontool.Config{Name: "employee_chat", Description: "local orchestration"}, func(tool.Context, bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
		calls++
		return &bot.EmployeeChatResult{Status: bot.EmployeeChatCompleted}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cb := botNativeCallbacks(nil, nil)[0]
	args := map[string]any{"message": "inspect"}
	if _, err := cb(ctx, raw, args); err == nil || calls != 0 {
		t.Fatal("same name bypassed action policy")
	}
	deny := ""
	sealed, err := NewBotEmployeeChatTool(raw, func(c tool.Context) (tool.Context, error) {
		if deny != "" {
			return nil, errors.New(deny)
		}
		return c, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := &model.LLMRequest{}
	if err := sealed.(proposalCaptureRunnable).ProcessRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if req.Tools["employee_chat"] != sealed {
		t.Fatal("raw target registered")
	}
	if _, err := cb(ctx, sealed, args); err != nil || calls != 1 {
		t.Fatalf("sealed dispatch: %v count=%d", err, calls)
	}
	if _, err := cb(ctx, sealed, map[string]any{"message": 42}); err == nil || calls != 1 {
		t.Fatal("invalid arguments dispatched")
	}
	for _, reason := range []string{"worker", "control", "title", "owner", "session", "capability", "deadline", "lease", "revoked", "budget"} {
		deny = reason
		if _, err := cb(ctx, sealed, args); err == nil || calls != 1 {
			t.Fatalf("%s dispatched", reason)
		}
		if _, err := sealed.(proposalCaptureRunnable).Run(ctx, args); err == nil || calls != 1 {
			t.Fatalf("direct %s dispatched", reason)
		}
	}
	deny = ""
	for _, c := range []context.Context{context.Background(), botPolicyContext(t, botBoundaryContext(t), nil, nil)} {
		bad := &contextToolContext{minimalReadonlyContext{Context: c}}
		if _, err := sealed.(proposalCaptureRunnable).Run(bad, args); err == nil || calls != 1 {
			t.Fatal("missing identity/policy/service accepted")
		}
	}
	cancelled, cancel := context.WithCancel(base)
	cancel()
	if _, err := sealed.(proposalCaptureRunnable).Run(&contextToolContext{minimalReadonlyContext{Context: cancelled}}, args); err == nil || calls != 1 {
		t.Fatal("cancelled call accepted")
	}
	foreign, _ := execution.BotRunIdentityFromContext(base)
	foreign.ActorID = "foreign"
	foreignCtx := &contextToolContext{minimalReadonlyContext{Context: execution.WithBotRunIdentity(base, foreign)}}
	foreignTool, err := NewBotEmployeeChatTool(raw, func(tool.Context) (tool.Context, error) { return ctx, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignTool.(proposalCaptureRunnable).Run(foreignCtx, args); err == nil || calls != 1 {
		t.Fatal("foreign binding accepted")
	}
	if err := AuthorizeBotTool(base, "employee_chat", args); err == nil {
		t.Fatal("global policy widened")
	}
	if _, err := NewBotEmployeeChatTool(raw, nil); err == nil {
		t.Fatal("missing guard accepted")
	}
}
