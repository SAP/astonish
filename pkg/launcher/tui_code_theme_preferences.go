package launcher

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/tui"
	"github.com/SAP/astonish/pkg/tui/backend"
)

var _ backend.CodeThemePreferencesBackend = (*localAgentBackend)(nil)
var _ backend.CodeThemePreferencesBackend = (*lazyCodeBackend)(nil)

// Preferences are independent of the agent: even an uninitialized local backend
// can read and save them without opening a session or loading application config.
func (b *localAgentBackend) LoadCodeThemePreference(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p, err := config.LoadCodeUIPreferences()
	return p.ThemeID, err
}

func (b *localAgentBackend) SaveCodeThemePreference(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	th, err := tui.ResolveCodeTheme(tui.CodeThemeID(id))
	if err != nil {
		return err
	}
	return config.SaveCodeUIPreferences(config.CodeUIPreferences{ThemeID: string(th.CodeThemeID)})
}

func (b *lazyCodeBackend) LoadCodeThemePreference(ctx context.Context) (string, error) {
	if b.inner == nil {
		return "", fmt.Errorf("code backend not opened")
	}
	if inner, ok := b.inner.(backend.CodeThemePreferencesBackend); ok {
		return inner.LoadCodeThemePreference(ctx)
	}
	return "", fmt.Errorf("code backend does not support theme preferences")
}

func (b *lazyCodeBackend) SaveCodeThemePreference(ctx context.Context, id string) error {
	if b.inner == nil {
		return fmt.Errorf("code backend not opened")
	}
	if inner, ok := b.inner.(backend.CodeThemePreferencesBackend); ok {
		return inner.SaveCodeThemePreference(ctx, id)
	}
	return fmt.Errorf("code backend does not support theme preferences")
}
