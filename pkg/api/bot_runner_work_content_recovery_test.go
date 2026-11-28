package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

func TestBotContentConciseReviewContract(t *testing.T) {
	for _, guidance := range []string{
		"Return concise JSON",
		"every content item and every criterion",
		"all required schema fields",
		"shortest sufficient exact contiguous quotes",
		"Do not restate the draft or repeat quoted evidence",
		"genuinely nonfactual separators",
		"Factual headings are not exempt",
	} {
		if !strings.Contains(botWorkContentGuidance, guidance) {
			t.Errorf("missing concise review guidance: %s", guidance)
		}
	}
	// Match the failed real review's cardinality, not its private draft. Concision
	// must retain every schema field, criterion and item in every review mode.
	w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: strings.Repeat("Market rose.\n", 45), Bodies: map[string]string{"tool-response:feed": `{"content":"Market rose."}`}}}
	var criterionIDs []string
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("criterion-%d", i)
		criterionIDs = append(criterionIDs, id)
		w.Criteria = append(w.Criteria, store.BotWorkCriterion{ID: id})
	}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	in := botContentInput(w)
	if len(in.Items) != 45 {
		t.Fatalf("lost input coverage: %d", len(in.Items))
	}
	var itemIDs []string
	for _, item := range in.Items {
		itemIDs = append(itemIDs, item.ID)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{botWorkReviewPrompt, botWorkPendingReviewPrompt, botWorkBlockedReviewPrompt} {
		schema := botWorkResponseSchema(stage, raw)
		reviews := []map[string]any{schema}
		if stage == botWorkPendingReviewPrompt {
			p := schema["properties"].(map[string]any)
			reviews = []map[string]any{p["current_turn"].(map[string]any), p["deferred_application"].(map[string]any)}
		}
		for _, review := range reviews {
			p := review["properties"].(map[string]any)
			items := p["content_assessments"].(map[string]any)
			assessment := items["items"].(map[string]any)
			ap := assessment["properties"].(map[string]any)
			criteria := p["criteria"].(map[string]any)["items"].(map[string]any)
			cp := criteria["properties"].(map[string]any)
			if items["maxItems"] != 45 || !reflect.DeepEqual(ap["item_id"].(map[string]any)["enum"], itemIDs) || !reflect.DeepEqual(cp["criterion_id"].(map[string]any)["enum"], criterionIDs) {
				t.Fatal("concision lost item/criterion coverage")
			}
			if !reflect.DeepEqual(assessment["required"], []string{"item_id", "claim_type", "relevance", "support", "explanation", "sources", "execution_sources"}) || !reflect.DeepEqual(criteria["required"], []string{"criterion_id", "verdict", "method", "evidence_refs", "explanation"}) {
				t.Fatal("concision changed required review fields")
			}
		}
	}
}

func TestBotContentCitationRecovery(t *testing.T) {
	for _, mode := range []string{"quote", "unknown-ref", "coverage", "repeated", "negative", "preserve-negative", "preserve-missing-evidence", "wrong-digest", "cancel", "budget", "metered", "format-then-invalid", "verdict", "explanation", "verdict-repeated", "verdict-wrong-digest", "verdict-authority", "verdict-cancel", "verdict-budget"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "AI news", Charter: req.Version.Charter})
			w.Criteria = []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "supported AI news"}}
			w.Candidate = &store.BotWorkCandidate{Text: "Lab releases AI.\nUnsupported allegation.", Revision: 1, EventID: "event", Bodies: map[string]string{"tool-response:feed": `{"content":"Lab releases AI."}`}}
			w.Candidate.Digest = w.Candidate.ContentDigest()
			w.Phase = store.BotWorkReviewing
			good := botWorkContentReview{BotWorkReview: store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "review"}}}}
			for _, item := range botContentInput(w).Items {
				good.ContentAssessments = append(good.ContentAssessments, botWorkContentAssessment{ItemID: item.ID, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "finding", Sources: []botWorkContentSource{{Ref: "source_1", Quote: "Lab releases AI."}}})
			}
			raw, _ := json.Marshal(good)
			var bad botWorkContentReview
			json.Unmarshal(raw, &bad)
			bad.ContentAssessments[0].Sources[0].Quote = "private-secret invented quote"
			switch mode {
			case "unknown-ref":
				bad.ContentAssessments[0].Sources[0].Ref = "private-secret-ref"
			case "coverage":
				bad.ContentAssessments = bad.ContentAssessments[:1]
			case "wrong-digest":
				bad.CandidateDigest = "wrong"
			case "negative":
				good.ContentAssessments[1].Support = store.BotCriterionFailed
			case "preserve-missing-evidence":
				bad.ContentAssessments[1].Sources = nil
			case "preserve-negative":
				bad.ContentAssessments[1].Support = store.BotCriterionFailed
			}
			if strings.HasPrefix(mode, "verdict") || mode == "explanation" {
				bad.ContentAssessments[0].Sources[0].Quote = "Lab releases AI."
				bad.ContentAssessments[1].Support = store.BotCriterionFailed
				if mode == "explanation" {
					bad.ContentAssessments[0].Explanation = " "
				} else {
					bad.ContentAssessments[0].Support = "private-secret-verdict"
				}
				if mode == "verdict-wrong-digest" {
					bad.CandidateDigest = "wrong"
				}
				if mode == "verdict-authority" {
					bad.Criteria[0].Method = "forged"
				}
			}
			// Share boundary and retry assertions with citation failures.
			checkMode := strings.TrimPrefix(mode, "verdict-")
			first, _ := json.Marshal(bad)
			second, _ := json.Marshal(good)
			if checkMode == "repeated" {
				second = first
			}
			if mode == "format-then-invalid" {
				second = first
				first = []byte("not-json")
			}
			m := &formatRecoveryModel{t: t, texts: []string{string(first), string(second)}, skipContentCoverage: true}
			var llm model.LLM = m
			if checkMode == "cancel" {
				m.after = cancel
			}
			if checkMode == "budget" || mode == "metered" {
				limit := int64(2)
				if mode == "metered" {
					limit = 5
				}
				req.Budget, _ = bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
				llm, _ = provider.NewBudgetLLM(m, req.Budget, provider.BudgetLLMOptions{MaxOutputTokens: 1, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 1, nil }})
			}
			var usage store.BotUsage
			got, err := reviewBotCandidate(ctx, req, llm, w, &usage)
			fail := checkMode == "repeated" || checkMode == "authority" || checkMode == "wrong-digest" || checkMode == "cancel" || checkMode == "budget" || mode == "format-then-invalid"
			calls := 2
			if checkMode == "wrong-digest" || checkMode == "authority" || checkMode == "cancel" || checkMode == "budget" {
				calls = 1
			}
			if (err != nil) != fail || m.calls != calls {
				t.Fatalf("err=%v calls=%d want=%d", err, m.calls, calls)
			}
			if mode == "repeated" {
				var protocol *botContentProtocolError
				if !errors.As(err, &protocol) || protocol.reason != botContentQuoteMismatch || protocol.assessmentIndex != 0 || protocol.sourceIndex != 0 || len(protocol.itemHash) != 64 || len(protocol.sourceHash) != 64 {
					t.Fatalf("missing safe diagnostic: %v", err)
				}
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("raw diagnostic leaked")
			}
			if !fail && (mode == "negative" || mode == "verdict" || mode == "explanation" || mode == "preserve-negative" || mode == "preserve-missing-evidence") && got.Criteria[0].Verdict != store.BotCriterionFailed {
				t.Fatal("grounded negative erased")
			}
			if usage.InputTokens != int64(calls) || usage.OutputTokens != int64(calls) {
				t.Fatalf("usage=%+v", usage)
			}
			if checkMode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if checkMode == "budget" && !errors.Is(err, bot.ErrBudgetExceeded) {
				t.Fatal(err)
			}
			if mode == "metered" {
				u := req.Budget.(interface{ Usage() store.BotUsage }).Usage()
				if u.InputTokens != 2 || u.OutputTokens != 2 || u.ToolCalls != 0 {
					t.Fatal(u)
				}
			}
		})
	}
}
