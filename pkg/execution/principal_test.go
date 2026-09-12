package execution

import (
	"context"
	"testing"
)

func TestPrincipalValidate(t *testing.T) {
	t.Parallel()

	principal := Principal{
		Kind:           PrincipalKindUser,
		Authentication: AuthMethodPlatformJWT,
		Surface:        SurfaceStudio,
		Subject:        "user-1",
		OrgSlug:        "org-1",
		TeamSlug:       "team-1",
		Authenticated:  true,
	}
	if err := principal.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	ctx, err := WithPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatalf("WithPrincipal() error = %v", err)
	}
	got, ok := PrincipalFromContext(ctx)
	if !ok || got.Subject != principal.Subject || got.TeamSlug != principal.TeamSlug {
		t.Fatalf("PrincipalFromContext() = %#v, %v", got, ok)
	}
}

func TestPrincipalValidateRejectsIncompletePlatformIdentity(t *testing.T) {
	t.Parallel()

	err := (Principal{
		Kind:           PrincipalKindUser,
		Authentication: AuthMethodPlatformJWT,
		Surface:        SurfaceStudio,
		Subject:        "user-1",
		Authenticated:  true,
	}).Validate()
	if err == nil {
		t.Fatal("Validate() succeeded for a principal without a tenant")
	}
}
