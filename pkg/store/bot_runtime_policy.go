package store

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

// BotRuntimePolicyForConfiguration resolves only trusted composition policy.
// Absence preserves legacy behavior; callers never fill missing saved policy.
func BotRuntimePolicyForConfiguration(ctx context.Context, botID, owner uuid.UUID) (*execution.BotRuntimePolicy, error) {
	resolve, ok := BotMandateAdmissionFromContext(ctx)
	if !ok {
		if svc := FromContext(ctx); svc != nil {
			resolve = svc.BotMandatePolicy
		}
	}
	if resolve == nil {
		return nil, nil
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Kind != execution.PrincipalKindUser || p.Subject != owner.String() {
		return nil, ErrBotForbidden
	}
	policy, err := resolve(ctx, BotMandateRef{ID: uuid.New(), BotID: botID, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, OwnerID: owner.String(), Revision: 1})
	if err != nil {
		return nil, err
	}
	if err := policy.RuntimePolicy.Validate(); err != nil {
		return nil, ErrBotInvalid
	}
	return policy.RuntimePolicy.Clone(), nil
}

// NewBotVersionForCreation is an explicit trusted creation path. The historical
// DefaultBotVersion constructor and all reads keep legacy limit semantics.
func NewBotVersionForCreation(ctx context.Context, owner uuid.UUID) (BotVersion, error) {
	v := DefaultBotVersion()
	p, err := BotRuntimePolicyForConfiguration(ctx, uuid.New(), owner)
	if err != nil {
		return v, err
	}
	v.Budget.RuntimePolicy = p
	return v, nil
}
