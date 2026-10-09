package store

import (
	"context"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/google/uuid"
)

const (
	singleUserDisplayName = "Local User"
	defaultTeamName       = "General"
	defaultTeamSlug       = "general"
)

// ProvisionSingleUser creates the default local user, organization, team, and
// personal store for SQLite single-user mode. It is safe to call repeatedly:
// an existing organization means the local workspace has already been created.
func ProvisionSingleUser(ctx context.Context, backend PlatformBackend, orgName, orgSlug string) error {
	count, err := backend.Organizations().Count(ctx)
	if err != nil {
		return fmt.Errorf("count organizations: %w", err)
	}
	if count > 0 {
		return nil
	}
	if orgName == "" {
		orgName = "Default Organization"
	}
	if orgSlug == "" {
		orgSlug = "default"
	}

	now := time.Now()
	orgID := uuid.NewString()
	user := &User{
		ID:           config.SingleUserID,
		Email:        config.SingleUserEmail,
		DisplayName:  singleUserDisplayName,
		PlatformRole: "superadmin",
		Status:       "active",
		CreatedAt:    now,
	}
	if err := backend.Users().Create(ctx, user); err != nil {
		return fmt.Errorf("create single user: %w", err)
	}

	org := &Organization{
		ID:        orgID,
		Name:      orgName,
		Slug:      orgSlug,
		Status:    "active",
		CreatedAt: now,
	}
	if err := backend.Organizations().Create(ctx, org); err != nil {
		return fmt.Errorf("create organization: %w", err)
	}
	if err := backend.Organizations().AddMember(ctx, user.ID, org.ID, "owner"); err != nil {
		return fmt.Errorf("add single user to organization: %w", err)
	}
	if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		return fmt.Errorf("provision organization: %w", err)
	}

	orgStore, err := backend.ForOrg(org.Slug)
	if err != nil {
		return fmt.Errorf("open organization store: %w", err)
	}
	team := &Team{
		ID:         uuid.NewString(),
		Name:       defaultTeamName,
		Slug:       defaultTeamSlug,
		SchemaName: defaultTeamSlug,
		CreatedAt:  now,
	}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		return fmt.Errorf("create default team: %w", err)
	}
	if err := orgStore.ProvisionTeam(ctx, team.Slug); err != nil {
		return fmt.Errorf("provision default team: %w", err)
	}
	if err := orgStore.Teams().AddMember(ctx, &TeamMembership{
		UserID:   user.ID,
		TeamID:   team.ID,
		Role:     "admin",
		JoinedAt: now,
	}); err != nil {
		return fmt.Errorf("add single user to default team: %w", err)
	}
	if err := orgStore.ProvisionPersonalSchema(ctx, user.ID); err != nil {
		return fmt.Errorf("provision personal store: %w", err)
	}
	return nil
}
