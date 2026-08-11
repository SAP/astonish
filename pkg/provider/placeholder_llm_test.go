package provider

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/model"
)

func TestPlaceholderLLM_ErrorsOnUse(t *testing.T) {
	llm := NewPlaceholderLLM()
	if llm.Name() == "" {
		t.Error("placeholder should report a non-empty name")
	}

	var gotErr error
	for _, err := range llm.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatal("expected placeholder generation to yield an error")
	}
	if !strings.Contains(gotErr.Error(), "/model") {
		t.Errorf("expected error to mention /model, got %v", gotErr)
	}
}
