package client

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"net/url"
)

// SendMessage admits one continuous home conversation turn without lifecycle consent.
func (b Bots) SendMessage(ctx context.Context, id string, version int64, key, instructions string) (*store.BotMessageReceipt, error) {
	return b.SendMessageRequest(ctx, id, store.BotMessageRequest{Version: version, IdempotencyKey: key, Instructions: instructions})
}

// SendMessageRequest exposes optional turn controls without changing legacy callers.
func (b Bots) SendMessageRequest(ctx context.Context, id string, request store.BotMessageRequest) (*store.BotMessageReceipt, error) {
	var receipt store.BotMessageReceipt
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/messages"), request, &receipt)
	return &receipt, err
}

// ContinueInteractive reauthorizes a saved interactive run, never lifecycle activation.
func (b Bots) ContinueInteractive(ctx context.Context, id, runID string, revision int64, key string, response *store.BotInteractionResponse) (*store.BotRun, error) {
	if revision < 1 || key == "" {
		return nil, fmt.Errorf("reviewed revision and idempotency key required")
	}
	return b.ContinueInteractiveRequest(ctx, id, runID, store.BotContinueInteractiveRequest{ExpectedRevision: revision, IdempotencyKey: key, Response: response})
}

// ContinueInteractiveRequest retains the originating run and accepts optional controls.
// Definition execution consent requires the exact persisted originating identity;
// it never grants additional tool permission or starts a replacement run.
func (b Bots) ContinueInteractiveRequest(ctx context.Context, id, runID string, request store.BotContinueInteractiveRequest) (*store.BotRun, error) {
	if request.ExpectedRevision < 1 || request.IdempotencyKey == "" {
		return nil, fmt.Errorf("reviewed revision and idempotency key required")
	}
	var run store.BotRun
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/runs/"+url.PathEscape(runID)+"/continue-interactive"), request, &run)
	return &run, err
}

type BotProposalDecision string

const (
	BotProposalConfirm BotProposalDecision = "confirm"
	BotProposalReject  BotProposalDecision = "reject"
)

func (b Bots) Proposals(ctx context.Context, id string) ([]store.BotControlProposal, error) {
	var result struct {
		Proposals []store.BotControlProposal `json:"proposals"`
	}
	err := b.request(ctx, "GET", b.path("/"+url.PathEscape(id)+"/proposals"), nil, &result)
	return result.Proposals, err
}
func (b Bots) DecideProposal(ctx context.Context, id, proposalID, digest string, decision BotProposalDecision) (*store.BotControlProposal, error) {
	if digest == "" || (decision != BotProposalConfirm && decision != BotProposalReject) {
		return nil, fmt.Errorf("exact digest and confirm/reject decision required")
	}
	var result store.BotControlProposal
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/proposals/"+url.PathEscape(proposalID)+"/decision"), struct {
		Digest   string              `json:"digest"`
		Decision BotProposalDecision `json:"decision"`
	}{digest, decision}, &result)
	return &result, err
}

// ApproveAndContinue performs one atomic saved-action decision; it never activates a Bot.
func (b Bots) ApproveAndContinue(ctx context.Context, id, approvalID, digest string, response *store.BotInteractionResponse) (*store.BotApproveContinueResult, error) {
	if digest == "" {
		return nil, fmt.Errorf("exact action digest required")
	}
	var result store.BotApproveContinueResult
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/approvals/"+url.PathEscape(approvalID)+"/approve-and-continue"), struct {
		Digest   string                        `json:"action_digest"`
		Response *store.BotInteractionResponse `json:"response,omitempty"`
	}{digest, response}, &result)
	return &result, err
}
