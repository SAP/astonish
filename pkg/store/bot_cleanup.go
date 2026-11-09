package store

import "context"

// BotCleanupKind separates cancelling physical run workers from retiring a home.
// Releasing a run teardown barrier never authorizes retention purge.
type BotCleanupKind string

const (
	BotCleanupRunTeardown BotCleanupKind = "run_teardown"
	BotCleanupHome        BotCleanupKind = "home"
	// BotCleanupScope is required on an internal service principal, not an owner JWT.
	BotCleanupScope = "bots:cleanup"
)

// BotCleanupRequest identifies precisely what the runtime verified. Fence is the
// current bot fence AFTER lifecycle cancellation; RunFence is the old worker's
// lease fence. Home must match the entire opaque binding, not a session name.
// This is an internal capability contract, not a public HTTP request body.
type BotCleanupRequest struct {
	Kind             BotCleanupKind
	BotID            BotID
	ExpectedRevision int64
	Fence            int64
	Home             BotHomeBinding
	RunID            RunID
	RunFence         int64
}

// BotCleanupStore is optional; public BotStore implementations need not support
// runtime cleanup. ConfirmCleanup records an attestation; it performs no physical
// deletion and makes no independent claim that a sandbox or home was deleted.
type BotCleanupStore interface {
	ConfirmCleanup(context.Context, BotCleanupRequest) error
}

// BotCleanupVerifier must be installed only by trusted runtime composition, never
// from request data. Returning nil attests that ALL workers and descendants for
// the exact bot/fence are stopped; for home cleanup it additionally attests that
// the exact retained home has been retired. It must verify completed work, not
// initiate deletion: it runs outside the database transaction and a racing
// lifecycle change can invalidate its receipt. Implementations must fence their
// own physical operations independently. A missing callback fails closed.
type BotCleanupVerifier func(context.Context, BotCleanupRequest) error

type botCleanupVerifierKey struct{}

func WithBotCleanupVerifier(ctx context.Context, verify BotCleanupVerifier) context.Context {
	return context.WithValue(ctx, botCleanupVerifierKey{}, verify)
}

func BotCleanupVerifierFromContext(ctx context.Context) (BotCleanupVerifier, bool) {
	if ctx == nil {
		return nil, false
	}
	verify, ok := ctx.Value(botCleanupVerifierKey{}).(BotCleanupVerifier)
	return verify, ok && verify != nil
}
