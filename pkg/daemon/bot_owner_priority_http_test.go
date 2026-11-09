package daemon

import (
	"context"
	"errors"
)

type priorityHTTPBackend struct {
	activationHomeBackend
	pending bool
	session string
}

func (b *priorityHTTPBackend) VerifySessionDestroyed(_ context.Context, id string) error {
	b.session = id
	if b.pending {
		return errors.New("old session still terminating")
	}
	return nil
}
