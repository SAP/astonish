package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotAgentPriorRunReadBinding(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	req.Run.Trigger, req.Run.TriggerID = store.TriggerCron, uuid.New()
	req.Run.CreatedAt = time.Now()
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})
	prior := bot.BotPriorRunContext{BotID: req.Bot.ID, RunID: uuid.New(), TriggerID: req.Run.TriggerID, CreatedAt: req.Run.CreatedAt.Add(-time.Minute), OutcomeAt: req.Run.CreatedAt.Add(-time.Second), Status: store.RunFailed, FailureKind: store.BotReportFailureOperational}
	resources := &BotChatResources{Work: &agentExecutionWorkStore{state: w}, PriorRun: func(context.Context) (*bot.BotPriorRunContext, error) { return &prior, nil }}
	service := &botAgentWorkService{req: req, resources: resources}
	read, err := service.Read(ctx, bot.BotWorkReadRequest{})
	if err != nil || read.PriorRun == nil || read.PriorRun.RunID != prior.RunID {
		t.Fatalf("read: %+v %v", read, err)
	}
	read.PriorRun.Status = store.RunSucceeded
	if prior.Status != store.RunFailed {
		t.Fatal("callback result aliases returned snapshot")
	}
	original := prior
	for _, tc := range []string{"bot", "run", "trigger", "future", "late", "active", "kind", "zero"} {
		t.Run(tc, func(t *testing.T) {
			prior = original
			switch tc {
			case "bot":
				prior.BotID = uuid.New()
			case "run":
				prior.RunID = req.Run.ID
			case "trigger":
				prior.TriggerID = uuid.New()
			case "future":
				prior.CreatedAt = req.Run.CreatedAt
			case "late":
				prior.OutcomeAt = req.Run.CreatedAt
			case "active":
				prior.Status = store.RunRunning
			case "kind":
				prior.FailureKind = "injected arbitrary text"
			case "zero":
				prior.OutcomeAt = time.Time{}
			}
			if _, err := service.Read(ctx, bot.BotWorkReadRequest{}); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("unsafe prior accepted: %v", err)
			}
		})
	}
}
