package api

import (
	"encoding/json"
	"regexp"
	"strings"

	"google.golang.org/adk/session"
)

// Provider diagnostics can contain unregistered secrets and request bodies.
// Retain original evidence in typed completion, not public presentation.
const publicChatFailure = "The work stopped because of an operational failure. Completion could not be confirmed. Review the run diagnostics before deciding how to continue."
const publicChatEmptyResponse = "The model returned an empty response. Please try sending your message again."
const publicChatTruncatedStream = "The model stream ended without a finish_reason — retrying automatically."
const publicChatRateLimit = "The model provider reported a rate_limit error. Please wait before trying again."

func publicChatErrorText(text string) string {
	switch text {
	case publicChatEmptyResponse, publicChatTruncatedStream, publicChatRateLimit:
		return text
	}
	// Classification can inspect diagnostics, but never echo provider prose.
	if strings.Contains(strings.ToLower(text), "rate_limit") {
		return publicChatRateLimit
	}
	return publicChatFailure
}

var publicChatRetryReason = regexp.MustCompile(`^Failed after ([0-9]{1,3}) attempts\.(?: The error persisted across all retry attempts\.)?$`)

func publicChatErrorData(kind string, data map[string]any) map[string]any {
	switch kind {
	case "error":
		text, _ := data["error"].(string)
		return map[string]any{"error": publicChatErrorText(text)}
	case "error_info":
		out := map[string]any{"title": "Work stopped", "reason": publicChatFailure, "suggestion": "Review the run diagnostics before deciding how to continue."}
		// Only fixed product guidance survives; originalError is always private.
		if data["title"] == "Max Retries Exceeded" {
			out["title"] = "Max Retries Exceeded"
		}
		if reason, ok := data["reason"].(string); ok && publicChatRetryReason.MatchString(reason) {
			out["reason"] = reason
		}
		if data["suggestion"] == "Try a different model or simplify your request." {
			out["suggestion"] = "Try a different model or simplify your request."
		}
		return out
	default:
		return data
	}
}

func rawMessageMap(data map[string]json.RawMessage) map[string]any {
	out := make(map[string]any, len(data))
	for key, raw := range data {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			out[key] = value
		}
	}
	return out
}

func publicChatSyntheticErrorText(event *session.Event) string {
	text := strings.TrimSuffix(strings.TrimPrefix(event.Content.Parts[0].Text, "[Error: "), "]")
	return publicChatErrorText(text)
}

// Recognize only runner-owned legacy synthetic rows, not user/model prose.
func isSyntheticRunError(event *session.Event) bool {
	return event != nil && event.Author == "model" && strings.HasPrefix(event.ID, "error-") && event.Content != nil && event.Content.Role == "model" && len(event.Content.Parts) == 1 && event.Content.Parts[0] != nil && strings.HasPrefix(event.Content.Parts[0].Text, "[Error:")
}
