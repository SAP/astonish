package httpool

import (
	"context"
	"errors"
	"net/http"
)

// ErrAuthReplayUnavailable means authentication was rejected but the request
// body cannot be safely replayed. The rejected token has still been invalidated.
var ErrAuthReplayUnavailable = errors.New("authentication replay unavailable: request body cannot be recreated")

// RoundTripWithToken decorates a clone of req with a proactively acquired token
// and permits at most one authentication replay. acquire receives an empty
// rejected token initially and the exact rejected token on recovery; invalidate
// must likewise invalidate only the supplied token. Callbacks must be safe for
// concurrent use if the enclosing transport is shared.
//
// The base transport owns each body handed to it. Before a body is handed off,
// this helper closes it on acquisition failure or cancellation. A rejected
// response is closed before recovery, but the final response is returned intact
// (including a second authentication rejection). Nonempty request bodies are
// replayed only through GetBody, never by buffering the original stream.
func RoundTripWithToken(req *http.Request, base http.RoundTripper, acquire func(context.Context, string) (string, error), invalidate func(string), decorate func(*http.Request, string), recoverStatus func(int) bool) (*http.Response, error) {
	ctx := req.Context()
	initial := req.Clone(ctx)
	if initial.Header == nil {
		initial.Header = make(http.Header)
	}
	closeBody := func(r *http.Request) {
		if r.Body != nil {
			_ = r.Body.Close()
		}
	}
	if err := ctx.Err(); err != nil {
		closeBody(initial)
		return nil, err
	}
	token, err := acquire(ctx, "")
	if ctxErr := ctx.Err(); ctxErr != nil {
		closeBody(initial)
		return nil, ctxErr
	}
	if err != nil {
		closeBody(initial)
		return nil, err
	}
	decorate(initial, token)
	resp, err := base.RoundTrip(initial)
	if err != nil || resp == nil || !recoverStatus(resp.StatusCode) {
		return resp, err
	}

	invalidate(token)
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	retry := req.Clone(ctx)
	if retry.Header == nil {
		retry.Header = make(http.Header)
	}
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, ErrAuthReplayUnavailable
		}
		retry.Body, err = req.GetBody()
		if err != nil || retry.Body == nil {
			closeBody(retry)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, ErrAuthReplayUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		closeBody(retry)
		return nil, err
	}
	newToken, err := acquire(ctx, token)
	if ctxErr := ctx.Err(); ctxErr != nil {
		closeBody(retry)
		return nil, ctxErr
	}
	if err != nil {
		closeBody(retry)
		return nil, err
	}
	decorate(retry, newToken)
	resp, err = base.RoundTrip(retry)
	if err == nil && resp != nil && recoverStatus(resp.StatusCode) {
		invalidate(newToken)
	}
	return resp, err
}
