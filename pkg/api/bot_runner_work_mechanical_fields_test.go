package api

import (
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotWorkMechanicalCorrectedNarrativeAccepted(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Instructions = "Deliver a report with the complete source URL and observed retrieval time."
	req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	req.Work.Phase = store.BotWorkProducing
	req.Work.Criteria = []store.BotWorkCriterion{{ID: "retrieval", SourceIDs: []string{"assignment", "charter"}, Text: "Report retrieval time", VerificationKind: store.BotWorkVerificationRetrievalTime}}
	ws := &workMemoryStore{state: req.Work.Clone()}
	calls := 0
	corrected := "Source: https://example.org/data?q=1\n| Retrieval time (UTC) | 2026-11-10T18:37Z |\n| Source measurement time | 2026-11-10T18:30Z |\n- Retrieval time given as `2026-11-10T18:37Z`, distinct from measurement time `2026-11-10T18:30Z`"
	result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, &metadataApprovingModel{}, false, func(q bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		calls++
		text := "Source: https://example.org/data?q=1\nRetrieved at: 2026-11-10T18:30Z"
		if calls > 1 {
			if !strings.Contains(q.Instructions, "18:37Z") {
				t.Fatal("repair missing actual observation")
			}
			text = corrected
		}
		c.FinalEventID = "final"
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: text, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "https://example.org/data?q=1", ObservedAt: time.Date(2026, 11, 10, 18, 37, 45, 0, time.UTC)}}}}, nil
	})
	if err != nil || calls != 2 || ws.state.Phase != store.BotWorkAccepted {
		t.Fatalf("calls=%d phase=%s err=%v", calls, ws.state.Phase, err)
	}
	if result.Report.Summary != corrected || ws.state.Candidate.Text != corrected {
		t.Fatal("corrected report or measurement narrative rewritten")
	}
}

func TestBotWorkMechanicalFieldBoundaries(t *testing.T) {
	source := "https://example.org/data?q=1"
	good := "Source: " + source + "\n| Retrieval time (UTC) | 2026-11-10T18:37Z |\n| Source measurement time (UTC) | 2026-11-10T18:30Z |"
	for _, tc := range []struct {
		name, text string
		want       store.BotCriterionVerdict
	}{
		{"actual final repair narrative", good + "\n- Retrieval time given as a single explicit UTC timestamp `2026-11-10T18:37Z`, kept distinct from the source's own measurement time `2026-11-10T18:30Z`", store.BotCriterionSatisfied},
		{"UTC declared in label", "Source: " + source + "\n**Retrieved (UTC):** 2026-11-10 18:37 (HTTP 200)", store.BotCriterionSatisfied},
		{"measurement in next table cell", "| Retrieval time | 2026-11-10T18:37Z | Measurement time | 2026-11-10T18:30Z |\nSource: " + source, store.BotCriterionSatisfied},
		{"wrong actual field", strings.Replace(good, "18:37Z", "18:30Z", 1), store.BotCriterionFailed},
		{"wrong second explicit field", good + "\nRetrieved at: 2026-11-10T18:30Z", store.BotCriterionFailed},
		{"measurement narrative alone", "Source: " + source + "\nRetrieval time differs from measurement time 2026-11-10T18:30Z", store.BotCriterionFailed},
		{"multiple sources", good + "\nSource: https://example.org/other\nRetrieval time: 2026-11-10T18:39Z", store.BotCriterionSatisfied},
		{"wrong source minute", good + "\nSource: https://example.org/other\nRetrieval time: 2026-11-10T18:37Z", store.BotCriterionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &store.BotWorkCandidate{Text: tc.text, Observations: []store.BotEvidence{
				{Kind: store.BotEvidenceObservation, Source: source, ObservedAt: time.Date(2026, 11, 10, 18, 37, 45, 0, time.UTC)},
				{Kind: store.BotEvidenceObservation, Source: "https://example.org/other", ObservedAt: time.Date(2026, 11, 10, 18, 39, 12, 0, time.UTC)},
			}}
			c.Digest = c.ContentDigest()
			got, _, finding := botCheckMetadata(c, store.BotWorkVerificationRetrievalTime)
			if got != tc.want {
				t.Fatalf("got %s want %s: %s", got, tc.want, finding)
			}
			if c.Text != tc.text {
				t.Fatal("checker rewrote report")
			}
		})
	}
}
