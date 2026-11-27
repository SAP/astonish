package api

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

type employeeReturnJournalContext struct {
	tool.Context
	base    context.Context
	session string
}

func (c employeeReturnJournalContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return nil
}
func (c employeeReturnJournalContext) Value(key any) any      { return c.base.Value(key) }
func (c employeeReturnJournalContext) Err() error             { return c.base.Err() }
func (c employeeReturnJournalContext) SessionID() string      { return c.session }
func (c employeeReturnJournalContext) FunctionCallID() string { return "employee-return" }

type employeeReturnJournalService struct {
	result *bot.EmployeeChatResult
	err    error
}

func (s employeeReturnJournalService) Send(context.Context, bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
	return s.result, s.err
}

func TestEmployeeChatServiceReturnJournal(t *testing.T) {
	for _, status := range []bot.EmployeeChatStatus{bot.EmployeeChatCompleted, bot.EmployeeChatPartial, bot.EmployeeChatNeedsInput, bot.EmployeeChatFailed, bot.EmployeeChatCancelled} {
		t.Run(string(status), func(t *testing.T) {
			for _, mode := range []string{"returned", "nil", "error", "worker-uncertain"} {
				t.Run(mode, func(t *testing.T) {
					ctx, req, _ := botChatTestRequest(t)
					identity := req.Policy.Identity()
					identity.ActorID = req.Bot.OwnerID.String()
					ctx = execution.WithBotRunIdentity(ctx, identity)
					ctx = execution.WithRunPolicy(ctx, req.Policy)
					ctx, _ = bot.WithToolOutcomes(ctx)
					result := &bot.EmployeeChatResult{WorkerID: uuid.NewString(), Status: status, PrivateText: "untrusted worker success claim"}
					service := employeeReturnJournalService{result: result}
					if mode == "nil" {
						service.result = nil
					}
					if mode == "error" {
						service.err = errors.New("service unavailable")
					}
					ctx = bot.WithEmployeeChatService(ctx, service)
					tc := employeeReturnJournalContext{base: ctx, session: req.Run.SessionID.String()}
					raw, err := functiontool.New(functiontool.Config{Name: "employee_chat", Description: "Private service"}, func(c tool.Context, request bot.EmployeeChatRequest) (*bot.EmployeeChatResult, error) {
						return bot.EmployeeChatServiceFromContext(c).Send(c, request)
					})
					if err != nil {
						t.Fatal(err)
					}
					sealed, err := agent.NewBotEmployeeChatTool(raw, func(c tool.Context) (tool.Context, error) { return c, nil })
					if err != nil {
						t.Fatal(err)
					}
					journal := &botAgentReadJournal{}
					call := &genai.FunctionCall{ID: tc.FunctionCallID(), Name: sealed.Name(), Args: map[string]any{"message": "inspect"}}
					journal.capture(ctx, call)
					out, runErr := sealed.(interface {
						Run(tool.Context, any) (map[string]any, error)
					}).Run(tc, call.Args)
					if runErr != nil && mode != "error" {
						t.Fatalf("sealed run: %v", runErr)
					}
					if mode == "error" && runErr == nil {
						t.Fatal("service error lost")
					}
					cr := newChatRunner(tc.session, req.Bot.OwnerID.String(), studioChatAppName, false)
					defer cr.cancel()
					cr.ctx = context.WithValue(ctx, botAgentReadJournalKey{}, journal)
					cr.observeBotToolOutcome("tool", &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: out})
					want := store.BotReportEffectsReadOnly
					if mode == "nil" || mode == "error" {
						want = store.BotReportEffectsUncertain
					}
					if got := journal.effectState(); got != want {
						t.Fatalf("state=%s want=%s report=%+v", got, want, cr.completion.Report)
					}
					if mode == "returned" || mode == "worker-uncertain" {
						if len(cr.completion.Report.Unverified) != 0 || len(cr.completion.Report.Changed) != 0 || len(cr.completion.Report.Artifacts) != 0 {
							t.Fatalf("service return invented effects/uncertainty: %+v", cr.completion.Report)
						}
					}
					if mode == "worker-uncertain" {
						worker := ChatCompletion{Report: store.BotReport{EffectState: store.BotReportEffectsUncertain, Unverified: []string{"actual private worker effect unverified"}}}
						mergeBotEmployeeCompletion(&cr.completion, worker)
						if cr.completion.Report.EffectState != store.BotReportEffectsUncertain || len(cr.completion.Report.Unverified) != 1 {
							t.Fatalf("worker uncertainty erased: %+v", cr.completion.Report)
						}
					}
				})
			}
		})
	}
}
