package tui

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotOperationExceptionReviewBinding(t *testing.T) {
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	v := store.BotOperationView{SchemaVersion: 1, BotID: bot.ID, HomeSessionID: bot.HomeSessionID, OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: store.BotOperationBlocked, Decision: store.BotOperationRequireAuthorization, Summary: "Needs review"}
	v.Exception = &store.BotOperationExceptionView{OperationID: v.OperationID, PayloadDigest: "digest", ExpiresAt: time.Now().Add(time.Hour)}
	snap := &store.BotSnapshot{Bot: bot, Operations: []store.BotOperationView{v}}
	m := model{bots: botsState{open: true, exceptions: true, snapshot: snap}, tr: &events.Transcript{BotID: bot.ID.String(), SessionID: bot.HomeSessionID.String()}}
	next, cmd := m.handleBotExceptionKey("enter")
	if cmd != nil || next.(model).bots.open {
		t.Fatal("default cancel must close without mutation")
	}
	next, _ = m.handleBotExceptionKey("n")
	m = next.(model)
	newer := v
	newer.Revision = 2
	newer.StateSequence = 2
	newer.Status = store.BotOperationCancelled
	m.tr.Apply(events.Event{Kind: events.KindBotOperation, BotOperation: &newer})
	next, cmd = m.handleBotExceptionKey("enter")
	if cmd != nil || !strings.Contains(next.(model).bots.err, "changed") {
		t.Fatal("stale exception permitted")
	}
	snap.Operations[0].Decision = store.BotOperationClarify
	if len(operationExceptions(snap)) != 0 {
		t.Fatal("question became authorization exception")
	}
}
