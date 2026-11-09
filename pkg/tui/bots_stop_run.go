package tui

import (
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// botStopRunRoute does not execute mutations. The exact CLI confirmation is the
// explicit action; viewer Enter/Escape/detach never cancels daemon work.
func (m model) botStopRunRoute(run store.BotRun) string {
	bot := m.botCap().SelectedBot()
	if bot == nil || run.BotID != bot.ID || run.ID == uuid.Nil || (bot.Scope != store.BotScopePersonal && bot.Scope != store.BotScopeTeam) {
		return "Run identity changed; reload and review the selected Bot. No cancellation performed."
	}
	return fmt.Sprintf("Stop only run %s [%s] for Bot %s (%s). Review, then execute in another terminal:\nastonish bots stop-run %s --scope %s --run %s --confirm %s:%s:%s\nRequests durable run cancellation, not Bot stop/delete. Detach never cancels daemon work.", run.ID, run.Status, bot.ID, bot.Scope, bot.ID, bot.Scope, run.ID, bot.Scope, bot.ID, run.ID)
}
