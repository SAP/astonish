package sandbox

import (
	"context"
	"errors"
	"sync"
)

// BotHomeFileResource is a confined backend helper, never an agent session.
type BotHomeFileResource interface{ Close() error }
type botHomeFileOperationKey struct{}

// BotHomeFileOperation owns resources for one SQL transaction. No resources are
// shared across owners; borrowers are exclusive and Close joins them before SQL commit.
type BotHomeFileOperation struct {
	mu     sync.Mutex
	closed bool
	idle   map[any][]BotHomeFileResource
	active map[*homeFileBorrow]context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

// Non-zero size keeps concurrent borrow identities distinct; pointers to
// different zero-sized allocations are allowed to compare equal in Go.
type homeFileBorrow struct{ _ byte }

func WithBotHomeFileOperation(ctx context.Context) (context.Context, func() error) {
	s := &BotHomeFileOperation{idle: make(map[any][]BotHomeFileResource), active: make(map[*homeFileBorrow]context.CancelFunc)}
	return context.WithValue(ctx, botHomeFileOperationKey{}, s), s.close
}
func BotHomeFileOperationFromContext(ctx context.Context) *BotHomeFileOperation {
	s, _ := ctx.Value(botHomeFileOperationKey{}).(*BotHomeFileOperation)
	return s
}

// WithBotHomeFileOperationFromContext carries resource ownership only, never identity.
func WithBotHomeFileOperationFromContext(ctx, source context.Context) context.Context {
	if source == nil {
		return ctx
	}
	if s := BotHomeFileOperationFromContext(source); s != nil {
		return context.WithValue(ctx, botHomeFileOperationKey{}, s)
	}
	return ctx
}

// Borrow reserves an exclusive helper. Busy helpers are never waited on: a
// nested commit barrier may need another helper before releasing the first.
func (s *BotHomeFileOperation) Borrow(ctx context.Context, key any, create func(context.Context) (BotHomeFileResource, error)) (context.Context, BotHomeFileResource, func(bool) error, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ctx, nil, nil, errors.New("bot home file operation closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	lease := &homeFileBorrow{}
	s.active[lease] = cancel
	s.wg.Add(1)
	var resource BotHomeFileResource
	if idle := s.idle[key]; len(idle) > 0 {
		resource = idle[len(idle)-1]
		s.idle[key] = idle[:len(idle)-1]
	}
	s.mu.Unlock()
	var once sync.Once
	var releaseErr error
	release := func(healthy bool) error {
		once.Do(func() {
			defer s.wg.Done()
			s.mu.Lock()
			delete(s.active, lease)
			keep := healthy && !s.closed && ctx.Err() == nil
			if resource != nil && keep {
				s.idle[key] = append(s.idle[key], resource)
			}
			s.mu.Unlock()
			cancel()
			if resource != nil && !keep {
				releaseErr = resource.Close()
			}
			s.mu.Lock()
			s.err = errors.Join(s.err, releaseErr)
			s.mu.Unlock()
		})
		return releaseErr
	}
	if resource == nil {
		var err error
		resource, err = create(ctx)
		if err != nil {
			return ctx, nil, nil, errors.Join(err, release(false))
		}
	}
	return ctx, resource, release, nil
}
func (s *BotHomeFileOperation) close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		if len(s.active) != 0 {
			s.err = errors.Join(s.err, errors.New("bot home file operation ended with active borrowers"))
		}
		for _, cancel := range s.active {
			cancel()
		}
		s.mu.Unlock()
		s.wg.Wait()
		for _, idle := range s.idle {
			for _, resource := range idle {
				s.err = errors.Join(s.err, resource.Close())
			}
		}
		s.idle = nil
	})
	return s.err
}
