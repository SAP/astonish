package api_test

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"iter"
	"strings"
)

// workerWorkAssessment answers only the controller protocol; acceptance still
// passes through the real validator, budget meter and persisted WorkStore.
func workerWorkAssessment(r *model.LLMRequest, accept func(*store.BotWorkState) bool) (iter.Seq2[*model.LLMResponse, error], bool) {
	if r.Config == nil || r.Config.SystemInstruction == nil || len(r.Config.SystemInstruction.Parts) == 0 || len(r.Contents) != 1 {
		return nil, false
	}
	prompt := r.Config.SystemInstruction.Parts[0].Text
	var output any
	if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") {
		var c store.BotWorkContract
		if json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &c) != nil {
			return nil, false
		}
		sources := []string{"assignment", "charter"}
		for _, a := range c.Attachments {
			sources = append(sources, a.SourceID)
		}
		for _, g := range c.Goals {
			sources = append(sources, "goal:"+g.ID.String())
		}
		output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "controlled-result", SourceIDs: sources, Text: "Truthfully report the controlled result and pending controls"}}}
	} else if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
		var w store.BotWorkState
		if json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &w) != nil || w.Candidate == nil {
			return nil, false
		}
		verdict := store.BotCriterionUnverified
		if accept != nil && accept(&w) {
			verdict = store.BotCriterionSatisfied
		}
		output = store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "controlled-result", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Inspected captured controlled result"}}}
	} else {
		return nil, false
	}
	raw, _ := json.Marshal(output)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}, true
}
