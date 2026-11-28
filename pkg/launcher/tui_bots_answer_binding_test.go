package launcher

import "testing"

func TestBotOwnerAnswerOnlyTypedProjection(t *testing.T) {
	for _, text := range []string{
		`{"response":{"question_id":"q","answer":"no"}}`,
		`{"type":"ordinary","response":{"question_id":"q","answer":"no"}}`,
		`{"type":"bot_owner_answer","response":{"answer":"no"}}`,
		`{"type":"bot_owner_answer","approval_id":"title-approval","response":{"question_id":"title-question","answer":""}}`,
		`{"type":"bot_owner_answer","approval_id":"title-approval","response":{"question_id":"","answer":"forecast"}}`,
		`{"type":"title_confirmation","response":{"question_id":"title-question","answer":"forecast"}}`,
		`{"type":"bot_owner_answer","response":`,
		"I think the title means compare forecasts; this prose is not an owner answer.",
		"ordinary text",
	} {
		if got := botOwnerAnswerText(text); got != text {
			t.Fatalf("untyped content hidden: %q => %q", text, got)
		}
	}
}
