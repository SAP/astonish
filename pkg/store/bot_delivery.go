package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

// BotDeliveryAuthorizer revalidates the persisted approved target and live channel
// authority. Implementations are trusted composition, never request body fields.
type BotDeliveryAuthorizer interface {
	AuthorizeBotDelivery(context.Context, BotID, BotChannelBinding) error
}

// CheckBotDeliveryIdentity resolves binding.ID from the server's linked identity
// store. A reviewed destination string (or an enabled transport) is not authority.
// Only private Telegram destinations are currently provable. Slack links prove a
// member/workspace identity, NOT installation ownership or access to a room; until
// that proof is persisted, Bot Slack sends must not reach global credentials.
func CheckBotDeliveryIdentity(ctx context.Context, backend PlatformBackend, owner uuid.UUID, target BotChannelBinding) error {
	deny := func() error {
		return fmt.Errorf("%w: notification destination lacks scoped channel authority", ErrBotForbidden)
	}
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || p.Subject != owner.String() || p.OrgSlug == "" || p.TeamSlug == "" || backend == nil || target.ID == uuid.Nil {
		return deny()
	}
	user, err := backend.Users().GetByID(ctx, p.Subject)
	if err != nil {
		return err
	}
	if user == nil || user.Status != "active" {
		return deny()
	}
	org, err := backend.Organizations().GetBySlug(ctx, p.OrgSlug)
	if err != nil {
		return err
	}
	if org == nil || org.Status != "active" {
		return deny()
	}
	role, err := backend.Organizations().GetMemberRole(ctx, p.Subject, org.ID)
	if err != nil {
		return err
	}
	if role != "member" && role != "admin" && role != "owner" {
		return deny()
	}
	data, err := backend.ForOrg(p.OrgSlug)
	if err != nil {
		return err
	}
	member, err := data.Teams().IsTeamMember(ctx, p.Subject, p.TeamSlug)
	if err != nil {
		return err
	}
	if !member {
		return deny()
	}
	links := backend.UserChannels()
	if links == nil {
		return deny()
	}
	linked, err := links.GetByID(ctx, target.ID.String())
	if err != nil {
		return err
	}
	if linked == nil || linked.ID != target.ID.String() || linked.UserID != p.Subject || !linked.Enabled || !linked.Verified || linked.ChannelType != target.Channel {
		return deny()
	}
	// Positive, canonical Telegram user IDs designate private chats. Negative group
	// IDs, arbitrary threads, email addresses and Slack rooms need separate proof.
	if target.Channel != "telegram" || target.ThreadID != "" || target.ChatID != linked.ExternalID {
		return deny()
	}
	id, err := strconv.ParseInt(linked.ExternalID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != linked.ExternalID {
		return deny()
	}
	return nil
}
