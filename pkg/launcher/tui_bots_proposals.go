package launcher

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func (b *botPlatformBackend) ContinueBotInteractive(ctx context.Context, run store.BotRun, response *store.BotInteractionResponse) (*store.BotRun, error) {
	bot := b.SelectedBot()
	if bot == nil || bot.ID != run.BotID || !run.IsInteractiveChat() {
		return nil, fmt.Errorf("invalid interactive run binding")
	}
	return b.bots.ContinueInteractive(ctx, bot.ID.String(), run.ID.String(), bot.Revision, uuid.NewString(), response)
}

func (b *botPlatformBackend) BotProposals(ctx context.Context) ([]store.BotControlProposal, error) {
	bot := b.SelectedBot()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	return b.bots.Proposals(ctx, bot.ID.String())
}
func (b *botPlatformBackend) DecideBotProposal(ctx context.Context, p store.BotControlProposal, confirm bool) error {
	bot := b.SelectedBot()
	if bot == nil || bot.ID != p.BotID {
		return fmt.Errorf("proposal does not belong to selected Bot")
	}
	decision := client.BotProposalReject
	if confirm {
		decision = client.BotProposalConfirm
	}
	_, err := b.bots.DecideProposal(ctx, bot.ID.String(), p.ID.String(), p.Digest, decision)
	return err
}
func (b *botPlatformBackend) ApproveBotAndContinue(ctx context.Context, a store.BotApproval, response *store.BotInteractionResponse) (*store.BotApproveContinueResult, error) {
	bot := b.SelectedBot()
	if bot == nil || bot.ID != a.BotID {
		return nil, fmt.Errorf("approval does not belong to selected Bot")
	}
	if a.Interaction != nil && a.Interaction.ValidateResponse(response) != nil {
		return nil, fmt.Errorf("invalid bound Bot interaction answer")
	}
	// Ordinary answers preserve original authority; only explicit Continue renews it.
	return b.bots.ApproveAndContinue(ctx, bot.ID.String(), a.ID.String(), a.ActionDigest, response)
}
