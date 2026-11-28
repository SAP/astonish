package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/adk/model"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotContentCitationDiagnosticAllFailures(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"content": "First exact passage.\nSecond exact passage. Path C:\\models.", "url": "https://example.org/source"})
	in.Items = append(in.Items, botWorkContentItem{ID: "body:0:line:1", Text: "Another material claim."})
	in.ExecutionEvidence["receipt"] = "Read completed."
	prior := &botWorkContentReview{ContentAssessments: []botWorkContentAssessment{
		{ItemID: in.Items[0].ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified, Explanation: "Missing evidence", Sources: []botWorkContentSource{{Ref: "source_1", Quote: "First exact passage. Second exact passage."}, {Ref: "foreign", Quote: "First exact passage."}}},
		{ItemID: in.Items[1].ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionFailed, Explanation: "Unsupported number", Sources: []botWorkContentSource{{Ref: "source_1", Quote: "90 million users"}, {Ref: "source_1", Quote: "Second exact passage."}}, ExecutionSources: []botWorkContentSource{{Ref: "receipt", Quote: "Applied pending control."}}},
	}}
	var failure *botContentProtocolError
	if err := applyBotContentReview(in, prior); !errors.As(err, &failure) {
		t.Fatal("expected exact-quote rejection")
	}
	raw := strings.TrimPrefix(botContentCorrectionDiagnostic(in, prior, failure), citationDiagnosticLabel)
	var diagnostic struct {
		InvalidCitations []struct {
			AssessmentIndex         int                     `json:"assessment_index"`
			SourceIndex             int                     `json:"source_index"`
			SourceField             botContentCitationField `json:"source_field"`
			CitedSource             botWorkContentSource    `json:"cited_source"`
			ExactMatchCandidateRefs []string                `json:"exact_match_candidate_refs"`
		} `json:"invalid_citations"`
	}
	if err := json.Unmarshal([]byte(raw), &diagnostic); err != nil {
		t.Fatal(err)
	}
	if len(diagnostic.InvalidCitations) != 4 {
		t.Fatalf("want all four invalid citations, got %d", len(diagnostic.InvalidCitations))
	}
	for i, got := range diagnostic.InvalidCitations {
		if i == 1 {
			if got.CitedSource.Ref != "foreign" || len(got.ExactMatchCandidateRefs) != 1 || got.ExactMatchCandidateRefs[0] != "source_1" {
				t.Fatal("missing exact candidate for unknown ref")
			}
		} else if len(got.ExactMatchCandidateRefs) != 0 {
			t.Fatal("normalized, invented or wrong-namespace quote received a suggestion")
		}
	}
	if diagnostic.InvalidCitations[3].SourceField != botContentCitationExecutionSources {
		t.Fatal("lost receipt namespace")
	}
	if !strings.Contains(botWorkContentGuidance, "For failed/unverified support, use an empty sources array") {
		t.Fatal("missing unsupported-claim citation guidance")
	}
	// Text repair may use existing stories, but cannot turn an inadequate
	// inventory into accepted coverage or discard original requirements.
	for _, want := range []string{
		"Use research only when more evidence is needed",
		"if evidence has stories use text repair, otherwise request research to read the actual stories within existing search authority",
		"a bare headline/category inventory is not a briefing and needs_work",
		"Identify gaps from original owner wording and preserve every explicit requirement, freshness constraint and citation/evidence gate",
		"a complete review of the revised answer against the FULL original assignment and captured evidence, with every criterion and content_assessments",
		"removing or qualifying unsupported claims",
		"Use empty revised_text and null revised_review if not possible",
	} {
		if !strings.Contains(botEmployeeAssessmentGuidance, want) {
			t.Errorf("text repair must remove embellishments without accepting inadequate coverage: missing %q", want)
		}
	}
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Grounded answer", Charter: req.Version.Charter})
	w.Candidate = in.Candidate
	w.Phase = store.BotWorkReviewing
	in.BotWorkState = w
	in.BotWorkState.Criteria = []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "Grounded answer"}}
	prior.BotWorkReview = store.BotWorkReview{CandidateDigest: in.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Review"}}}
	probe := w.Clone()
	probe.Review = &prior.BotWorkReview
	if err := probe.Validate(); err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(prior)
	var corrected botWorkContentReview
	if err := json.Unmarshal(first, &corrected); err != nil {
		t.Fatal(err)
	}
	for i := range corrected.ContentAssessments {
		corrected.ContentAssessments[i].Sources = nil
		corrected.ContentAssessments[i].ExecutionSources = nil
	}
	second, _ := json.Marshal(corrected)
	m := &citationDiagnosticModel{t: t, texts: []string{string(first), string(second)}, check: func(r *model.LLMRequest) {
		if !strings.Contains(r.Contents[2].Parts[0].Text, "invalid_citations") || !strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "Correct every malformed binding") {
			t.Fatal("complete diagnostic not wired to correction")
		}
	}}
	var out botWorkContentReview
	var usage store.BotUsage
	if err := botWorkJSON(ctx, req, m, botWorkReviewPrompt, in, &out, &usage); err != nil {
		t.Fatal(err)
	}
	if m.calls != 2 || out.ContentAssessments[0].Support != store.BotCriterionUnverified || out.ContentAssessments[1].Support != store.BotCriterionFailed {
		t.Fatal("recovery lost bounded calls or negative support")
	}
}

func TestBotContentCitationDiagnosticBounds(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"content": "exact"})
	for _, mode := range []string{"count", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			prior := &botWorkContentReview{ContentAssessments: []botWorkContentAssessment{{ItemID: in.Items[0].ID}}}
			quote, count := "absent", 65
			if mode == "bytes" {
				quote, count = strings.Repeat("x", 32768), 1
			}
			for i := 0; i < count; i++ {
				prior.ContentAssessments[0].Sources = append(prior.ContentAssessments[0].Sources, botWorkContentSource{Ref: "source_1", Quote: quote})
			}
			issues, truncated := botContentInvalidCitations(in, prior)
			if !truncated || len(issues) > 64 {
				t.Fatal("unbounded or silently incomplete diagnostic")
			}
			encoded, _ := json.Marshal(issues)
			if len(encoded) > 32768+65 {
				t.Fatal("diagnostic byte ceiling exceeded")
			}
		})
	}
}

func TestBotContentCitationDiagnosticDecodedEscapesRemainExact(t *testing.T) {
	const text = "First exact passage.\nSecond says \"AI\". Path C:\\models. Literal \\n stays literal."
	in := capturedContentInput(t, map[string]any{"content": text})
	checkContentQuote(t, in, "source_1", text, true)
	checkContentQuote(t, in, "source_1", strings.ReplaceAll(text, "\n", " "), false)
	checkContentQuote(t, in, "source_1", strings.ReplaceAll(text, `\n`, "\n"), false)
	checkContentQuote(t, in, "source_1", `Second says \"AI\".`, false)
}
