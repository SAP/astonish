package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/provider/llmerror"
	"google.golang.org/genai"
)

func TestIsRecoverableEmptyCompletion(t *testing.T) {
	empty := &EmptyCompletionError{Provider: "test", FinishReason: genai.FinishReasonMaxTokens}
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"length", empty, true},
		{"stop", &EmptyCompletionError{FinishReason: genai.FinishReasonStop}, true},
		{"wrapped", fmt.Errorf("wrapped: %w", empty), true},
		{"single_join", errors.Join(empty), true},
		{"nil", nil, false},
		{"canceled", context.Canceled, false},
		{"joined_cancel", errors.Join(empty, context.Canceled), false},
		{"wrapped_join", fmt.Errorf("wrapped: %w", errors.Join(empty, errors.New("settlement failed"))), false},
		{"interrupted", llmerror.NewInterruptedStream(empty), false},
		{"wrapped_interrupted", fmt.Errorf("wrapped: %w", llmerror.NewInterruptedStream(empty)), false},
		{"visible", &EmptyCompletionError{FinishReason: genai.FinishReasonMaxTokens, HasVisibleOutput: true}, false},
		{"tools", &EmptyCompletionError{FinishReason: genai.FinishReasonMaxTokens, HasToolCalls: true}, false},
		{"refusal", &EmptyCompletionError{FinishReason: genai.FinishReasonStop, Refused: true}, false},
		{"unknown", &EmptyCompletionError{FinishReason: genai.FinishReasonOther}, false},
		{"missing", &EmptyCompletionError{FinishReason: genai.FinishReasonUnspecified}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRecoverableEmptyCompletion(tt.err); got != tt.want {
				t.Errorf("got %v want %v", got, tt.want)
			}
			if llmerror.IsRetryable(tt.err) {
				t.Error("completion must not enable generic transport replay")
			}
		})
	}
}
