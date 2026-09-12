package execution

import "testing"

func TestCapabilityAuthorizerRejectsServicePersonalMemory(t *testing.T) {
	t.Parallel()

	principal := Principal{
		Kind:           PrincipalKindService,
		Authentication: AuthMethodOAuth,
		Surface:        SurfaceMCP,
		ClientID:       "workload-1",
		OrgSlug:        "org-1",
		TeamSlug:       "team-1",
		Scopes:         []string{string(CapabilityMemoryRead)},
		Authenticated:  true,
	}
	if err := (CapabilityAuthorizer{}).Authorize(principal, CapabilityMemoryRead); err == nil {
		t.Fatal("Authorize() allowed a service principal to read personal memory")
	}
}

func TestCapabilityAuthorizerRequiresOAuthScope(t *testing.T) {
	t.Parallel()

	principal := Principal{
		Kind:           PrincipalKindUser,
		Authentication: AuthMethodOAuth,
		Surface:        SurfaceMCP,
		Subject:        "user-1",
		OrgSlug:        "org-1",
		TeamSlug:       "team-1",
		Authenticated:  true,
	}
	if err := (CapabilityAuthorizer{}).Authorize(principal, CapabilityChat); err == nil {
		t.Fatal("Authorize() allowed an OAuth principal without chat scope")
	}
}
