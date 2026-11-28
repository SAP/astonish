package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

func TestResolveBotOwnerEmailDelivery(t *testing.T) {
	for _, tc := range []string{"ready", "unverified", "disabled", "foreign", "changed", "no-transport", "missing", "wrong-owner"} {
		t.Run(tc, func(t *testing.T) {
			owner := uuid.New()
			b := &ownerEmailBackend{user: &User{ID: owner.String(), Email: "Owner@Example.Test", Status: "active"}, link: &UserChannel{ID: uuid.NewString(), UserID: owner.String(), ChannelType: BotDeliveryChannelEmail, ExternalID: "owner@example.test", Verified: true, Enabled: true}}
			ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			want := BotOwnerEmailNeedsVerification
			ready := true
			switch tc {
			case "ready":
				want = BotOwnerEmailReady
			case "unverified":
				b.link.Verified = false
			case "disabled":
				b.link.Enabled = false
			case "foreign":
				b.link.UserID = uuid.NewString()
			case "changed":
				b.user.Email = "new@example.test"
			case "no-transport":
				ready = false
				want = BotOwnerEmailTransportUnavailable
			case "missing":
				b.user.Email = ""
				want = BotOwnerEmailMissing
			case "wrong-owner":
				owner = uuid.New()
			}
			got, err := ResolveBotOwnerEmailDelivery(ctx, b, owner, ready)
			if tc == "wrong-owner" {
				if !errors.Is(err, ErrBotForbidden) || got.AccountEmail != "" {
					t.Fatalf("foreign discovery: %+v %v", got, err)
				}
				return
			}
			if err != nil || got.State != want || got.NextStep == "" {
				t.Fatalf("got %+v %v", got, err)
			}
			if tc == "ready" {
				if got.Binding == nil || got.Binding.ID.String() != b.link.ID || got.Binding.ChatID != "owner@example.test" {
					t.Fatal(got)
				}
			} else if got.Binding != nil {
				t.Fatal("unavailable target offered")
			}
			if tc != "missing" && got.AccountEmail == "" {
				t.Fatal("known account omitted")
			}
			if tc == "unverified" && !strings.Contains(got.NextStep, "link code") {
				t.Fatal("no verification path")
			}
		})
	}
}
