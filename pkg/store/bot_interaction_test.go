package store

import (
	"encoding/json"
	"testing"
)

func TestBotInteractionTextCustom(t *testing.T) {
	for _, raw := range []string{`{"questionId":"q","kind":"text","prompt":"Details"}`, `{"questionId":"q","kind":"select","prompt":"Pick","options":[{"id":"x"}],"allow_custom_answer":true}`} {
		i := &BotInteraction{Kind: BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(raw)}
		if err := i.ValidateResponse(&BotInteractionResponse{QuestionID: "q", Answer: "custom answer"}); err != nil {
			t.Fatal(err)
		}
		for _, r := range []*BotInteractionResponse{{QuestionID: "q", Answer: "  "}, {QuestionID: "other", Answer: "valid"}} {
			if i.ValidateResponse(r) == nil {
				t.Fatal("accepted invalid response")
			}
		}
	}
}
func TestBotInteractionTypedAnswer(t *testing.T) {
	for _, kind := range []string{"yesno", "select"} {
		i := &BotInteraction{Kind: BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"` + kind + `","prompt":"Choose","options":[{"id":"safe"}]}`)}
		answer := "no"
		if kind == "select" {
			answer = "safe"
		}
		if i.ValidateResponse(&BotInteractionResponse{QuestionID: "q", Answer: answer}) != nil {
			t.Fatal("valid typed answer rejected")
		}
		for _, r := range []*BotInteractionResponse{nil, {QuestionID: "other", Answer: answer}, {QuestionID: "q", Answer: "true"}, {QuestionID: "q", Answer: "unknown"}} {
			if i.ValidateResponse(r) == nil {
				t.Fatalf("invalid answer accepted: %+v", r)
			}
		}
	}
	i := &BotInteraction{Kind: BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"select","prompt":"Choose","options":[{"id":"x"},{"id":"x"}]}`)}
	if i.Validate() == nil {
		t.Fatal("ambiguous choice accepted")
	}
}
