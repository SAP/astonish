package api

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
)

// The source admission creates a restricted caller-ceiling receipt only. Its
// temporary registry is closed before returning; it cannot authorize runtime.
// Durable execution must verify the persisted mandate separately after commit.
type botMandateSourceAdmission struct{}

func (botMandateSourceAdmission) Register(ctx context.Context, id execution.BotRunIdentity) (execution.BotTurnAuthorization, error) {
	registry := execution.NewBotTurnAuthorizationRegistry(1)
	defer registry.Close()
	return registry.Register(ctx, id)
}
func (botMandateSourceAdmission) Cancel(execution.BotRunIdentity) {}
