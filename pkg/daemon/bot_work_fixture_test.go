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
	if strings.HasPrefix(prompt, "BOT_EMPLOYEE_MESSAGE") {
		// Auxiliary narration is not another scripted execution turn. Decline
		// it here to exercise the production typed fallback without replay.
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, store.ErrBotUnsupported) }, true
	}
	var output any
	if strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
		// Orientation is a separate structured assessment, not a scripted
		// execution turn. These preexisting fixtures exercise non-skill work;
		// production still validates and persists this no-skill decision.
		output = map[string]any{"kind": store.BotSkillNoSkillNeeded, "skill_id": "", "title": "", "responsibility": "", "method": []string{}, "expectations": []string{}, "reason": "Controlled non-skill worker fixture"}
	} else if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") {
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
	// Unknown substantive text remains factual; exemptions are explicit.
	var content workerContentInput
	_ = json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &content)
	if review, ok := output.(store.BotWorkReview); ok && content.Items != nil {
		assessments := []workerContentAssessment{}
		for _, item := range content.Items {
			assessments = append(assessments, workerAssessContent(content, item))
		}
		output = struct {
			store.BotWorkReview
			ContentAssessments []workerContentAssessment `json:"content_assessments"`
		}{review, assessments}
	}
	raw, _ := json.Marshal(output)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
	}, true
}

type workerClaimType string

const (
	workerClaimFactual    workerClaimType = "factual"
	workerClaimNonfactual workerClaimType = "nonfactual"
	workerClaimExecution  workerClaimType = "execution"
)

type workerContentItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type workerContentInput struct {
	Items             []workerContentItem `json:"content_items"`
	Sources           map[string]string   `json:"content_sources"`
	ExecutionEvidence map[string]string   `json:"execution_evidence"`
}
type workerContentSource struct {
	Ref   string `json:"ref"`
	Quote string `json:"quote"`
}
type workerContentAssessment struct {
	ItemID           string                    `json:"item_id"`
	ClaimType        workerClaimType           `json:"claim_type"`
	Relevance        store.BotCriterionVerdict `json:"relevance"`
	Support          store.BotCriterionVerdict `json:"support"`
	Explanation      string                    `json:"explanation"`
	Sources          []workerContentSource     `json:"sources"`
	ExecutionSources []workerContentSource     `json:"execution_sources"`
}

func workerAssessContent(in workerContentInput, item workerContentItem) workerContentAssessment {
	a := workerContentAssessment{ItemID: item.ID, ClaimType: workerClaimFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified, Explanation: "No captured source supports this substantive claim.", Sources: []workerContentSource{}, ExecutionSources: []workerContentSource{}}
	switch item.Text {
	case "durable controlled result":
		a.ClaimType, a.Support = workerClaimNonfactual, store.BotCriterionSatisfied
		a.Explanation = "Literal placeholder identifying the controlled text response; no external fact or action is asserted."
	case triggerExecutionAnswer, "No delivery fact in active context.":
		a.ClaimType, a.Support = workerClaimNonfactual, store.BotCriterionSatisfied
		a.Explanation = "Honest limitation, not an assertion of an external outcome."
	case "Verified AI news source":
		for ref, body := range in.Sources {
			if strings.HasPrefix(ref, "source_") && strings.Contains(body, item.Text) {
				a.Sources = append(a.Sources, workerContentSource{Ref: ref, Quote: item.Text})
				a.Support = store.BotCriterionSatisfied
				a.Explanation = "Exact text returned by the controlled AI news endpoint, without expanded claims."
			}
		}
	}
	// Control receipts support only their exact pending acknowledgment, never
	// an expanded claim that a scheduled operation has already been applied.
	for ref, raw := range in.ExecutionEvidence {
		var receipt store.BotEvidence
		if json.Unmarshal([]byte(raw), &receipt) == nil && receipt.Summary == item.Text && strings.Contains(strings.ToLower(receipt.Summary), "pending") {
			a.ClaimType, a.Support = workerClaimExecution, store.BotCriterionSatisfied
			a.ExecutionSources = append(a.ExecutionSources, workerContentSource{Ref: ref, Quote: raw})
			a.Explanation = "Exact captured pending receipt; application is not claimed."
		}
	}
	return a
}

func TestWorkerContentAssessmentProtocol(t *testing.T) {
	const news = "Verified AI news source"
	const pending = "Schedule operation pending, not applied."
	receipt, err := json.Marshal(store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: pending})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text        string
		sources, receipts map[string]string
		kind              workerClaimType
		support           store.BotCriterionVerdict
	}{
		{"placeholder", "durable controlled result", nil, nil, workerClaimNonfactual, store.BotCriterionSatisfied},
		{"honest-limitation", triggerExecutionAnswer, nil, nil, workerClaimNonfactual, store.BotCriterionSatisfied},
		{"captured-source", news, map[string]string{"source_1": news}, nil, workerClaimFactual, store.BotCriterionSatisfied},
		{"obsolete-ref", news, map[string]string{"tool-response:1": news}, nil, workerClaimFactual, store.BotCriterionUnverified},
		{"missing-source", news, nil, nil, workerClaimFactual, store.BotCriterionUnverified},
		{"receipt-is-not-news", news, nil, map[string]string{"observation:0": string(receipt)}, workerClaimFactual, store.BotCriterionUnverified},
		{"unknown-fact", "Delivery is Thursday", nil, nil, workerClaimFactual, store.BotCriterionUnverified},
		{"pending-receipt", pending, nil, map[string]string{"observation:0": string(receipt)}, workerClaimExecution, store.BotCriterionSatisfied},
		{"pending-is-not-applied", "Schedule applied.", nil, map[string]string{"observation:0": string(receipt)}, workerClaimFactual, store.BotCriterionUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := workerContentInput{Items: []workerContentItem{{ID: "body:0:line:0", Text: tc.text}}, Sources: tc.sources, ExecutionEvidence: tc.receipts}
			input := struct {
				store.BotWorkState
				workerContentInput
			}{store.BotWorkState{Candidate: &store.BotWorkCandidate{Digest: "digest", Text: tc.text}}, in}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			req := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("BOT_WORK_REVIEW_V1", "system")}, Contents: []*genai.Content{genai.NewContentFromText(string(raw), "user")}}
			responses, ok := workerWorkAssessment(req, func(*store.BotWorkState) bool { return true })
			if !ok {
				t.Fatal("review not recognized")
			}
			count := 0
			for response, err := range responses {
				if err != nil {
					t.Fatal(err)
				}
				count++
				var out struct {
					Assessments []workerContentAssessment `json:"content_assessments"`
				}
				if err := json.Unmarshal([]byte(response.Content.Parts[0].Text), &out); err != nil {
					t.Fatal(err)
				}
				if len(out.Assessments) != 1 {
					t.Fatalf("missing coverage: %+v", out)
				}
				a := out.Assessments[0]
				if a.ItemID != in.Items[0].ID || a.ClaimType != tc.kind || a.Support != tc.support || a.Sources == nil || a.ExecutionSources == nil {
					t.Fatalf("invalid claim assessment: %+v", a)
				}
				for _, source := range a.Sources {
					if !strings.Contains(tc.sources[source.Ref], source.Quote) || source.Quote == "" {
						t.Fatalf("invented source: %+v", source)
					}
				}
				for _, source := range a.ExecutionSources {
					if !strings.Contains(tc.receipts[source.Ref], source.Quote) || source.Quote == "" {
						t.Fatalf("invented receipt: %+v", source)
					}
				}
			}
			if count != 1 {
				t.Fatalf("responses=%d", count)
			}
		})
	}
}

// Orientation must not consume a scripted producer turn or fabricate work
// acceptance. The subsequent producer response must remain unchanged.
func TestWorkerSkillOrientationPreservesScript(t *testing.T) {
	provider := &workerTestModel{}
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("BOT_SKILL_ORIENT_V1\nReturn one JSON object.", "system")}, Contents: []*genai.Content{genai.NewContentFromText(`{"owner_source":"Report the controlled result"}`, "user")}}
	responses := 0
	for response, err := range provider.GenerateContent(t.Context(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		responses++
		if response == nil || !response.TurnComplete || response.Content == nil || len(response.Content.Parts) != 1 || response.Content.Parts[0].FunctionCall != nil {
			t.Fatalf("orientation must return one completed structured response: %+v", response)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal([]byte(response.Content.Parts[0].Text), &out); err != nil {
			t.Fatal(err)
		}
		if len(out) != 7 {
			t.Fatalf("orientation response must contain exactly the required fields: %s", response.Content.Parts[0].Text)
		}
		var kind store.BotSkillOrientationKind
		if err := json.Unmarshal(out["kind"], &kind); err != nil || kind != store.BotSkillNoSkillNeeded {
			t.Fatalf("unexpected orientation kind %q: %v", kind, err)
		}
		for _, field := range []string{"skill_id", "title", "responsibility"} {
			if string(out[field]) != `""` {
				t.Fatalf("no-skill orientation invented %s: %s", field, out[field])
			}
		}
		for _, field := range []string{"method", "expectations"} {
			if string(out[field]) != "[]" {
				t.Fatalf("no-skill orientation invented %s: %s", field, out[field])
			}
		}
		var reason string
		if err := json.Unmarshal(out["reason"], &reason); err != nil || strings.TrimSpace(reason) == "" {
			t.Fatalf("missing orientation rationale: %v", err)
		}
		if response.UsageMetadata == nil || response.UsageMetadata.TotalTokenCount != 10 {
			t.Fatal("orientation lost controlled usage accounting")
		}
	}
	if responses != 1 || provider.calls != 0 {
		t.Fatalf("orientation consumed producer script: responses=%d calls=%d", responses, provider.calls)
	}
	req.Config.SystemInstruction = genai.NewContentFromText("ordinary producer", "system")
	responses = 0
	for response, err := range provider.GenerateContent(t.Context(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		responses++
		if response.Content.Parts[0].Text != "durable controlled result" {
			t.Fatalf("producer response changed: %+v", response)
		}
	}
	if responses != 1 || provider.calls != 1 {
		t.Fatalf("producer script did not execute exactly once: responses=%d calls=%d", responses, provider.calls)
	}
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
