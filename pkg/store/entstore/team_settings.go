package entstore

import (
	"context"
	"encoding/json"
	"fmt"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
)

const settingsKey = "team_settings"

// teamSettingsStore implements store.SettingsStore using the Ent team client.
type teamSettingsStore struct {
	client *teament.Client
}

var _ store.SettingsStore = (*teamSettingsStore)(nil)

func (s *teamSettingsStore) Get(ctx context.Context) (*store.TeamSettings, error) {
	client := s.client
	if tx, ok := ctx.Value(botPolicyTransactionKey{}).(botPolicyTransaction); ok && tx.outer == client {
		client = tx.current
	}
	setting, err := client.Setting.Get(ctx, settingsKey)
	if err != nil {
		if teament.IsNotFound(err) {
			return &store.TeamSettings{}, nil
		}
		return nil, fmt.Errorf("entstore: SettingsStore.Get: %w", err)
	}

	// Marshal value map to JSON and then unmarshal into TeamSettings.
	data, err := json.Marshal(setting.Value)
	if err != nil {
		return nil, fmt.Errorf("entstore: SettingsStore.Get: marshal: %w", err)
	}

	var ts store.TeamSettings
	if err := json.Unmarshal(data, &ts); err != nil {
		return nil, fmt.Errorf("entstore: SettingsStore.Get: unmarshal: %w", err)
	}
	return &ts, nil
}

func (s *teamSettingsStore) Save(ctx context.Context, settings *store.TeamSettings) error {
	// Marshal TeamSettings to JSON then to map[string]any.
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("entstore: SettingsStore.Save: marshal: %w", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("entstore: SettingsStore.Save: unmarshal: %w", err)
	}

	// Use get + update/create pattern.
	_, getErr := s.client.Setting.Get(ctx, settingsKey)
	if getErr != nil {
		if teament.IsNotFound(getErr) {
			// Create.
			_, err = s.client.Setting.Create().
				SetID(settingsKey).
				SetValue(value).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("entstore: SettingsStore.Save: create: %w", err)
			}
			return nil
		}
		return fmt.Errorf("entstore: SettingsStore.Save: get: %w", getErr)
	}

	// Update.
	err = s.client.Setting.UpdateOneID(settingsKey).
		SetValue(value).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("entstore: SettingsStore.Save: update: %w", err)
	}
	return nil
}
