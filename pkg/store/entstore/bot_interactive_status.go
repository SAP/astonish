package entstore

import (
	pbot "github.com/SAP/astonish/ent/personal/bot"
	tbot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
)

func personalInteractiveRunStatuses(run store.BotRun) []pbot.Status {
	if run.IsInteractiveChat() && run.Intent.RequiredStatus() != "" {
		return []pbot.Status{pbot.StatusPaused, pbot.StatusActive}
	}
	return []pbot.Status{pbot.Status(run.Intent.RequiredStatus())}
}
func teamInteractiveRunStatuses(run store.BotRun) []tbot.Status {
	if run.IsInteractiveChat() && run.Intent.RequiredStatus() != "" {
		return []tbot.Status{tbot.StatusPaused, tbot.StatusActive}
	}
	return []tbot.Status{tbot.Status(run.Intent.RequiredStatus())}
}
