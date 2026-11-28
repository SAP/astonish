package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

func TestBotRoutineCaptureOutcome(t *testing.T) {
	for _, scenario := range []string{"pending", "absent", "wrong-call", "foreign-run", "applied", "mixed", "partial-error"} {
		t.Run(scenario, func(t *testing.T) {
			id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()}
			ctx, _ := bot.WithToolOutcomes(execution.WithBotRunIdentity(context.Background(), id))
			c := bot.RoutineCapture{OperationID: uuid.New(), BotID: id.BotID, SourceRunID: id.RunID, OwnerID: uuid.MustParse(id.ActorID), Operation: store.BotControlGoalCreate, Status: store.BotOperationPending, Revision: 1, ObservedAt: time.Now()}
			out := bot.ToolOutcome{Verified: true, RoutineCapture: &c}
			response := &genai.FunctionResponse{ID: "call", Name: "propose_bot_control", Response: map[string]any{"operation_id": c.OperationID.String(), "status": "pending", "decision": "execute", bot.ToolOutcomeKey: out}}
			cr := &ChatRunner{ctx: ctx}
			switch scenario {
			case "wrong-call":
				response.ID = "other"
			case "foreign-run":
				c.SourceRunID = uuid.New()
			case "applied":
				response.Response["status"] = "applied"
			case "mixed":
				out.NextRunAt = &c.ObservedAt
			case "partial-error":
				bot.RecordToolOutcome(ctx, "bad", bot.ToolOutcome{Unverified: []string{"invalid bot request: payload.goal.goal required"}})
				cr.observeBotToolOutcome("user", &genai.FunctionResponse{ID: "bad", Name: "propose_bot_control", Response: map[string]any{"error": "bad nesting"}})
			}
			if scenario != "absent" {
				bot.RecordToolOutcome(ctx, "call", out)
			}
			cr.observeBotToolOutcome("user", response)
			cr.completion.FinalText = "Done, goal applied."
			report := botCompletionReport(cr.completion, bot.RunRequest{})
			if scenario == "pending" || scenario == "partial-error" {
				if len(report.Unchanged) != 1 || len(report.Changed) != 0 || report.NextRunAt != nil || !strings.Contains(report.Summary, "goal_create") || !strings.Contains(report.Summary, "pending, not applied") || strings.Contains(report.Summary, "Done") {
					t.Fatalf("not pending-only: %+v", report)
				}
				if scenario == "pending" && len(report.Unverified) != 0 {
					t.Fatal(report.Unverified)
				}
				if scenario == "partial-error" && (len(report.Unverified) != 2 || !strings.Contains(report.Unverified[1], "payload.goal.goal")) {
					t.Fatalf("erased actual error: %+v", report)
				}
				cr.observeBotToolOutcome("user", response)
				if len(cr.completion.Report.Unverified) != len(report.Unverified)+1 {
					t.Fatal("reused receipt")
				}
			} else if len(report.Unverified) == 0 || len(report.Unchanged) != 0 || len(report.Changed) != 0 {
				t.Fatalf("trusted forged receipt: %+v", report)
			}
		})
	}
}
