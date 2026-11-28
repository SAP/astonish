package api

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/provider/sap"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

// Real SAP discovery/auth/serialization against an owned reject stub. No live
// credentials or provider requests; a typed nil map must not become schema:null.
func TestBotEmployeeGuidedReviewSAPRequestContract(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	state := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: "Explain limitations", Charter: req.Version.Charter})
	state.Phase = store.BotWorkReviewing
	state.Criteria = []store.BotWorkCriterion{{ID: "assignment", SourceIDs: []string{"assignment", "charter"}, Text: "Explain limitations", VerificationKind: store.BotWorkVerificationModel}}
	state.Candidate = &store.BotWorkCandidate{Text: "Evidence unavailable.", Revision: 1, EventID: "fixture-event"}
	state.Candidate.Digest = state.Candidate.ContentDigest()
	var generations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/token":
			fmt.Fprint(w, `{"access_token":"review-fixture-token","expires_in":3600}`)
		case "/v2/lm/deployments":
			fmt.Fprint(w, `{"resources":[{"id":"review-fixture","status":"RUNNING","details":{"resources":{"backendDetails":{"model":{"name":"gpt-5.6-terra"}}}}}]}`)
		case "/v2/inference/deployments/review-fixture/chat/completions":
			generations.Add(1)
			var body struct {
				ResponseFormat struct {
					Type       string `json:"type"`
					JSONSchema struct {
						Strict bool           `json:"strict"`
						Schema map[string]any `json:"schema"`
					} `json:"json_schema"`
				} `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			reason := ""
			if body.ResponseFormat.Type != "json_schema" || !body.ResponseFormat.JSONSchema.Strict {
				reason = "strict structured output required"
			}
			if body.ResponseFormat.JSONSchema.Schema == nil {
				reason = "Invalid schema for response_format 'structured_response': schema must be an object, got null"
			} else if err := reviewFixtureStrictSchema(body.ResponseFormat.JSONSchema.Schema); err != nil {
				reason = err.Error()
			}
			if reason != "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": reason, "type": "invalid_request_error", "param": "response_format"}})
				return
			}
			props := body.ResponseFormat.JSONSchema.Schema["properties"].(map[string]any)
			for _, field := range []string{"candidate_digest", "criteria", "content_assessments", "repair_kind", "missing_dimensions", "source_needs", "public_queries", "formatted_text", "formatted_review"} {
				if props[field] == nil {
					t.Errorf("missing guided field %s", field)
				}
			}
			if props["repair_kind"].(map[string]any)["type"] != "string" || props["public_queries"].(map[string]any)["maxItems"] != float64(8) {
				t.Error("guided feedback lost typed bounded contract")
			}
			formatted := props["formatted_review"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
			originalItems := props["content_assessments"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			formattedItems := formatted["properties"].(map[string]any)["content_assessments"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			if originalItems["item_id"].(map[string]any)["enum"] == nil || formattedItems["item_id"].(map[string]any)["enum"] != nil {
				t.Error("original IDs must stay pinned; formatted IDs are validated after claim preservation")
			}
			response := map[string]any{"candidate_digest": state.Candidate.Digest, "criteria": []store.BotCriterionAssessment{{CriterionID: "assignment", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + state.Candidate.Digest}, Explanation: "Evidence unavailable"}}, "content_assessments": []botWorkContentAssessment{{ItemID: "body:0:line:0", ClaimType: botContentNonfactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionSatisfied, Explanation: "Honest limitation"}}, "repair_kind": "research", "missing_dimensions": []string{"coverage"}, "source_needs": []string{"decisive reporting"}, "public_queries": []string{"public AI news"}, "formatted_text": "", "formatted_review": nil}
			raw, _ := json.Marshal(response)
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": string(raw)}}}})
			fmt.Fprintf(w, "data: %s\n\ndata: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", chunk)
		default:
			t.Errorf("unexpected local route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	llm, err := sap.NewProviderWithConfig(context.Background(), "gpt-5.6-terra", "review-fixture-id", "review-fixture-secret", server.URL, server.URL, "review-fixture-group")
	if err != nil {
		t.Fatal(err)
	}
	var response botEmployeeAssessment
	usage := store.BotUsage{}
	err = botWorkJSON(ctx, req, llm, botWorkReviewPrompt+botEmployeeAssessmentGuidance, botContentInput(state), &response, &usage)
	if err != nil {
		t.Fatalf("guided SAP assessment transport failed: %v", err)
	}
	if generations.Load() != 1 || response.Kind != botEmployeeRepairResearch {
		t.Fatalf("calls=%d kind=%s", generations.Load(), response.Kind)
	}
}

func TestBotAssessmentUnknownContractNeverCallsProvider(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	calls := 0
	llm := &agentCheckModel{run: func(context.Context, *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) {
			calls++
			yield(nil, fmt.Errorf("provider rejected schema:null"))
		}
	}}
	var response map[string]any
	usage := store.BotUsage{}
	if err := botWorkJSON(ctx, req, llm, "UNKNOWN_ASSESSMENT_PROTOCOL", struct{}{}, &response, &usage); err == nil {
		t.Fatal("unknown assessment contract accepted")
	}
	if calls != 0 {
		t.Fatalf("invalid schema reached provider: %d calls", calls)
	}
}

func reviewFixtureStrictSchema(schema map[string]any) error {
	if schema["type"] == "object" {
		if schema["additionalProperties"] != false {
			return fmt.Errorf("strict object permits additional properties")
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			return fmt.Errorf("missing properties")
		}
		required, ok := schema["required"].([]any)
		if !ok || len(required) != len(props) {
			return fmt.Errorf("strict required must include every property")
		}
		for _, key := range required {
			if _, ok := props[key.(string)]; !ok {
				return fmt.Errorf("unknown required property")
			}
		}
	}
	if values, ok := schema["enum"].([]any); ok && len(values) == 0 {
		return fmt.Errorf("empty enum")
	}
	for key, v := range schema {
		switch child := v.(type) {
		case map[string]any:
			if key == "properties" {
				for _, prop := range child {
					if err := reviewFixtureStrictSchema(prop.(map[string]any)); err != nil {
						return err
					}
				}
			} else if err := reviewFixtureStrictSchema(child); err != nil {
				return err
			}
		case []any:
			if strings.HasSuffix(key, "Of") {
				for _, branch := range child {
					if err := reviewFixtureStrictSchema(branch.(map[string]any)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
