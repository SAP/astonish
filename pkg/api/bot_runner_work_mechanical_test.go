package api

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type metadataApprovingModel struct{}

func (*metadataApprovingModel) Name() string { return "always-approve" }
func (*metadataApprovingModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var w store.BotWorkState
		_ = json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &w)
		review := store.BotWorkReview{CandidateDigest: w.Candidate.Digest}
		for _, c := range w.Criteria {
			review.Criteria = append(review.Criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Everything is correct"})
		}
		raw, _ := json.Marshal(review)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
	}
}

func TestBotWorkMechanicalMetadata(t *testing.T) {
	source := "https://example.org/data?q=1&unit=metric"
	good := "Source: " + source + "\n| Retrieval time (UTC) | 2026-11-10T15:55Z |"
	for _, tc := range []struct {
		name, text                  string
		kind                        store.BotWorkVerificationKind
		verdict                     store.BotCriterionVerdict
		noEvidence, private, future bool
	}{
		{name: "unsupported field cannot hide behind correct field", text: good + "\nRetrieval time (UTC): yesterday", kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionFailed},
		{name: "correct subset", text: good, kind: store.BotWorkVerificationSourceURL, verdict: store.BotCriterionSatisfied},
		{name: "correct", text: good, kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionSatisfied},
		{name: "wrong minute despite correct elsewhere", text: strings.Replace(good, "15:55Z", "15:45Z", 1) + "\nRetrieved at: 2026-11-10T15:55Z", kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionFailed},
		{name: "full query", text: good, kind: store.BotWorkVerificationSourceURL, verdict: store.BotCriterionSatisfied},
		{name: "missing query", text: "Source: https://example.org/data", kind: store.BotWorkVerificationSourceURL, verdict: store.BotCriterionFailed},
		{name: "abbreviated", text: "Source: https://example.org/data?q=...", kind: store.BotWorkVerificationSourceURL, verdict: store.BotCriterionFailed},
		{name: "no evidence", text: good, kind: store.BotWorkVerificationSourceURL, verdict: store.BotCriterionUnverified, noEvidence: true},
		{name: "private tool body not delivered", text: good, kind: store.BotWorkVerificationSourceURL, verdict: store.BotCriterionFailed, private: true},
		{name: "measurement is not retrieval", text: "Source: " + source + "\nMeasurement: 2026-11-10T15:55Z", kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionFailed},
		{name: "future definition", text: "Saved standards", kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionSatisfied, noEvidence: true, future: true},
		{name: "legacy no URL", text: "Hello", kind: "", verdict: store.BotCriterionSatisfied, noEvidence: true},
		{name: "UTC format", text: strings.Replace(good, "2026-11-10T15:55Z", "2026-11-10 15:55:21 UTC", 1), kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionSatisfied},
		{name: "offset format", text: strings.Replace(good, "15:55Z", "10:55:21-05:00", 1), kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionSatisfied},
		{name: "wrong source association", text: good + "\nSource: https://example.org/other\nRetrieved at: 2026-11-10T15:55Z", kind: store.BotWorkVerificationRetrievalTime, verdict: store.BotCriterionUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &store.BotWorkCandidate{Text: tc.text}
			if !tc.noEvidence {
				c.Observations = []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: source, ObservedAt: time.Date(2026, 11, 10, 15, 55, 21, 0, time.UTC)}}
			}
			if tc.name == "correct subset" {
				c.Observations = append(c.Observations, store.BotEvidence{Kind: store.BotEvidenceObservation, Source: "https://example.org/unused", ObservedAt: time.Now()})
			}
			if tc.private {
				c.Text = "Report omitted"
				c.Bodies = map[string]string{"tool-response:x": tc.text, "producer-final-draft": tc.text}
			}
			c.Digest = c.ContentDigest()
			criterion := store.BotWorkCriterion{ID: "c", SourceIDs: []string{"assignment"}, VerificationKind: tc.kind}
			if tc.future {
				criterion.FulfillmentScope = store.BotWorkCriterionFutureTaskDefinition
			}
			w := &store.BotWorkState{Contract: store.BotWorkContract{Instructions: "Include the complete source URL and retrieval time."}, Candidate: c, Criteria: []store.BotWorkCriterion{criterion}}
			review := &store.BotWorkReview{CandidateDigest: c.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel}}}
			applyBotMechanicalChecks(w, review)
			if got := review.Criteria[0]; got.Verdict != tc.verdict {
				t.Fatalf("got %+v want %s", got, tc.verdict)
			}
		})
	}
}

func TestBotWorkMechanicalRepair(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Instructions = "Deliver a report with the complete source URL and observed retrieval time."
	req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	req.Work.Phase = store.BotWorkProducing
	req.Work.Criteria = []store.BotWorkCriterion{{ID: "retrieval", SourceIDs: []string{"assignment", "charter"}, Text: "Report retrieval time", VerificationKind: store.BotWorkVerificationRetrievalTime}}
	ws := &workMemoryStore{state: req.Work.Clone()}
	calls := 0
	_, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, &metadataApprovingModel{}, false, func(q bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		calls++
		stamp := "2026-11-10T15:45Z"
		if calls > 1 {
			stamp = "2026-11-10T15:55Z"
			if !strings.Contains(q.Instructions, "Mechanical") || !strings.Contains(q.Instructions, "15:55") {
				t.Error("mechanical correction missing")
			}
		}
		c.FinalEventID = "final"
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Source: https://example.org/data?q=1\nRetrieval time (UTC): " + stamp, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "https://example.org/data?q=1", ObservedAt: time.Date(2026, 11, 10, 15, 55, 21, 0, time.UTC)}}}}, nil
	})
	if err != nil || calls != 2 || ws.state.Phase != store.BotWorkAccepted {
		t.Fatalf("wrong minute escaped approving model: calls=%d phase=%s err=%v", calls, ws.state.Phase, err)
	}
	if ws.state.Review.Criteria[0].Method != store.BotAssessmentMechanical {
		t.Fatal("missing runtime mechanical method")
	}
}
