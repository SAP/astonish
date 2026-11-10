package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"

	"github.com/SAP/astonish/pkg/store"
)

func scheduledRetrievalFixture(t *testing.T) *store.BotWorkState {
	t.Helper()
	raw, err := os.ReadFile("testdata/bot_scheduled_retrieval_work.json")
	if err != nil {
		t.Fatal(err)
	}
	var w store.BotWorkState
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	return &w
}

func TestBotWorkMechanicalScheduledPersistedCandidate(t *testing.T) {
	w := scheduledRetrievalFixture(t)
	c := w.Candidate
	if c.Revision != 3 || w.Repairs != 2 || c.Digest != "773eee2def4ab1e0ea8d5ac6c413ccec5a742dc5ecd67d9a643b72755c333a46" {
		t.Fatal("wrong persisted candidate")
	}
	if !strings.Contains(c.Text, "| Retrieval time (UTC) | 2026-11-10 19:03 UTC |") {
		t.Fatal("fixture missing corrected field")
	}
	text := c.Text
	got, refs, finding := botCheckMetadata(c, store.BotWorkVerificationRetrievalTime)
	if got != store.BotCriterionSatisfied {
		t.Fatalf("exact scheduled candidate rejected: %s refs=%v: %s", got, refs, finding)
	}
	if c.Text != text {
		t.Fatal("candidate rewritten")
	}
}

func TestBotWorkMechanicalScheduledTrace(t *testing.T) {
	raw, err := os.ReadFile("testdata/bot_scheduled_retrieval_trace.json")
	if err != nil {
		t.Fatal(err)
	}
	var trace []struct {
		EventID      int `json:"event_id"`
		Author, Text string
	}
	if err := json.Unmarshal(raw, &trace); err != nil {
		t.Fatal(err)
	}
	w := scheduledRetrievalFixture(t)
	count := 0
	for _, e := range trace {
		if e.Author != "chat" {
			continue
		}
		count++
		c := *w.Candidate
		c.Text = e.Text
		c.Observations = c.Observations[:count]
		verdict, _, finding := botCheckMetadata(&c, store.BotWorkVerificationRetrievalTime)
		if verdict != store.BotCriterionSatisfied {
			t.Fatalf("event %d: %s", e.EventID, finding)
		}
	}
	if count != 3 {
		t.Fatalf("got %d candidates", count)
	}
}

func TestBotWorkMechanicalScheduledSameURL(t *testing.T) {
	for _, tc := range []struct {
		name, minute string
		observations int
		want         store.BotCriterionVerdict
	}{
		{"first fetch", "19:02", 1, store.BotCriterionSatisfied},
		{"earlier fetch after repair", "19:02", 2, store.BotCriterionSatisfied},
		{"middle fetch after second repair", "19:03", 3, store.BotCriterionSatisfied},
		{"latest fetch", "19:04", 3, store.BotCriterionSatisfied},
		{"measurement cannot satisfy field", "19:00", 3, store.BotCriterionFailed},
		{"unobserved future fetch", "19:05", 3, store.BotCriterionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := scheduledRetrievalFixture(t).Candidate
			c.Text = "Source: " + c.Observations[0].Source + "\n| Retrieval time (UTC) | 2026-11-10 " + tc.minute + " UTC |\n- **Measurement vs. fetch time:** 2026-11-10 19:00 UTC is the measurement timestamp."
			c.Observations = c.Observations[:tc.observations]
			got, _, finding := botCheckMetadata(c, store.BotWorkVerificationRetrievalTime)
			if got != tc.want {
				t.Fatalf("got %s want %s: %s", got, tc.want, finding)
			}
		})
	}
}

func TestBotWorkMechanicalScheduledInvalidFieldStillBlocked(t *testing.T) {
	c := scheduledRetrievalFixture(t).Candidate
	// Keep the correct 19:03 claim in narrative; it must not mask a wrong table field.
	c.Text = strings.Replace(c.Text, "| Retrieval time (UTC) | 2026-11-10 19:03 UTC |", "| Retrieval time (UTC) | 2026-11-10 19:00 UTC |", 1)
	got, _, finding := botCheckMetadata(c, store.BotWorkVerificationRetrievalTime)
	if got != store.BotCriterionFailed || !strings.Contains(finding, "reported 2026-11-10T19:00Z") {
		t.Fatalf("wrong table field escaped: %s %s", got, finding)
	}
}

func TestBotWorkMechanicalScheduledSemanticFailurePreserved(t *testing.T) {
	w := scheduledRetrievalFixture(t)
	review := &store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c4", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, Explanation: "The reported content does not match the cited source body."}}}
	applyBotMechanicalChecks(w, review)
	if review.Criteria[0].Verdict != store.BotCriterionFailed || review.Criteria[0].Method != store.BotAssessmentModel {
		t.Fatalf("mechanical pass erased semantic failure: %+v", review.Criteria[0])
	}
}

func TestBotWorkMechanicalScheduledRepairWithoutRefetch(t *testing.T) {
	fixture := scheduledRetrievalFixture(t).Candidate
	ctx, req, _ := botChatTestRequest(t)
	req.Instructions = scheduledRetrievalFixture(t).Contract.Instructions
	req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	req.Work.Phase = store.BotWorkProducing
	req.Work.Criteria = []store.BotWorkCriterion{{ID: "retrieval", SourceIDs: []string{"assignment", "charter"}, Text: "Report retrieval time", VerificationKind: store.BotWorkVerificationRetrievalTime}}
	ws := &workMemoryStore{state: req.Work.Clone()}
	calls := 0
	_, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, &metadataApprovingModel{}, false, func(q bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		calls++
		text := fixture.Text
		if calls == 1 {
			text = strings.Replace(text, "| Retrieval time (UTC) | 2026-11-10 19:03 UTC |", "| Retrieval time (UTC) | 2026-11-10 19:00 UTC |", 1)
		}
		// Reuse the same captured run ledger; correction performs no new fetch.
		c.FinalEventID = "final"
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: text, Unchanged: fixture.Observations}}, nil
	})
	if err != nil || calls != 2 || ws.state.Phase != store.BotWorkAccepted {
		t.Fatalf("calls=%d phase=%s err=%v", calls, ws.state.Phase, err)
	}
	if ws.state.Candidate.Text != fixture.Text {
		t.Fatal("corrected content changed")
	}
}
