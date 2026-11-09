package channels

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

func botRunCommand() *Command {
	return &Command{Name: "bot_run", Description: "Queue a reviewed Bot channel trigger: /bot_run personal|team bot-id trigger-id", Handler: func(ctx context.Context, cc CommandContext) (string, error) {
		p, ok := execution.PrincipalFromContext(ctx)
		if !ok || p.Validate() != nil || p.Authentication != execution.AuthMethodChannel || p.Surface != execution.SurfaceChannel || p.Subject != cc.UserID || cc.MessageID == "" {
			return "", store.ErrBotForbidden
		}
		args := strings.Fields(cc.RawText)
		if len(args) != 4 {
			return "Usage: /bot_run personal|team bot-id trigger-id", nil
		}
		scope := store.BotScope(args[1])
		repo := store.BotStoreFromContext(ctx, scope)
		if repo == nil {
			return "", store.ErrBotUnsupported
		}
		triggers, ok := repo.(store.BotTriggerStore)
		if !ok {
			return "", store.ErrBotUnsupported
		}
		id, e := uuid.Parse(args[2])
		if e != nil {
			return "", store.ErrBotInvalid
		}
		tid, e := uuid.Parse(args[3])
		if e != nil {
			return "", store.ErrBotInvalid
		}
		b, e := repo.Get(ctx, id)
		if e != nil {
			return "", e
		}
		v, e := repo.GetVersion(ctx, id, b.Version)
		if e != nil {
			return "", e
		}
		for _, tr := range v.Triggers {
			if tr.ID != tid {
				continue
			}
			binding := tr.ChannelBinding
			if tr.Kind != store.TriggerChannel || binding == nil || binding.Channel != cc.ChannelID || binding.ChatID != cc.ChatID || binding.ThreadID != cc.ThreadID {
				return "", store.ErrBotForbidden
			}
			run, e := triggers.AdmitExternalRun(ctx, store.BotExternalEvent{BotID: id, Version: b.Version, TriggerID: tid, EventID: cc.MessageID, Binding: &store.BotChannelBinding{ID: binding.ID, Channel: cc.ChannelID, ChatID: cc.ChatID, ThreadID: cc.ThreadID}})
			if e != nil {
				return "", e
			}
			return fmt.Sprintf("Bot run %s queued.", run.ID), nil
		}
		return "", store.ErrBotNotFound
	}}
}
