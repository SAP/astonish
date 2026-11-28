package store

import (
	"context"
	"time"
)

// BotSpendStore is an optional scoped purchase-spend ledger, not execution
// permission. A caller must still obtain action approval and enforce tool policy.
// Amounts are integer currency minor units; zero caps deny spending. Reservations
// count against both caps until a known outcome is recorded. No timeout, run
// completion, or uncertain external result automatically frees a reservation.
// Records and request keys are retained for the lifetime of the Bot root (and
// follow its existing deletion/retention policy). This is not an accounting export.
type BotSpendStore interface {
	ReserveSpend(context.Context, BotSpendRequest) (*BotSpendReservation, error)
	ResolveSpend(context.Context, BotSpendResolution) (*BotSpendReservation, error)
	GetSpend(context.Context, BotID, string) (*BotSpendReservation, error)
}

type BotSpendState string

const (
	BotSpendReserved  BotSpendState = "reserved"
	BotSpendUncertain BotSpendState = "uncertain"
	BotSpendSettled   BotSpendState = "settled"
	BotSpendReleased  BotSpendState = "released"
)

type BotSpendOutcome string

const (
	BotSpendOutcomeUnknown  BotSpendOutcome = "unknown"
	BotSpendOutcomeCharged  BotSpendOutcome = "charged"
	BotSpendOutcomeNoEffect BotSpendOutcome = "no_effect"
)

// BotSpendRequest permanently binds Key within a Bot to a run, reviewed version,
// UTC calendar month (YYYY-MM), currency, action digest and positive amount.
// New reservations require the current UTC month and active reviewed version.
// Reusing a key with any changed binding is a conflict, even after release.
// Lease is authorization transport, not part of the immutable request: nil
// requires the actual Bot owner; workers require their live fenced run lease.
type BotSpendRequest struct {
	BotID        BotID     `json:"bot_id"`
	RunID        RunID     `json:"run_id"`
	Version      int64     `json:"version"`
	Key          string    `json:"key"`
	Currency     string    `json:"currency"`
	Month        string    `json:"month"`
	AmountMinor  int64     `json:"amount_minor"`
	ActionDigest string    `json:"action_digest"`
	Lease        *BotLease `json:"-"`
}

// BotSpendResolution records trusted reconciliation evidence, not proof that an
// external effect occurred. Unknown retains the entire reservation. Charged
// requires a confirmed positive charge <= the reserved amount; any remainder
// must be known not to have taken effect. NoEffect requires a confirmed absence
// of effects and zero charge. Both final outcomes require a nonempty Evidence
// reference. Final outcomes are immutable; exact retries are safe. The actual
// owner may reconcile historical reservations after pause/version/lease changes;
// workers must still hold a live lease for the original reviewed run.
type BotSpendResolution struct {
	BotID        BotID           `json:"bot_id"`
	Key          string          `json:"key"`
	Outcome      BotSpendOutcome `json:"outcome"`
	ChargedMinor int64           `json:"charged_minor"`
	Evidence     string          `json:"evidence"`
	Lease        *BotLease       `json:"-"`
}

type BotSpendReservation struct {
	BotID        BotID         `json:"bot_id"`
	RunID        RunID         `json:"run_id"`
	Version      int64         `json:"version"`
	Key          string        `json:"key"`
	Currency     string        `json:"currency"`
	Month        string        `json:"month"`
	AmountMinor  int64         `json:"amount_minor"`
	ActionDigest string        `json:"action_digest"`
	State        BotSpendState `json:"state"`
	ChargedMinor int64         `json:"charged_minor"`
	Evidence     string        `json:"evidence,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}
