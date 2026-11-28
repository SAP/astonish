package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestBotTurnAuthorizerOriginalCeilingAndRevocation(t *testing.T) {
	p := Principal{Kind: PrincipalKindUser, Authentication: AuthMethodOAuth, Surface: SurfaceStudio, Subject: "u", OrgSlug: "o", TeamSlug: "t", Authenticated: true, Scopes: []string{"chat"}}
	ctx, _ := WithPrincipal(context.Background(), p)
	ctx, _ = WithAuthenticationEvidence(ctx, AuthenticationEvidence{Method: AuthMethodOAuth, ExpiresAt: time.Now().Add(time.Minute)})
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "u", OrgSlug: "o", TeamSlug: "t"}
	registry := NewBotTurnAuthorizationRegistry(2)
	original, err := registry.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	ceiling := original.CallerCeiling()
	// Exercise persisted JSON: monotonic clock/location must not break equality.
	raw, _ := json.Marshal(ceiling)
	_ = json.Unmarshal(raw, &ceiling)
	revoked := false
	a, err := registry.Authorizer(id, ceiling, func(_ context.Context, p Principal) (Principal, error) {
		if revoked {
			return Principal{}, errors.New("membership revoked")
		}
		p.Scopes = append(p.Scopes, "tool:execute")
		return p, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Authorize(ctx, CapabilityChat); err != nil {
		t.Fatal(err)
	}
	if err = a.AuthorizeTool(ctx, "shell_command"); err == nil {
		t.Fatal("current caller elevated original chat-only OAuth grant")
	}
	if err = a.AuthorizeTool(ctx, "*"); err == nil {
		t.Fatal("wildcard accepted")
	}
	revoked = true
	if err = a.Authorize(ctx, CapabilityChat); err == nil {
		t.Fatal("revoked member authorized")
	}
	revoked = false
	registry.Cancel(id)
	if err = a.Authorize(ctx, CapabilityChat); err == nil {
		t.Fatal("cancelled authority authorized")
	}
	if _, err = NewBotTurnAuthorizationRegistry(1).Authorizer(id, ceiling, func(_ context.Context, p Principal) (Principal, error) { return p, nil }); err == nil {
		t.Fatal("persisted ceiling restored authority")
	}
}
