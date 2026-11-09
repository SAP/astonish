package entstore

import (
	"encoding/json"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotCallerCeilingRoundTrip(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), Version: 1, ExecutionMode: execution.BotExecutionModeChatInteractive, CallerCeiling: &execution.BotCallerCeiling{Principal: execution.Principal{Subject: "private-caller", Scopes: []string{"tool:execute"}}, ExpiresAt: time.Now().UTC()}}
	data, err := botJSONMap(botStoredRun{Run: run})
	if err != nil {
		t.Fatal(err)
	}
	p, err := decodePersonalBotRun(&personalent.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data})
	if err != nil {
		t.Fatal(err)
	}
	team, err := decodeTeamBotRun(&teament.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data})
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range []*botStoredRun{p, team} {
		if saved.Run.CallerCeiling == nil || saved.Run.CallerCeiling.Principal.Subject != "private-caller" {
			t.Fatal("ceiling lost")
		}
		b, err := json.Marshal(saved.Run)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "private-caller") || strings.Contains(string(b), "CallerCeiling") {
			t.Fatal("public auth metadata")
		}
	}
}
func TestBotCallerCeilingUnknownModeRejected(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), Version: 1, ExecutionMode: "future"}
	data, err := botJSONMap(botStoredRun{Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePersonalBotRun(&personalent.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data}); err == nil {
		t.Fatal("personal accepted unknown mode")
	}
	if _, err := decodeTeamBotRun(&teament.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data}); err == nil {
		t.Fatal("team accepted unknown mode")
	}
}

func TestBotCallerCeilingLegacyControlOnly(t *testing.T) {
	for _, intent := range []store.BotExecutionIntent{store.BotIntentDraftConversation, store.BotIntentConversation} {
		if !(store.BotRun{Intent: intent}).ControlOnly() {
			t.Fatal("legacy widened")
		}
	}
	if !(store.BotRun{ExecutionMode: "future"}).ControlOnly() {
		t.Fatal("unknown mode not fail closed")
	}
}
