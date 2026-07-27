package daemon

import (
	"context"
	"fmt"
	"sync"

	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/store"
)

// channelPlatformResolver implements channels.PlatformResolver using the
// platform store to look up user-channel links and inject channel-scoped context.
type channelPlatformResolver struct {
	backend store.PlatformBackend

	gatewayConfig *openshell.GRPCClientConfig

	// memorySaveOrMerge is the cross-session memory merge function.
	// Injected at construction time from the ChatAgent's PlatformReflector.
	memorySaveOrMerge store.MemorySaveOrMergeFunc

	// Persistent per-user routing preferences set via /org and /team commands.
	// Key: "channelType:externalID" (e.g., "telegram:12345").
	prefsMu sync.RWMutex
	prefs   map[string]*channels.RoutingPref
}

// Ensure channelPlatformResolver implements channels.PlatformResolver.
var _ channels.PlatformResolver = (*channelPlatformResolver)(nil)

// SetRoutingPref stores a persistent routing preference for a channel identity.
func (r *channelPlatformResolver) SetRoutingPref(channelType, externalID, orgSlug, teamSlug string) {
	key := channelType + ":" + externalID
	r.prefsMu.Lock()
	defer r.prefsMu.Unlock()
	if r.prefs == nil {
		r.prefs = make(map[string]*channels.RoutingPref)
	}
	r.prefs[key] = &channels.RoutingPref{OrgSlug: orgSlug, TeamSlug: teamSlug}
}

// GetRoutingPref returns the current routing preference for a channel identity.
func (r *channelPlatformResolver) GetRoutingPref(channelType, externalID string) *channels.RoutingPref {
	key := channelType + ":" + externalID
	r.prefsMu.RLock()
	defer r.prefsMu.RUnlock()
	return r.prefs[key]
}

// ResolveChannelUser looks up the external channel identity in user_channels,
// determines the org/team to use, and returns a context enriched with channel-scoped stores.
func (r *channelPlatformResolver) ResolveChannelUser(
	ctx context.Context,
	channelType, externalID string,
) (context.Context, string, string, error) {
	return r.ResolveChannelUserWithHint(ctx, channelType, externalID, nil)
}

// ResolveChannelUserWithHint is like ResolveChannelUser but accepts an optional
// routing hint (from email plus-addressing or similar per-message override).
//
// Routing priority:
//  1. Per-message RoutingHint (from email +addressing) — passed via msg.RoutingHint
//  2. Persistent user preference (from /org, /team commands)
//  3. First org + first team (fallback default)
func (r *channelPlatformResolver) ResolveChannelUserWithHint(
	ctx context.Context,
	channelType, externalID string,
	hint *channels.RoutingHint,
) (context.Context, string, string, error) {
	// Look up the user-channel link
	link, err := r.backend.UserChannels().GetByExternalID(ctx, channelType, externalID)
	if err != nil {
		return ctx, "", "", fmt.Errorf("lookup failed: %w", err)
	}
	if link == nil {
		return ctx, "", "", fmt.Errorf("no linked user for %s/%s", channelType, externalID)
	}
	if !link.Enabled || !link.Verified {
		return ctx, "", "", fmt.Errorf("channel link for %s/%s is not active", channelType, externalID)
	}

	// Get the user info for display name
	user, err := r.backend.Users().GetByID(ctx, link.UserID)
	if err != nil || user == nil {
		return ctx, "", "", fmt.Errorf("user %s not found", link.UserID)
	}

	// Determine org/team routing (priority: hint > pref > default)
	orgSlug, teamSlug, routeErr := r.resolveRouting(ctx, link.UserID, channelType, externalID, hint)
	if routeErr != nil {
		return ctx, link.UserID, user.DisplayName, routeErr
	}

	// Get the org data store
	orgStore, err := r.backend.ForOrg(orgSlug)
	if err != nil {
		return ctx, link.UserID, user.DisplayName, fmt.Errorf("failed to resolve org %s: %w", orgSlug, err)
	}

	// Get the team data store
	teamStore := orgStore.ForTeam(teamSlug)

	var personalStore store.PersonalDataStore
	if link.UserID != "" {
		personalStore = orgStore.ForUser(link.UserID)
	}

	var personalCredentials store.CredentialStore
	if personalStore != nil {
		personalCredentials = personalStore.Credentials()
	}

	// Inject channel-scoped stores into the context. Credentials are personal-first
	// with team fallback, matching Studio chat for linked channel users.
	enrichedCtx := ctx
	enrichedCtx = store.WithCredentialStore(enrichedCtx, store.NewMergedCredentialStore(personalCredentials, teamStore.Credentials()))
	enrichedCtx = store.WithFlowStore(enrichedCtx, teamStore.Flows())
	enrichedCtx = store.WithSkillStores(enrichedCtx, &store.SkillStores{
		Platform: r.backend.PlatformSkills(),
		Org:      orgStore.OrgSkills(),
		Team:     teamStore.Skills(),
	})
	enrichedCtx = store.WithMCPServerStores(enrichedCtx, &store.MCPServerStores{
		Platform: r.backend.PlatformMCPServers(),
		Org:      orgStore.OrgMCPServers(),
		Team:     teamStore.MCPServers(),
	})
	enrichedCtx = store.WithMemoryStore(enrichedCtx, teamStore.Memories())
	enrichedCtx = store.WithFleetTemplateStore(enrichedCtx, teamStore.FleetTemplates())
	enrichedCtx = store.WithFleetPlanStore(enrichedCtx, teamStore.FleetPlans())
	enrichedCtx = store.WithSessionService(enrichedCtx, teamStore.Sessions())
	enrichedCtx = store.WithTeamDataStore(enrichedCtx, teamStore)
	enrichedCtx = r.withNetworkPolicyStores(enrichedCtx, orgStore, teamStore)
	if r.gatewayConfig != nil {
		enrichedCtx = netpolicy.WithGatewayConfig(enrichedCtx, r.gatewayConfig)
	}

	// Inject cross-session memory merge function for channel interactions.
	if r.memorySaveOrMerge != nil {
		enrichedCtx = store.WithMemorySaveOrMerge(enrichedCtx, r.memorySaveOrMerge)
	}

	// Inject provider settings stores for per-message provider resolution.
	// This enables the channel manager to resolve the effective LLM provider
	// using the same 3-tier cascade (Platform → Org → Team) as the Studio chat.
	enrichedCtx = store.WithProviderStores(enrichedCtx, &store.ProviderStores{
		Platform: r.backend.PlatformSettings(),
		Org:      r.backend.OrgSettings(orgSlug),
		Team:     teamStore.Settings(),
	})

	return enrichedCtx, link.UserID, user.DisplayName, nil
}

func (r *channelPlatformResolver) withNetworkPolicyStores(
	ctx context.Context,
	orgStore store.OrgDataStore,
	teamStore store.TeamDataStore,
) context.Context {
	nps := &store.NetworkPolicyStores{
		Org:  orgStore.OrgNetworkPolicies(),
		Team: teamStore.NetworkPolicies(),
	}
	if p, ok := r.backend.(platformNetworkPolicyProvider); ok {
		nps.Platform = p.PlatformNetworkPolicies()
	}
	return store.WithNetworkPolicyStores(ctx, nps)
}

// resolveRouting determines the org/team to use for this message.
// Priority: per-message hint > persistent user preference > first-org/first-team.
// Always validates that the user is a member of the resolved org/team.
func (r *channelPlatformResolver) resolveRouting(
	ctx context.Context,
	userID, channelType, externalID string,
	hint *channels.RoutingHint,
) (string, string, error) {
	// 1. Per-message routing hint (highest priority)
	if hint != nil && hint.OrgSlug != "" {
		return r.validateRouting(ctx, userID, hint.OrgSlug, hint.TeamSlug)
	}

	// 2. Persistent user preference
	if pref := r.GetRoutingPref(channelType, externalID); pref != nil && pref.OrgSlug != "" {
		return r.validateRouting(ctx, userID, pref.OrgSlug, pref.TeamSlug)
	}

	// 3. Default: first org membership, first team
	return r.defaultRouting(ctx, userID)
}

// validateRouting checks that the user is a member of the requested org/team.
// If teamSlug is empty, defaults to the user's first team in that org.
func (r *channelPlatformResolver) validateRouting(
	ctx context.Context,
	userID, orgSlug, teamSlug string,
) (string, string, error) {
	// Verify org membership
	orgs, err := r.backend.Organizations().GetUserOrgs(ctx, userID)
	if err != nil {
		return "", "", fmt.Errorf("failed to check org membership: %w", err)
	}
	var found bool
	for _, m := range orgs {
		if m.OrgSlug == orgSlug {
			found = true
			break
		}
	}
	if !found {
		return "", "", fmt.Errorf("you are not a member of org '%s'", orgSlug)
	}

	// Get teams the user belongs to in this org
	orgDS, err := r.backend.ForOrg(orgSlug)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve org %s: %w", orgSlug, err)
	}
	teams, err := orgDS.Teams().ListTeamsForUser(ctx, userID)
	if err != nil || len(teams) == 0 {
		return "", "", fmt.Errorf("you have no teams in org '%s'", orgSlug)
	}

	// If team not specified, use the first team
	if teamSlug == "" {
		return orgSlug, teams[0].Slug, nil
	}

	// Verify team membership
	for _, t := range teams {
		if t.Slug == teamSlug {
			return orgSlug, teamSlug, nil
		}
	}
	return "", "", fmt.Errorf("you are not a member of team '%s' in org '%s'", teamSlug, orgSlug)
}

// defaultRouting returns the user's first org and first team.
func (r *channelPlatformResolver) defaultRouting(ctx context.Context, userID string) (string, string, error) {
	orgs, err := r.backend.Organizations().GetUserOrgs(ctx, userID)
	if err != nil || len(orgs) == 0 {
		return "", "", fmt.Errorf("user has no org memberships")
	}
	orgSlug := orgs[0].OrgSlug

	orgDS, err := r.backend.ForOrg(orgSlug)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve org %s: %w", orgSlug, err)
	}
	teams, err := orgDS.Teams().ListTeamsForUser(ctx, userID)
	if err != nil || len(teams) == 0 {
		return "", "", fmt.Errorf("user has no teams in org '%s'", orgSlug)
	}

	return orgSlug, teams[0].Slug, nil
}

// ListUserRoutes returns all available org/team combinations for the user
// identified by a channel identity. Used by /context to show routing options.
func (r *channelPlatformResolver) ListUserRoutes(ctx context.Context, channelType, externalID string) ([]channels.RouteOption, error) {
	// Look up the user-channel link
	link, err := r.backend.UserChannels().GetByExternalID(ctx, channelType, externalID)
	if err != nil {
		return nil, fmt.Errorf("lookup failed: %w", err)
	}
	if link == nil {
		return nil, fmt.Errorf("no linked user for %s/%s", channelType, externalID)
	}

	// Get all orgs the user belongs to
	orgs, err := r.backend.Organizations().GetUserOrgs(ctx, link.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to list orgs: %w", err)
	}

	var routes []channels.RouteOption
	for _, org := range orgs {
		orgDS, err := r.backend.ForOrg(org.OrgSlug)
		if err != nil {
			continue
		}
		teams, err := orgDS.Teams().ListTeamsForUser(ctx, link.UserID)
		if err != nil {
			continue
		}
		for _, t := range teams {
			routes = append(routes, channels.RouteOption{
				OrgSlug:  org.OrgSlug,
				OrgName:  org.OrgName,
				TeamSlug: t.Slug,
				TeamName: t.Name,
			})
		}
	}

	return routes, nil
}
