package sandbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type operationTestResource struct {
	closed atomic.Int32
	err    error
}

func (r *operationTestResource) Close() error { r.closed.Add(1); return r.err }
func TestBotHomeFileOperationReuseIsolationAndCleanup(t *testing.T) {
	ctx, end := WithBotHomeFileOperation(t.Context())
	s := BotHomeFileOperationFromContext(ctx)
	count := 0
	create := func(context.Context) (BotHomeFileResource, error) { count++; return &operationTestResource{}, nil }
	_, first, release, err := s.Borrow(ctx, "readonly", create)
	if err != nil {
		t.Fatal(err)
	}
	if err = release(true); err != nil {
		t.Fatal(err)
	}
	_, second, release2, err := s.Borrow(ctx, "readonly", create)
	if err != nil || first != second || count != 1 {
		t.Fatalf("not reused: %d %v", count, err)
	}
	_, third, release3, err := s.Borrow(ctx, "readonly", create)
	if err != nil || third == second {
		t.Fatal("concurrent stream shared")
	}
	_, writable, release4, err := s.Borrow(ctx, "writable", create)
	if err != nil || writable == second {
		t.Fatal("privilege shared")
	}
	_ = release2(true)
	_ = release3(true)
	_ = release4(true)
	if err = end(); err != nil {
		t.Fatal(err)
	}
	for _, r := range []BotHomeFileResource{first, third, writable} {
		if r.(*operationTestResource).closed.Load() != 1 {
			t.Fatal("not closed exactly once")
		}
	}
	if _, _, _, err = s.Borrow(ctx, "readonly", create); err == nil {
		t.Fatal("closed operation reused")
	}
	if err = end(); err != nil {
		t.Fatal(err)
	}
}
func TestBotHomeFileOperationEndCancelsJoinsAndPropagates(t *testing.T) {
	ctx, end := WithBotHomeFileOperation(t.Context())
	expected := errors.New("cleanup failed")
	r := &operationTestResource{err: expected}
	borrowed, _, release, err := BotHomeFileOperationFromContext(ctx).Borrow(ctx, 1, func(context.Context) (BotHomeFileResource, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct{})
	go func() { <-borrowed.Done(); _ = release(false); close(joined) }()
	if err = end(); !errors.Is(err, expected) {
		t.Fatalf("cleanup error lost: %v", err)
	}
	<-joined
	if r.closed.Load() != 1 {
		t.Fatal("cleanup not joined")
	}
}
