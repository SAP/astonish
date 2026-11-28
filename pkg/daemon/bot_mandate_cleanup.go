package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
)

// queueMandateCleanup holds only a composition-created exact capability, never
// a user context or an owner impersonation. Failed durable reservations remain
// charged until release succeeds; retry cannot authorize further work.
func (w *BotWorker) queueMandateCleanup(m *bot.MandateExecution) {
	w.mandateCleanupMu.Lock()
	defer w.mandateCleanupMu.Unlock()
	if w.mandateCleanup == nil {
		w.mandateCleanup = make(map[execution.BotRunIdentity]*bot.MandateExecution)
	}
	copy := *m
	w.mandateCleanup[m.Identity] = &copy
}

func (w *BotWorker) retryMandateCleanup(ctx context.Context) error {
	w.mandateCleanupMu.Lock()
	pending := make([]*bot.MandateExecution, 0, len(w.mandateCleanup))
	for _, m := range w.mandateCleanup {
		pending = append(pending, m)
	}
	w.mandateCleanupMu.Unlock()
	var failures []error
	for _, m := range pending {
		c, cancel := context.WithTimeout(botWorkerContext{ctx}, 5*time.Second)
		err := m.Runtime.ReleaseBotMandateRun(c, m.Authorization.Ref(), m.Identity.RunID)
		cancel()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		w.mandateCleanupMu.Lock()
		if w.mandateCleanup[m.Identity] == m {
			delete(w.mandateCleanup, m.Identity)
		}
		w.mandateCleanupMu.Unlock()
	}
	return errors.Join(failures...)
}
