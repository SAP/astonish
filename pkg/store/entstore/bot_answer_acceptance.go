package entstore

import (
	"context"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
)

// appendAcceptedBotAnswer runs inside the owner decision's root-row CAS. It
// needs no worker lease, model, sandbox or new turn authority. The approval and
// both ordered transcript rows either commit together or all roll back.
func appendAcceptedBotAnswer(ctx context.Context, tx botDraftDB, a store.BotApproval) error {
	if a.Status != store.ApprovalApproved || a.Interaction == nil || a.Interaction.Kind != store.BotInteractionQuestion {
		return nil
	}
	if a.DecidedBy == nil || a.DecidedAt == nil {
		return store.ErrBotInvalid
	}
	rec, err := tx.GetBot(ctx, a.BotID)
	if err != nil {
		return err
	}
	if *a.DecidedBy != rec.Bot.OwnerID {
		return store.ErrBotForbidden
	}
	saved, err := tx.ReadRun(ctx, a.BotID, a.RunID)
	if err != nil {
		return err
	}
	events, err := preparedPlanEvents(ctx, tx, saved.Run)
	if err != nil {
		return err
	}
	content, err := store.BotInteractionAnswerMessage(events, &a)
	if err != nil {
		return err
	}
	transcript, ok := tx.(botTranscriptDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	event := &session.Event{ID: "bot-owner-answer:" + a.ID.String(), Author: "user", Timestamp: *a.DecidedAt, LLMResponse: model.LLMResponse{Content: content}}
	projectCtx := workTranscriptContext(ctx, saved.Work)
	projectCtx = context.WithValue(projectCtx, botWorkOwnerInputKey{}, true)
	return transcript.appendRunTranscript(projectCtx, rec.Bot, saved.Run, event)
}
