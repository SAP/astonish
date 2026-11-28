package bot

import (
	"errors"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

type CheckpointResponseReason string

const (
	CheckpointResponseMultipleCalls CheckpointResponseReason = "multiple_tool_calls"
	CheckpointResponseTooLarge      CheckpointResponseReason = "response_too_large"
)

// CheckpointResponseError is created only by whole-response validation, before
// any call from that response is forwarded. Earlier turns may have had effects.
// Keep only numeric facts here, never provider text, tool names or arguments.
type CheckpointResponseError struct {
	Reason        CheckpointResponseReason
	ToolCalls     int
	ResponseBytes int
	LimitBytes    int
}

func (e *CheckpointResponseError) Error() string {
	switch e.Reason {
	case CheckpointResponseMultipleCalls:
		return fmt.Sprintf("%s: checkpoint capture rejected multiple tool calls (count=%d); serial tool calls required", store.ErrBotUnsupported, e.ToolCalls)
	case CheckpointResponseTooLarge:
		return fmt.Sprintf("%s: checkpoint capture response exceeds %d-byte limit (bytes=%d)", store.ErrBotUnsupported, e.LimitBytes, e.ResponseBytes)
	default:
		return "bot capability unsupported: checkpoint response rejected"
	}
}
func (e *CheckpointResponseError) Unwrap() error { return store.ErrBotUnsupported }

// CheckpointFailureMessage recognizes native boundary evidence only, never an
// unsupported sentinel alone or provider/tool text resembling our diagnostics.
func CheckpointFailureMessage(err error) (string, bool) {
	var e *CheckpointResponseError
	if !errors.As(err, &e) || e == nil {
		return "", false
	}
	var reason string
	switch e.Reason {
	case CheckpointResponseMultipleCalls:
		reason = "The model returned multiple tool requests in one response, which could not be processed because this run supports one tool request at a time."
	case CheckpointResponseTooLarge:
		reason = "The model returned a response that exceeded the supported size limit and could not be processed."
	default:
		return "", false
	}
	return "### Model response could not be processed\n\n" + reason + "\n\nNone of the tool requests from that response were executed. Earlier work may already have completed; its recorded evidence is retained below.\n\nNo approval is needed for this failure. This run has stopped and has not been automatically retried. Review any earlier work before sending a new request.", true
}

// CheckpointFailureDetails excludes all wrapper text and untrusted response data.
func CheckpointFailureDetails(err error) string {
	var e *CheckpointResponseError
	if errors.As(err, &e) && e != nil {
		return e.Error()
	}
	return "Checkpoint response validation stopped the run."
}
