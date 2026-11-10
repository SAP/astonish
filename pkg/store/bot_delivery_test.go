package store

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

type ownerEmailBackend struct {
	PlatformBackend
	user *User
	link *UserChannel
}
type ownerEmailUsers struct {
	UserStore
	b *ownerEmailBackend
}

func (s ownerEmailUsers) GetByID(context.Context, string) (*User, error) { return s.b.user, nil }

type ownerEmailOrgs struct{ OrganizationStore }

func (ownerEmailOrgs) GetBySlug(context.Context, string) (*Organization, error) {
	return &Organization{ID: "org", Status: "active"}, nil
}
func (ownerEmailOrgs) GetMemberRole(context.Context, string, string) (string, error) {
	return "member", nil
}

type ownerEmailOrg struct{ OrgDataStore }
type ownerEmailTeams struct{ TeamManagementStore }

func (ownerEmailOrg) Teams() TeamManagementStore                                   { return ownerEmailTeams{} }
func (ownerEmailTeams) IsTeamMember(context.Context, string, string) (bool, error) { return true, nil }

type ownerEmailLinks struct {
	UserChannelStore
	b *ownerEmailBackend
}

func (s ownerEmailLinks) GetByID(context.Context, string) (*UserChannel, error) { return s.b.link, nil }
func (s ownerEmailLinks) ListByUser(context.Context, string) ([]*UserChannel, error) {
	return []*UserChannel{s.b.link}, nil
}
func (b *ownerEmailBackend) Users() UserStore                    { return ownerEmailUsers{b: b} }
func (b *ownerEmailBackend) Organizations() OrganizationStore    { return ownerEmailOrgs{} }
func (b *ownerEmailBackend) ForOrg(string) (OrgDataStore, error) { return ownerEmailOrg{}, nil }
func (b *ownerEmailBackend) UserChannels() UserChannelStore      { return ownerEmailLinks{b: b} }

func TestBotOwnerEmailIdentity(t *testing.T) {
	for _, tc := range []string{"verified", "unverified", "disabled", "foreign-link", "changed-email", "wrong-owner", "thread", "recipient", "header", "display-name", "inactive", "wrong-user-result"} {
		t.Run(tc, func(t *testing.T) {
			owner := uuid.New()
			b := &ownerEmailBackend{user: &User{ID: owner.String(), Email: "owner@example.test", Status: "active"}, link: &UserChannel{ID: uuid.NewString(), UserID: owner.String(), ChannelType: "email", ExternalID: "owner@example.test", Verified: true, Enabled: true}}
			target := BotChannelBinding{ID: uuid.MustParse(b.link.ID), Channel: "email", ChatID: b.link.ExternalID}
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			switch tc {
			case "unverified":
				b.link.Verified = false
			case "disabled":
				b.link.Enabled = false
			case "foreign-link":
				b.link.UserID = uuid.NewString()
			case "changed-email":
				b.user.Email = "new@example.test"
			case "wrong-owner":
				owner = uuid.New()
			case "thread":
				target.ThreadID = "thread"
			case "recipient":
				target.ChatID = "other@example.test"
			case "header":
				b.user.Email = "owner@example.test\r\nBcc: other@example.test"
				b.link.ExternalID = b.user.Email
				target.ChatID = b.user.Email
			case "display-name":
				b.user.Email = "Owner <owner@example.test>"
				b.link.ExternalID = b.user.Email
				target.ChatID = b.user.Email
			case "inactive":
				b.user.Status = "suspended"
			case "wrong-user-result":
				b.user.ID = uuid.NewString()
			}
			err = CheckBotDeliveryIdentity(ctx, b, owner, target)
			if tc == "verified" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrBotForbidden) {
				t.Fatalf("wanted forbidden, got %v", err)
			}
		})
	}
}
