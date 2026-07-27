package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

// TestResolveChannelUser_InjectsAllSkillStores verifies that the channel
// resolver injects Platform, Org, and Team skill stores into the context.
// Regression test for: channels previously only injected Team skills, causing
// Telegram users to not see Organization and Platform skills.
func TestResolveChannelUser_InjectsAllSkillStores(t *testing.T) {
	esStore, userID := setupChannelResolverFixture(t, "testorg", "testteam", "telegram", "tg12345")

	// Step 5: Create the resolver and call ResolveChannelUser
	resolver := &channelPlatformResolver{backend: esStore}
	enrichedCtx, resolvedUserID, displayName, resolveErr := resolver.ResolveChannelUser(context.Background(), "telegram", "tg12345")
	if resolveErr != nil {
		t.Fatalf("ResolveChannelUser: %v", resolveErr)
	}

	if resolvedUserID != userID {
		t.Errorf("userID = %q, want %q", resolvedUserID, userID)
	}
	if displayName != "Test User" {
		t.Errorf("displayName = %q, want %q", displayName, "Test User")
	}

	// Step 6: Verify that SkillStores in context has all three tiers
	ss := store.SkillStoresFromContext(enrichedCtx)
	if ss == nil {
		t.Fatal("SkillStores not found in context")
	}
	if ss.Platform == nil {
		t.Error("SkillStores.Platform is nil — platform skills not injected")
	}
	if ss.Org == nil {
		t.Error("SkillStores.Org is nil — org skills not injected")
	}
	if ss.Team == nil {
		t.Error("SkillStores.Team is nil — team skills not injected")
	}

	// Also verify MCP server stores have all three tiers (existing behavior)
	mcpStores := store.MCPServerStoresFromContext(enrichedCtx)
	if mcpStores == nil {
		t.Fatal("MCPServerStores not found in context")
	}
	if mcpStores.Platform == nil {
		t.Error("MCPServerStores.Platform is nil")
	}
	if mcpStores.Org == nil {
		t.Error("MCPServerStores.Org is nil")
	}
	if mcpStores.Team == nil {
		t.Error("MCPServerStores.Team is nil")
	}
}

// TestResolveChannelUser_PreviousBug_OnlyTeamSkills is a negative test that
// documents the previous bug. It verifies the fix is in place by ensuring
// Platform and Org are NOT nil (they were nil before the fix).
func TestResolveChannelUser_PreviousBug_OnlyTeamSkills(t *testing.T) {
	esStore, _ := setupChannelResolverFixture(t, "org2", "team2", "telegram", "tg99999")

	// Resolve
	resolver := &channelPlatformResolver{backend: esStore}
	enrichedCtx, _, _, resolveErr := resolver.ResolveChannelUser(context.Background(), "telegram", "tg99999")
	if resolveErr != nil {
		t.Fatalf("ResolveChannelUser: %v", resolveErr)
	}

	// THE KEY ASSERTION: Before the fix, Platform and Org were nil.
	// After the fix, they must be non-nil.
	ss := store.SkillStoresFromContext(enrichedCtx)
	if ss == nil {
		t.Fatal("SkillStores not in context")
	}

	// This is the regression check — if this fails, the bug is back.
	if ss.Platform == nil {
		t.Fatal("REGRESSION: SkillStores.Platform is nil — only Team was injected (pre-fix behavior)")
	}
	if ss.Org == nil {
		t.Fatal("REGRESSION: SkillStores.Org is nil — only Team was injected (pre-fix behavior)")
	}
}

func TestResolveChannelUser_InjectsPersonalFirstCredentialsForEmail(t *testing.T) {
	esStore, userID := setupChannelResolverFixture(t, "mailorg", "mailteam", "email", "user@example.com")
	ctx := context.Background()

	orgStore, err := esStore.ForOrg("mailorg")
	if err != nil {
		t.Fatalf("ForOrg: %v", err)
	}
	teamStore := orgStore.ForTeam("mailteam")
	personalStore := orgStore.ForUser(userID)
	if personalStore == nil {
		t.Fatal("personal store is nil")
	}

	if err := teamStore.Credentials().Set(ctx, "openstack-keystone", &store.Credential{
		Type:  store.CredBearer,
		Token: "team-openstack-token",
	}); err != nil {
		t.Fatalf("set team credential: %v", err)
	}
	if err := teamStore.Credentials().Set(ctx, "sap-github-bearer", &store.Credential{
		Type:  store.CredBearer,
		Token: "team-github-token",
	}); err != nil {
		t.Fatalf("set team shadowed credential: %v", err)
	}
	if err := personalStore.Credentials().Set(ctx, "sap-github-bearer", &store.Credential{
		Type:  store.CredBearer,
		Token: "personal-github-token",
	}); err != nil {
		t.Fatalf("set personal credential: %v", err)
	}

	resolver := &channelPlatformResolver{backend: esStore}
	enrichedCtx, resolvedUserID, _, resolveErr := resolver.ResolveChannelUser(ctx, "email", "user@example.com")
	if resolveErr != nil {
		t.Fatalf("ResolveChannelUser: %v", resolveErr)
	}
	if resolvedUserID != userID {
		t.Fatalf("resolved user = %q, want %q", resolvedUserID, userID)
	}

	cs := store.CredentialStoreFromContext(enrichedCtx)
	merged, ok := cs.(*store.MergedCredentialStore)
	if !ok {
		t.Fatalf("credential store = %T, want *store.MergedCredentialStore", cs)
	}
	if merged.Personal == nil {
		t.Fatal("merged credential store has nil Personal store")
	}
	if merged.Team == nil {
		t.Fatal("merged credential store has nil Team store")
	}

	listed := cs.List(enrichedCtx)
	if listed["sap-github-bearer"] != store.CredBearer {
		t.Fatalf("personal GitHub credential missing from channel context list: %+v", listed)
	}
	if listed["openstack-keystone"] != store.CredBearer {
		t.Fatalf("team fallback credential missing from channel context list: %+v", listed)
	}

	header, value, err := cs.Resolve(enrichedCtx, "sap-github-bearer")
	if err != nil {
		t.Fatalf("Resolve personal GitHub credential: %v", err)
	}
	if header != "Authorization" || value != "Bearer personal-github-token" {
		t.Fatalf("resolved GitHub credential = %q %q, want personal bearer token", header, value)
	}
}

func TestResolveChannelUser_InjectsNetworkPolicyStoresForEmail(t *testing.T) {
	esStore, _ := setupChannelResolverFixture(t, "netorg", "netteam", "email", "net@example.com")
	ctx := context.Background()

	orgStore, err := esStore.ForOrg("netorg")
	if err != nil {
		t.Fatalf("ForOrg: %v", err)
	}
	teamStore := orgStore.ForTeam("netteam")
	if err := teamStore.NetworkPolicies().Save(ctx, &store.NetworkPolicyRule{
		ID:        "allow-sap-ghe",
		Host:      "github.wdf.sap.corp",
		Port:      443,
		Action:    store.NetworkPolicyAllow,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save team network policy: %v", err)
	}

	resolver := &channelPlatformResolver{backend: esStore}
	enrichedCtx, _, _, resolveErr := resolver.ResolveChannelUser(ctx, "email", "net@example.com")
	if resolveErr != nil {
		t.Fatalf("ResolveChannelUser: %v", resolveErr)
	}

	nps := store.NetworkPolicyStoresFromContext(enrichedCtx)
	if nps == nil {
		t.Fatal("NetworkPolicyStores not found in context")
	}
	if nps.Platform == nil {
		t.Fatal("NetworkPolicyStores.Platform is nil")
	}
	if nps.Org == nil {
		t.Fatal("NetworkPolicyStores.Org is nil")
	}
	if nps.Team == nil {
		t.Fatal("NetworkPolicyStores.Team is nil")
	}

	endpoints := netpolicy.CollectAllowEndpoints(enrichedCtx, nps)
	for _, endpoint := range endpoints {
		if endpoint.Host == "github.wdf.sap.corp" && endpoint.Port == 443 {
			return
		}
	}
	t.Fatalf("allow endpoint github.wdf.sap.corp:443 not collected from channel context: %+v", endpoints)
}

func TestResolveChannelUser_InjectsGatewayConfigForEmail(t *testing.T) {
	esStore, _ := setupChannelResolverFixture(t, "gworg", "gwteam", "email", "gw@example.com")
	cfg := &openshell.GRPCClientConfig{Addr: "openshell-gateway.example:443", TLS: true}

	resolver := &channelPlatformResolver{backend: esStore, gatewayConfig: cfg}
	enrichedCtx, _, _, resolveErr := resolver.ResolveChannelUser(context.Background(), "email", "gw@example.com")
	if resolveErr != nil {
		t.Fatalf("ResolveChannelUser: %v", resolveErr)
	}

	got := netpolicy.GatewayConfigFromContext(enrichedCtx)
	if got == nil {
		t.Fatal("gateway config not found in context")
	}
	if got.Addr != cfg.Addr || got.TLS != cfg.TLS {
		t.Fatalf("gateway config = %+v, want %+v", got, cfg)
	}
}

func setupChannelResolverFixture(t *testing.T, orgSlug, teamSlug, channelType, externalID string) (store.PlatformBackend, string) {
	t.Helper()

	tmp := t.TempDir()
	_, esStore, err := entstore.NewPlatformServices(context.Background(), entstore.Config{
		DSN:     "file:" + filepath.Join(tmp, "platform.db"),
		DataDir: tmp,
	})
	if err != nil {
		t.Fatalf("NewPlatformServices: %v", err)
	}
	t.Cleanup(func() { esStore.Close() })

	ctx := context.Background()
	userID := uuid.New().String()
	user := &store.User{
		ID:          userID,
		Email:       "test@example.com",
		DisplayName: "Test User",
		Status:      "active",
		CreatedAt:   time.Now(),
	}
	if err := esStore.Users().Create(ctx, user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	orgID := uuid.New().String()
	org := &store.Organization{
		ID:        orgID,
		Name:      "Test Org",
		Slug:      orgSlug,
		Status:    "active",
		CreatedAt: time.Now(),
	}
	if err := esStore.Organizations().Create(ctx, org); err != nil {
		t.Fatalf("Create org: %v", err)
	}
	if err := esStore.Organizations().AddMember(ctx, userID, orgID, "member"); err != nil {
		t.Fatalf("Add org member: %v", err)
	}

	if err := esStore.ProvisionOrg(ctx, orgID, orgSlug); err != nil {
		t.Fatalf("Provision org: %v", err)
	}

	orgStore, err := esStore.ForOrg(orgSlug)
	if err != nil {
		t.Fatalf("ForOrg: %v", err)
	}

	teamID := uuid.New().String()
	team := &store.Team{
		ID:         teamID,
		Name:       "Test Team",
		Slug:       teamSlug,
		SchemaName: "team_" + teamSlug,
		CreatedAt:  time.Now(),
	}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatalf("Create team: %v", err)
	}
	if err := orgStore.Teams().AddMember(ctx, &store.TeamMembership{
		UserID:   userID,
		TeamID:   teamID,
		Role:     "member",
		JoinedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Add team member: %v", err)
	}

	now := time.Now()
	link := &store.UserChannel{
		ID:          uuid.New().String(),
		UserID:      userID,
		ChannelType: channelType,
		ExternalID:  externalID,
		DisplayName: "@testuser",
		Enabled:     true,
		Verified:    true,
		VerifiedAt:  &now,
		CreatedAt:   time.Now(),
	}
	if err := esStore.UserChannels().Link(ctx, link); err != nil {
		t.Fatalf("Link user channel: %v", err)
	}

	return esStore, userID
}
