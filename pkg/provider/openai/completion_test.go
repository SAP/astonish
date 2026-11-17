package openai

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openailib "github.com/sashabaranov/go-openai"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestChatCompletionTerminationEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, finish, content, refusal string
		tools                          bool
		noChoices                      bool
		wantErr                        bool
		wantFinish                     genai.FinishReason
	}{
		{name: "length4096_zero_visible", finish: "length", wantErr: true, wantFinish: genai.FinishReasonMaxTokens},
		{name: "empty_stop", finish: "stop", wantErr: true, wantFinish: genai.FinishReasonStop},
		{name: "normal_text", finish: "stop", content: "answer", wantFinish: genai.FinishReasonStop},
		{name: "normal_tool", finish: "tool_calls", tools: true, wantFinish: genai.FinishReasonStop},
		{name: "refusal", finish: "stop", refusal: "private refusal", wantErr: true, wantFinish: genai.FinishReasonSafety},
		{name: "content_filter", finish: "content_filter", wantErr: true, wantFinish: genai.FinishReasonSafety},
		{name: "unknown_finish", finish: "unknown", wantErr: true, wantFinish: genai.FinishReasonOther},
		{name: "length_text", finish: "length", content: "partial", wantErr: true, wantFinish: genai.FinishReasonMaxTokens},
		{name: "length_tools", finish: "length", tools: true, wantErr: true, wantFinish: genai.FinishReasonMaxTokens},
		{name: "whitespace_stop", finish: "stop", content: " \n ", wantErr: true, wantFinish: genai.FinishReasonStop},
		{name: "missing_finish", wantErr: true, wantFinish: genai.FinishReasonUnspecified},
		{name: "missing_tools", finish: "tool_calls", wantErr: true, wantFinish: genai.FinishReasonStop},
		{name: "nil_choices", noChoices: true, wantErr: true, wantFinish: genai.FinishReasonUnspecified},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request openailib.ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.MaxCompletionTokens != 4096 {
					t.Errorf("max completion tokens=%d", request.MaxCompletionTokens)
				}
				message := map[string]any{"role": "assistant", "content": tt.content, "refusal": tt.refusal}
				if tt.tools {
					message["tool_calls"] = []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}
				}
				choices := []any{}
				if !tt.noChoices {
					choices = append(choices, map[string]any{"message": message, "finish_reason": tt.finish})
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": choices, "usage": map[string]any{"prompt_tokens": 17915, "completion_tokens": 4096, "total_tokens": 22011, "completion_tokens_details": map[string]any{"reasoning_tokens": 4096}, "prompt_tokens_details": map[string]any{"cached_tokens": 12}}})
			}))
			defer server.Close()
			cfg := openailib.DefaultConfig("test")
			cfg.BaseURL = server.URL
			p := NewProvider(openailib.NewClientWithConfig(cfg), "test", false)
			yields := 0
			for response, err := range p.GenerateContent(context.Background(), &model.LLMRequest{Config: &genai.GenerateContentConfig{MaxOutputTokens: 4096}}, false) {
				yields++
				if (err != nil) != tt.wantErr {
					t.Errorf("error=%v wantErr=%v", err, tt.wantErr)
				}
				wantRecovery := tt.wantErr && (tt.finish == "stop" || tt.finish == "length") && strings.TrimSpace(tt.content) == "" && !tt.tools && tt.refusal == ""
				if llmerror.IsRecoverableEmptyCompletion(err) != wantRecovery {
					t.Errorf("recovery=%v want=%v error=%v", llmerror.IsRecoverableEmptyCompletion(err), wantRecovery, err)
				}
				if tt.wantErr {
					var terminal *llmerror.EmptyCompletionError
					if !errors.As(err, &terminal) {
						t.Errorf("untyped completion error: %v", err)
					}
					if err != nil && strings.Contains(err.Error(), "private") {
						t.Error("payload in error")
					}
				}
				if response == nil {
					t.Fatal("missing response/usage")
				}
				if response.Content != nil {
					tools := 0
					for _, part := range response.Content.Parts {
						if part.FunctionCall != nil {
							tools++
						}
					}
					if (tools > 0) != (tt.tools && !tt.wantErr) {
						t.Errorf("executable tools=%d", tools)
					}
				}
				if response.FinishReason != tt.wantFinish {
					t.Errorf("finish=%q want %q", response.FinishReason, tt.wantFinish)
				}
				usage := response.UsageMetadata
				if usage == nil || usage.PromptTokenCount != 17915 || usage.CandidatesTokenCount != 0 || usage.ThoughtsTokenCount != 4096 || usage.TotalTokenCount != 22011 || usage.CachedContentTokenCount != 12 {
					t.Errorf("usage=%+v", usage)
				}
			}
			if calls != 1 || yields != 1 {
				t.Errorf("calls=%d yields=%d", calls, yields)
			}
		})
	}
}
