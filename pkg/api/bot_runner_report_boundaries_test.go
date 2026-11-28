package api

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotBlockedCompletionRetainsOutstandingObligation(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Perform the scheduled source read."})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	w.AgentWork.Disposition = store.AgentWorkBlocked
	w.AgentWork.Remaining = []store.BotWorkObligation{{ID: req.Run.ID, SourceRunID: req.Run.ID, Summary: "Perform the required source read.", Reason: "Required source read was denied.", Continuation: store.BotWorkContinueOwnerRequest}}
	w.Unresolved = []string{"Required source read was denied."}
	req.Work = w
	saved := &agentExecutionWorkStore{state: w}
	result, next, err := runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, w, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		*c = ChatCompletion{Final: true, FinalEventID: "final", FinalText: "The source was denied; the assignment remains incomplete."}
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: c.FinalText, EffectState: store.BotReportEffectsReadOnly}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunPartial || (next.AgentWork.Disposition != store.AgentWorkBlocked && next.AgentWork.Disposition != store.AgentWorkPartial) || len(next.AgentWork.Remaining) != 1 || len(next.Unresolved) == 0 {
		t.Fatalf("outstanding denied obligation was promoted: result=%+v work=%+v", result, next)
	}
}

func TestBotBlockedPromotionUsesTypedOutstandingWork(t *testing.T) {
	_, req, _ := botChatTestRequest(t)
	base := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Read source"})
	completion := ChatCompletion{FinalEventID: "final", FinalText: "Available evidence."}
	report := store.BotReport{Summary: completion.FinalText, EffectState: store.BotReportEffectsReadOnly}
	for _, status := range []store.BotWorkItemStatus{store.BotWorkItemPending, store.BotWorkItemInProgress, store.BotWorkItemBlocked} {
		t.Run(string(status), func(t *testing.T) {
			req.Work = base.Clone()
			req.Work.AgentWork.Items = []store.BotWorkItem{{Status: status}}
			if botCanPromoteStaleBlockedReadOnlyFinal(completion, req, report) {
				t.Fatal("unfinished typed item promoted to complete")
			}
		})
	}
	req.Work = base
	completion.Err = &botToolFeedbackError{cause: errors.New("source read unavailable"), recoverable: true}
	if !botCanPromoteStaleBlockedReadOnlyFinal(completion, req, report) {
		t.Fatal("recoverable read-only feedback without retained obligations rejected")
	}
}
func TestBotAgentFailureProjectionFinalEnvelopeBinding(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Saved action"})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	req.Work = w
	saved := &agentExecutionWorkStore{state: w}
	result, _, _ := runBotAgentWork(ctx, req, &BotChatResources{Work: saved}, w, func(_ bot.RunRequest, _ *ChatCompletion) (bot.RunResult, error) {
		return botChatFailure(bot.ErrorPermanent, &bot.RunError{Class: bot.ErrorPermanent, Cause: errors.New("saved effect requires reconciliation"), UncertainEffect: true, TerminalForAttempt: true})
	})
	if result.Status != store.RunPartial {
		t.Fatalf("status=%s", result.Status)
	}
	if err := result.Report.ValidateEmployeeMessageIntegrity(result.Status); err != nil {
		t.Fatalf("final source projection invalidated employee envelope: %v", err)
	}
}
