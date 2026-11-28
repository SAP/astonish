package provider

import "github.com/SAP/astonish/pkg/provider/llmerror"

// EmptyCompletionError is a provider termination failure with payload-free
// evidence. The accompanying LLMResponse retains billable usage metadata.
type EmptyCompletionError = llmerror.EmptyCompletionError

// IsRecoverableEmptyCompletion reports whether bounded empty-answer correction
// is safe. Callers must separately enforce their budget and attempt limit.
func IsRecoverableEmptyCompletion(err error) bool {
	return llmerror.IsRecoverableEmptyCompletion(err)
}
