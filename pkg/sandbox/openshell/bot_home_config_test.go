package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
)

// Exercise the real disk loader, generic factory and registered OpenShell
// factory, not merely YAML unmarshalling or a directly constructed Config.
func loadBotHomeBackend(t *testing.T, contents string) *OpenShellBackend {
	t.Helper()
	t.Setenv("SUDO_USER", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := config.GetConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := config.LoadAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	backend, cleanup, err := sandbox.BackendFromAppConfig(app)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	b, ok := backend.(*OpenShellBackend)
	if !ok {
		t.Fatalf("backend = %T", backend)
	}
	if closer, ok := b.gateway.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	return b
}

func TestBotHomeAppConfigFactory(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		enabled        bool
		claim          string
	}{
		{name: "default-off"},
		{name: "claim-alone-does-not-opt-in", settings: "    bot_home_claim_name: retained-homes\n", claim: "retained-homes"},
		{name: "enabled", settings: "    bot_home_mounts_enabled: true\n    bot_home_claim_name: retained-homes\n", enabled: true, claim: "retained-homes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := loadBotHomeBackend(t, "sandbox:\n  backend: openshell\n  openshell:\n    gateway_addr: localhost:1\n    gateway_tls: false\n"+tc.settings)
			if b.cfg.BotHomeMountsEnabled != tc.enabled || b.cfg.BotHomeClaimName != tc.claim {
				t.Fatalf("factory lost config: enabled=%t claim=%q", b.cfg.BotHomeMountsEnabled, b.cfg.BotHomeClaimName)
			}
			binding, err := b.EnsureBotHome(context.Background(), sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: "user", BotID: "bot"})
			if tc.enabled {
				if err != nil {
					t.Fatal(err)
				}
				if binding == nil {
					t.Fatal("missing binding")
				}
			} else if !errors.Is(err, sandbox.ErrBotHomeUnsupported) {
				t.Fatalf("default must fail closed: %v", err)
			}
		})
	}
}
