package agent

// SetAllToolsForSession changes the broad tool grant without touching folder,
// one-shot, or plan-scoped consent.
func (p *SessionAuthPolicy) SetAllToolsForSession(allow bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowAllToolsSession = allow
}
