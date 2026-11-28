package slides

import (
	"context"
	"encoding/json"
	"google.golang.org/genai"
	"strings"
	"testing"
)

func TestAskUserGeneratedSchema(t *testing.T) {
	tools, err := GetTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name() != "ask_user" {
			continue
		}
		d := tool.(interface {
			Declaration() *genai.FunctionDeclaration
		}).Declaration()
		data, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "allow_custom_answer") || !strings.Contains(string(data), "free text") {
			t.Fatalf("missing schema contract: %s", data)
		}
		return
	}
	t.Fatal("ask_user absent")
}
func TestAskUserRejectsInvalidContract(t *testing.T) {
	for _, raw := range []string{`{"kind":"text","prompt":"P","allow_custom_answer":false}`, `{"kind":"yesno","prompt":"P","allow_custom_answer":true}`, `{"kind":"select","prompt":"P","options":[{"id":"x","label":"X"},{"id":"x","label":"Y"}]}`} {
		var args AskUserArgs
		_ = json.Unmarshal([]byte(raw), &args)
		if _, err := askUser(context.Background(), args); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestAskUserTextCustomContract(t *testing.T) {
	for _, raw := range []string{`{"kind":"text","prompt":"Details?"}`, `{"kind":"select","prompt":"Pick","options":[{"id":"a","label":"A"}],"allow_custom_answer":true}`} {
		var args AskUserArgs
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatal(err)
		}
		result, err := askUser(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(result)
		var got map[string]any
		_ = json.Unmarshal(data, &got)
		if args.Kind == "select" && got["allow_custom_answer"] != true {
			t.Fatalf("lost custom flag: %s", data)
		}
	}
}
