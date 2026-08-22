package entstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	platforment "github.com/SAP/astonish/ent/platform"
	"github.com/SAP/astonish/ent/platform/orgmembership"
	"github.com/SAP/astonish/ent/platform/user"
	"github.com/SAP/astonish/pkg/store"
)

// userStore implements store.UserStore using the Ent platform client.
type userStore struct {
	client *platforment.Client
}

func (s *Store) Users() store.UserStore {
	return &userStore{client: s.platformClient}
}

func (us *userStore) Create(ctx context.Context, u *store.User) error {
	uid, err := uuid.Parse(u.ID)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	create := us.client.User.Create().
		SetID(uid).
		SetEmail(u.Email).
		SetDisplayName(u.DisplayName).
		SetStatus(user.Status(u.Status))

	if u.PasswordHash != "" {
		create.SetPasswordHash(u.PasswordHash)
	}
	if u.OIDCSubject != "" {
		create.SetOidcSubject(u.OIDCSubject)
	}
	if u.OIDCIssuer != "" {
		create.SetOidcIssuer(u.OIDCIssuer)
	}
	if u.PlatformRole != "" {
		create.SetPlatformRole(u.PlatformRole)
	}
	if !u.CreatedAt.IsZero() {
		create.SetCreatedAt(u.CreatedAt)
	}

	_, err = create.Save(ctx)
	return err
}

func (us *userStore) GetByID(ctx context.Context, id string) (*store.User, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID: %w", err)
	}
	ent, err := us.client.User.Get(ctx, uid)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return entUserToStore(ent), nil
}

func (us *userStore) GetByEmail(ctx context.Context, email string) (*store.User, error) {
	ent, err := us.client.User.Query().
		Where(user.EmailEQ(email)).
		Only(ctx)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return entUserToStore(ent), nil
}

func (us *userStore) GetByOIDC(ctx context.Context, issuer, subject string) (*store.User, error) {
	ent, err := us.client.User.Query().
		Where(
			user.OidcIssuerEQ(issuer),
			user.OidcSubjectEQ(subject),
		).
		Only(ctx)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return entUserToStore(ent), nil
}

func (us *userStore) Update(ctx context.Context, u *store.User) error {
	uid, err := uuid.Parse(u.ID)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	update := us.client.User.UpdateOneID(uid).
		SetEmail(u.Email).
		SetDisplayName(u.DisplayName).
		SetStatus(user.Status(u.Status))

	if u.PasswordHash != "" {
		update.SetPasswordHash(u.PasswordHash)
	} else {
		update.ClearPasswordHash()
	}
	if u.OIDCSubject != "" {
		update.SetOidcSubject(u.OIDCSubject)
	} else {
		update.ClearOidcSubject()
	}
	if u.OIDCIssuer != "" {
		update.SetOidcIssuer(u.OIDCIssuer)
	} else {
		update.ClearOidcIssuer()
	}
	if u.PlatformRole != "" {
		update.SetPlatformRole(u.PlatformRole)
	} else {
		update.ClearPlatformRole()
	}
	if !u.LastLoginAt.IsZero() {
		update.SetLastLoginAt(u.LastLoginAt)
	}

	return update.Exec(ctx)
}

func (us *userStore) Delete(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}
	return us.client.User.DeleteOneID(uid).Exec(ctx)
}

func (us *userStore) List(ctx context.Context) ([]*store.User, error) {
	ents, err := us.client.User.Query().
		Order(user.ByEmail()).
		All(ctx)
	if err != nil {
		return nil, err
	}

	users := make([]*store.User, len(ents))
	for i, e := range ents {
		users[i] = entUserToStore(e)
	}
	return users, nil
}

func (us *userStore) ListByOrg(ctx context.Context, orgID string) ([]*store.UserWithRole, error) {
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org ID: %w", err)
	}

	// Query users through the org membership edge.
	memberships, err := us.client.OrgMembership.Query().
		Where(orgmembership.OrgIDEQ(oid)).
		WithUser().
		All(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]*store.UserWithRole, 0, len(memberships))
	for _, m := range memberships {
		if m.Edges.User == nil {
			continue
		}
		u := entUserToStore(m.Edges.User)
		result = append(result, &store.UserWithRole{
			User:     *u,
			Role:     string(m.Role),
			JoinedAt: m.JoinedAt,
		})
	}
	return result, nil
}

func (us *userStore) SetPlatformRole(ctx context.Context, userID, role string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("invalid user ID: %w", err)
	}

	update := us.client.User.UpdateOneID(uid)
	if role == "" {
		update.ClearPlatformRole()
	} else {
		update.SetPlatformRole(role)
	}
	return update.Exec(ctx)
}

func (us *userStore) CountByPlatformRole(ctx context.Context, role string) (int, error) {
	return us.client.User.Query().
		Where(user.PlatformRoleEQ(role)).
		Count(ctx)
}

// entUserToStore converts an Ent User entity to the store.User DTO.
func entUserToStore(e *platforment.User) *store.User {
	u := &store.User{
		ID:          e.ID.String(),
		Email:       e.Email,
		DisplayName: e.DisplayName,
		Status:      string(e.Status),
		CreatedAt:   e.CreatedAt,
	}
	if e.PasswordHash != nil {
		u.PasswordHash = *e.PasswordHash
	}
	if e.OidcSubject != nil {
		u.OIDCSubject = *e.OidcSubject
	}
	if e.OidcIssuer != nil {
		u.OIDCIssuer = *e.OidcIssuer
	}
	if e.PlatformRole != nil {
		u.PlatformRole = *e.PlatformRole
	}
	if e.LastLoginAt != nil {
		u.LastLoginAt = *e.LastLoginAt
	}
	return u
}

// Compile-time assertion.
var _ store.UserStore = (*userStore)(nil)
