package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"slices"
	"time"
)

type BotOperationStatus string

const (
	BotOperationPending   BotOperationStatus = "pending"
	BotOperationApplying  BotOperationStatus = "applying"
	BotOperationApplied   BotOperationStatus = "applied"
	BotOperationBlocked   BotOperationStatus = "blocked"
	BotOperationFailed    BotOperationStatus = "failed"
	BotOperationCancelled BotOperationStatus = "cancelled"
)

func (s BotOperationStatus) Valid() bool {
	switch s {
	case BotOperationPending, BotOperationApplying, BotOperationApplied, BotOperationBlocked, BotOperationFailed, BotOperationCancelled:
		return true
	}
	return false
}

// Blocked requires renewed policy evaluation before re-entering pending. All
// terminal states are immutable; retries create a new idempotent operation.
func (s BotOperationStatus) CanTransitionTo(n BotOperationStatus) bool {
	switch s {
	case BotOperationPending:
		return n == BotOperationApplying || n == BotOperationBlocked || n == BotOperationCancelled
	case BotOperationApplying:
		return n == BotOperationApplied || n == BotOperationBlocked || n == BotOperationFailed || n == BotOperationCancelled
	case BotOperationBlocked:
		return n == BotOperationPending || n == BotOperationCancelled
	}
	return false
}

type BotOperationPolicyDecision string

const (
	BotOperationExecute              BotOperationPolicyDecision = "execute"
	BotOperationClarify              BotOperationPolicyDecision = "clarify"
	BotOperationRequireAuthorization BotOperationPolicyDecision = "require_authorization"
	BotOperationDeny                 BotOperationPolicyDecision = "deny"
)

func (d BotOperationPolicyDecision) Valid() bool {
	return d == BotOperationExecute || d == BotOperationClarify || d == BotOperationRequireAuthorization || d == BotOperationDeny
}

// Postconditions are populated only by the trusted executor after observing
// durable state. Neither model narration nor an attempted tool call is success.
type BotOperationPostcondition struct {
	Resource string `json:"resource"`
	Revision int64  `json:"revision"`
	Verified bool   `json:"verified"`
}
type BotOperationReceipt struct {
	Schedule       *BotOperationScheduleReceipt `json:"schedule,omitempty"`
	Status         BotOperationStatus           `json:"status"`
	ObservedAt     time.Time                    `json:"observed_at"`
	Postconditions []BotOperationPostcondition  `json:"postconditions,omitempty"`
	Reason         string                       `json:"reason,omitempty"`
}

func (r BotOperationReceipt) Clone() BotOperationReceipt {
	r.Postconditions = slices.Clone(r.Postconditions)
	if r.Schedule != nil {
		s := *r.Schedule
		if s.NextRun != nil {
			n := *s.NextRun
			s.NextRun = &n
		}
		r.Schedule = &s
	}
	return r
}
func (r BotOperationReceipt) Validate() error {
	if !r.Status.Valid() {
		return ErrBotInvalid
	}
	if r.Status == BotOperationApplied {
		if r.ObservedAt.IsZero() || len(r.Postconditions) == 0 {
			return ErrBotInvalid
		}
		for _, p := range r.Postconditions {
			if p.Resource == "" || p.Revision < 1 || !p.Verified {
				return ErrBotInvalid
			}
		}
	}
	return nil
}

type BotOperation struct {
	Exception      *BotOperationExceptionReference `json:"exception,omitempty"`
	Provenance     *BotOperationProvenance         `json:"provenance,omitempty"`
	ID             uuid.UUID                       `json:"id"`
	MandateRef     BotMandateRef                   `json:"mandate_ref"`
	IdempotencyKey string                          `json:"idempotency_key"`
	Payload        BotControlPayload               `json:"payload"`
	Status         BotOperationStatus              `json:"status"`
	Decision       BotOperationPolicyDecision      `json:"decision"`
	Receipt        *BotOperationReceipt            `json:"receipt,omitempty"`
	Revision       int64                           `json:"revision"`
}

// Clone uses the closed JSON payload contract to copy every nested pointer/map.
// Invalid non-finite payloads return an error rather than silently losing data.
func (o BotOperation) Clone() (BotOperation, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return BotOperation{}, err
	}
	var c BotOperation
	err = json.Unmarshal(b, &c)
	return c, err
}
func (o BotOperation) Validate() error {
	if o.ID == uuid.Nil || o.MandateRef.Validate() != nil || o.IdempotencyKey == "" || o.Revision < 1 || !o.Status.Valid() || !o.Decision.Valid() {
		return ErrBotInvalid
	}
	if err := o.Payload.Validate(); err != nil {
		return err
	}
	if (o.Status == BotOperationApplying || o.Status == BotOperationApplied) && o.Decision != BotOperationExecute {
		return ErrBotForbidden
	}
	if o.Status == BotOperationApplied && o.Receipt == nil {
		return ErrBotInvalid
	}
	if o.Receipt != nil {
		if o.Receipt.Status != o.Status {
			return ErrBotInvalid
		}
		if err := o.Receipt.Validate(); err != nil {
			return err
		}
	}
	_, err := json.Marshal(o)
	return err
}

// Compare-and-swap transitions and unique scoped idempotency keys are required.
type BotOperationStore interface {
	CreateBotOperation(context.Context, BotOperation) (*BotOperation, error)
	ReadBotOperation(context.Context, BotMandateRef, uuid.UUID) (*BotOperation, error)
	TransitionBotOperation(context.Context, BotMandateRef, uuid.UUID, int64, BotOperationStatus, BotOperationReceipt) (*BotOperation, error)
}
