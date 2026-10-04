package config

import "testing"

func TestEffectiveAuthModeSQLiteDefaultsToNone(t *testing.T) {
	var cfg PlatformAuthConfig
	if got := cfg.EffectiveAuthMode("sqlite"); got != AuthModeNone {
		t.Fatalf("SQLite auth mode = %q, want %q", got, AuthModeNone)
	}
}

func TestEffectiveAuthModePostgresDefaultsToBuiltin(t *testing.T) {
	var cfg PlatformAuthConfig
	if got := cfg.EffectiveAuthMode("postgres"); got != AuthModeBuiltin {
		t.Fatalf("Postgres auth mode = %q, want %q", got, AuthModeBuiltin)
	}
}

func TestEffectiveAuthModeExplicitOverrides(t *testing.T) {
	for _, mode := range []string{AuthModeNone, AuthModeBuiltin, AuthModeOIDC} {
		cfg := PlatformAuthConfig{Mode: mode}
		if got := cfg.EffectiveAuthMode("sqlite"); got != mode {
			t.Errorf("SQLite explicit mode = %q, want %q", got, mode)
		}
	}
}

func TestValidateAuthModeRejectsNoneForPostgres(t *testing.T) {
	if err := ValidateAuthMode("postgres", AuthModeNone); err == nil {
		t.Fatal("expected none auth mode to be rejected for postgres")
	}
	if err := ValidateAuthMode("sqlite", AuthModeNone); err != nil {
		t.Fatalf("none auth mode should be allowed for sqlite: %v", err)
	}
}
