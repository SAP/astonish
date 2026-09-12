package execution

import (
	"context"
	"testing"
)

func TestServiceAuthorizeAttachesPrincipal(t *testing.T) {
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
	ctx, err := NewService(nil).Authorize(context.Background(), principal, CapabilityChat)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	got, ok := PrincipalFromContext(ctx)
	if !ok || got.Subject != "user-1" {
		t.Fatalf("principal from authorized context = %#v, %v", got, ok)
	}
}
