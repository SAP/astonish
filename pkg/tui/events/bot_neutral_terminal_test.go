package events

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotNeutralTerminalLiveRestoreReplay(t *testing.T) {
	b, r, s, h := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	running := store.BotRunState{SchemaVersion: 1, BotID: b, RunID: r, SessionID: s, HomeSessionID: h, Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunRunning, StateSequence: 1}
	terminal := running
	terminal.Status = store.RunPartial
	terminal.StateSequence = 2
	terminal.TerminalReason = store.BotTerminalNoAnswer
	tr := Transcript{BotID: b.String(), SessionID: h.String()}
	tr.applyBotRunState(&running)
	tr.restoreBotRunState()
	if !tr.Streaming {
		t.Fatal("not busy")
	}
	tr.applyBotRunState(&terminal)
	tr.restoreBotRunState()
	if tr.Streaming || tr.Status != terminal.NeutralTerminalLabel() || len(tr.Items) != 0 {
		t.Fatal("terminal became silent or speech")
	}
	tr.applyBotRunState(&running)
	tr.applyBotRunState(&terminal)
	tr.restoreBotRunState()
	if len(tr.BotRunStates) != 1 || tr.Status != terminal.NeutralTerminalLabel() {
		t.Fatal("replay duplicated or regressed")
	}
	restored := Transcript{BotID: b.String(), SessionID: h.String()}
	restored.applyBotRunState(&terminal)
	restored.restoreBotRunState()
	if restored.Status != tr.Status {
		t.Fatal("restore differs")
	}
}
