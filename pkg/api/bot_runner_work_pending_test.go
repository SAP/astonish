package api

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type pendingAssessmentModel struct {
	bad    bool
	scoped bool
}

func (*pendingAssessmentModel) Name() string { return "stubborn-application-reviewer" }
func (m *pendingAssessmentModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var w store.BotWorkState
		_ = json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &w)
		var out any
		if strings.HasPrefix(r.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_PREPARE") {
			out = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "schedule", SourceIDs: []string{"assignment", "charter"}, Text: "Schedule daily weather at 09:00 UTC"}}}
		} else {
			a := store.BotCriterionAssessment{CriterionID: "schedule", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Schedule has not applied; read the schedule again"}
			review := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{a}}
			m.scoped = strings.Contains(r.Contents[0].Parts[0].Text, `"scope":"current_turn_pending_acknowledgment"`)
			if m.scoped {
				current := review
				current.Criteria = append([]store.BotCriterionAssessment(nil), review.Criteria...)
				var pending struct {
					Controls []botWorkPendingControl `json:"trusted_pending_controls"`
				}
				_ = json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &pending)
				payloadMatches := len(pending.Controls) == 1 && strings.Contains(string(pending.Controls[0].Payload), `"cron":"0 9 * * *"`)
				if !m.bad && payloadMatches {
					current.Criteria[0].Verdict = store.BotCriterionSatisfied
					current.Criteria[0].Explanation = "Queued payload matches original requested daily 09:00 UTC weather; acknowledgment says pending, not applied"
				}
				out = map[string]any{"current_turn": current, "deferred_application": review}
			} else {
				out = review
			}
		}
		raw, _ := json.Marshal(out)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
	}
}

func TestBotWorkPendingNoApplicationRepair(t *testing.T) {
	for _, mode := range []string{"pending", "optimistic-producer", "routine", "wrong-payload", "missing-payload", "mixed-missing-deliverable", "unverified", "forged"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Instructions = "Schedule daily weather at 09:00 UTC"
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			ws := &workMemoryStore{state: req.Work.Clone()}
			mdl := &pendingAssessmentModel{bad: mode == "wrong-payload" || mode == "mixed-missing-deliverable"}
			calls := 0
			result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, mdl, false, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				calls++
				id := execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, ActorID: uuid.NewString()}
				ledger, _ := bot.WithToolOutcomes(execution.WithBotRunIdentity(ctx, id))
				capture := bot.ScheduleCapture{PayloadJSON: `{"operation":"schedule_create","schedule":{"trigger":{"cron":"0 9 * * *","timezone":"UTC"}}}`, OperationID: uuid.New(), BotID: id.BotID, SourceRunID: id.RunID, OwnerID: uuid.MustParse(id.ActorID), Operation: store.BotControlScheduleCreate, Status: store.BotOperationPending, Revision: 1, ObservedAt: time.Now()}
				if mode == "wrong-payload" {
					capture.PayloadJSON = strings.ReplaceAll(capture.PayloadJSON, "0 9 * * *", "0 18 * * *")
				}
				if mode == "missing-payload" {
					capture.PayloadJSON = ""
				}
				cr := &ChatRunner{ctx: ledger}
				response := &genai.FunctionResponse{ID: "enqueue", Name: "schedule_job", Response: map[string]any{"operation_id": capture.OperationID.String(), "status": "pending"}}
				if mode == "routine" {
					routine := bot.RoutineCapture{OperationID: capture.OperationID, BotID: id.BotID, SourceRunID: id.RunID, OwnerID: capture.OwnerID, Operation: store.BotControlGoalCreate, Status: store.BotOperationPending, Revision: 1, ObservedAt: time.Now(), PayloadJSON: `{"operation":"goal_create","goal":{"instructions":"weather","cron":"0 9 * * *"}}`}
					response.Name = "propose_bot_control"
					response.Response["decision"] = "execute"
					bot.RecordToolOutcome(ledger, "enqueue", bot.ToolOutcome{Verified: true, RoutineCapture: &routine})
				} else if mode != "forged" {
					bot.RecordToolOutcome(ledger, "enqueue", bot.ToolOutcome{Verified: true, ScheduleCapture: &capture})
				}
				cr.observeBotToolOutcome("user", response)
				*c = cr.completion
				c.FinalEventID = "final"
				if mode == "optimistic-producer" {
					c.FinalText = "Done — I've set up schedule."
				}
				if mode == "unverified" {
					c.Report.Unverified = append(c.Report.Unverified, "unrelated send uncertain")
				}
				if mode == "mixed-missing-deliverable" {
					c.Report.Summary += "\nMissing requested report"
				}
				return bot.RunResult{Status: store.RunSucceeded, Report: c.Report}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("pending operation replayed/read for repair: producer calls=%d", calls)
			}
			if mode == "optimistic-producer" && strings.Contains(result.Report.Summary, "Done — I've set up schedule.") {
				t.Fatal("permissive pending assessor published optimistic producer application claim")
			}
			if mode == "pending" || mode == "optimistic-producer" || mode == "routine" {
				if result.Status != store.RunSucceeded || ws.state.Phase != store.BotWorkAccepted || !mdl.scoped || ws.state.Repairs != 0 || !strings.Contains(result.Report.Summary, "pending") || len(result.Report.Changed) != 0 {
					t.Fatalf("pending not accepted truthfully: %+v state=%+v scoped=%v", result, ws.state, mdl.scoped)
				}
			} else if ws.state.Phase == store.BotWorkAccepted {
				t.Fatal("invalid or unrelated work bypassed semantic assessment")
			}
		})
	}
}
