package daemon

import (
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Controlled provider protocol only: the production work controller still
// extracts, validates, persists, meters and commits every verdict. No fixture
// writes Accepted state. The predicate examines captured content/observations.
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
		output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "fixture-result", SourceIDs: sources, Text: "Report the controlled result truthfully; enqueued controls remain pending, not applied."}}}
	} else if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") || strings.HasPrefix(prompt, "BOT_WORK_PENDING_REVIEW_V1") {
		var w store.BotWorkState
		if json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &w) != nil || w.Candidate == nil {
			return nil, false
		}
		pass := w.Candidate.Text == "durable controlled result"
		if strings.Contains(strings.ToLower(w.Candidate.Text), "pending") && len(w.Candidate.Observations) > 0 {
			pass = true
		}
		if accept != nil {
			pass = accept(&w)
		}
		if strings.HasPrefix(prompt, "BOT_WORK_PENDING_REVIEW_V1") {
			var pending struct {
				Scope    string `json:"scope"`
				Controls []struct {
					Operation      store.BotControlOperation `json:"operation"`
					Acknowledgment string                    `json:"acknowledgment"`
					Payload        store.BotControlPayload   `json:"payload"`
				} `json:"trusted_pending_controls"`
			}
			if json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &pending) != nil {
				return nil, false
			}
			pass = pass && pending.Scope == "current_turn_pending_acknowledgment" && len(pending.Controls) > 0
			for _, control := range pending.Controls {
				pass = pass && control.Payload.Validate() == nil && control.Payload.Operation == control.Operation && control.Acknowledgment != "" && strings.Contains(w.Candidate.Text, control.Acknowledgment)
			}
		}
		verdict := store.BotCriterionUnverified
		if pass {
			verdict = store.BotCriterionSatisfied
		}
		output = store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "fixture-result", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Checked captured controlled result and pending receipt status."}}}
		if strings.HasPrefix(prompt, "BOT_WORK_PENDING_REVIEW_V1") {
			deferred := store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "fixture-result", Verdict: store.BotCriterionUnverified, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Pending receipt does not establish application; the real worker applies after source completion."}}}
			output = map[string]any{"current_turn": output, "deferred_application": deferred}
		}
	} else {
		return nil, false
	}
	raw, _ := json.Marshal(output)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
	}, true
}

// These provider-only checks cannot fabricate store application receipts. They
// ensure pending scope neither drops the full work input nor overrides a failed
// content assessment merely because an enqueue exists.
func TestWorkerPendingAssessmentProtocol(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload store.BotControlPayload
		accept  bool
		want    store.BotCriterionVerdict
	}{
		{"valid-pending", store.BotControlPayload{Operation: store.BotControlPriorityPause, Reason: "Owner requested pause"}, true, store.BotCriterionSatisfied},
		{"missing-payload", store.BotControlPayload{}, true, store.BotCriterionUnverified},
		{"other-work-failed", store.BotControlPayload{Operation: store.BotControlPriorityPause, Reason: "Owner requested pause"}, false, store.BotCriterionUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := store.BotWorkState{Contract: store.BotWorkContract{Instructions: "Pause immediately and retain the original report requirements."}, Candidate: &store.BotWorkCandidate{Digest: "controlled-digest", Text: "pending, not applied", Observations: []store.BotEvidence{{Summary: "pending record"}}}}
			input := struct {
				store.BotWorkState
				Scope    string           `json:"scope"`
				Controls []map[string]any `json:"trusted_pending_controls"`
			}{w, "current_turn_pending_acknowledgment", []map[string]any{{"operation": store.BotControlPriorityPause, "acknowledgment": "pending, not applied", "payload": tc.payload}}}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			req := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("BOT_WORK_PENDING_REVIEW_V1", "system")}, Contents: []*genai.Content{genai.NewContentFromText(string(raw), "user")}}
			seen := false
			responses, ok := workerWorkAssessment(req, func(got *store.BotWorkState) bool {
				seen = true
				if got.Contract.Instructions != w.Contract.Instructions {
					t.Fatal("original request dropped")
				}
				return tc.accept
			})
			if !ok || !seen {
				t.Fatal("pending review entered producer or skipped content predicate")
			}
			for response, err := range responses {
				if err != nil {
					t.Fatal(err)
				}
				var out struct {
					Current  store.BotWorkReview `json:"current_turn"`
					Deferred store.BotWorkReview `json:"deferred_application"`
				}
				if err := json.Unmarshal([]byte(response.Content.Parts[0].Text), &out); err != nil {
					t.Fatal(err)
				}
				if out.Current.CandidateDigest != w.Candidate.Digest || out.Deferred.CandidateDigest != w.Candidate.Digest || len(out.Current.Criteria) != 1 || len(out.Deferred.Criteria) != 1 {
					t.Fatalf("invalid review identity/coverage: %+v", out)
				}
				if out.Current.Criteria[0].Verdict != tc.want || out.Deferred.Criteria[0].Verdict != store.BotCriterionUnverified {
					t.Fatalf("enqueue/application conflated: %+v", out)
				}
			}
		})
	}
}
