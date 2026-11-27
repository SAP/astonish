package store

import "testing"

func TestBotNeutralTerminalClosedReasons(t *testing.T) {
	for _, status := range []RunStatus{RunRunning, RunWaitingApproval, RunSucceeded, RunSkipped, RunCancelled} {
		if (BotRunState{Status: status, TerminalReason: BotTerminalNoAnswer}).NeutralTerminalLabel() != "" {
			t.Fatal("extra notice", status)
		}
	}
	for _, status := range []RunStatus{RunPartial, RunFailed} {
		if (BotRunState{Status: status, TerminalReason: BotTerminalNoAnswer}).NeutralTerminalLabel() == "" {
			t.Fatal("silent", status)
		}
		if (BotRunState{Status: status, TerminalReason: "diagnostic"}).NeutralTerminalLabel() != "" {
			t.Fatal("raw diagnostic")
		}
		projected := BuildBotRunState(Bot{}, BotRun{Status: status, TerminalReason: BotTerminalNoAnswer})
		if projected.TerminalReason != BotTerminalNoAnswer {
			t.Fatal("snapshot lost terminal outcome")
		}
	}
}
