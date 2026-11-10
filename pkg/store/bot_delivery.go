package store

import (
	"context"
	"fmt"
	"net/mail"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

// BotDeliveryAuthorizer revalidates the persisted approved target and live channel
// authority. Implementations are trusted composition, never request body fields.
type BotDeliveryAuthorizer interface {
	AuthorizeBotDelivery(context.Context, BotID, BotChannelBinding) error
}

const BotDeliveryChannelEmail = "email"

// botDeliveryOwner checks live identity and membership before reading private
// account information. The principal must be captured by trusted composition.
func botDeliveryOwner(ctx context.Context, backend PlatformBackend, owner uuid.UUID) (*User, error) {
	deny := func() (*User, error) {
		return nil, fmt.Errorf("%w: notification destination lacks scoped channel authority", ErrBotForbidden)
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || owner == uuid.Nil || p.Subject != owner.String() || p.OrgSlug == "" || p.TeamSlug == "" || backend == nil {
		return deny()
	}
	user, err := backend.Users().GetByID(ctx, p.Subject)
	if err != nil {
		return nil, err
	}
	if user == nil || user.ID != p.Subject || user.Status != "active" {
		return deny()
	}
	org, err := backend.Organizations().GetBySlug(ctx, p.OrgSlug)
	if err != nil {
		return nil, err
	}
	if org == nil || org.Status != "active" {
		return deny()
	}
	role, err := backend.Organizations().GetMemberRole(ctx, p.Subject, org.ID)
	if err != nil {
		return nil, err
	}
	if role != "member" && role != "admin" && role != "owner" {
		return deny()
	}
	data, err := backend.ForOrg(p.OrgSlug)
	if err != nil {
		return nil, err
	}
	if data == nil || data.Teams() == nil {
		return deny()
	}
	member, err := data.Teams().IsTeamMember(ctx, p.Subject, p.TeamSlug)
	if err != nil {
		return nil, err
	}
	if !member {
		return deny()
	}
	return user, nil
}

// CanonicalBotOwnerEmail accepts one bare mailbox, never display names, lists,
// threading selectors or injected headers. It matches channel-link normalization.
func CanonicalBotOwnerEmail(raw string) string {
	if strings.ContainsAny(raw, "\r\n\x00") {
		return ""
	}
	address := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" || parsed.Address != address {
		return ""
	}
	return address
}

// CheckBotDeliveryIdentity accepts a verified private Telegram link or a verified
// email link matching the owner's CURRENT account address. Account email alone
// is not proof. Slack links do not prove installation/room authority.
func CheckBotDeliveryIdentity(ctx context.Context, backend PlatformBackend, owner uuid.UUID, target BotChannelBinding) error {
	deny := func() error {
		return fmt.Errorf("%w: notification destination lacks scoped channel authority", ErrBotForbidden)
	}
	user, err := botDeliveryOwner(ctx, backend, owner)
	if err != nil {
		return err
	}
	if target.ID == uuid.Nil || backend.UserChannels() == nil {
		return deny()
	}
	linked, err := backend.UserChannels().GetByID(ctx, target.ID.String())
	if err != nil {
		return err
	}
	if linked == nil || linked.ID != target.ID.String() || linked.UserID != owner.String() || !linked.Enabled || !linked.Verified || linked.ChannelType != target.Channel || target.ThreadID != "" || target.ChatID != linked.ExternalID {
		return deny()
	}
	switch target.Channel {
	case BotDeliveryChannelEmail:
		address := CanonicalBotOwnerEmail(user.Email)
		if address == "" || address != linked.ExternalID {
			return deny()
		}
	case "telegram":
		id, err := strconv.ParseInt(linked.ExternalID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != linked.ExternalID {
			return deny()
		}
	default:
		return deny()
	}
	return nil
}

type BotOwnerEmailState string

const (
	BotOwnerEmailUnavailable          BotOwnerEmailState = "unavailable"
	BotOwnerEmailMissing              BotOwnerEmailState = "account_email_missing"
	BotOwnerEmailNeedsVerification    BotOwnerEmailState = "verification_required"
	BotOwnerEmailTransportUnavailable BotOwnerEmailState = "transport_unavailable"
	BotOwnerEmailReady                BotOwnerEmailState = "ready_for_review"
)

// BotOwnerEmailDelivery is discovery, not a grant. It contains no platform
// credentials. Binding is only offered after proof and transport readiness.
type BotOwnerEmailDelivery struct {
	State        BotOwnerEmailState `json:"state"`
	AccountEmail string             `json:"account_email,omitempty"`
	Binding      *BotChannelBinding `json:"binding,omitempty"`
	NextStep     string             `json:"next_step"`
}

func ResolveBotOwnerEmailDelivery(ctx context.Context, backend PlatformBackend, owner uuid.UUID, transportReady bool) (BotOwnerEmailDelivery, error) {
	user, err := botDeliveryOwner(ctx, backend, owner)
	if err != nil {
		return BotOwnerEmailDelivery{}, err
	}
	result := BotOwnerEmailDelivery{State: BotOwnerEmailMissing, NextStep: "Set an account email in your profile, then verify it in Connected Channels."}
	result.AccountEmail = CanonicalBotOwnerEmail(user.Email)
	if result.AccountEmail == "" {
		return result, nil
	}
	result.State = BotOwnerEmailNeedsVerification
	result.NextStep = "Verify your account email in Settings > Connected Channels using the email link code; the address is already known. Do not ask for it again."
	if backend.UserChannels() == nil {
		return result, nil
	}
	links, err := backend.UserChannels().ListByUser(ctx, owner.String())
	if err != nil {
		return BotOwnerEmailDelivery{}, err
	}
	for _, link := range links {
		if link == nil || link.UserID != owner.String() || link.ChannelType != BotDeliveryChannelEmail || link.ExternalID != result.AccountEmail || !link.Verified || !link.Enabled {
			continue
		}
		id, err := uuid.Parse(link.ID)
		if err != nil || id == uuid.Nil {
			continue
		}
		binding := BotChannelBinding{ID: id, Channel: BotDeliveryChannelEmail, ChatID: link.ExternalID}
		if err := CheckBotDeliveryIdentity(ctx, backend, owner, binding); err != nil {
			return BotOwnerEmailDelivery{}, err
		}
		if !transportReady {
			result.State = BotOwnerEmailTransportUnavailable
			result.NextStep = "Ask a platform administrator to configure and enable the email channel transport. Do not request mailbox credentials or another address."
			return result, nil
		}
		result.State = BotOwnerEmailReady
		result.Binding = &binding
		result.NextStep = "Use this exact binding only in a separately reviewed Bot configuration for owner email delivery. Discovery is not approval and does not send mail. Do not ask for the known address or use mailbox tools."
		return result, nil
	}
	return result, nil
}
