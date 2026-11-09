package launcher

import (
	"context"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// Provision is explicit draft setup, separate from Ensure's registered-only
// runtime contract. Trusted composition must authorize the requesting editor
// before establishing this live owner context and attest the returned binding.
func (m *BotHomeManager) Provision(ctx context.Context, b store.Bot) (*sandbox.BotHomeBinding, error) {
	if m == nil || m.Backend == nil || m.Bots == nil {
		return nil, store.ErrBotUnsupported
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || p.Subject != b.OwnerID.String() {
		return nil, store.ErrBotForbidden
	}
	current, err := m.Bots.Get(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Revision != b.Revision || current.Status != store.BotDraft || current.OwnerID != b.OwnerID || current.Scope != b.Scope {
		return nil, store.ErrBotConflict
	}
	scope := sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: p.Subject, BotID: b.ID.String()}
	actual, err := m.Backend.EnsureBotHome(ctx, scope)
	if err != nil {
		return nil, store.ErrBotUnsupported
	}
	if actual == nil || actual.Validate() != nil || actual.Scope != scope {
		return nil, store.ErrBotConflict
	}
	return actual, nil
}
