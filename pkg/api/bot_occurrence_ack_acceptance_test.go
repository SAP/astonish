package api_test

import "testing"

// This drives HTTP activation, real executor/store admission, stale-occurrence
// replay, worker polling, approval continuation, and HTTP run readback.
func TestBotOccurrenceAcknowledgementHTTPAcceptance(t *testing.T) {
	TestBotRuntimeHTTPAcceptance(t)
}
