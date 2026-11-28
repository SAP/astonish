package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSaveOpenShellBotHomeConfig(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := GetConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, settings string
		enabled        bool
		claim          string
	}{
		{name: "default-off", settings: "    gateway_tls: false\n"},
		{name: "opt-in", settings: "    bot_home_mounts_enabled: true\n    bot_home_claim_name: retained-homes\n", enabled: true, claim: "retained-homes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("sandbox:\n  backend: openshell\n  openshell:\n"+tc.settings), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadAppConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Sandbox.OpenShell.BotHomeMountsEnabled != tc.enabled || cfg.Sandbox.OpenShell.BotHomeClaimName != tc.claim {
				t.Fatal("loaded incorrect bot home settings")
			}
			if err := SaveAppConfig(cfg); err != nil {
				t.Fatal(err)
			}
			roundTrip, err := LoadAppConfig()
			if err != nil {
				t.Fatal(err)
			}
			if roundTrip.Sandbox.OpenShell.BotHomeMountsEnabled != tc.enabled || roundTrip.Sandbox.OpenShell.BotHomeClaimName != tc.claim {
				t.Fatal("bot home settings lost on save/reload")
			}
		})
	}
}
