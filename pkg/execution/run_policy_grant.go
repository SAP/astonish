package execution

import "errors"

var errApprovalGrantPolicy = errors.New("approval grants require a parent policy")

// WithGrant returns an immutable copy with one additional exact-action grant.
// It preserves all tool ceilings; live consumption remains mandatory.
func (p *RunPolicy) WithGrant(g ScopedGrant) (*RunPolicy, error) {
	if p == nil || p.child {
		return nil, errApprovalGrantPolicy
	}
	return NewRunPolicy(RunPolicySpec{Identity: p.identity, Level: p.level, AllowedTools: p.allowed, DeniedTools: p.denied, Grants: append(append([]ScopedGrant(nil), p.grants...), g)})
}
