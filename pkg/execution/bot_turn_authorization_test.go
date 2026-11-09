package execution

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotTurnAuthorizationRegistry(t *testing.T) {
	p := Principal{Kind: PrincipalKindUser, Authentication: AuthMethodPlatformJWT, Surface: SurfaceStudio, Subject: "user", OrgSlug: "org", TeamSlug: "team", Authenticated: true, Scopes: []string{"tool:execute"}}
	ctx, _ := WithPrincipal(context.Background(), p)
	ctx, err := WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewBotTurnAuthorizationRegistry(1)
	defer registry.Close()
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	auth, err := registry.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	auth.Principal.Scopes[0] = "forged"
	got, err := registry.Resolve(id)
	if err != nil || got.Principal.Scopes[0] != "tool:execute" {
		t.Fatalf("copy: %+v %v", got, err)
	}
	other := id
	other.RunID = uuid.New()
	if _, err = registry.Register(ctx, other); err == nil {
		t.Fatal("unbounded registry")
	}
	registry.Cancel(id)
	if _, err = registry.Resolve(id); err == nil {
		t.Fatal("cancelled authority")
	}
	registry.Close()
	if _, err = registry.Register(ctx, id); err == nil {
		t.Fatal("closed registry accepted authority")
	}
}

func TestBotTurnAuthorizationExpiryAndIdentity(t *testing.T) {
	p := Principal{Kind: PrincipalKindUser, Authentication: AuthMethodOAuth, Surface: SurfaceStudio, Subject: "u", OrgSlug: "o", TeamSlug: "t", Authenticated: true, Scopes: []string{"chat"}}
	ctx, _ := WithPrincipal(context.Background(), p)
	ctx, err := WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodOAuth, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	r := NewBotTurnAuthorizationRegistry(1)
	defer r.Close()
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "u", OrgSlug: "o", TeamSlug: "t"}
	a, err := r.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	ceiling := a.CallerCeiling()
	ceiling.Principal.Scopes[0] = "tool:execute"
	got, err := r.Resolve(id)
	if err != nil || got.Principal.Scopes[0] != "chat" {
		t.Fatal("ceiling aliases live authority")
	}
	wrong := id
	wrong.Version++
	if _, err := r.Resolve(wrong); err == nil {
		t.Fatal("wrong version accepted")
	}
	a.Evidence.ExpiresAt = time.Now().Add(-time.Second)
	r.entries[id] = a
	if _, err := r.Resolve(id); err == nil {
		t.Fatal("expired authority accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Register(cancelled, id); err == nil {
		t.Fatal("cancelled context accepted")
	}
	if _, err := r.Register(ctx, id); err != nil {
		t.Fatal("expired entry retained capacity")
	}
	fresh := NewBotTurnAuthorizationRegistry(1)
	defer fresh.Close()
	if _, err := fresh.Resolve(id); err == nil {
		t.Fatal("authority survived restart")
	}
}

func TestBotTurnAuthorizationEvidenceRequired(t *testing.T) {
	p := Principal{Kind: PrincipalKindUser, Authentication: AuthMethodPlatformJWT, Surface: SurfaceStudio, Subject: "u", OrgSlug: "o", TeamSlug: "t", Authenticated: true}
	ctx, _ := WithPrincipal(context.Background(), p)
	r := NewBotTurnAuthorizationRegistry(2)
	defer r.Close()
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "u", OrgSlug: "o", TeamSlug: "t"}
	if _, err := r.Register(ctx, id); err == nil {
		t.Fatal("principal alone minted authority")
	}
	if _, err := WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodPlatformJWT}); err == nil {
		t.Fatal("token without expiry")
	}
}
