package api

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotHomeCanonicalRunStateSurvivesNarrative(t *testing.T) {
	b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	r := store.BotRun{ID: uuid.New(), BotID: b.ID, SessionID: uuid.New(), Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunPartial}
	e, err := store.NewBotRunStateEvent(b, r, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := projectBotHomeNarrative(e, b.HomeSessionID, b.HomeSessionID)
	if got.Actions.StateDelta[store.BotRunStateKey] == nil || got.Content != nil {
		t.Fatal("canonical terminal state erased or converted to speech")
	}
}
