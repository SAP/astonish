package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotControlProposalStore = (*personalBotStore)(nil)
var _ store.BotControlProposalStore = (*teamBotStore)(nil)

func (s *personalBotStore) ProposeControl(c context.Context, q store.BotControlProposalRequest) (*store.BotControlProposal, error) {
	return s.repository().ProposeControl(c, q)
}
func (s *teamBotStore) ProposeControl(c context.Context, q store.BotControlProposalRequest) (*store.BotControlProposal, error) {
	return s.repository().ProposeControl(c, q)
}
func (s *personalBotStore) ReadControlProposal(c context.Context, b store.BotID, id uuid.UUID) (*store.BotControlProposal, error) {
	return s.repository().ReadControlProposal(c, b, id)
}
func (s *teamBotStore) ReadControlProposal(c context.Context, b store.BotID, id uuid.UUID) (*store.BotControlProposal, error) {
	return s.repository().ReadControlProposal(c, b, id)
}
func (s *personalBotStore) ListControlProposals(c context.Context, b store.BotID) ([]store.BotControlProposal, error) {
	return s.repository().ListControlProposals(c, b)
}
func (s *teamBotStore) ListControlProposals(c context.Context, b store.BotID) ([]store.BotControlProposal, error) {
	return s.repository().ListControlProposals(c, b)
}
func (s *personalBotStore) ConfirmControlProposal(c context.Context, q store.BotControlProposalDecision) (*store.BotControlProposal, error) {
	return s.repository().ConfirmControlProposal(c, q)
}
func (s *teamBotStore) ConfirmControlProposal(c context.Context, q store.BotControlProposalDecision) (*store.BotControlProposal, error) {
	return s.repository().ConfirmControlProposal(c, q)
}
func (s *personalBotStore) RejectControlProposal(c context.Context, q store.BotControlProposalDecision) (*store.BotControlProposal, error) {
	return s.repository().RejectControlProposal(c, q)
}
func (s *teamBotStore) RejectControlProposal(c context.Context, q store.BotControlProposalDecision) (*store.BotControlProposal, error) {
	return s.repository().RejectControlProposal(c, q)
}
