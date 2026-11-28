package api

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotSkillOwnerAdmissionSource(t *testing.T) {
	b, runID, sessionID := uuid.New(), uuid.New(), uuid.New()
	req := bot.RunRequest{Bot: store.Bot{ID: b}, Run: store.BotRun{ID: runID, BotID: b, SessionID: sessionID, Trigger: store.TriggerManual, CreatedAt: time.Now().UTC()}, Instructions: "Owner assignment"}
	w := &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1}
	w.Contract.Instructions = "Prompt augmented with untrusted text"
	// No transcript/session service: admission must not manufacture a user event.
	p, text, err := botSkillOwnerSource(context.Background(), req, &BotChatResources{}, w)
	if err != nil || p == nil || p.Source != store.BotSkillSourceRunAdmission || p.EventID != "" || text != req.Instructions || p.SourceDigest != botSkillHash(req.Instructions) {
		t.Fatalf("source=%+v text=%q err=%v", p, text, err)
	}
	req.Run.Trigger = store.TriggerCron
	p, _, err = botSkillOwnerSource(context.Background(), req, &BotChatResources{}, w)
	if err != nil || p != nil {
		t.Fatalf("scheduled payload became owner source: %+v %v", p, err)
	}
	goal := store.BotGoal{ID: uuid.New(), BotID: b, Title: "Saved goal", Revision: 1}
	w.Contract.Goals = []store.BotGoal{goal}
	p, _, err = botSkillOwnerSource(context.Background(), req, &BotChatResources{}, w)
	if err != nil || p == nil || p.Source != "" || p.GoalID == nil || *p.GoalID != goal.ID {
		t.Fatalf("goal source changed: %+v %v", p, err)
	}
}
