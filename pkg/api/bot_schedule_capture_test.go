package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

func TestBotPrivatePendingScheduleCompletionBoundary(t *testing.T) {
	for _, scenario := range []string{"valid", "unknown", "uncertain", "changed", "unverified", "missing-response", "foreign-source", "producer-error", "not-home", "non-schedule", "invalid-payload", "mismatched-payload"} {
		t.Run(scenario, func(t *testing.T) {
			_, req, _ := botChatTestRequest(t)
			req.Run.Presentation = store.BotRunPresentationHomeInteractive
			req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
			req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Schedule the report"})
			req.Work.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
			payload := store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "Owner request", Schedule: &store.BotControlSchedule{ExpectedRevision: 1, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "*/5 * * * *", Timezone: "UTC", Instructions: "Read the report", Enabled: true}}}
			raw, _ := json.Marshal(payload)
			c := ChatCompletion{Final: true, FinalEventID: "final", FinalText: "pending", Usage: store.BotUsage{ToolCalls: 1}, toolResponses: 1, reportBodies: map[string]string{botEmployeePrivateScheduleSourceKey: req.Run.ID.String()}, pendingControls: []botWorkPendingControl{{OperationID: uuid.NewString(), Operation: payload.Operation, Acknowledgment: "pending", Payload: raw}}}
			report := store.BotReport{EffectState: store.BotReportEffectsConfirmed}
			switch scenario {
			case "unknown":
				report.EffectState = store.BotReportEffectsUnknown
			case "uncertain":
				report.EffectState = store.BotReportEffectsUncertain
			case "changed":
				report.Changed = []store.BotEvidence{{Summary: "unrelated write"}}
			case "unverified":
				report.Unverified = []string{"unknown effect"}
			case "missing-response":
				c.toolResponses = 0
			case "foreign-source":
				c.reportBodies[botEmployeePrivateScheduleSourceKey] = uuid.NewString()
			case "producer-error":
				c.Err = errors.New("failed")
			case "not-home":
				req.Run.Presentation = ""
			case "non-schedule":
				c.pendingControls[0].Operation = store.BotControlOperation("pause")
			case "mismatched-payload":
				c.pendingControls[0].Operation = store.BotControlScheduleRemove
			case "invalid-payload":
				c.pendingControls[0].Payload = json.RawMessage(`{}`)
			}
			if got := botPrivatePendingScheduleCompletion(c, req, report); got != (scenario == "valid") {
				t.Fatalf("pending review scope=%t for %s", got, scenario)
			}
		})
	}
}

func TestBotPrivatePendingScheduleNoModelReview(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Run.Presentation = store.BotRunPresentationHomeInteractive
	req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: "Schedule the report", Charter: "Deliver useful reports"})
	w.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
	req.Work = w
	memory := &agentExecutionWorkStore{state: w}
	reviewer := &workStreamModel{t: t, attempts: [][]workStreamEvent{{{err: errors.New("secondary content review must not decide capture completion")}}}}
	payload := store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "Owner request", Schedule: &store.BotControlSchedule{ExpectedRevision: 1, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "*/5 * * * *", Timezone: "UTC", Instructions: "Read the report", Enabled: true}}}
	raw, _ := json.Marshal(payload)
	result, saved, err := runBotAgentWork(ctx, req, &BotChatResources{Work: memory}, w, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		*c = ChatCompletion{Final: true, FinalEventID: "final", FinalText: "Untrusted optimistic private draft", Usage: store.BotUsage{ToolCalls: 1}, toolResponses: 1, reportBodies: map[string]string{botEmployeePrivateScheduleSourceKey: req.Run.ID.String()}, pendingControls: []botWorkPendingControl{{OperationID: uuid.NewString(), Operation: payload.Operation, Acknowledgment: "Schedule request captured; application is pending.", Payload: raw}}}
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: c.FinalText, EffectState: store.BotReportEffectsConfirmed}}, nil
	}, reviewer)
	if err != nil || reviewer.calls != 0 || saved.Phase != store.BotWorkFinished || !saved.HasAcceptedEmployeeReview() || strings.Contains(result.Report.Summary, "optimistic") || result.Report.NextRunAt != nil || len(result.Report.Changed) != 0 {
		t.Fatalf("capture gated or application claimed: calls=%d result=%+v saved=%+v error=%v", reviewer.calls, result, saved, err)
	}
	for _, assessment := range saved.Review.Criteria {
		if assessment.Method != store.BotAssessmentMechanical || !strings.Contains(assessment.Explanation, "NOT verified") {
			t.Fatalf("capture attestation misrepresented: %+v", assessment)
		}
	}
}

func TestBotScheduleCaptureOutcome(t *testing.T) {
	for _, name := range []string{"schedule_job", "execute_tool"} {
		for _, scenario := range []string{"pending", "absent", "wrong-call", "model-role", "wrong-id", "applied-response", "foreign-run", "foreign-owner", "invalid-revision", "applied-capture", "mixed-effect", "unverified"} {
			t.Run(name+"/"+scenario, func(t *testing.T) {
				id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()}
				ctx, _ := bot.WithToolOutcomes(execution.WithBotRunIdentity(context.Background(), id))
				capture := bot.ScheduleCapture{OperationID: uuid.New(), BotID: id.BotID, SourceRunID: id.RunID, OwnerID: uuid.MustParse(id.ActorID), Operation: store.BotControlScheduleCreate, Status: store.BotOperationPending, Revision: 1, ObservedAt: time.Now()}
				outcome := bot.ToolOutcome{Verified: true, ScheduleCapture: &capture}
				response := &genai.FunctionResponse{ID: "call", Name: name, Response: map[string]any{"operation_id": capture.OperationID.String(), "status": string(store.BotOperationPending), bot.ToolOutcomeKey: outcome}}
				role := "user"
				switch scenario {
				case "wrong-id":
					response.Response["operation_id"] = uuid.NewString()
				case "applied-response":
					response.Response["status"] = string(store.BotOperationApplied)
				case "foreign-run":
					capture.SourceRunID = uuid.New()
				case "foreign-owner":
					capture.OwnerID = uuid.New()
				case "invalid-revision":
					capture.Revision = 0
				case "applied-capture":
					capture.Status = store.BotOperationApplied
				case "mixed-effect":
					outcome.NextRunAt = &capture.ObservedAt
				case "unverified":
					outcome.Verified = false
				case "wrong-call":
					response.ID = "other"
				case "model-role":
					role = "model"
				}
				if scenario != "absent" {
					bot.RecordToolOutcome(ctx, "call", outcome)
				}
				cr := &ChatRunner{ctx: ctx}
				cr.observeBotToolOutcome(role, response)
				cr.completion.FinalText = "Done — I've set up the schedule."
				cr.completion.Usage.ToolCalls = 1
				report := botCompletionReport(cr.completion, bot.RunRequest{})
				if scenario == "pending" {
					if len(report.Unverified) != 0 || len(report.Unchanged) != 1 || len(report.Changed) != 0 || report.NextRunAt != nil || strings.Contains(report.Summary, "Done") || !strings.Contains(report.Summary, "pending") {
						t.Fatalf("not capture-only: %+v", report)
					}
					cr.observeBotToolOutcome("user", response)
					if len(cr.completion.Report.Unverified) != 1 {
						t.Fatal("receipt reused")
					}
				} else if len(report.Unverified) == 0 || len(report.Unchanged) != 0 || len(report.Changed) != 0 || report.NextRunAt != nil {
					t.Fatalf("untrusted receipt accepted: %+v", report)
				}
			})
		}
	}
}
