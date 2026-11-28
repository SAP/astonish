package api_test

import (
	"encoding/json"
	"google.golang.org/adk/model"
)

// Neutral coverage for old boundary-only controlled models. New content tests
// supply their own item/source assessments and do not use this helper.
func fixtureHTTPContentCoverage(req *model.LLMRequest, response *model.LLMResponse) {
	if response == nil || response.Content == nil || len(response.Content.Parts) != 1 || len(req.Contents) == 0 {
		return
	}
	var in struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"content_items"`
	}
	if json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &in) != nil || in.Items == nil {
		return
	}
	var out map[string]any
	if json.Unmarshal([]byte(response.Content.Parts[0].Text), &out) != nil || out["criteria"] == nil || out["content_assessments"] != nil {
		return
	}
	assessments := []any{}
	for _, item := range in.Items {
		assessments = append(assessments, map[string]any{"item_id": item.ID, "claim_type": "nonfactual", "execution_sources": []any{}, "relevance": "satisfied", "support": "satisfied", "explanation": "Controlled non-content boundary fixture", "sources": []any{}})
	}
	out["content_assessments"] = assessments
	raw, _ := json.Marshal(out)
	response.Content.Parts[0].Text = string(raw)
}
