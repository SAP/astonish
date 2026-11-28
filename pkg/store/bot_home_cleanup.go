package store

import "context"

// BotHomeCleanupClaim is a durable, irrevocable reservation, not evidence of
// physical deletion. Request is immutable across retries. OwnerID is obtained
// from the stored Bot; tenant identity comes from the routed store.
type BotHomeCleanupClaim struct {
	Request  BotCleanupRequest
	OwnerID  string
	OrgSlug  string
	TeamSlug string
}

// BotHomeCleanupStore is a service-only optional retention capability. Reserving
// atomically closes restoration before any external storage operation. Failure
// does not release the reservation; retry deletion with the same claim.
type BotHomeCleanupStore interface {
	ReserveHomeCleanup(context.Context, BotHomeBinding, int64) (*BotHomeCleanupClaim, error)
}
