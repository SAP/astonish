package api

import (
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

func TestBotScheduleListResponseCannotMintRead(t *testing.T) {
	for _, scenario := range []string{"json-only", "wrong-call", "verified-read"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
			ctx, _ = bot.WithToolOutcomes(ctx)
			outcome := bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Scoped schedules read", Source: "bot-schedules://test/list-call", ObservedAt: time.Now().UTC(), ActorID: uuid.MustParse(req.Policy.Identity().ActorID)}}}
			if scenario != "json-only" {
				bot.RecordToolOutcome(ctx, "list-call", outcome)
			}
			id := "list-call"
			if scenario == "wrong-call" {
				id = "other-call"
			}
			cr := newChatRunner("session", "user", studioChatAppName, false)
			defer cr.cancel()
			cr.ctx = ctx
			response := &genai.FunctionResponse{ID: id, Name: "list_scheduled_jobs", Response: map[string]any{"count": 1, "jobs": []any{map[string]any{"id": uuid.NewString()}}, "verified": true, bot.ToolOutcomeKey: outcome}}
			cr.observeBotToolOutcome("user", response)
			report := cr.Completion().Report
			if len(report.Changed) != 0 || len(report.Artifacts) != 0 || report.NextRunAt != nil {
				t.Fatalf("read claimed effects: %+v", report)
			}
			if scenario == "verified-read" {
				if len(report.Unchanged) != 1 || len(report.Unverified) != 0 {
					t.Fatalf("read not consumed: %+v", report)
				}
				cr.observeBotToolOutcome("user", response)
				if len(cr.Completion().Report.Unverified) != 1 {
					t.Fatal("read reused")
				}
			} else if len(report.Unchanged) != 0 || len(report.Unverified) != 1 {
				t.Fatalf("unobserved read trusted: %+v", report)
			}
		})
	}
}
