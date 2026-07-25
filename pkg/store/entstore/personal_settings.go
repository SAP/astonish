// Package entstore — PersonalSettings implementation.
package entstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	personalent "github.com/SAP/astonish/ent/personal"
	"github.com/SAP/astonish/ent/personal/personalsettings"
	"github.com/SAP/astonish/pkg/store"
)

// personalSettingsStore implements store.PersonalSettingsStore for personal scope.
//
// Storage model: one row per user_id (unique index). In personal-mode SQLite
// where the caller has no user_id, uuid.Nil (all-zeros) is used as a sentinel
// for the single local user — mirrored from ent/personal/schema/personal_settings.go
// (DECISION-1 in .omo/plans/per-chat-app-model-pin.md).
type personalSettingsStore struct {
	client *personalent.Client
	userID string
}

var _ store.PersonalSettingsStore = (*personalSettingsStore)(nil)

func (ps *personalSettingsStore) userUUID() uuid.UUID {
	if ps.userID == "" {
		return uuid.Nil
	}
	uid, err := uuid.Parse(ps.userID)
	if err != nil {
		return uuid.Nil
	}
	return uid
}

func (ps *personalSettingsStore) Get(ctx context.Context) (*store.PersonalSettings, error) {
	uid := ps.userUUID()
	ent, err := ps.client.PersonalSettings.Query().
		Where(personalsettings.UserIDEQ(uid)).
		Only(ctx)
	if err != nil {
		if personalent.IsNotFound(err) {
			return &store.PersonalSettings{}, nil
		}
		return nil, fmt.Errorf("get personal settings: %w", err)
	}
	return &store.PersonalSettings{
		DefaultProvider: ent.DefaultProvider,
		DefaultModel:    ent.DefaultModel,
		BrandTheme:      ent.BrandTheme,
	}, nil
}

func (ps *personalSettingsStore) Save(ctx context.Context, settings *store.PersonalSettings) error {
	if settings == nil {
		return fmt.Errorf("save personal settings: nil settings")
	}
	uid := ps.userUUID()

	existing, err := ps.client.PersonalSettings.Query().
		Where(personalsettings.UserIDEQ(uid)).
		Only(ctx)
	if err != nil && !personalent.IsNotFound(err) {
		return fmt.Errorf("save personal settings: query: %w", err)
	}

	if existing != nil {
		return existing.Update().
			SetDefaultProvider(settings.DefaultProvider).
			SetDefaultModel(settings.DefaultModel).
			SetBrandTheme(settings.BrandTheme).
			Exec(ctx)
	}

	_, err = ps.client.PersonalSettings.Create().
		SetUserID(uid).
		SetDefaultProvider(settings.DefaultProvider).
		SetDefaultModel(settings.DefaultModel).
		SetBrandTheme(settings.BrandTheme).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("save personal settings: create: %w", err)
	}
	return nil
}
