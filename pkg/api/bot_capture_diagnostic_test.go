package api

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// These are deliberately synthetic provider responses, not a reconstruction of
// the discarded response from the 2026-11-09 offline run.
type captureResponseFixture struct{ responses []*model.LLMResponse }

func (captureResponseFixture) Name() string { return "capture-response-fixture" }
func (f captureResponseFixture) GenerateContent(_ context.Context, _ *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if streaming {
			panic("checkpoint capture must request non-streaming output")
		}
		for _, response := range f.responses {
			if !yield(response, nil) {
				return
			}
		}
	}
}

func TestBotApprovalCaptureUnsupportedResponseDiagnostic(t *testing.T) {
	call := func(name string) *genai.Part {
		return &genai.Part{FunctionCall: &genai.FunctionCall{Name: name, Args: map[string]any{"secret": "must-not-appear"}}}
	}
	for _, tc := range []struct {
		name      string
		responses []*model.LLMResponse
		want      string
	}{
		{"batch", []*model.LLMResponse{{Content: &genai.Content{Parts: []*genai.Part{call("web_fetch"), call("http_request")}}}}, "checkpoint capture rejected multiple tool calls (count=2); serial tool calls required"},
		{"split-batch", []*model.LLMResponse{{Content: &genai.Content{Parts: []*genai.Part{call("web_fetch")}}}, {Content: &genai.Content{Parts: []*genai.Part{call("http_request")}}}}, "checkpoint capture rejected multiple tool calls (count=2); serial tool calls required"},
		{"oversized", []*model.LLMResponse{{Content: &genai.Content{Parts: []*genai.Part{{Text: strings.Repeat("x", 4<<20)}}}}}, "checkpoint capture response exceeds 4194304-byte limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &botApprovalCaptureModel{LLM: captureResponseFixture{tc.responses}, capture: &botApprovalCapture{}}
			var got error
			for response, err := range m.GenerateContent(context.Background(), &model.LLMRequest{}, true) {
				if response != nil {
					t.Fatal("unsupported response escaped before whole-response validation")
				}
				got = err
			}
			if !errors.Is(got, store.ErrBotUnsupported) || !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("missing boundary diagnostic: %v; want %q", got, tc.want)
			}
			if strings.Contains(got.Error(), "must-not-appear") {
				t.Fatal("provider arguments leaked into diagnostic")
			}
		})
	}
}

func TestBotApprovalCaptureSerialResponseUnchanged(t *testing.T) {
	response := &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "web_fetch"}}}}}
	m := &botApprovalCaptureModel{LLM: captureResponseFixture{[]*model.LLMResponse{response}}, capture: &botApprovalCapture{}}
	count := 0
	for got, err := range m.GenerateContent(context.Background(), &model.LLMRequest{}, true) {
		if err != nil || got != response {
			t.Fatalf("serial response changed: %v %v", got, err)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("responses=%d", count)
	}
}
