package chatruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// ErrLiveAuthorityDenied means the captured runtime identity no longer has
// access to its resources, or its authoritative directory is unavailable.
var ErrLiveAuthorityDenied = errors.New("chat runtime live authority denied")

type membershipRole string

const (
	membershipMember   membershipRole = "member"
	membershipAdmin    membershipRole = "admin"
	membershipOwner    membershipRole = "owner"
	platformSuperadmin                = "superadmin"
)

func (r membershipRole) rank() int {
	switch r {
	case membershipMember:
		return 1
	case membershipAdmin:
		return 2
	case membershipOwner:
		return 3
	default:
		return 0
	}
}

type authorityCeiling struct {
	org        membershipRole
	team       membershipRole
	superadmin bool
}

// liveAuthority is private to Inputs: team tools must not gain a tenant router
// merely because their binding needs fresh membership checks. Its immutable
// directory baseline never promotes or rewrites the captured principal. A loss
// of any observed relevant privilege invalidates use until it is restored.
// Principal.Roles is intentionally not parsed: it is a flattened transport
// ceiling, not an authoritative mapping of platform/org/team memberships.
type liveAuthority struct {
	platform store.PlatformStore
	router   store.TenantRouter
	team     bool
	baseline authorityCeiling
}

func newLiveAuthority(ctx context.Context, s *store.Services, scope ResourceScope, p execution.Principal) (*liveAuthority, error) {
	// Local Code and explicitly non-platform test compositions have no directory.
	// FallbackAllowed is not an exemption for a platform deployment.
	if s == nil || s.Mode != store.ModePlatform {
		return nil, nil
	}
	if s.Platform == nil || s.TenantRouter == nil {
		return nil, fmt.Errorf("%w: platform and tenant router required", ErrLiveAuthorityDenied)
	}
	a := &liveAuthority{platform: s.Platform, router: s.TenantRouter, team: scope == ResourceScopeTeam || hasTeamResources(s)}
	baseline, err := a.observe(ctx, p)
	if err != nil {
		return nil, err
	}
	a.baseline = baseline
	return a, nil
}

func hasTeamResources(s *store.Services) bool {
	// Personal Chat merges private and team capabilities. Personal-only Bot
	// compositions need not depend on a selected team's membership, but a
	// personal label alone cannot exempt a runtime that exposes team stores.
	return s.Sessions != nil || s.Bots != nil || s.BotTemplates != nil || s.BotMandates != nil ||
		s.Memory != nil || s.MemorySearcher != nil || s.Credentials != nil || s.Docs != nil ||
		s.Apps != nil || s.AppState != nil || s.AppStateSQL != nil || s.Flows != nil || s.Scheduler != nil ||
		s.FleetTemplates != nil || s.FleetPlans != nil || s.FleetSetupProfiles != nil || s.FleetSetupDrafts != nil ||
		s.FleetRunStates != nil || s.FleetMailbox != nil || s.FleetTaskBoard != nil || s.DrillReports != nil ||
		s.TeamSkills != nil || s.TeamMCPServers != nil || s.TeamA2AAgents != nil || s.TeamNetworkPolicies != nil || s.Settings != nil
}

func (a *liveAuthority) check(ctx context.Context, p execution.Principal) error {
	if a == nil {
		return nil
	}
	current, err := a.observe(ctx, p)
	if err != nil {
		return err
	}
	if current.org.rank() < a.baseline.org.rank() || current.team.rank() < a.baseline.team.rank() || (a.baseline.superadmin && !current.superadmin) {
		return fmt.Errorf("%w: captured privileges reduced", ErrLiveAuthorityDenied)
	}
	return nil
}

func (a *liveAuthority) observe(ctx context.Context, p execution.Principal) (authorityCeiling, error) {
	var observed authorityCeiling
	if err := ctx.Err(); err != nil {
		return observed, err
	}
	// A subject-less service cannot borrow a user's membership.
	if p.Kind == execution.PrincipalKindService || p.Subject == "" || p.OrgSlug == "" {
		return observed, fmt.Errorf("%w: platform user required", ErrLiveAuthorityDenied)
	}
	users, orgs := a.platform.Users(), a.platform.Organizations()
	if users == nil || orgs == nil {
		return observed, fmt.Errorf("%w: identity stores unavailable", ErrLiveAuthorityDenied)
	}
	user, err := users.GetByID(ctx, p.Subject)
	if err != nil {
		return observed, fmt.Errorf("%w: user lookup: %w", ErrLiveAuthorityDenied, err)
	}
	if user == nil || user.ID != p.Subject || user.Status != "active" {
		return observed, fmt.Errorf("%w: inactive user", ErrLiveAuthorityDenied)
	}
	observed.superadmin = user.PlatformRole == platformSuperadmin
	org, err := orgs.GetBySlug(ctx, p.OrgSlug)
	if err != nil {
		return observed, fmt.Errorf("%w: organization lookup: %w", ErrLiveAuthorityDenied, err)
	}
	if org == nil || org.ID == "" || org.Slug != p.OrgSlug || org.Status != "active" {
		return observed, fmt.Errorf("%w: inactive organization", ErrLiveAuthorityDenied)
	}
	role, err := orgs.GetMemberRole(ctx, p.Subject, org.ID)
	if err != nil {
		return observed, fmt.Errorf("%w: organization membership: %w", ErrLiveAuthorityDenied, err)
	}
	observed.org = membershipRole(role)
	if observed.org.rank() == 0 {
		return observed, fmt.Errorf("%w: organization membership revoked", ErrLiveAuthorityDenied)
	}
	if !a.team {
		return observed, nil
	}
	ods, err := a.router.ForOrg(p.OrgSlug)
	if err != nil {
		return observed, fmt.Errorf("%w: tenant lookup: %w", ErrLiveAuthorityDenied, err)
	}
	if ods == nil || ods.Teams() == nil || p.TeamSlug == "" {
		return observed, fmt.Errorf("%w: team directory unavailable", ErrLiveAuthorityDenied)
	}
	teams := ods.Teams()
	team, err := teams.GetTeamBySlug(ctx, p.TeamSlug)
	if err != nil {
		return observed, fmt.Errorf("%w: team lookup: %w", ErrLiveAuthorityDenied, err)
	}
	if team == nil || team.ID == "" || team.Slug != p.TeamSlug {
		return observed, fmt.Errorf("%w: team unavailable", ErrLiveAuthorityDenied)
	}
	role, err = teams.GetMemberRole(ctx, p.Subject, team.ID)
	if err != nil {
		return observed, fmt.Errorf("%w: team membership: %w", ErrLiveAuthorityDenied, err)
	}
	observed.team = membershipRole(role)
	// Admin management bypass is not execution membership.
	if observed.team != membershipAdmin && observed.team != membershipMember {
		return observed, fmt.Errorf("%w: team membership revoked", ErrLiveAuthorityDenied)
	}
	return observed, nil
}
