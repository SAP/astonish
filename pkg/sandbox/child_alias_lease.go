package sandbox

import "sync"

// ChildAliasLeaser adds request-owned alias teardown without changing the pool's
// lifecycle Remove contract (which may evict the owner and every sibling).
// A nil release means no fresh alias was acquired. Existing aliases are borrowed
// and must never be overwritten or released by a later request.
type ChildAliasLeaser interface {
	LeaseChildAlias(child, parent string) (release func())
}

func (p *backendPool) LeaseChildAlias(child, parent string) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || child == "" || parent == "" || child == parent || p.clients[child] != nil || p.homes[child] != nil {
		return nil
	}
	client := p.clients[parent]
	if client == nil {
		return nil
	}
	p.clients[child] = client
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.clients[child] == client {
				delete(p.clients, child)
			}
		})
	}
}

func (a *nodePoolAdapter) LeaseChildAlias(child, parent string) func() {
	p := a.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || child == "" || parent == "" || child == parent || p.clients[child] != nil {
		return nil
	}
	if _, exists := p.sessionScopes[child]; exists {
		return nil
	}
	client := p.clients[parent]
	if client == nil {
		return nil
	}
	p.clients[child] = client
	if scope, ok := p.sessionScopes[parent]; ok {
		p.sessionScopes[child] = scope
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.clients[child] == client {
				delete(p.clients, child)
				delete(p.sessionScopes, child)
			}
		})
	}
}

// A single-client fleet view creates no aliases and lends its pinned client.
func (p *singleClientPool) LeaseChildAlias(_, _ string) func() { return nil }
