package store

import (
	"github.com/SAP/astonish/pkg/execution"
	"testing"
)

func TestBotProgressContinuationIntent(t *testing.T) {
	if !BotIntentProgressContinuation.AllowsStatus(BotPaused) || BotIntentProgressContinuation.AllowsStatus(BotActive) || BotIntentProgressContinuation.AllowsStatus(BotStopped) {
		t.Fatal("continuation changed lifecycle eligibility")
	}
	for _, mode := range []execution.BotExecutionMode{execution.BotExecutionModeLegacy, execution.BotExecutionModeChatInteractive} {
		if (BotRun{Intent: BotIntentProgressContinuation, ExecutionMode: mode}).ControlOnly() {
			t.Fatal("authorized continuation remains control-only")
		}
		if !(BotRun{Intent: BotIntentConversation, ExecutionMode: mode}).ControlOnly() {
			t.Fatal("ordinary conversation gained execution")
		}
	}
	if !(BotRun{Intent: BotIntentProgressContinuation, ExecutionMode: "unknown"}).ControlOnly() {
		t.Fatal("unknown execution mode acquired authority")
	}
}
