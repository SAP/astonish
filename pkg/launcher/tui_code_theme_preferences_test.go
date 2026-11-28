package launcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

func TestCodeThemePreferenceLocalAndLazyNoAgentInitialization(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	ctx := context.Background()
	local := &localAgentBackend{}
	if id, err := local.LoadCodeThemePreference(ctx); err != nil || id != "" {
		t.Fatalf("missing: %q %v", id, err)
	}
	lazy := &lazyCodeBackend{}
	if _, err := lazy.LoadCodeThemePreference(ctx); err == nil || !strings.Contains(err.Error(), "not opened") {
		t.Fatal(err)
	}
	if err := lazy.SaveCodeThemePreference(ctx, "dark-orange"); err == nil {
		t.Fatal("lazy save before open")
	}
	if lazy.inner != nil || local.result != nil {
		t.Fatal("preferences initialized agent")
	}
	lazy.inner = local
	for _, id := range []string{"dark-orange", "blue-gray"} {
		if err := lazy.SaveCodeThemePreference(ctx, id); err != nil {
			t.Fatal(err)
		}
		if got, err := lazy.LoadCodeThemePreference(ctx); err != nil || got != id {
			t.Fatalf("forward: %q %v", got, err)
		}
	}
	if local.result != nil || local.appConfig != nil {
		t.Fatal("preferences loaded agent/config")
	}
	path, _ := config.GetCodeUIPreferencesPath()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected files %v %v", entries, err)
	}
}

func TestCodeThemePreferenceValidationAndErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SUDO_USER", "")
	b := &localAgentBackend{}
	ctx := context.Background()
	path, _ := config.GetCodeUIPreferencesPath()
	if err := b.SaveCodeThemePreference(ctx, "unsupported"); err == nil {
		t.Fatal("unsupported theme saved")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("validation wrote disk")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.LoadCodeThemePreference(cancelled); err != context.Canceled {
		t.Fatal(err)
	}
	if err := b.SaveCodeThemePreference(cancelled, "blue-gray"); err != context.Canceled {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	future := `{"version":2,"theme_id":"future"}`
	if err := os.WriteFile(path, []byte(future), 0600); err != nil {
		t.Fatal(err)
	}
	lazy := &lazyCodeBackend{inner: b}
	if _, err := lazy.LoadCodeThemePreference(ctx); err == nil {
		t.Fatal("future load accepted")
	}
	if err := lazy.SaveCodeThemePreference(ctx, "dark-orange"); err == nil {
		t.Fatal("future save accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != future {
		t.Fatal("future state changed")
	}
}
