package launcher

import (
	"context"
	"sync"
	"testing"
	"time"
)

type chatRuntimeHookContextKey struct{}

// heldRuntimeContext deliberately withholds cancellation forwarding. Cancellation
// itself is synchronous; only registered AfterFunc callbacks are held, modeling
// the scheduling gap without depending on GOMAXPROCS, sleeps or scheduler yields.
type heldRuntimeContext struct {
	context.Context
	mu        sync.Mutex
	callbacks map[int]func()
	next      int
}

func newHeldRuntimeContext() (*heldRuntimeContext, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	return &heldRuntimeContext{Context: ctx, callbacks: make(map[int]func())}, cancel
}

// Hide the standard library's private cancelCtx key so it uses AfterFunc.
func (c *heldRuntimeContext) Value(key any) any {
	return context.WithoutCancel(c.Context).Value(key)
}
func (c *heldRuntimeContext) AfterFunc(f func()) func() bool {
	c.mu.Lock()
	id := c.next
	c.next++
	c.callbacks[id] = f
	c.mu.Unlock()
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, exists := c.callbacks[id]
		delete(c.callbacks, id)
		return exists
	}
}
func (c *heldRuntimeContext) links() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.callbacks)
}

func TestChatRuntimeContextFixture(t *testing.T) {
	ctx, cancel := newHeldRuntimeContext()
	defer cancel()
	called := false
	stop := ctx.AfterFunc(func() { called = true })
	cancel()
	if ctx.Err() != context.Canceled || called || ctx.links() != 1 {
		t.Fatal("fixture must cancel without forwarding registered callbacks")
	}
	if !stop() || stop() || ctx.links() != 0 {
		t.Fatal("fixture callback stop must be idempotent")
	}
}

func TestChatRuntimeContextSynchronousCancellation(t *testing.T) {
	for _, source := range []string{"parent", "bound"} {
		for _, check := range []string{"Err", "Done"} {
			t.Run(source+"/"+check, func(t *testing.T) {
				parent, cancelParent := newHeldRuntimeContext()
				defer cancelParent()
				bound, cancelBound := newHeldRuntimeContext()
				defer cancelBound()
				merged, release := intersectChatRuntimeContext(parent, bound)
				defer release()
				if merged.Err() != nil {
					t.Fatal("live intersection cancelled")
				}
				if source == "parent" {
					cancelParent()
				} else {
					cancelBound()
				}
				if check == "Err" && merged.Err() != context.Canceled {
					t.Fatalf("cancellation hidden behind queued callback: %v", merged.Err())
				}
				select {
				case <-merged.Done():
				default:
					t.Fatal("Done remains open after synchronous lifetime check")
				}
				if merged.Err() != context.Canceled {
					t.Fatal("closed Done must have stable Err")
				}
				release()
				release()
				if parent.links() != 0 || bound.links() != 0 {
					t.Fatal("release leaked cancellation links")
				}
			})
		}
	}
}

func TestChatRuntimeContextDerivedCancellation(t *testing.T) {
	for _, sameLifetime := range []bool{false, true} {
		name := "independent-lifetimes"
		if sameLifetime {
			name = "same-lifetime"
		}
		t.Run(name, func(t *testing.T) {
			parent, cancelParent := newHeldRuntimeContext()
			defer cancelParent()
			bound, cancelBound := newHeldRuntimeContext()
			defer cancelBound()
			var hook context.Context = bound
			if sameLifetime {
				hook = context.WithValue(parent, chatRuntimeHookContextKey{}, "hook")
			}
			outer, releaseOuter := intersectChatRuntimeContext(parent, hook)
			defer releaseOuter()
			// Force a derived WithCancel to wait for propagation rather than use
			// the standard library's direct cancelCtx child registration.
			held := &heldRuntimeContext{Context: outer, callbacks: make(map[int]func())}
			derived, cancelDerived := context.WithCancel(held)
			defer cancelDerived()
			cancelParent()
			if derived.Err() != nil {
				t.Fatal("fixture did not withhold derived cancellation")
			}
			for _, source := range []string{"parent", "bound", "both"} {
				a, b := context.Context(derived), context.Background()
				if source == "bound" {
					a, b = b, a
				}
				if source == "both" {
					b = a
				}
				inner, release := intersectChatRuntimeContext(a, b)
				if inner.Err() != context.Canceled {
					t.Errorf("%s: derived context escaped lifetime fence", source)
				}
				release()
			}
		})
	}
}

func TestChatRuntimeContextRelease(t *testing.T) {
	parent, cancelParent := newHeldRuntimeContext()
	defer cancelParent()
	bound, cancelBound := newHeldRuntimeContext()
	defer cancelBound()
	merged, release := intersectChatRuntimeContext(parent, bound)
	if parent.links() != 1 || bound.links() != 1 {
		t.Fatal("missing cancellation links")
	}
	release()
	release()
	if parent.Err() != nil || bound.Err() != nil {
		t.Fatal("release cancelled a borrowed lifetime")
	}
	if merged.Err() != context.Canceled || parent.links() != 0 || bound.links() != 0 {
		t.Fatal("release did not cancel and unlink")
	}
	cancelParent()
	cancelBound()
	if merged.Err() != context.Canceled {
		t.Fatal("cancellation state changed after release")
	}
}

func TestChatRuntimeContextDeadlineAndValues(t *testing.T) {
	type key int
	for _, earlier := range []string{"parent", "bound"} {
		t.Run(earlier, func(t *testing.T) {
			first, second := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
			pd, bd := first, second
			if earlier == "bound" {
				pd, bd = bd, pd
			}
			parent, cancelParent := context.WithDeadline(context.WithValue(context.Background(), key(0), "parent"), pd)
			defer cancelParent()
			bound, cancelBound := context.WithDeadline(context.WithValue(context.Background(), key(1), "bound"), bd)
			defer cancelBound()
			merged, release := intersectChatRuntimeContext(parent, bound)
			defer release()
			if got, ok := merged.Deadline(); !ok || !got.Equal(first) {
				t.Fatalf("wrong deadline %v %v", got, ok)
			}
			if merged.Value(key(0)) != nil || merged.Value(key(1)) != "bound" {
				t.Fatal("intersection must expose only bound values")
			}
		})
	}
	for _, source := range []string{"parent", "bound"} {
		t.Run("expired-"+source, func(t *testing.T) {
			expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
			defer cancel()
			live, cancelLive := context.WithCancel(context.Background())
			defer cancelLive()
			parent, bound := expired, live
			if source == "bound" {
				parent, bound = live, expired
			}
			merged, release := intersectChatRuntimeContext(parent, bound)
			defer release()
			if merged.Err() != context.DeadlineExceeded {
				t.Fatalf("expired deadline: %v", merged.Err())
			}
		})
	}
}

func TestChatRuntimeContextNotification(t *testing.T) {
	for _, source := range []string{"parent", "bound"} {
		t.Run(source, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			bound, cancelBound := context.WithCancel(context.Background())
			defer cancelBound()
			merged, release := intersectChatRuntimeContext(parent, bound)
			defer release()
			done := merged.Done() // subscribe before cancellation; no subsequent Err/Done poll
			if source == "parent" {
				cancelParent()
			} else {
				cancelBound()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("asynchronous cancellation notification lost")
			}
			if merged.Err() != context.Canceled {
				t.Fatal("notification did not latch cancellation")
			}
		})
	}
}
