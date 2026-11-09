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
