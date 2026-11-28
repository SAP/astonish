package question

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionAnswers(t *testing.T) {
	yes := true
	no := false
	for _, tc := range []struct {
		name   string
		q      Question
		answer string
		valid  bool
	}{
		{"text", Question{Kind: Text, Prompt: "Details"}, " hello ", true},
		{"blank", Question{Kind: Text, Prompt: "Details"}, " \n", false},
		{"unicode limit", Question{Kind: Text, Prompt: "Details"}, strings.Repeat("🙂", 4096), true},
		{"too long", Question{Kind: Text, Prompt: "Details"}, strings.Repeat("🙂", 4097), false},
		{"yes", Question{Kind: YesNo, Prompt: "Proceed"}, "yes", true},
		{"not permission", Question{Kind: YesNo, Prompt: "Proceed"}, "approve", false},
		{"closed", Question{Kind: Select, Prompt: "Pick", Options: []Option{{ID: "x"}}}, "other", false},
		{"false", Question{Kind: Select, Prompt: "Pick", Options: []Option{{ID: "x"}}, AllowCustomAnswer: &no}, "other", false},
		{"custom", Question{Kind: Select, Prompt: "Pick", Options: []Option{{ID: "x"}}, AllowCustomAnswer: &yes}, "other", true},
		{"wrong flag", Question{Kind: Text, Prompt: "Pick", AllowCustomAnswer: &no}, "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.q.ValidateAnswer(tc.answer) == nil; got != tc.valid {
				t.Fatalf("valid=%v", got)
			}
		})
	}
	var q Question
	_ = json.Unmarshal([]byte(`{"kind":"yesno","prompt":"Proceed"}`), &q)
	data, _ := json.Marshal(q)
	if strings.Contains(string(data), "allow_custom_answer") {
		t.Fatal(string(data))
	}
}
