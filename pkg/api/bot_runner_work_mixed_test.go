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

func TestBotPendingCandidateText(t *testing.T) {
	const receipt = "Schedule request queued; application pending."
	controls := []botWorkPendingControl{{Acknowledgment: receipt}, {Acknowledgment: "Routine request queued; application pending."}}
	if got := botPendingAcknowledgment(controls); got != receipt+"\n\n"+controls[1].Acknowledgment {
		t.Fatalf("server acknowledgments lost or altered: %q", got)
	}
}

// Mixed prose delivery is deliberately unsupported: the reviewer must see the
// draft, but private content cannot satisfy an original report-delivery criterion.
type mixedPendingAssessmentModel struct {
	t           *testing.T
	final       string
	receipt     string
	stale       bool
	receiptOnly bool
	seen        string
}

func (*mixedPendingAssessmentModel) Name() string { return "mixed-pending-fixture" }
func (m *mixedPendingAssessmentModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var out any
		if strings.HasPrefix(r.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_PREPARE") {
			out = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "mixed", SourceIDs: []string{"assignment", "charter"}, Text: "Provide today's report and queue daily weather at 09:00 UTC; do not claim application"}}}
		} else {
			if !strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "must FAIL CURRENT_TURN") {
				m.t.Error("pending assessor was not instructed to reject false applied claims in current turn")
			}
			var input struct {
				*store.BotWorkState
				Controls []botWorkPendingControl `json:"trusted_pending_controls"`
			}
			if err := json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &input); err != nil {
				m.t.Fatal(err)
			}
			if len(input.Controls) != 1 {
				m.t.Fatal("missing trusted receipt")
			}
			w := input.BotWorkState
			m.seen = w.Candidate.Text
			if m.seen != m.receipt {
				m.t.Errorf("publishable candidate is not server-only receipt: %q", m.seen)
			}
			if w.Candidate.Bodies[botWorkPendingProducerDraft] != m.final {
				m.t.Error("review lost private producer draft")
			}
			verdict := store.BotCriterionSatisfied
			explanation := "Exact queued receipt meets the control-only request."
			if !m.receiptOnly {
				verdict = store.BotCriterionUnverified
				explanation = "Mixed prose delivery unsupported: report draft retained privately, not published."
			}
			digest := w.Candidate.Digest
			if m.stale {
				digest = strings.Repeat("0", 64)
			}
			current := store.BotWorkReview{CandidateDigest: digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "mixed", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + digest}, Explanation: explanation}}}
			deferred := current
			deferred.Criteria = append([]store.BotCriterionAssessment(nil), current.Criteria...)
			deferred.Criteria[0].Verdict = store.BotCriterionUnverified
			deferred.Criteria[0].Explanation = "Application remains pending."
			out = map[string]any{"current_turn": current, "deferred_application": deferred}
		}
		raw, _ := json.Marshal(out)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
	}
}

func TestBotWorkMixedPendingPublication(t *testing.T) {
	for _, tc := range []struct {
		name, final                  string
		accepted, stale, receiptOnly bool
	}{
		{name: "honest mixed conservatively unverified", final: "Report: sunny today. Schedule request queued."},
		{name: "already combined remains private", final: "Report: sunny today.\n\nSchedule request queued."},
		{name: "optimistic control-only remains private", final: "Done — I've set up schedule.", accepted: true, receiptOnly: true},
		{name: "false applied", final: "Report: sunny today. The schedule is now active."},
		{name: "false scheduled", final: "Report: sunny today. I scheduled the daily report."},
		{name: "missing report", final: "Schedule request queued."},
		{name: "stale digest", final: "Report: sunny today.", stale: true},
		{name: "legacy receipt", accepted: true, receiptOnly: true},
		{name: "exact receipt no duplicate", accepted: true, receiptOnly: true, final: "receipt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Instructions = "Provide today's report and queue daily weather at 09:00 UTC."
			if tc.receiptOnly {
				req.Instructions = "Queue daily weather at 09:00 UTC."
			}
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
			ws := &workMemoryStore{state: req.Work.Clone()}
			mdl := &mixedPendingAssessmentModel{t: t, stale: tc.stale, receiptOnly: tc.receiptOnly}
			calls := 0
			result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, mdl, false, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
				calls++
				id := execution.BotRunIdentity{BotID: req.Bot.ID, RunID: req.Run.ID, ActorID: uuid.NewString()}
				ledger, _ := bot.WithToolOutcomes(execution.WithBotRunIdentity(ctx, id))
				capture := bot.ScheduleCapture{PayloadJSON: `{"operation":"schedule_create","schedule":{"trigger":{"cron":"0 9 * * *","timezone":"UTC"}}}`, OperationID: uuid.New(), BotID: id.BotID, SourceRunID: id.RunID, OwnerID: uuid.MustParse(id.ActorID), Operation: store.BotControlScheduleCreate, Status: store.BotOperationPending, Revision: 1, ObservedAt: time.Now()}
				bot.RecordToolOutcome(ledger, "enqueue", bot.ToolOutcome{Verified: true, ScheduleCapture: &capture})
				cr := &ChatRunner{ctx: ledger}
				cr.observeBotToolOutcome("user", &genai.FunctionResponse{ID: "enqueue", Name: "schedule_job", Response: map[string]any{"operation_id": capture.OperationID.String(), "status": "pending"}})
				*c = cr.completion
				c.FinalEventID = "final"
				c.FinalText = tc.final
				if tc.final == "receipt" {
					c.FinalText = capture.Acknowledgment()
				}
				mdl.final, mdl.receipt = c.FinalText, capture.Acknowledgment()
				report := botCompletionReport(*c, req)
				if report.Summary != mdl.receipt {
					t.Fatal("legacy report builder changed")
				}
				return bot.RunResult{Status: store.RunSucceeded, Report: report}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("producer replayed %d times", calls)
			}
			if tc.accepted {
				if result.Status != store.RunSucceeded || ws.state.Phase != store.BotWorkAccepted {
					t.Fatalf("not accepted: %+v", result)
				}
				if result.Report.Summary != mdl.seen || result.Report.Summary != ws.state.Candidate.Text {
					t.Fatal("published text differs from reviewed candidate")
				}
				if strings.Count(result.Report.Summary, mdl.receipt) != 1 {
					t.Fatal("receipt missing or duplicated")
				}
			} else {
				if result.Status != store.RunPartial || ws.state.Phase == store.BotWorkAccepted {
					t.Fatalf("unsafe acceptance: %+v", result)
				}
				if strings.Contains(result.Report.Summary, "Report: sunny today.") || strings.Contains(result.Report.Summary, "The schedule is now active.") {
					t.Fatal("rejected draft published")
				}
			}
			if ws.state.Candidate.Bodies[botWorkPendingProducerDraft] != mdl.final {
				t.Fatal("persisted private draft was lost")
			}
			if !tc.accepted && !tc.stale && (result.Report.Failure == nil || !strings.Contains(result.Report.Failure.TechnicalDetails, "Mixed prose delivery unsupported")) {
				t.Fatal("mixed delivery diagnostic was lost")
			}
			if ws.state.Candidate.Digest != ws.state.Candidate.ContentDigest() {
				t.Fatal("candidate changed after hashing")
			}
			mutated := ws.state.Clone()
			mutated.Candidate.Bodies[botWorkPendingProducerDraft] += " altered"
			if mutated.Candidate.ContentDigest() == ws.state.Candidate.Digest {
				t.Fatal("private draft is not digest-bound")
			}
		})
	}
}
