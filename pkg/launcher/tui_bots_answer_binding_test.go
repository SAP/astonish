package launcher

import "testing"

func TestBotOwnerAnswerOnlyTypedProjection(t *testing.T) {
	for _, text := range []string{
		`{"response":{"question_id":"q","answer":"no"}}`,
		`{"type":"ordinary","response":{"question_id":"q","answer":"no"}}`,
		`{"type":"bot_owner_answer","response":{"answer":"no"}}`,
		"ordinary text",
	} {
		if got := botOwnerAnswerText(text); got != text {
			t.Fatalf("untyped content hidden: %q => %q", text, got)
		}
	}
}
