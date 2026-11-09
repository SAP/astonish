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
	Scopes []string
	// ExceptionConstraints is the trusted owner's ceiling for exact, expiring
	// operation decisions; it is never included in worker mandates.
	ExceptionConstraints []execution.BotActionResourceConstraint
	Constraints          []execution.BotActionResourceConstraint
	Limits               BotMandateLimits
	TTL                  time.Duration
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
type BotMandateQuotaError struct {
	Dimension              string
	Used, Requested, Limit int64
}

func (e *BotMandateQuotaError) Error() string {
	return fmt.Sprintf("bot mandate quota exhausted: %s used=%d requested=%d limit=%d; reduce task/context or request an operator-reviewed finite budget", e.Dimension, e.Used, e.Requested, e.Limit)
}
func (e *BotMandateQuotaError) Unwrap() error { return ErrBotForbidden }

type BotMandateRuntime interface {
	BotMandateStore
	execution.BotMandateVerifier
	BotMandateQuotaStore
}
