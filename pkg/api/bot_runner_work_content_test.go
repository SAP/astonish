package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

// Existing boundary fixtures deliberately isolate criterion/mechanical behavior.
// Supply neutral content coverage only there; content regressions never use this
// helper and must inspect and assess the actual source/owner contract.
func fixtureContentCoverage(r *model.LLMRequest, raw []byte) []byte {
	var in botWorkContentInput
	if len(r.Contents) == 0 || json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &in) != nil || in.Items == nil {
		return raw
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil || out["criteria"] == nil || out["content_assessments"] != nil {
		return raw
	}
	assessments := []botWorkContentAssessment{}
	for _, item := range in.Items {
		assessments = append(assessments, botWorkContentAssessment{ItemID: item.ID, ClaimType: botContentNonfactual, ExecutionSources: []botWorkContentSource{}, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Controlled non-content boundary fixture", Sources: []botWorkContentSource{}})
	}
	out["content_assessments"] = assessments
	result, _ := json.Marshal(out)
	return result
}

func TestBotContentReviewRequiresCoverage(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Report AI news only, supported by fetched sources.", Charter: req.Version.Charter})
	w.Criteria = []store.BotWorkCriterion{{ID: "content", SourceIDs: []string{"assignment", "charter"}, Text: "Report AI news"}}
	w.Candidate = &store.BotWorkCandidate{Text: "GTA has 90 million new AI users.", Revision: 1, EventID: "event", Bodies: map[string]string{"tool-response:feed": "GTA release announced."}}
	w.Candidate.Digest = w.Candidate.ContentDigest()
	w.Phase = store.BotWorkReviewing
	r := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Adjacent tech categories are fine."}}}
	raw, _ := json.Marshal(r)
	m := &formatRecoveryModel{t: t, texts: []string{string(raw), string(raw)}, skipContentCoverage: true}
	var usage store.BotUsage
	got, err := reviewBotCandidate(ctx, req, m, w, &usage)
	if err == nil && got.Criteria[0].Verdict == store.BotCriterionSatisfied {
		t.Fatal("whole pass without content coverage published unrelated unsupported item")
	}
}

func TestBotContentReviewBindingAndDowngrades(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mutate          func(*botWorkContentReview)
		invalid, failed bool
	}{
		{"supported", func(*botWorkContentReview) {}, false, false},
		{"irrelevant-forged-pass", func(r *botWorkContentReview) { r.ContentAssessments[0].Relevance = store.BotCriterionFailed }, false, true},
		{"unsupported-forged-pass", func(r *botWorkContentReview) { r.ContentAssessments[0].Support = store.BotCriterionUnverified }, false, true},
		{"missing", func(r *botWorkContentReview) { r.ContentAssessments = nil }, true, false},
		{"duplicate", func(r *botWorkContentReview) {
			r.ContentAssessments = append(r.ContentAssessments, r.ContentAssessments[0])
		}, true, false},
		{"foreign-item", func(r *botWorkContentReview) { r.ContentAssessments[0].ItemID = "foreign" }, true, false},
		{"invented-quote", func(r *botWorkContentReview) { r.ContentAssessments[0].Sources[0].Quote = "90 million users" }, true, false},
		{"self-corroboration", func(r *botWorkContentReview) { r.ContentAssessments[0].Sources[0].Ref = "candidate:fake" }, true, false},
		{"invalid-verdict", func(r *botWorkContentReview) { r.ContentAssessments[0].Support = "pass" }, true, false},
		{"missing-explanation", func(r *botWorkContentReview) { r.ContentAssessments[0].Explanation = " " }, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := botWorkContentInput{Items: []botWorkContentItem{{ID: "body:0:line:0", Text: "Lab releases a model."}}, Sources: map[string]string{"tool-response:feed": "Lab releases a model."}}
			r := botWorkContentReview{BotWorkReview: store.BotWorkReview{BlockedResponse: &store.BotBlockedResponseAssessment{Verdict: store.BotCriterionSatisfied}, Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel}}}, ContentAssessments: []botWorkContentAssessment{{ItemID: in.Items[0].ID, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "The entire claim is supported and matches owner scope", Sources: []botWorkContentSource{{Ref: "tool-response:feed", Quote: in.Items[0].Text}}}}}
			tc.mutate(&r)
			err := applyBotContentReview(in, &r)
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v err=%v", tc.invalid, err)
			}
			if err == nil && (r.Criteria[0].Verdict == store.BotCriterionFailed) != tc.failed {
				t.Fatalf("content finding did not dominate whole pass: %+v", r)
			}
			if err == nil && (r.BlockedResponse.Verdict == store.BotCriterionFailed) != tc.failed {
				t.Fatal("blocked-response pass bypassed content findings")
			}
			if tc.failed && !strings.Contains(r.Criteria[0].Explanation, in.Items[0].Text) {
				t.Fatal("repair lost exact item")
			}
		})
	}
}

func TestBotContentInputCoversCompoundClaimsAndExcludesPrivateDrafts(t *testing.T) {
	w := &store.BotWorkState{Contract: store.BotWorkContract{Instructions: "AI only", Charter: "Supported claims"}, Candidate: &store.BotWorkCandidate{Text: "# AI report\n\nSupported claim. Additional unsupported impact.", Bodies: map[string]string{"tool-response:feed": `{"content":"Supported claim."}`, botWorkPendingProducerDraft: "Private unrelated draft"}}}
	in := botContentInput(w)
	if len(in.Items) != 2 || in.Items[1].Text != "Supported claim. Additional unsupported impact." || in.Items[0].Text != "# AI report" {
		t.Fatalf("content coverage lost a heading or compound claim: %+v", in.Items)
	}
	if _, ok := in.Sources[botWorkPendingProducerDraft]; ok {
		t.Fatal("private draft became corroborating evidence")
	}
	if len(in.Sources) != 1 || in.OwnerScope["assignment"] != "AI only" {
		t.Fatalf("source map does not bind original scope and captured evidence: %+v", in.Sources)
	}
	if !strings.Contains(botWorkContentGuidance, "each material claim within an entry") || !strings.Contains(botWorkContentGuidance, "One failed/unverified claim makes its entry") {
		t.Fatal("compound claims are not required to be assessed")
	}
}

func TestBotContentArtifactCoverage(t *testing.T) {
	id := uuid.New()
	w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: "Summary", Artifacts: []store.BotArtifact{{ID: id}}, Bodies: map[string]string{id.String(): "# Artifact\nUnsupported artifact claim.", "tool-response:feed": "Captured facts."}}}
	in := botContentInput(w)
	if len(in.Items) != 3 || in.Items[2].ID != "body:1:line:1" || in.Items[2].Text != "Unsupported artifact claim." {
		t.Fatalf("published artifact escaped coverage: %+v", in.Items)
	}
	if _, ok := in.Sources[id.String()]; ok {
		t.Fatal("artifact prose became corroborating source")
	}
}

func TestBotContentReviewOversizedCoverageFailsClosed(t *testing.T) {
	in := botWorkContentInput{}
	r := botWorkContentReview{}
	for i := 0; i <= botWorkContentMaxItems; i++ {
		in.Items = append(in.Items, botWorkContentItem{})
		r.ContentAssessments = append(r.ContentAssessments, botWorkContentAssessment{})
	}
	if err := applyBotContentReview(in, &r); err == nil {
		t.Fatal("oversized content review passed")
	}
}

func sameCaptureReview(in botWorkContentInput, ref, quote string) botWorkContentReview {
	return botWorkContentReview{BotWorkReview: store.BotWorkReview{Criteria: []store.BotCriterionAssessment{{CriterionID: "content", Verdict: store.BotCriterionSatisfied}}, BlockedResponse: &store.BotBlockedResponseAssessment{Verdict: store.BotCriterionSatisfied}}, ContentAssessments: []botWorkContentAssessment{{ItemID: in.Items[0].ID, ClaimType: botContentFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Captured passage", Sources: []botWorkContentSource{{Ref: ref, Quote: quote}}}}}
}

func TestBotContentSameCaptureBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*botWorkContentInput, *botWorkContentReview)
		valid  bool
	}{
		{"same-field", func(*botWorkContentInput, *botWorkContentReview) {}, true},
		{"unknown", func(_ *botWorkContentInput, r *botWorkContentReview) {
			r.ContentAssessments[0].Sources[0].Ref = "source_unknown"
		}, false},
		{"different-capture", func(in *botWorkContentInput, _ *botWorkContentReview) {
			o := in.Origins["source_2"]
			o.CaptureRef = "tool-response:other"
			in.Origins["source_2"] = o
		}, false},
		{"different-digest", func(in *botWorkContentInput, _ *botWorkContentReview) {
			o := in.Origins["source_2"]
			o.CaptureDigest = "other"
			in.Origins["source_2"] = o
		}, false},
		{"missing-origin", func(in *botWorkContentInput, _ *botWorkContentReview) { delete(in.Origins, "source_1") }, false},
		{"missing-alternate", func(in *botWorkContentInput, _ *botWorkContentReview) { delete(in.Sources, "source_2") }, false},
		{"digits", func(_ *botWorkContentInput, r *botWorkContentReview) {
			r.ContentAssessments[0].Sources[0].Quote = "Yield did not rise 5.109%."
		}, false},
		{"negation", func(_ *botWorkContentInput, r *botWorkContentReview) {
			r.ContentAssessments[0].Sources[0].Quote = "Yield did rise 5.107%."
		}, false},
		{"execution", func(in *botWorkContentInput, r *botWorkContentReview) {
			in.ExecutionEvidence = in.Sources
			a := &r.ContentAssessments[0]
			a.ClaimType = botContentExecution
			a.ExecutionSources = a.Sources
			a.Sources = nil
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := capturedContentInput(t, map[string]any{"content": "Yield\u00a0did not rise 5.107%.", "url": "https://same.test"})
			r := sameCaptureReview(in, "source_1", "Yield did not rise 5.107%.")
			tc.mutate(&in, &r)
			err := applyBotContentReview(in, &r)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if tc.valid && (r.ContentAssessments[0].Sources[0].Ref != "source_2" || r.ContentAssessments[0].Sources[0].Quote != "Yield did not rise 5.107%.") {
				t.Fatal("exact quote not rebound to supplied view")
			}
		})
	}
	for _, fields := range []map[string]any{
		{"body": "unrelated", "content": "matching passage"},
		{"body": map[string]any{"a": "unrelated", "a#nbsp-space": "matching passage"}},
		{"body": `<rss><item>unrelated</item><item>matching passage</item></rss>`},
	} {
		in := capturedContentInput(t, fields)
		ref := "source_1"
		for id, o := range in.Origins {
			if strings.HasSuffix(o.Path, "/item[1]/text()[1]") {
				ref = id
			}
		}
		checkContentQuote(t, in, ref, "matching passage", false)
	}
}

func TestBotContentSameCaptureDeterministicSelection(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"content": "S\\&P\u00a0index"})
	for i := 0; i < 30; i++ {
		r := sameCaptureReview(in, "source_1", "S&P") // Exists in both ampersand views.
		if err := applyBotContentReview(in, &r); err != nil || r.ContentAssessments[0].Sources[0].Ref != "source_3" {
			t.Fatalf("unstable alternate: %+v %v", r, err)
		}
		r = sameCaptureReview(in, "source_4", "S&P")
		if err := applyBotContentReview(in, &r); err != nil || r.ContentAssessments[0].Sources[0].Ref != "source_4" {
			t.Fatal("already exact ref was rebound")
		}
	}
	for _, missing := range []string{"base", "digest", "capture"} {
		copyIn := capturedContentInput(t, map[string]any{"content": "S\\&P"})
		o := copyIn.Origins["source_1"]
		switch missing {
		case "base":
			o.BasePath = ""
		case "digest":
			o.CaptureDigest = ""
		case "capture":
			o.CaptureRef = ""
		}
		copyIn.Origins["source_1"] = o
		checkContentQuote(t, copyIn, "source_1", "S&P", false)
	}
	in = capturedContentInput(t, map[string]any{"body": `<rss><item>S\&amp;P&#160;index</item><item>other</item></rss>`})
	checkContentQuote(t, in, "tool-response:capture#json:/body#xml:/rss[1]/item[1]/text()[1]", "S&P index", true)
	checkContentQuote(t, in, "tool-response:capture#json:/body", "S&P index", false)
	checkContentQuote(t, in, "tool-response:capture#json:/body#xml:/rss[1]/item[2]/text()[1]", "S&P index", false)
	checkContentQuote(t, in, "source_1", "  ", false)
}

func TestBotContentSameCaptureNegativePreservation(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"content": "Yield\u00a0did not rise 5.107%."})
	prior := sameCaptureReview(in, "source_1", "Yield did not rise 5.107%.")
	prior.ContentAssessments[0].Support = store.BotCriterionFailed
	corrected := sameCaptureReview(in, "source_2", "Yield did not rise 5.107%.")
	preserveBotContentNegatives(in, &prior, &corrected)
	if corrected.ContentAssessments[0].Support != store.BotCriterionFailed {
		t.Fatal("same-field valid negative discarded")
	}
	if prior.ContentAssessments[0].Sources[0].Ref != "source_1" {
		t.Fatal("original diagnostic citation mutated")
	}
	if err := applyBotContentReview(in, &corrected); err != nil {
		t.Fatal(err)
	}
	if corrected.Criteria[0].Verdict != store.BotCriterionFailed || corrected.BlockedResponse.Verdict != store.BotCriterionFailed {
		t.Fatal("negative finding failed to block whole pass")
	}
}

func TestBotContentSameCaptureEqualURLIsolation(t *testing.T) {
	in := botContentInput(&store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: "Claim", Bodies: map[string]string{
		"tool-response:a": `{"url":"https://same.test","content":"unrelated"}`,
		"tool-response:b": `{"url":"https://same.test","content":"matching passage"}`,
	}}})
	checkContentQuote(t, in, "source_1", "matching passage", false)
	// Even identical digest metadata cannot bridge distinct capture refs.
	o := in.Origins["source_2"]
	o.CaptureDigest = in.Origins["source_1"].CaptureDigest
	in.Origins["source_2"] = o
	checkContentQuote(t, in, "source_1", "matching passage", false)
}

func TestBotContentSameCapturePartialNegativePreservation(t *testing.T) {
	in := capturedContentInput(t, map[string]any{"content": "Yield\u00a0did not rise 5.107%."})
	in.Items = append(in.Items, botWorkContentItem{ID: "second", Text: "Second claim"})
	prior := sameCaptureReview(in, "source_unknown", "missing")
	negative := sameCaptureReview(in, "source_1", "Yield did not rise 5.107%.").ContentAssessments[0]
	negative.ItemID = "second"
	negative.Relevance = store.BotCriterionFailed
	prior.ContentAssessments = append(prior.ContentAssessments, negative)
	if err := applyBotContentReview(in, &prior); err == nil {
		t.Fatal("unknown ref accepted")
	}
	corrected := sameCaptureReview(in, "source_2", "Yield did not rise 5.107%.")
	positive := corrected.ContentAssessments[0]
	positive.ItemID = "second"
	corrected.ContentAssessments = append(corrected.ContentAssessments, positive)
	preserveBotContentNegatives(in, &prior, &corrected)
	if corrected.ContentAssessments[1].Relevance != store.BotCriterionFailed {
		t.Fatal("negative after invalid ref lost")
	}
	if prior.ContentAssessments[1].Sources[0].Ref != "source_1" {
		t.Fatal("partial check rewrote original diagnostic ref")
	}
	if err := applyBotContentReview(in, &corrected); err != nil {
		t.Fatal(err)
	}
	if corrected.Criteria[0].Verdict != store.BotCriterionFailed {
		t.Fatal("partial negative did not dominate whole pass")
	}
}

func TestBotContentPromptScopeAndEvidence(t *testing.T) {
	for _, want := range []string{"each material", "Adjacent", "HTTP 200", "headline", "explicit owner", "numbers"} {
		if !strings.Contains(botWorkReviewPrompt, want) {
			t.Errorf("review lacks %q", want)
		}
	}
}
