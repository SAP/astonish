package llmerror

import (
	"errors"
	"fmt"

	"google.golang.org/genai"
)

// EmptyCompletionError preserves a completed request's unusable/incomplete final
// result. It is not a transport error and does not authorize generic retries.
// Usage is carried by the accompanying LLMResponse, including on failure.
// No model prose, reasoning, refusal text or tool arguments belong in this error.
type EmptyCompletionError struct {
	Provider         string
	FinishReason     genai.FinishReason
	HasVisibleOutput bool
	HasToolCalls     bool
	Refused          bool
}

func (e *EmptyCompletionError) Error() string {
	return fmt.Sprintf("%s completion ended without a usable final answer (finish=%s)", e.Provider, e.FinishReason)
}

// IsRecoverableEmptyCompletion permits a bounded, caller-owned empty-answer
// correction only for known stop/output-limit endings. Refusals, unknown or
// missing finish reasons, tools, and any emitted stream output are terminal.
// It never increases a budget or initiates a provider request itself.
func IsRecoverableEmptyCompletion(err error) bool {
	// Do not swallow an independent failure joined by a budget/timeout wrapper.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		return len(causes) == 1 && IsRecoverableEmptyCompletion(causes[0])
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		if _, interrupted := err.(*InterruptedStreamError); interrupted {
			return false
		}
		return IsRecoverableEmptyCompletion(wrapped)
	}
	var interrupted *InterruptedStreamError
	if errors.As(err, &interrupted) {
		return false
	}
	var completion *EmptyCompletionError
	if !errors.As(err, &completion) || completion == nil {
		return false
	}
	return !completion.HasVisibleOutput && !completion.HasToolCalls && !completion.Refused &&
		(completion.FinishReason == genai.FinishReasonStop || completion.FinishReason == genai.FinishReasonMaxTokens)
}
