package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/provider/openai_compat"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotWorkVerificationSchema(t *testing.T) {
	schema := botWorkResponseSchema(botWorkPreparePrompt)
	item := schema["properties"].(map[string]any)["criteria"].(map[string]any)["items"].(map[string]any)
	kind := item["properties"].(map[string]any)["verification_kind"].(map[string]any)
	got := kind["enum"].([]string)
	if len(got) != 3 || got[0] != "model" || got[1] != "source_url" || got[2] != "retrieval_time" {
		t.Fatalf("unexpected verification kinds: %v", got)
	}
	required := false
	for _, key := range item["required"].([]string) {
		if key == "verification_kind" {
			required = true
		}
	}
	if !required {
		t.Fatal("strict preparation omitted explicit kind")
	}
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	var usage store.BotUsage
	if err := prepareBotWorkContract(ctx, req, &workAssessmentModel{t: t}, w, &usage); err != nil {
		t.Fatal(err)
	}
	if w.Criteria[0].VerificationKind != store.BotWorkVerificationModel || w.Criteria[0].FulfillmentScope != store.BotWorkCriterionCurrentTurn {
		t.Fatal("legacy extraction defaults not normalized")
	}
}

func TestBotWorkReviewRequiresCompleteCitation(t *testing.T) {
	for _, want := range []string{"NOT a complete source URL", "observed fetch time", "not the source's measurement/forecast time", "Do not mark missing or approximate required fields satisfied"} {
		if !strings.Contains(botWorkReviewPrompt, want) {
			t.Fatalf("missing citation check %q", want)
		}
	}
}

func TestBotWorkStructuredAssessmentWire(t *testing.T) {
	for _, prompt := range []string{botWorkPreparePrompt, botWorkReviewPrompt, botWorkPendingReviewPrompt} {
		t.Run(strings.Split(prompt, "\n")[0], func(t *testing.T) {
			calls := 0
			input := map[string]any{"candidate": &store.BotWorkCandidate{Digest: "wire-current", Observations: []store.BotEvidence{{}}}, "criteria": []store.BotWorkCriterion{{ID: "wire-criterion"}, {ID: "excluded", Excluded: true}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var wire struct {
					Stream    bool  `json:"stream"`
					Tools     []any `json:"tools"`
					MaxTokens int   `json:"max_completion_tokens"`
					Format    struct {
						Type   string `json:"type"`
						Schema struct {
							Strict bool           `json:"strict"`
							Schema map[string]any `json:"schema"`
						} `json:"json_schema"`
					} `json:"response_format"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
				}
				if wire.Format.Type != "json_schema" || !wire.Format.Schema.Strict || wire.Format.Schema.Schema["additionalProperties"] != false {
					t.Errorf("missing strict assessment schema: %+v", wire.Format)
				}
				props, _ := wire.Format.Schema.Schema["properties"].(map[string]any)
				if prompt == botWorkPendingReviewPrompt {
					if props["current_turn"] == nil || props["deferred_application"] == nil {
						t.Error("missing phase-separated review schemas")
					}
				} else if props["criteria"] == nil {
					t.Error("missing criteria schema")
				}
				if prompt == botWorkReviewPrompt && props["candidate_digest"] == nil {
					t.Error("missing candidate identity schema")
				}
				checkReview := func(p map[string]any) {
					assertEnum := func(schema any, want string) {
						b, _ := json.Marshal(schema.(map[string]any)["enum"])
						if string(b) != want {
							t.Errorf("wire enum: got %s want %s", b, want)
						}
					}
					assertEnum(p["candidate_digest"], `["wire-current"]`)
					item := p["criteria"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
					assertEnum(item["criterion_id"], `["wire-criterion"]`)
					assertEnum(item["evidence_refs"].(map[string]any)["items"], `["candidate:wire-current","observation:0"]`)
					assertEnum(item["method"], `["model_assessment"]`)
				}
				if prompt == botWorkReviewPrompt {
					checkReview(props)
				}
				if prompt == botWorkPendingReviewPrompt {
					checkReview(props["current_turn"].(map[string]any)["properties"].(map[string]any))
					checkReview(props["deferred_application"].(map[string]any)["properties"].(map[string]any))
				}
				if !wire.Stream || len(wire.Tools) != 0 || wire.MaxTokens != 8192 {
					t.Errorf("tools/output limit changed: %+v", wire)
				}
				// Even a proxy ignoring the schema must fail closed after one format correction.
				writeBotWorkTestSSE(t, w, []string{"Candidate is correct. ", "private-secret"}, false)
			}))
			defer server.Close()
			ctx, req, _ := botChatTestRequest(t)
			llm := openai_compat.NewProvider(ctx, "test-only", server.URL, "claude-4.8-opus", false)
			var out any
			var usage store.BotUsage
			err := botWorkJSON(ctx, req, llm, prompt, input, &out, &usage)
			if err == nil || !strings.Contains(err.Error(), "malformed assessment") || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("malformed response not safely rejected: %v", err)
			}
			if calls != 2 || usage.InputTokens != 8 || usage.OutputTokens != 4 {
				t.Fatalf("retry/accounting changed: calls=%d usage=%+v", calls, usage)
			}
		})
	}
}
