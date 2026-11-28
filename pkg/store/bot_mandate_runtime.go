package store

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"time"
)

// BotMandatePolicy is resolved from current trusted operator policy and exact
// tool/resource catalog, never copied from request or model output.
type BotMandatePolicy struct {
	RuntimePolicy  *execution.BotRuntimePolicy
	PublicResearch *execution.BotPublicResearchScope
	Scopes         []string
	// ExceptionConstraints is the trusted owner's ceiling for exact, expiring
	// operation decisions; it is never included in worker mandates.
	ExceptionConstraints []execution.BotActionResourceConstraint
	Constraints          []execution.BotActionResourceConstraint
	Limits               BotMandateLimits
	TTL                  time.Duration
}

// Clone detaches policy snapshots; resolvers remain trusted composition inputs.
func (p BotMandatePolicy) Clone() BotMandatePolicy {
	m := execution.BotMandate{RuntimePolicy: p.RuntimePolicy, PublicResearch: p.PublicResearch, Scopes: p.Scopes, Constraints: p.Constraints}.Clone()
	p.RuntimePolicy, p.PublicResearch, p.Scopes, p.Constraints = m.RuntimePolicy, m.PublicResearch, m.Scopes, m.Constraints
	p.ExceptionConstraints = execution.BotMandate{Constraints: p.ExceptionConstraints}.Clone().Constraints
	return p
}

// Progress occupancy is independent of cumulative quota. Suspend releases ONLY
// concurrency occupancy; reacquire rechecks current authority and capacity for
// the same logical run without charging a new daily run or resetting any ledger.
type BotMandateProgressQuotaRequest struct {
	MandateRef BotMandateRef
	Mutation   BotProgressMutation
}

func (r BotMandateProgressQuotaRequest) Validate(now time.Time) error {
	if err := r.MandateRef.Validate(); err != nil {
		return err
	}
	if err := r.Mutation.Validate(now); err != nil {
		return err
	}
	if r.MandateRef.BotID != r.Mutation.Binding.BotID {
		return ErrBotConflict
	}
	return nil
}

type BotMandateProgressQuotaStore interface {
	SuspendBotMandateRun(context.Context, BotMandateProgressQuotaRequest) error
	ReacquireBotMandateRun(context.Context, BotMandateProgressQuotaRequest) error
}

type BotMandatePolicyResolver func(context.Context, BotMandateRef) (BotMandatePolicy, error)
type botMandateAdmissionKey struct{}

func WithBotMandateAdmission(ctx context.Context, resolver BotMandatePolicyResolver) context.Context {
	return context.WithValue(ctx, botMandateAdmissionKey{}, resolver)
}
func BotMandateAdmissionFromContext(ctx context.Context) (BotMandatePolicyResolver, bool) {
	if ctx == nil {
		return nil, false
	}
	r, ok := ctx.Value(botMandateAdmissionKey{}).(BotMandatePolicyResolver)
	return r, ok && r != nil
}

// Quota charges are reservations made BEFORE a tool/model side effect, never
// post-hoc accounting. Releasing a run never refunds charged aggregate usage.
type BotMandateQuotaStore interface {
	ReserveBotMandateRun(context.Context, BotMandateRef, uuid.UUID) error
	ConsumeBotMandateQuota(context.Context, BotMandateRef, uuid.UUID, int64, int64, int64) error
	ReleaseBotMandateRun(context.Context, BotMandateRef, uuid.UUID) error
	ReserveBotMandateModel(context.Context, BotMandateRef, uuid.UUID, uuid.UUID, int64) error
	SettleBotMandateModel(context.Context, BotMandateRef, uuid.UUID, uuid.UUID, int64) error
}

// BotMandateQuotaError identifies a finite quota gate without disguising it as
// a permission failure. Existing forbidden classification remains compatible.
const (
	BotQuotaRunTokens   = "run_tokens"
	BotQuotaTotalTokens = "total_tokens"
)

type BotMandateQuotaError struct {
	Dimension              string
	Used, Requested, Limit int64
}

func (e *BotMandateQuotaError) Error() string {
	return fmt.Sprintf("bot mandate quota exhausted: %s used=%d requested=%d limit=%d; reduce task/context or request an operator-reviewed finite budget", e.Dimension, e.Used, e.Requested, e.Limit)
}
func (e *BotMandateQuotaError) Unwrap() error { return ErrBotForbidden }

// BotEmployeePresentationPreparer is server-only terminal-source communication.
// Storage derives the original pin, limits, deadlines and immutable evidence;
// neither public requests nor model output may supply these fields.
type BotEmployeePresentationPreparer interface {
	PrepareEmployeePresentation(context.Context, BotEmployeePresentationRequest) (*BotEmployeePresentation, error)
}

type BotMandateRuntime interface {
	BotMandateStore
	execution.BotMandateVerifier
	BotMandateQuotaStore
}
