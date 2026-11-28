package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotWorkRequirementGroundingSourceBoundaries(t *testing.T) {
	goalID := uuid.New()
	for _, tc := range []struct {
		name     string
		contract store.BotWorkContract
		ids      []string
		kind     store.BotWorkVerificationKind
		want     bool
	}{
		{"fresh sources are not URLs", store.BotWorkContract{Instructions: "Use fresh reliable sources."}, []string{"assignment"}, store.BotWorkVerificationSourceURL, false},
		{"URL is not retrieval time", store.BotWorkContract{Instructions: "Include full source URLs."}, []string{"assignment"}, store.BotWorkVerificationRetrievalTime, false},
		{"measurement is not retrieval", store.BotWorkContract{Instructions: "Include the measurement timestamp."}, []string{"assignment"}, store.BotWorkVerificationRetrievalTime, false},
		{"retrieval does not imply URL", store.BotWorkContract{Instructions: "Include retrieval time."}, []string{"assignment"}, store.BotWorkVerificationSourceURL, false},
		{"explicit link", store.BotWorkContract{Instructions: "Link to each source."}, []string{"assignment"}, store.BotWorkVerificationSourceURL, true},
		{"explicit fetch timestamp", store.BotWorkContract{Instructions: "Include the fetch timestamp."}, []string{"assignment"}, store.BotWorkVerificationRetrievalTime, true},
		{"explicit when fetched", store.BotWorkContract{Instructions: "State when you fetched the source."}, []string{"assignment"}, store.BotWorkVerificationRetrievalTime, true},
		{"explicit date of access", store.BotWorkContract{Instructions: "Include date of access."}, []string{"assignment"}, store.BotWorkVerificationRetrievalTime, true},
		{"uncited charter", store.BotWorkContract{Instructions: "Latest news", Charter: "Include retrieval time."}, []string{"assignment"}, store.BotWorkVerificationRetrievalTime, false},
		{"uncited goal", store.BotWorkContract{Instructions: "Latest news", Goals: []store.BotGoal{{ID: goalID, Description: "Include retrieval time."}}}, []string{"assignment"}, store.BotWorkVerificationRetrievalTime, false},
		{"goal evidence is not requirement", store.BotWorkContract{Goals: []store.BotGoal{{ID: goalID, Evidence: []store.BotEvidence{{Source: "https://example.org/retrieval time"}}}}}, []string{"goal:" + goalID.String()}, store.BotWorkVerificationRetrievalTime, false},
		{"attachment filename is not text", store.BotWorkContract{Attachments: []store.BotWorkAttachment{{SourceID: "attachment:1", Filename: "retrieval time.txt", Text: "Latest news"}}}, []string{"attachment:1"}, store.BotWorkVerificationRetrievalTime, false},
		{"blocked attachment", store.BotWorkContract{Attachments: []store.BotWorkAttachment{{SourceID: "attachment:1", Text: "Include retrieval time.", Blocker: "unsupported"}}}, []string{"attachment:1"}, store.BotWorkVerificationRetrievalTime, false},
		{"unknown source", store.BotWorkContract{Instructions: "Include retrieval time."}, []string{"foreign"}, store.BotWorkVerificationRetrievalTime, false},
		{"legacy model", store.BotWorkContract{Instructions: "Include retrieval time."}, []string{"assignment"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			criterion := store.BotWorkCriterion{SourceIDs: tc.ids, Text: "Include full source URL and retrieval time", VerificationKind: tc.kind}
			if got := botWorkMechanicalRequirementGrounded(tc.contract, criterion); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestBotWorkRequirementGroundingNoMechanicalRepair(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Instructions = "Give me the latest stock market news"
	req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: "Be honest and useful."})
	req.Work.Phase = store.BotWorkProducing
	req.Work.Criteria = []store.BotWorkCriterion{
		{ID: "c11", SourceIDs: []string{"assignment", "charter"}, Text: "Report full source URLs", VerificationKind: store.BotWorkVerificationSourceURL},
		{ID: "c12", SourceIDs: []string{"assignment", "charter"}, Text: "Report retrieval timestamps", VerificationKind: store.BotWorkVerificationRetrievalTime},
	}
	ws := &workMemoryStore{state: req.Work.Clone()}
	calls := 0
	_, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, &metadataApprovingModel{}, false, func(_ bot.RunRequest, c *ChatCompletion) (bot.RunResult, error) {
		calls++
		c.FinalEventID = "final"
		return bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Today's market news: shares rose following earnings.", Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "https://example.org/markets", ObservedAt: time.Now()}}}}, nil
	})
	if err != nil || calls != 1 || ws.state.Repairs != 0 || ws.state.Phase != store.BotWorkAccepted {
		t.Fatalf("invented metadata repair: calls=%d phase=%s repairs=%d err=%v", calls, ws.state.Phase, ws.state.Repairs, err)
	}
	if !reflect.DeepEqual(ws.state.Criteria, req.Work.Criteria) {
		t.Fatal("persisted criteria rewritten")
	}
}

func TestBotWorkRequirementGroundingPreparation(t *testing.T) {
	for _, kind := range []store.BotWorkVerificationKind{store.BotWorkVerificationSourceURL, store.BotWorkVerificationRetrievalTime} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			contract := store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Give me the latest stock market news", Charter: "Provide useful, honest answers using fresh reliable information."}
			w := store.NewBotWorkState(contract)
			criteria := []store.BotWorkCriterion{{ID: "c12", SourceIDs: []string{"assignment", "charter"}, Text: "Provide current news with source and retrieval timestamp fields", VerificationKind: kind}}
			raw, _ := json.Marshal(map[string]any{"criteria": criteria})
			var usage store.BotUsage
			if err := prepareBotWorkContract(ctx, req, &formatRecoveryModel{t: t, texts: []string{string(raw)}}, w, &usage); err != nil {
				t.Fatal(err)
			}
			if w.Criteria[0].EffectiveVerificationKind() != store.BotWorkVerificationModel {
				t.Fatalf("topic invented mandatory %s: %+v", kind, w.Criteria[0])
			}
			if len(w.Criteria) != 1 || w.Criteria[0].Text != criteria[0].Text || !reflect.DeepEqual(w.Criteria[0].SourceIDs, criteria[0].SourceIDs) || !reflect.DeepEqual(w.Contract, contract) {
				t.Fatal("grounding deleted or rewrote original criteria/sources")
			}
		})
	}
}

func TestBotWorkRequirementGroundingReview(t *testing.T) {
	for _, kind := range []store.BotWorkVerificationKind{store.BotWorkVerificationSourceURL, store.BotWorkVerificationRetrievalTime} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Give me the latest stock market news", Charter: "Be honest and useful."})
			w.Phase = store.BotWorkReviewing
			w.Criteria = []store.BotWorkCriterion{{ID: "c12", SourceIDs: []string{"assignment", "charter"}, Text: "News with exact source and retrieval time fields", VerificationKind: kind}}
			w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "Today's market news: shares rose following earnings.", Observations: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "https://example.org/markets?day=today", ObservedAt: time.Date(2026, 11, 10, 21, 58, 48, 0, time.UTC)}}}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			before := w.Clone()
			var usage store.BotUsage
			review, err := reviewBotCandidate(ctx, req, &metadataApprovingModel{}, w, &usage)
			if err != nil {
				t.Fatal(err)
			}
			if got := review.Criteria[0]; got.Verdict != store.BotCriterionSatisfied || got.Method != store.BotAssessmentModel {
				t.Fatalf("unrequested metadata triggered repair: %+v", got)
			}
			if !reflect.DeepEqual(w, before) {
				t.Fatal("resumed immutable work mutated")
			}
			for _, verdict := range []store.BotCriterionVerdict{store.BotCriterionFailed, store.BotCriterionUnverified} {
				review.Criteria[0].Verdict = verdict
				applyBotMechanicalChecks(w, review)
				if review.Criteria[0].Verdict != verdict || review.Criteria[0].Method != store.BotAssessmentModel {
					t.Fatal("lack of grounding erased semantic failure")
				}
			}
		})
	}
}

func TestBotWorkRequirementGroundingExplicitSources(t *testing.T) {
	for _, source := range []string{"assignment", "charter", "goal-description", "goal-completion", "attachment"} {
		for _, kind := range []store.BotWorkVerificationKind{store.BotWorkVerificationSourceURL, store.BotWorkVerificationRetrievalTime} {
			t.Run(source+"/"+string(kind), func(t *testing.T) {
				ctx, req, _ := botChatTestRequest(t)
				contract := store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Give me the latest stock market news", Charter: "Be honest."}
				standard := "Include the complete source URL and observed UTC retrieval time in the report."
				ids := []string{"assignment", "charter"}
				switch source {
				case "assignment":
					contract.Instructions += ". " + standard
				case "charter":
					contract.Charter += " " + standard
				case "goal-description", "goal-completion":
					goal := store.BotGoal{ID: uuid.New(), BotID: req.Bot.ID, Title: "Report standards"}
					if source == "goal-description" {
						goal.Description = standard
					} else {
						goal.CompletionCriteria = standard
					}
					contract.Goals = []store.BotGoal{goal}
					ids = append(ids, "goal_1")
				case "attachment":
					contract.Attachments = store.PinBotWorkAttachments([]store.ChatFile{{Filename: "standards.txt", MimeType: "text/plain", Data: []byte(standard)}})
					ids = append(ids, "attachment_1")
				}
				w := store.NewBotWorkState(contract)
				raw, _ := json.Marshal(map[string]any{"criteria": []store.BotWorkCriterion{{ID: "field", SourceIDs: ids, Text: standard, VerificationKind: kind}}})
				var usage store.BotUsage
				if err := prepareBotWorkContract(ctx, req, &formatRecoveryModel{t: t, texts: []string{string(raw)}}, w, &usage); err != nil {
					t.Fatal(err)
				}
				if w.Criteria[0].VerificationKind != kind {
					t.Fatal("explicit field lost mechanical verification")
				}
				w.Phase = store.BotWorkReviewing
				w.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "final", Text: "News without required metadata", Observations: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: "https://example.org/news", ObservedAt: time.Now()}}}
				w.Candidate.Digest = w.Candidate.ContentDigest()
				review, err := reviewBotCandidate(ctx, req, &metadataApprovingModel{}, w, &usage)
				if err != nil {
					t.Fatal(err)
				}
				if got := review.Criteria[0]; got.Verdict != store.BotCriterionFailed || got.Method != store.BotAssessmentMechanical {
					t.Fatalf("explicit requirement bypassed: %+v", got)
				}
			})
		}
	}
}
