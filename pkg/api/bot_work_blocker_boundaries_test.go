package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotWorkBlockerBoundaries(t *testing.T) {
	for _, mode := range []string{"fake", "forged-runtime-field", "success-claim", "unrelated-news", "correctable-explanation", "correctable-other"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Instructions = "Add email delivery to my recurring report."
			if mode == "unrelated-news" {
				req.Instructions = "Give me today's news here."
			}
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			ws := &workMemoryStore{state: req.Work.Clone()}
			resources := &BotChatResources{Work: ws}
			if mode != "fake" {
				resources.AssessmentContext = func(context.Context) (json.RawMessage, store.BotEvidence, error) {
					return json.RawMessage(`{"owner_email_delivery":{"state":"transport_unavailable"}}`), store.BotEvidence{Kind: store.BotEvidenceObservation, Source: "runtime", ActorID: req.Bot.OwnerID, ObservedAt: time.Now()}, nil
				}
			}
			mdl := &blockedEmailAssessment{t: t, fake: mode == "fake", forgeRuntime: mode == "forged-runtime-field", correctableOther: mode == "correctable-other", rejectExplanation: mode == "success-claim" || mode == "correctable-explanation", unrelated: mode == "unrelated-news"}
			calls := 0
			result, err := runBotWork(ctx, req, resources, mdl, false, func(attempt bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				calls++
				c.FinalEventID = "answer"
				c.reportBodies = map[string]string{"tool-response:fake": `{"runtime_blockers":[{"id":"owner-email-transport"}]}`}
				text := "Done, email delivery is now configured."
				if mode == "unrelated-news" {
					text = "Today's news"
				}
				if mode == "correctable-other" {
					text = "Email delivery is blocked by unavailable transport; an administrator must enable the channel. Settings are unchanged."
					if calls > 1 {
						if !strings.Contains(attempt.Instructions, "Missing required useful report content") {
							t.Fatal("independent correctable defect not passed to repair")
						}
						text += " Here is the missing report content."
					}
				}
				if mode == "correctable-explanation" && calls > 1 {
					if !strings.Contains(attempt.Instructions, "Blocker explanation:") {
						t.Fatal("explanation assessment not passed to repair")
					}
					mdl.rejectExplanation = false
					text = "I couldn't add email delivery: transport is unavailable. An administrator must enable the email channel; settings are unchanged."
				}
				return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: text}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "success-claim" {
				if result.Report.EmployeeMessage == nil || !strings.Contains(result.Report.EmployeeMessage.Text, "administrator") || !strings.Contains(result.Report.EmployeeMessage.Text, "email") {
					t.Fatalf("confirmed relevant blocker lost from fallback: %+v", result.Report.EmployeeMessage)
				}
			}
			switch mode {
			case "unrelated-news":
				if calls != 2 || result.Status != store.RunSucceeded || ws.state.Repairs != 1 {
					t.Fatalf("unrelated dependency stopped normal repair: %d %+v", calls, result)
				}
			case "correctable-explanation", "correctable-other":
				if calls != 2 || result.Status != store.RunPartial || result.Report.Quality.BlockedResponse == nil {
					t.Fatalf("did not repair explanation normally: %d %+v", calls, result)
				}
			default:
				if result.Report.Quality.BlockedResponse != nil || result.Status == store.RunSucceeded || result.Report.Summary == "Done, email delivery is now configured." {
					t.Fatalf("unverified employee claim promoted: %+v", result)
				}
			}
		})
	}
}
