package execution

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotContinuationCannotWidenOrRenewLive(t *testing.T) {
	p := Principal{Kind: PrincipalKindUser, Authentication: AuthMethodOAuth, Surface: SurfaceStudio, Subject: "u", OrgSlug: "o", TeamSlug: "t", Authenticated: true, Scopes: []string{"chat"}}
	ctx, _ := WithPrincipal(context.Background(), p)
	ctx, _ = WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodOAuth, ExpiresAt: time.Now().Add(time.Hour)})
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "u", OrgSlug: "o", TeamSlug: "t"}
	registry := NewBotTurnAuthorizationRegistry(3)
	first, err := registry.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.RegisterContinuation(ctx, id, first.CallerCeiling()); err == nil {
		t.Fatal("renewed live authority")
	}
	registry.mu.Lock()
	expired := registry.entries[id]
	expired.Evidence.ExpiresAt = time.Now().Add(-time.Second)
	registry.entries[id] = expired
	registry.mu.Unlock()
	if _, err = registry.Resolve(id); err == nil {
		t.Fatal("expired authority resolved")
	}
	p.Scopes = append(p.Scopes, "tool:execute")
	ctx, _ = WithPrincipal(context.Background(), p)
	ctx, _ = WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodOAuth, ExpiresAt: time.Now().Add(time.Hour)})
	next, err := registry.RegisterContinuation(ctx, id, first.CallerCeiling())
	if err != nil {
		t.Fatal(err)
	}
	if (CapabilityAuthorizer{}).Authorize(next.Principal, CapabilityToolExecute) == nil {
		t.Fatal("widened original scope ceiling")
	}
	registry.Cancel(id)
	p.Authentication = AuthMethodPlatformJWT
	ctx, _ = WithPrincipal(context.Background(), p)
	ctx, _ = WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
	if _, err = registry.RegisterContinuation(ctx, id, first.CallerCeiling()); err == nil {
		t.Fatal("authentication method widened")
	}
}
