package tools

import (
	"context"
	"errors"
)

// BotPublicFetchOperation is a first-party node operation, not a discoverable
// model capability. It preserves web_fetch's validation and SSRF checks.
const BotPublicFetchOperation = "_bot_public_fetch"

type publicFetchTransportFailure struct{ error }

func (e *publicFetchTransportFailure) Unwrap() error { return e.error }

// BotPublicFetchResult is emitted only by the concrete node handler. A failure
// here attests HTTP read execution, not successful retrieval or permission.
type BotPublicFetchResult struct {
	ReadExecuted bool `json:"read_executed"`
	Response     any  `json:"response"`
}

func botPublicFetch(ctx context.Context, args WebFetchArgs) (BotPublicFetchResult, error) {
	if err := ctx.Err(); err != nil {
		return BotPublicFetchResult{}, err
	}
	// Reject credential-bearing/non-public URL forms before network execution.
	if _, err := RoutineReadOrigin(args.URL); err != nil {
		return BotPublicFetchResult{}, err
	}
	result, err := WebFetch(nil, args)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return BotPublicFetchResult{}, ctxErr
	}
	if err != nil {
		var failure *publicFetchTransportFailure
		if !errors.As(err, &failure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return BotPublicFetchResult{}, err
		}
		message := err.Error()
		if len(message) > 8192 {
			message = message[:8192]
		}
		return BotPublicFetchResult{ReadExecuted: true, Response: map[string]any{"error": message}}, nil
	}
	return BotPublicFetchResult{ReadExecuted: true, Response: result}, nil
}
