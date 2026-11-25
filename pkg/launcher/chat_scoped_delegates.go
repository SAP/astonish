package launcher

import (
	"context"
	"sync"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/sandbox"
)

// scopedChatDelegates owns only aliases for fresh child sessions announced by
// this request's delegate manager. The pool and its parent/preexisting sessions
// are borrowed. RunTasks joins its children before returning, so joining the
// delegate tool calls here also joins their models, tools and retry evaluation.
type scopedChatDelegates struct {
	mu      sync.Mutex
	closed  bool
	active  map[*context.CancelFunc]struct{}
	aliases map[string]func()
	wg      sync.WaitGroup
	pool    sandbox.ToolNodePool
	browser *browser.Manager
}

func (d *scopedChatDelegates) begin(ctx context.Context) (context.Context, func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, func() {}, context.Canceled
	}
	ctx, cancel := context.WithCancel(ctx)
	if d.active == nil {
		d.active = make(map[*context.CancelFunc]struct{})
	}
	d.active[&cancel] = struct{}{}
	d.wg.Add(1)
	return ctx, func() {
		cancel()
		d.mu.Lock()
		delete(d.active, &cancel)
		d.mu.Unlock()
		d.wg.Done()
	}, nil
}

func (d *scopedChatDelegates) alias(parent, child string, previous func(string, string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || child == "" || parent == "" || child == parent {
		return
	}
	if _, exists := d.aliases[child]; exists {
		return
	}
	// OnChildSession runs only after the session service has created a fresh
	// child ID. Do not enumerate or adopt any other aliases in the shared pool.
	owner := sandbox.CanonicalSessionID(d.pool.GetOrCreate(parent), parent)
	var release func()
	if leaser, ok := d.pool.(sandbox.ChildAliasLeaser); ok {
		release = leaser.LeaseChildAlias(child, parent)
	} else {
		// Legacy/custom pools retain their existing alias behavior. Never call
		// Remove: it is not an alias-only lifecycle operation.
		d.pool.Alias(child, parent)
	}
	if previous != nil {
		previous(parent, child)
	}
	d.browser.AliasSession(child, owner)
	if d.aliases == nil {
		d.aliases = make(map[string]func())
	}
	d.aliases[child] = release
}

// close is called by the runtime's once-only cleanup, before browser/flow and
// resource teardown. Admission and WaitGroup.Add are fenced under the same lock.
func (d *scopedChatDelegates) close() {
	d.mu.Lock()
	d.closed = true
	for cancel := range d.active {
		(*cancel)()
	}
	d.mu.Unlock()
	d.wg.Wait()
	for _, release := range d.aliases {
		if release != nil {
			release()
		}
	}
	d.aliases = nil
}
