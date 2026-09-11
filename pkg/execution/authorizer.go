package execution

import "fmt"

// Capability is a typed execution permission. OAuth scope strings are mapped to
// these values by protocol adapters rather than being consumed by tools directly.
type Capability string

const (
	CapabilityChat        Capability = "chat"
	CapabilityMemoryRead  Capability = "memory:read"
	CapabilityMemoryWrite Capability = "memory:write"
	CapabilityIntegration Capability = "integration:use"
	CapabilityToolExecute Capability = "tool:execute"
)

// Authorizer makes a capability decision for a principal at the execution
// boundary. Implementations may incorporate live membership and policy checks.
type Authorizer interface {
	Authorize(Principal, Capability) error
}

// CapabilityAuthorizer is the baseline fail-closed authorizer. It is deliberately
// conservative for service identities: they can never access personal memory.
type CapabilityAuthorizer struct{}

// Authorize checks authentication, tenant completeness, identity constraints,
// and scope grants. Legacy platform and linked-channel users use their active
// membership-derived role until OAuth scopes replace the legacy JWT model.
func (CapabilityAuthorizer) Authorize(principal Principal, capability Capability) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if principal.Kind == PrincipalKindService && (capability == CapabilityMemoryRead || capability == CapabilityMemoryWrite) {
		return fmt.Errorf("service principals cannot access personal memory")
	}
	if principal.Authentication == AuthMethodOAuth && !principal.HasScope(string(capability)) {
		return fmt.Errorf("principal lacks required scope %q", capability)
	}
	return nil
}
